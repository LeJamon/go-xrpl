package adaptor

import (
	"encoding/binary"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// xrplEpochUnixOffset is the delta between Unix epoch (1970-01-01) and
// XRPL NetClock epoch (2000-01-01). The proposal close-time field on
// the wire — and in rippled's proposalUniqueId hash input — is seconds
// since the XRPL epoch, NOT seconds since Unix epoch. Keeping this
// separate from the converter.go alias documents intent at the call
// site where the difference matters most (divergent hashes = dedup
// desync across mixed Go/rippled peers).
const xrplEpochUnixOffset int64 = 946684800

// hashProposalSuppression returns the suppression key for a proposal,
// matching rippled's proposalUniqueId at
// rippled/src/xrpld/app/consensus/RCLCxPeerPos.cpp:66-83:
//
//	Serializer s(512);
//	s.addBitString(proposeHash);
//	s.addBitString(previousLedger);
//	s.add32(proposeSeq);
//	s.add32(closeTime.time_since_epoch().count());
//	s.addVL(publicKey);
//	s.addVL(signature);
//	return s.getSHA512Half();
//
// Key layout properties mirrored here:
//   - `addBitString` writes raw bytes (no length prefix, fixed-size hash).
//   - `add32` is BIG-endian 4 bytes.
//   - `add32(closeTime.time_since_epoch().count())` feeds the XRPL
//     NetClock count — seconds since the XRPL epoch (2000-01-01 UTC),
//     NOT Unix epoch. We derive that count from Proposal.CloseTime via
//     `Unix() - xrplEpochUnixOffset`, exactly matching the converter's
//     wire-format convention.
//   - `addVL` writes rippled's variable-length length prefix (1–3 bytes,
//     see Serializer::addEncoded) followed by the raw bytes. For the
//     33-byte compressed pubkey and 64–72-byte DER signature we'll
//     always use, the length prefix is a single byte (<=192).
//
// Why this matters (B2): rippled computes the suppression key from
// these structured fields. A Go peer that hashes the raw TMProposeSet
// protobuf envelope instead would compute a different key for the same
// proposal — breaking HashRouter parity across mixed-implementation
// peer sets (semantically-identical messages with different protobuf
// framing would be registered twice on one side and once on the other,
// desynchronizing reduce-relay slot feeding).
func hashProposalSuppression(p *consensus.Proposal) [32]byte {
	// Fixed-size segments totalling 72 bytes (32 + 32 + 4 + 4) plus the
	// VL-encoded pubkey (1 + 33 = 34) and signature (1 + up to 72 = up
	// to 73). Preallocate 180 — one allocation, no resizing on the
	// common path.
	buf := make([]byte, 0, 180)

	// addBitString(proposeHash) — 32 raw bytes, no length prefix.
	buf = append(buf, p.TxSet[:]...)
	// addBitString(previousLedger) — 32 raw bytes.
	buf = append(buf, p.PreviousLedger[:]...)
	// add32(proposeSeq) — big-endian uint32.
	buf = binary.BigEndian.AppendUint32(buf, p.Position)
	// add32(closeTime.time_since_epoch().count()) — big-endian uint32
	// of the XRPL NetClock count (seconds since 2000-01-01).
	// Negative pre-epoch times cannot occur for a well-formed proposal
	// (signing time is always post-epoch); clamp at zero so a bogus
	// pre-epoch Time still produces a deterministic hash rather than
	// wrapping to a large positive uint32.
	var closeTimeSec uint32
	if ct := p.CloseTime.Unix() - xrplEpochUnixOffset; ct > 0 {
		closeTimeSec = uint32(ct)
	}
	buf = binary.BigEndian.AppendUint32(buf, closeTimeSec)
	// addVL(publicKey) — length prefix + NodeID (33-byte compressed key).
	buf = appendVLPrefix(buf, len(p.NodeID))
	buf = append(buf, p.NodeID[:]...)
	// addVL(signature) — length prefix + raw signature bytes.
	buf = appendVLPrefix(buf, len(p.Signature))
	buf = append(buf, p.Signature...)

	return common.Sha512Half(buf)
}

// hashValidationSuppression returns the suppression key for a
// validation, matching rippled's PeerImp.cpp:2374:
//
//	auto key = sha512Half(makeSlice(m->validation()));
//
// The input is the inner, canonical STValidation blob as carried in
// the `validation` field of the TMValidation protobuf — NOT the
// protobuf envelope itself. Callers must pass the decoded inner blob
// (`*message.Validation.Validation`), and MUST NOT re-serialize it
// from the parsed consensus.Validation struct: STValidation field
// ordering rules mean a round-trip can produce different bytes even
// for a semantically-identical validation, which would re-introduce
// the exact desync B2 is fixing.
func hashValidationSuppression(serializedSTValidation []byte) [32]byte {
	return common.Sha512Half(serializedSTValidation)
}

// appendVLPrefix writes rippled's variable-length length prefix
// matching Serializer::addEncoded (libxrpl/protocol/Serializer.cpp:222).
// For lengths up to 192 the prefix is a single byte equal to the
// length; for 193-12480 two bytes; for 12481-918744 three bytes.
// Proposal pubkeys (33 B) and signatures (64-72 B) always fit in the
// single-byte range — but keeping the full encoder ensures we can't
// silently desync if a future caller passes a larger slice.
func appendVLPrefix(buf []byte, n int) []byte {
	switch {
	case n <= 192:
		return append(buf, byte(n))
	case n <= 12480:
		v := n - 193
		return append(buf, byte(193+(v>>8)), byte(v&0xff))
	case n <= 918744:
		v := n - 12481
		return append(buf, byte(241+(v>>16)), byte((v>>8)&0xff), byte(v&0xff))
	}
	// Caller error — rippled throws here; in Go we record the overflow
	// by producing a sentinel prefix that will make the resulting hash
	// differ from anything rippled would emit. This is "loud failure"
	// by design: a suppression hash for a 900KB+ field cannot exist in
	// any real proposal/validation, so any mismatch downstream will
	// surface the misuse immediately.
	return append(buf, 0xFF, 0xFF, 0xFF, 0xFF)
}

// messageSuppression tracks recently-seen proposal/validation message
// hashes so the reduce-relay slot feeds on duplicates only — matching
// rippled's PeerImp.cpp:1730-1738, where updateSlotAndSquelch fires
// inside the `!added` branch of HashRouter::addSuppressionPeer (i.e.,
// when the same message hash has already been observed from a
// different peer).
//
// Why duplicates-only: the reduce-relay selection machine needs
// multi-source signal to decide that a given validator's traffic is
// reaching us through redundant paths. Counting first-seen arrivals
// means "selection hits MaxMessageThreshold in ~N distinct messages"
// rather than rippled's "~N duplicates" — which accelerates selection
// N-fold and produces squelches earlier and more aggressively than
// the rest of the network would expect.
type messageSuppression struct {
	mu      sync.Mutex
	seen    map[[32]byte]time.Time
	ttl     time.Duration
	maxSize int
	now     func() time.Time
}

// newMessageSuppression returns a dedup tracker. ttl bounds how long a
// hash is remembered; maxSize caps memory for adversarial traffic
// (when the set is full we trim half the oldest entries).
func newMessageSuppression(ttl time.Duration, maxSize int) *messageSuppression {
	return &messageSuppression{
		seen:    make(map[[32]byte]time.Time),
		ttl:     ttl,
		maxSize: maxSize,
		now:     time.Now,
	}
}

// observe records that a message with the given hash was received.
// Returns (firstSeen, lastSeenAt):
//   - firstSeen=true, lastSeenAt=zero: never observed before (or TTL expired).
//   - firstSeen=false, lastSeenAt=prior observation time: a duplicate
//     within the TTL window; caller uses lastSeenAt to gate
//     reduce-relay slot feeding on the IDLED window (rippled
//     PeerImp.cpp:1736 checks `now - relayed < IDLED`).
//
// The stored time is always refreshed to `now` on every observe so a
// steady stream of duplicates stays live in the cache.
func (s *messageSuppression) observe(hash [32]byte) (firstSeen bool, lastSeenAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	// Evict stale entries if we're at capacity. A cheap scan rather
	// than a formal LRU — the tracker is a cache, not a hot path.
	if len(s.seen) >= s.maxSize {
		cutoff := now.Add(-s.ttl)
		for h, seenAt := range s.seen {
			if seenAt.Before(cutoff) {
				delete(s.seen, h)
			}
		}
		// If that didn't free enough space (adversarial churn), drop
		// half the map — bounded worst case.
		if len(s.seen) >= s.maxSize {
			i := 0
			for h := range s.seen {
				if i >= s.maxSize/2 {
					break
				}
				delete(s.seen, h)
				i++
			}
		}
	}

	if seenAt, ok := s.seen[hash]; ok && now.Sub(seenAt) < s.ttl {
		s.seen[hash] = now // refresh so a steady stream of duplicates stays live
		return false, seenAt
	}
	s.seen[hash] = now
	return true, time.Time{}
}
