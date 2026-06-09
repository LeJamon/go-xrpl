package adaptor

import (
	"encoding/binary"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
)

// hashProposalSuppression returns the suppression key for a proposal,
// computed over the proposal's structured fields:
//
//   - proposeHash (raw, fixed-size, no length prefix)
//   - previousLedger (raw, fixed-size)
//   - proposeSeq (big-endian uint32)
//   - closeTime as an XRPL NetClock count — seconds since the XRPL epoch
//     (2000-01-01 UTC), NOT Unix epoch. Derived from Proposal.CloseTime
//     via Unix() - protocol.RippleEpochUnix, big-endian uint32.
//   - publicKey (VL length prefix + raw bytes)
//   - signature (VL length prefix + raw bytes)
//
// For the 33-byte compressed pubkey and 64–72-byte DER signature, the VL
// length prefix is always a single byte (<=192).
//
// The key must be computed over these structured fields, not over the raw
// wire envelope: two peers that hashed differently-framed envelopes for the
// same proposal would compute different keys, breaking suppression parity
// across mixed-implementation peer sets and desynchronizing reduce-relay
// slot feeding.
func hashProposalSuppression(p *consensus.Proposal) [32]byte {
	// Preallocate enough for the fixed-size segments plus VL-encoded
	// pubkey and signature: one allocation, no resizing on the common path.
	buf := make([]byte, 0, 180)

	buf = append(buf, p.TxSet[:]...)
	buf = append(buf, p.PreviousLedger[:]...)
	buf = binary.BigEndian.AppendUint32(buf, p.Position)
	// closeTime as the XRPL NetClock count (seconds since 2000-01-01).
	// Negative pre-epoch times cannot occur for a well-formed proposal
	// (signing time is always post-epoch); clamp at zero so a bogus
	// pre-epoch Time still produces a deterministic hash rather than
	// wrapping to a large positive uint32.
	var closeTimeSec uint32
	if ct := p.CloseTime.Unix() - protocol.RippleEpochUnix; ct > 0 {
		closeTimeSec = uint32(ct)
	}
	buf = binary.BigEndian.AppendUint32(buf, closeTimeSec)
	// Hash the wire signing pubkey, NOT the master-derived NodeID: using
	// NodeID would break suppression-hash parity with other peers.
	buf = appendVLPrefix(buf, len(p.SigningPubKey))
	buf = append(buf, p.SigningPubKey[:]...)
	buf = appendVLPrefix(buf, len(p.Signature))
	buf = append(buf, p.Signature...)

	return common.Sha512Half(buf)
}

// hashValidationSuppression returns the suppression key for a
// validation: the SHA512-Half of the inner, canonical STValidation blob
// carried in the `validation` field of the TMValidation envelope — NOT
// the envelope itself. Callers must pass the decoded inner blob
// (*message.Validation.Validation) and MUST NOT re-serialize it from the
// parsed consensus.Validation struct: STValidation field ordering means a
// round-trip can produce different bytes for a semantically-identical
// validation, which would desync suppression keys across peers.
func hashValidationSuppression(serializedSTValidation []byte) [32]byte {
	return common.Sha512Half(serializedSTValidation)
}

// appendVLPrefix writes the XRPL variable-length length prefix: for
// lengths up to 192 a single byte equal to the length; for 193-12480 two
// bytes; for 12481-918744 three bytes. Proposal pubkeys (33 B) and
// signatures (64-72 B) always fit in the single-byte range — but keeping
// the full encoder ensures we can't silently desync if a future caller
// passes a larger slice.
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
	// Caller error: emit a sentinel prefix so the resulting hash can never
	// match a peer's. This is loud failure by design — a suppression hash
	// for a 900KB+ field cannot exist in any real proposal/validation, so
	// any mismatch downstream will surface the misuse immediately.
	return append(buf, 0xFF, 0xFF, 0xFF, 0xFF)
}

// messageSuppression tracks recently-seen proposal/validation message
// hashes so the reduce-relay slot feeds on duplicates only — i.e. only
// when the same message hash has already been observed from a different
// peer.
//
// Why duplicates-only: the reduce-relay selection machine needs
// multi-source signal to decide that a given validator's traffic is
// reaching us through redundant paths. Counting first-seen arrivals would
// make selection hit its threshold in ~N distinct messages rather than ~N
// duplicates, accelerating selection N-fold and producing squelches
// earlier and more aggressively than the rest of the network expects.
type messageSuppression struct {
	mu sync.Mutex
	// seen maps a suppression hash to the most recent observation time.
	// TTL-evicted on observe when at maxSize.
	seen map[[32]byte]time.Time
	// peers maps a suppression hash to the set of peer IDs known to
	// already have the message. Populated both on inbound observe (the
	// sender now has it because they sent it to us) and on outbound
	// broadcast (the recipient now has it because we sent it to them).
	// Used by validator-list broadcast to skip peers known to already have
	// the same content.
	peers   map[[32]byte]map[uint64]struct{}
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
		peers:   make(map[[32]byte]map[uint64]struct{}),
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
//     reduce-relay slot feeding on the IDLED window.
//
// The stored time is always refreshed to now on every observe so a
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
				delete(s.peers, h)
			}
		}
		if len(s.seen) >= s.maxSize {
			i := 0
			for h := range s.seen {
				if i >= s.maxSize/2 {
					break
				}
				delete(s.seen, h)
				delete(s.peers, h)
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

// recordPeer marks peerID as a peer known to already have the message
// identified by hash. Returns true if the peer was newly added to the
// per-hash set. Always refreshes the hash's last-seen time so a steady
// stream of activity keeps the entry live.
//
// Caller-side semantics:
//   - On inbound observe (peer just delivered the hash) — record the
//     sender so we know they have it.
//   - On outbound broadcast (we just sent the hash to the peer) —
//     record the recipient so we don't re-send and so a back-relay
//     is attributable.
func (s *messageSuppression) recordPeer(hash [32]byte, peerID uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.seen[hash] = now

	peers, ok := s.peers[hash]
	if !ok {
		peers = make(map[uint64]struct{})
		s.peers[hash] = peers
	}
	if _, present := peers[peerID]; present {
		return false
	}
	peers[peerID] = struct{}{}
	return true
}

// peerHasHash reports whether peerID is known to already have the
// message identified by hash. Used by broadcast paths to skip peers
// that would receive a redundant frame.
func (s *messageSuppression) peerHasHash(hash [32]byte, peerID uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	peers, ok := s.peers[hash]
	if !ok {
		return false
	}
	_, present := peers[peerID]
	return present
}
