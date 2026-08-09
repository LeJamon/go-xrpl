package adaptor

import (
	"container/list"
	"encoding/binary"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
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
//     via protocol.ToRippleTime, big-endian uint32.
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
	closeTimeSec := protocol.ToRippleTime(p.CloseTime)
	buf = binary.BigEndian.AppendUint32(buf, closeTimeSec)
	// Hash the wire signing pubkey, NOT the master-derived NodeID: using
	// NodeID would break suppression-hash parity with other peers.
	buf = appendVLPrefix(buf, len(p.SigningPubKey))
	buf = append(buf, p.SigningPubKey[:]...)
	buf = appendVLPrefix(buf, len(p.Signature))
	buf = append(buf, p.Signature...)

	return sha512half.Sum(buf)
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
	return sha512half.Sum(serializedSTValidation)
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
	mu      sync.Mutex
	entries map[[32]byte]*suppressionEntry
	order   list.List
	ttl     time.Duration
	maxSize int
	now     func() time.Time
}

type suppressionEntry struct {
	seenAt time.Time
	peers  map[uint64]struct{}
	order  *list.Element
}

func newMessageSuppression(ttl time.Duration, maxSize int) *messageSuppression {
	if maxSize < 1 {
		maxSize = 1
	}
	return &messageSuppression{
		entries: make(map[[32]byte]*suppressionEntry),
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

	_, firstSeen, lastSeenAt = s.touchLocked(hash, s.now())
	return firstSeen, lastSeenAt
}

func (s *messageSuppression) allowRetry(hash [32]byte) {
	s.mu.Lock()
	s.removeLocked(hash)
	s.mu.Unlock()
}

func (s *messageSuppression) seenRecently(hash [32]byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	entry, ok := s.entries[hash]
	if !ok {
		return false
	}
	if s.expired(entry, now) {
		s.removeLocked(hash)
		return false
	}
	entry.seenAt = now
	s.order.MoveToBack(entry.order)
	return true
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

	entry, _, _ := s.touchLocked(hash, s.now())
	if entry.peers == nil {
		entry.peers = make(map[uint64]struct{})
	}
	if _, present := entry.peers[peerID]; present {
		return false
	}
	entry.peers[peerID] = struct{}{}
	return true
}

// peerHasHash reports whether peerID is known to already have the
// message identified by hash. Used by broadcast paths to skip peers
// that would receive a redundant frame.
func (s *messageSuppression) peerHasHash(hash [32]byte, peerID uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[hash]
	if !ok {
		return false
	}
	if s.expired(entry, s.now()) {
		s.removeLocked(hash)
		return false
	}
	_, present := entry.peers[peerID]
	return present
}

func (s *messageSuppression) touchLocked(hash [32]byte, now time.Time) (*suppressionEntry, bool, time.Time) {
	if entry, ok := s.entries[hash]; ok {
		if !s.expired(entry, now) {
			lastSeenAt := entry.seenAt
			entry.seenAt = now
			s.order.MoveToBack(entry.order)
			return entry, false, lastSeenAt
		}
		s.removeLocked(hash)
	}

	s.expireLocked(now)
	for len(s.entries) >= s.maxSize {
		oldest := s.order.Front()
		if oldest == nil {
			break
		}
		s.removeLocked(oldest.Value.([32]byte))
	}

	entry := &suppressionEntry{seenAt: now}
	entry.order = s.order.PushBack(hash)
	s.entries[hash] = entry
	return entry, true, time.Time{}
}

func (s *messageSuppression) expireLocked(now time.Time) {
	for oldest := s.order.Front(); oldest != nil; oldest = s.order.Front() {
		hash := oldest.Value.([32]byte)
		entry := s.entries[hash]
		if entry != nil && !s.expired(entry, now) {
			return
		}
		s.removeLocked(hash)
	}
}

func (s *messageSuppression) expired(entry *suppressionEntry, now time.Time) bool {
	return now.Sub(entry.seenAt) >= s.ttl
}

func (s *messageSuppression) removeLocked(hash [32]byte) {
	entry, ok := s.entries[hash]
	if !ok {
		return
	}
	s.order.Remove(entry.order)
	delete(s.entries, hash)
}
