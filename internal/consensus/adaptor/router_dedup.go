package adaptor

import (
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/crypto/common"
)

// hashPayload returns the sha512Half of payload bytes — the same hash
// function rippled uses for HashRouter suppression keys. Using
// sha512Half here (vs. a cheaper sha256) keeps the hash function
// consistent with the rest of the protocol and rules out the
// theoretical case where a crafted payload produces the same
// sha256-truncated hash as a legitimate one. The 32-byte output keys
// the dedup map directly.
func hashPayload(payload []byte) [32]byte {
	return common.Sha512Half(payload)
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

// observe records that a message with the given hash was received. It
// returns true if this is the FIRST time we've seen the hash within
// the TTL window, false if a previous observation is still live.
// Callers use the false return to distinguish duplicates — rippled's
// `!added` branch — and feed the reduce-relay slot accordingly.
func (s *messageSuppression) observe(hash [32]byte) (firstSeen bool) {
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
		return false
	}
	s.seen[hash] = now
	return true
}
