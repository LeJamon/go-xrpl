package manifest

import (
	"sync"
)

// Disposition reports the outcome of ApplyManifest. Matches rippled's
// ManifestDisposition enum in Manifest.h.
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
	// contradiction. Rejecting prevents a confusing ambiguity in
	// signingToMasterKeys. Rippled Manifest.cpp:436-444.
	BadMasterKey

	// BadEphemeralKey: the incoming manifest's ephemeral key collides
	// with either an ephemeral or master key that the cache already
	// knows for a different master. Rippled Manifest.cpp:459-477.
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
// Safe for concurrent use.
type Cache struct {
	mu sync.RWMutex

	// byMaster maps master public key → the latest accepted manifest.
	// Entries persist across revocations: a revoked manifest is kept so
	// lookups see Revoked==true and can refuse to treat the master as
	// trusted.
	byMaster map[[33]byte]*Manifest

	// signingToMaster maps ephemeral signing key → master key. Cleared
	// when the master rotates (old ephemeral removed) or revokes
	// (entry removed so lookups no longer resolve).
	signingToMaster map[[33]byte][33]byte
}

// NewCache returns an empty Cache.
func NewCache() *Cache {
	return &Cache{
		byMaster:        make(map[[33]byte]*Manifest),
		signingToMaster: make(map[[33]byte][33]byte),
	}
}

// ApplyManifest ingests a parsed manifest, verifies it, and — if the
// checks pass — stores it atomically. Returns the disposition so the
// caller can decide whether to relay (Accepted) or charge the sender
// (Invalid / BadMasterKey / BadEphemeralKey). Stale is a no-op.
//
// Mirrors ManifestCache::applyManifest at rippled Manifest.cpp:382-580,
// collapsed to a single write-lock path — the two-phase read/write
// optimization there exists because signature verification is
// expensive; in our deployments the expected rate of inbound manifests
// is too low to matter.
func (c *Cache) ApplyManifest(m *Manifest) Disposition {
	if m == nil {
		return Invalid
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.byMaster[m.MasterKey]; ok {
		if m.Sequence <= existing.Sequence {
			return Stale
		}
	}

	// Signature verification happens under the lock so concurrent
	// callers can't race in two manifests for the same master at the
	// same sequence. Cost is bounded: O(manifests-per-second) × O(one
	// ed25519/secp256k1 verify).
	if err := m.Verify(); err != nil {
		return Invalid
	}

	// The manifest's master key must not already be recorded as
	// another manifest's ephemeral key — otherwise a subsequent
	// getMasterKey(m.MasterKey) would be ambiguous.
	if other, ok := c.signingToMaster[m.MasterKey]; ok && other != m.MasterKey {
		return BadMasterKey
	}

	if !m.Revoked() {
		// The ephemeral key must not already be used as ANOTHER
		// master's ephemeral key (rippled Manifest.cpp:459-468).
		if other, ok := c.signingToMaster[m.SigningKey]; ok && other != m.MasterKey {
			return BadEphemeralKey
		}
		// Nor may it collide with a known master key (rippled
		// Manifest.cpp:470-477).
		if _, ok := c.byMaster[m.SigningKey]; ok {
			return BadEphemeralKey
		}
	}

	// Drop the previous ephemeral mapping (if any) before installing
	// the new one; otherwise a validation signed with the OLD
	// ephemeral would still resolve to the master after rotation.
	if prev, ok := c.byMaster[m.MasterKey]; ok {
		if !prev.Revoked() {
			delete(c.signingToMaster, prev.SigningKey)
		}
	}

	c.byMaster[m.MasterKey] = m
	if !m.Revoked() {
		c.signingToMaster[m.SigningKey] = m.MasterKey
	}
	return Accepted
}

// GetMasterKey returns the master key associated with a signing key.
// If the signing key is not recorded in any manifest, returns the input
// unchanged — matching rippled's ManifestCache::getMasterKey
// (Manifest.cpp:322-332), which lets callers use the return value
// directly in UNL lookups: a non-validator peer's pubkey maps to itself.
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
// revoked — callers should treat "revoked or unknown" identically
// (rippled Manifest.cpp:310-320 returns the input key itself in that
// case, but the caller contexts here — RPC and consensus — want to
// distinguish "have a valid mapping" from "don't").
func (c *Cache) GetSigningKey(masterKey [33]byte) ([33]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.byMaster[masterKey]
	if !ok || m.Revoked() {
		return [33]byte{}, false
	}
	return m.SigningKey, true
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
	return append([]byte(nil), m.Serialized...), true
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
	return m.Sequence, true
}

// GetDomain returns the stored manifest's domain string. Second return
// is false on unknown, revoked, or when no domain was recorded.
func (c *Cache) GetDomain(masterKey [33]byte) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.byMaster[masterKey]
	if !ok || m.Revoked() || m.Domain == "" {
		return "", false
	}
	return m.Domain, true
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

// SerializedAll returns the wire bytes of every cached manifest in
// arbitrary order, with each entry defensively copied. Used by the
// post-handshake TMManifests emission to gossip the entire aggregated
// cache to a freshly-connected peer, mirroring rippled's
// OverlayImpl::getManifestsMessage which calls
// ValidatorManifests::for_each_manifest (Manifest.cpp:1184-1212).
func (c *Cache) SerializedAll() [][]byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.byMaster) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(c.byMaster))
	for _, m := range c.byMaster {
		if len(m.Serialized) == 0 {
			continue
		}
		out = append(out, append([]byte(nil), m.Serialized...))
	}
	return out
}
