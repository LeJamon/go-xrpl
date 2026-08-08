package manifest

import (
	"log/slog"
	"sort"
	"sync"
)

// Disposition reports the outcome of ApplyManifest.
type Disposition int

const (
	// Accepted: the manifest is new, passed verification, and has been
	// stored. Callers should relay accepted manifests to other peers.
	Accepted Disposition = iota

	// Stale: the manifest's sequence is not strictly greater than the
	// cached one for this master key. Not an error — a peer that hasn't
	// seen our latest will re-gossip older versions.
	Stale

	// Invalid: signature check failed (master sig, or ephemeral sig on
	// a non-revoked manifest). Charge the sender.
	Invalid

	// BadMasterKey: the incoming manifest's master key is already
	// recorded as another manifest's ephemeral key — a key-reuse
	// contradiction. Rejecting prevents an ambiguous inverse lookup.
	BadMasterKey

	// BadEphemeralKey: the incoming manifest's ephemeral key is already
	// known as either an ephemeral or master key in this cache.
	BadEphemeralKey
)

// String returns a debug-friendly label for the disposition.
func (d Disposition) String() string {
	switch d {
	case Accepted:
		return "accepted"
	case Stale:
		return "stale"
	case Invalid:
		return "invalid"
	case BadMasterKey:
		return "bad_master_key"
	case BadEphemeralKey:
		return "bad_ephemeral_key"
	default:
		return "unknown"
	}
}

// Cache stores the latest verified manifest per master key and maintains
// the inverse ephemeral→master lookup so consensus can translate a
// validation's signing key back to a UNL master key.
//
// Safe for concurrent use. Two locks separate the hot read path from
// the slow Apply signature-verification path: `mu` guards the maps,
// `applyMu` serializes ApplyManifest writers around the verify step
// so concurrent Applies cannot both pass the Stale gate and race a
// write while leaving RLock-only lookups unblocked.
type Cache struct {
	mu sync.RWMutex

	// applyMu serializes ApplyManifest's verify-then-mutate sequence
	// without holding c.mu across the secp256k1/ed25519 verify.
	applyMu sync.Mutex

	// byMaster maps master public key → the latest accepted manifest.
	// Entries persist across revocations: a revoked manifest is kept so
	// lookups see Revoked==true and can refuse to treat the master as
	// trusted.
	byMaster map[[33]byte]Manifest

	// signingToMaster maps ephemeral signing key → master key. Cleared
	// when the master rotates (old ephemeral removed) or revokes
	// (entry removed so lookups no longer resolve).
	signingToMaster map[[33]byte][33]byte

	// seq advances on every accepted manifest — both a first-insert for
	// a newly-seen master key and a replacement with a higher-sequence
	// manifest. Downstream emitters use it to invalidate encoded frames.
	seq uint64

	eventMu        sync.Mutex
	subscribers    map[uint64]func(*Manifest)
	nextSubscriber uint64
	events         []acceptedEvent
	dispatching    bool
}

type acceptedEvent struct {
	manifest    *Manifest
	subscribers []uint64
}

// NewCache returns an empty Cache.
func NewCache() *Cache {
	return &Cache{
		byMaster:        make(map[[33]byte]Manifest),
		signingToMaster: make(map[[33]byte][33]byte),
		subscribers:     make(map[uint64]func(*Manifest)),
	}
}

// ApplyManifest ingests a parsed manifest, verifies it, and — if the
// checks pass — stores it atomically. Returns the disposition so the
// caller can decide whether to relay (Accepted) or charge the sender
// (Invalid / BadMasterKey / BadEphemeralKey). Stale is a no-op.
func (c *Cache) ApplyManifest(m *Manifest) Disposition {
	if m == nil {
		return Invalid
	}
	owned, err := Deserialize(m.serialized)
	if err != nil {
		return Invalid
	}

	c.applyMu.Lock()

	c.mu.RLock()
	if existing, ok := c.byMaster[owned.masterKey]; ok && owned.sequence <= existing.sequence {
		c.mu.RUnlock()
		c.applyMu.Unlock()
		return Stale
	}
	c.mu.RUnlock()

	// Verify outside any map lock — GetMasterKey / GetSigningKey
	// lookups proceed unblocked through the (potentially) expensive
	// secp256k1 verify.
	if err := owned.Verify(); err != nil {
		c.applyMu.Unlock()
		return Invalid
	}

	disp := c.applyLocked(owned)
	drain := false
	if disp == Accepted {
		drain = c.enqueueAccepted(owned)
	}
	c.applyMu.Unlock()
	if drain {
		c.drainAccepted()
	}
	return disp
}

func (c *Cache) applyLocked(m *Manifest) Disposition {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check Stale under the write lock against any direct map
	// writer that bypassed applyMu.
	if existing, ok := c.byMaster[m.masterKey]; ok && m.sequence <= existing.sequence {
		return Stale
	}

	// The manifest's master key must not already be recorded as
	// another manifest's ephemeral key — otherwise a subsequent
	// GetMasterKey(m.masterKey) would be ambiguous.
	if _, ok := c.signingToMaster[m.masterKey]; ok {
		return BadMasterKey
	}

	if !m.Revoked() {
		if _, ok := c.signingToMaster[m.signingKey]; ok {
			return BadEphemeralKey
		}
		if _, ok := c.byMaster[m.signingKey]; ok {
			return BadEphemeralKey
		}
	}

	// Drop the previous ephemeral mapping (if any) before installing
	// the new one; otherwise a validation signed with the OLD
	// ephemeral would still resolve to the master after rotation.
	prev, isUpdate := c.byMaster[m.masterKey]
	if isUpdate && !prev.Revoked() {
		delete(c.signingToMaster, prev.signingKey)
	}

	c.byMaster[m.masterKey] = *cloneManifest(m)
	if !m.Revoked() {
		c.signingToMaster[m.signingKey] = m.masterKey
	}
	// Advance on insert, rotation, and revocation so emitters re-encode.
	c.seq++
	return Accepted
}

// SubscribeAccepted registers a subscriber for newly accepted manifests and
// returns an idempotent unsubscribe function. Delivery preserves acceptance
// order, runs after cache locks are released, and isolates subscriber panics.
func (c *Cache) SubscribeAccepted(fn func(*Manifest)) func() {
	if fn == nil {
		return func() {}
	}
	c.eventMu.Lock()
	c.nextSubscriber++
	id := c.nextSubscriber
	c.subscribers[id] = fn
	c.eventMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.eventMu.Lock()
			delete(c.subscribers, id)
			c.eventMu.Unlock()
		})
	}
}

func (c *Cache) enqueueAccepted(m *Manifest) bool {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	if len(c.subscribers) == 0 {
		return false
	}
	ids := make([]uint64, 0, len(c.subscribers))
	for id := range c.subscribers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	c.events = append(c.events, acceptedEvent{manifest: cloneManifest(m), subscribers: ids})
	if c.dispatching {
		return false
	}
	c.dispatching = true
	return true
}

func (c *Cache) drainAccepted() {
	for {
		c.eventMu.Lock()
		if len(c.events) == 0 {
			c.dispatching = false
			c.eventMu.Unlock()
			return
		}
		event := c.events[0]
		c.events[0] = acceptedEvent{}
		c.events = c.events[1:]
		c.eventMu.Unlock()

		for _, id := range event.subscribers {
			c.eventMu.Lock()
			fn := c.subscribers[id]
			c.eventMu.Unlock()
			if fn != nil {
				invokeAccepted(fn, cloneManifest(event.manifest))
			}
		}
	}
}

func invokeAccepted(fn func(*Manifest), m *Manifest) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("manifest subscriber panicked", "panic", recovered)
		}
	}()
	fn(m)
}

// Sequence returns the cache's "something has changed" counter.
// It increments for every accepted insertion, rotation, and revocation.
func (c *Cache) Sequence() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.seq
}

// GetMasterKey returns the master key associated with a signing key.
// If the signing key is not recorded in any manifest, returns the input
// unchanged so callers can use the result directly in UNL lookups.
func (c *Cache) GetMasterKey(signingKey [33]byte) [33]byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.signingToMaster[signingKey]; ok {
		return m
	}
	return signingKey
}

// GetSigningKey returns the current ephemeral signing key for a master
// key. The second return is false when the master is unknown or
// revoked — callers should treat "revoked or unknown" identically.
func (c *Cache) GetSigningKey(masterKey [33]byte) ([33]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.byMaster[masterKey]
	if !ok || m.Revoked() {
		return [33]byte{}, false
	}
	return m.signingKey, true
}

// GetManifest returns the raw serialized manifest bytes for a master
// key. Second return is false when the master is unknown or revoked.
func (c *Cache) GetManifest(masterKey [33]byte) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.byMaster[masterKey]
	if !ok || m.Revoked() {
		return nil, false
	}
	return append([]byte(nil), m.serialized...), true
}

// GetSequence returns the stored manifest's sequence number. Second
// return is false on unknown or revoked.
func (c *Cache) GetSequence(masterKey [33]byte) (uint32, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.byMaster[masterKey]
	if !ok || m.Revoked() {
		return 0, false
	}
	return m.sequence, true
}

// WithCurrent runs fn while the identified non-revoked manifest remains
// current. The callback must be brief and must not call methods on this Cache
// or acquire locks ordered before this cache's mutex.
func (c *Cache) WithCurrent(masterKey, signingKey [33]byte, sequence uint32, fn func()) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.byMaster[masterKey]
	if !ok || m.Revoked() || m.sequence != sequence || m.signingKey != signingKey {
		return false
	}
	fn()
	return true
}

// GetDomain returns the stored manifest's domain string. Second return
// is false on unknown, revoked, or when no domain was recorded.
func (c *Cache) GetDomain(masterKey [33]byte) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.byMaster[masterKey]
	if !ok || m.Revoked() || m.domain == "" {
		return "", false
	}
	return m.domain, true
}

// Revoked reports whether the cached manifest for masterKey is a
// revocation. Unknown masters return false — a master we've never seen
// is not revoked by absence.
func (c *Cache) Revoked(masterKey [33]byte) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.byMaster[masterKey]
	if !ok {
		return false
	}
	return m.Revoked()
}

// MasterToSigning returns a snapshot of the master→signing key map for
// every cached non-revoked manifest.
func (c *Cache) MasterToSigning() map[[33]byte][33]byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.byMaster) == 0 {
		return nil
	}
	out := make(map[[33]byte][33]byte, len(c.byMaster))
	for master, m := range c.byMaster {
		if m.Revoked() {
			continue
		}
		out[master] = m.signingKey
	}
	return out
}

// Snapshot returns independent copies of all cached manifests, including
// revocations.
func (c *Cache) Snapshot() []*Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.byMaster) == 0 {
		return nil
	}
	out := make([]*Manifest, 0, len(c.byMaster))
	for _, m := range c.byMaster {
		manifest := m
		out = append(out, cloneManifest(&manifest))
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].masterKey[:]) < string(out[j].masterKey[:])
	})
	return out
}

// SerializedAll returns the wire bytes of every cached manifest in
// arbitrary order, with each entry defensively copied.
func (c *Cache) SerializedAll() [][]byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.byMaster) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(c.byMaster))
	for _, m := range c.byMaster {
		if len(m.serialized) == 0 {
			continue
		}
		out = append(out, append([]byte(nil), m.serialized...))
	}
	return out
}
