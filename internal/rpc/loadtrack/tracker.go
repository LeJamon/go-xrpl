// Package loadtrack implements a per-client-IP load tracker that
// mirrors rippled's Resource::Manager / LoadFeeTrack approach: each
// inbound RPC method is assigned a Charge (a numeric cost), the cost
// accumulates against a per-IP balance, balances use an integer decaying sample
// over time, and a balance crossing a warning / drop threshold causes
// the next request to be slowed or rejected.
//
// References:
//   - rippled/include/xrpl/resource/Fees.h (charge catalogue)
//   - rippled/include/xrpl/resource/detail/Tuning.h (thresholds + decay)
//   - rippled/include/xrpl/resource/Gossip.h
//   - rippled/include/xrpl/resource/detail/Logic.h
//     (exportConsumers / importConsumers)
//
// Cross-server load-share is implemented via Gossip: a node exports a
// snapshot of its high-load consumers and peers import each other's
// snapshots so a misbehaving client cannot fan out across the network
// to dodge per-node rate limits. The threshold check uses the combined
// (local + remote) balance, matching rippled Entry.h:74.
package loadtrack

import (
	"sync"
	"time"
)

// Charge buckets — values copied from rippled Fees.cpp.
const (
	ChargeReference uint32 = 20
	ChargeMedium    uint32 = 400
	ChargeHeavy     uint32 = 3000
	ChargeMalformed uint32 = 100
	ChargeException uint32 = 100
	ChargeWarning   uint32 = 4000
	ChargeDrop      uint32 = 6000
)

// LoadKind names the cost bucket a handler falls into. The numeric
// value is the charge applied per invocation.
type LoadKind uint32

const (
	// LoadReference is the default — a lightweight RPC (ping, fee, server_info).
	LoadReference LoadKind = LoadKind(ChargeReference)
	// LoadMedium is a moderately expensive RPC that does ledger work.
	LoadMedium LoadKind = LoadKind(ChargeMedium)
	// LoadHeavy is a very expensive RPC, such as pathfinding or signing.
	LoadHeavy LoadKind = LoadKind(ChargeHeavy)
	// LoadMalformed is charged at malformed transport and authorization boundaries.
	LoadMalformed LoadKind = LoadKind(ChargeMalformed)
	// LoadException is charged when dispatch recovers a handler panic.
	// Numerically equal to LoadMalformed but reported as a distinct
	// label, mirroring rippled's separate feeExceptionRPC charge
	// (Fees.cpp).
	LoadException LoadKind = LoadKind(ChargeException)
)

// Thresholds — copied from rippled Tuning.h.
const (
	WarningThreshold = 5000
	DropThreshold    = 25000
	// DecayWindow is the denominator and update interval used by the
	// integer decaying sample.
	DecayWindow = 32 * time.Second
	// EntryExpiration is the LRU eviction deadline — entries that
	// haven't been touched for this long are dropped from the map.
	EntryExpiration = 5 * time.Minute
	// MinimumGossipBalance is the floor a (decayed) local balance must
	// clear before the consumer is included in an Export(); matches
	// rippled Tuning.h:44.
	MinimumGossipBalance = 1000
	// GossipExpiration is the lifetime of an imported snapshot from
	// any single peer origin; matches rippled Tuning.h:51 (30s).
	GossipExpiration = 30 * time.Second
)

// Outcome reports the load tracker's verdict for a single charge.
type Outcome int

const (
	// OutcomeOK means the request may proceed without warning.
	OutcomeOK Outcome = iota
	// OutcomeWarn means the request may proceed but the client is at the
	// warning threshold. Call Warn to decide whether to notify the client.
	OutcomeWarn
	// OutcomeDrop means the client is at the drop threshold. Admission
	// paths should use Disconnect before dispatch instead of this outcome.
	OutcomeDrop
)

type entry struct {
	// balance is the raw locally accumulated decaying sample. Thresholds
	// use its normalized value, balance / decayWindowSeconds.
	balance int64
	// remoteBalance is the sum of imported gossip contributions for
	// this key across all peer origins — mirrors
	// rippled Entry.remote_balance. Not decayed; it changes only on
	// import refresh / expiration.
	remoteBalance   int
	updated         time.Time
	lastSeen        time.Time
	lastWarningTime time.Time
}

// Gossip is the snapshot exchanged with peers — see
// rippled/include/xrpl/resource/Gossip.h.
//
// NOTE on wire compatibility: rippled's Gossip::Item keys consumers by
// `beast::IP::Endpoint`; go-xrpl keys by an opaque string (a client IP
// today). The two are equivalent in spirit but not in bytes, so a
// future peer-protocol message that carries a Gossip across the wire
// will need to normalise the key to whatever shape rippled emits before
// these snapshots can be round-tripped between implementations.
type Gossip struct {
	Items []GossipItem
}

// GossipItem describes one consumer's local balance, keyed by the
// addressing string the local node uses (a client IP, in practice).
type GossipItem struct {
	Key     string
	Balance int
}

// importRecord remembers the per-key contributions of the most recent
// import from a given origin so they can be subtracted when a fresh
// snapshot arrives — mirrors rippled detail/Import.h.
type importRecord struct {
	whenExpires time.Time
	items       map[string]int
}

// Tracker is the per-IP load accountant. Construct one with New.
type Tracker struct {
	mu          sync.Mutex
	now         func() time.Time
	clockOrigin time.Time
	entries     map[string]*entry
	imports     map[string]*importRecord

	// lastSweep is updated by sweep() to amortise the cost of LRU
	// eviction across Charge() calls.
	lastSweep time.Time
}

// New returns a Tracker that reads the wall clock.
func New() *Tracker {
	now := time.Now
	return &Tracker{
		now:         now,
		clockOrigin: now(),
		entries:     make(map[string]*entry),
		imports:     make(map[string]*importRecord),
	}
}

// newWithClock is used by tests to inject a fake clock.
func newWithClock(now func() time.Time) *Tracker {
	return &Tracker{
		now:         now,
		clockOrigin: now(),
		entries:     make(map[string]*entry),
		imports:     make(map[string]*importRecord),
	}
}

func (t *Tracker) currentTime() time.Time {
	return t.clockOrigin.Add(t.now().Sub(t.clockOrigin).Truncate(time.Second))
}

// Charge debits the configured charge against key (typically a client
// IP) and returns the resulting threshold state. An empty key is
// untracked and always returns OutcomeOK.
//
// The Outcome return is retained for compatibility. RPC transports should
// call Disconnect before dispatch, call Charge after dispatch, and then call
// Warn; those operations apply the additional drop and warning fees.
func (t *Tracker) Charge(key string, kind LoadKind) Outcome {
	if key == "" {
		return OutcomeOK
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.currentTime()
	t.expireImportsLocked(now)
	e, ok := t.entries[key]
	if !ok {
		e = &entry{}
		t.entries[key] = e
	}
	t.decayLocked(e, now)
	e.balance += int64(kind)
	e.updated = now
	e.lastSeen = now
	if now.Sub(t.lastSweep) >= EntryExpiration {
		t.sweepLocked(now)
		t.lastSweep = now
	}
	combined := combinedBalance(e)
	switch {
	case combined >= DropThreshold:
		return OutcomeDrop
	case combined >= WarningThreshold:
		return OutcomeWarn
	default:
		return OutcomeOK
	}
}

// Warn reports whether a load warning should be sent for key. At most one
// warning is returned for a given clock instant. Each emitted warning adds
// ChargeWarning to the local balance. Callers responsible for unlimited
// consumers must bypass this operation; an empty key is never limited.
func (t *Tracker) Warn(key string) bool {
	if key == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.currentTime()
	t.expireImportsLocked(now)
	e, ok := t.entries[key]
	if !ok {
		return false
	}
	t.decayLocked(e, now)
	if combinedBalance(e) < WarningThreshold || now.Equal(e.lastWarningTime) {
		return false
	}
	e.balance += int64(ChargeWarning)
	e.updated = now
	e.lastSeen = now
	e.lastWarningTime = now
	return true
}

// Disconnect reports whether key is at or above the drop threshold. Each
// true result adds ChargeDrop to the local balance so a dropped consumer
// cannot immediately reconnect. Callers responsible for unlimited consumers
// must bypass this operation; an empty key is never limited.
func (t *Tracker) Disconnect(key string) bool {
	if key == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.currentTime()
	t.expireImportsLocked(now)
	e, ok := t.entries[key]
	if !ok {
		return false
	}
	t.decayLocked(e, now)
	if combinedBalance(e) < DropThreshold {
		return false
	}
	e.balance += int64(ChargeDrop)
	e.updated = now
	e.lastSeen = now
	return true
}

// Balance reports the current combined (local + remote) balance for a
// key. Returns 0 if the key is unknown.
func (t *Tracker) Balance(key string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.currentTime()
	t.expireImportsLocked(now)
	e, ok := t.entries[key]
	if !ok {
		return 0
	}
	t.decayLocked(e, now)
	return float64(combinedBalance(e))
}

// LocalBalance reports just the locally-decayed component, ignoring
// any remote gossip contributions. Useful for diagnostics and for
// constructing Export() snapshots.
func (t *Tracker) LocalBalance(key string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.currentTime()
	t.expireImportsLocked(now)
	e, ok := t.entries[key]
	if !ok {
		return 0
	}
	t.decayLocked(e, now)
	return float64(localBalance(e))
}

// OverDropThreshold reports whether the combined balance for key is already
// at or above DropThreshold without applying ChargeDrop. New admission paths
// should use Disconnect. An empty key is never over-threshold.
func (t *Tracker) OverDropThreshold(key string) bool {
	if key == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.currentTime()
	t.expireImportsLocked(now)
	e, ok := t.entries[key]
	if !ok {
		return false
	}
	t.decayLocked(e, now)
	return combinedBalance(e) >= DropThreshold
}

// Reset removes a key from the tracker; used by tests.
func (t *Tracker) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// Export returns a snapshot of every consumer whose decayed local
// balance is at or above MinimumGossipBalance. Mirrors rippled
// Logic.h:256-278 exportConsumers().
//
// Divergence from rippled: rippled's exportConsumers iterates only its
// `inbound_` list, deliberately omitting outbound and admin endpoints
// so a node never advertises its own outbound peering as if it were
// remote client load. go-xrpl's tracker currently has no
// inbound/outbound/admin distinction — every entry is treated as a
// client-IP key — so iterating `t.entries` is the natural Go analogue.
// When the tracker grows separate "kinds" (e.g. when peer-connection
// charging starts sharing this surface), `Export` will need to filter
// to the inbound kind to stay faithful.
//
// Empty-key entries are skipped to mirror the symmetric filter in
// Import: we never emit a key shape we would refuse to absorb.
func (t *Tracker) Export() Gossip {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.currentTime()
	t.expireImportsLocked(now)
	g := Gossip{}
	for k, e := range t.entries {
		if k == "" {
			continue
		}
		t.decayLocked(e, now)
		balance := localBalance(e)
		if balance >= MinimumGossipBalance {
			g.Items = append(g.Items, GossipItem{Key: k, Balance: int(balance)})
		}
	}
	return g
}

// Import absorbs a peer's exported snapshot, tagged by origin so a
// subsequent Import from the same origin replaces (rather than
// double-counts) the prior contribution. Mirrors rippled
// Logic.h:282-336 importConsumers().
//
// Deliberate hardening over rippled: items with an empty key or a
// non-positive balance are dropped rather than admitted (rippled
// silently accepts both, on the assumption that its IP::Endpoint and
// signed-int balance fields are always trustworthy). Export is filtered
// symmetrically so the two surfaces stay self-consistent.
func (t *Tracker) Import(origin string, gossip Gossip) {
	if origin == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.currentTime()
	t.expireImportsLocked(now)

	if prev, ok := t.imports[origin]; ok {
		for k, bal := range prev.items {
			if e, ok := t.entries[k]; ok {
				e.remoteBalance -= bal
				if e.remoteBalance < 0 {
					e.remoteBalance = 0
				}
			}
		}
	}

	rec := &importRecord{
		whenExpires: now.Add(GossipExpiration),
		items:       make(map[string]int, len(gossip.Items)),
	}
	for _, item := range gossip.Items {
		if item.Key == "" || item.Balance <= 0 {
			continue
		}
		e, ok := t.entries[item.Key]
		if !ok {
			e = &entry{updated: now, lastSeen: now}
			t.entries[item.Key] = e
		}
		e.remoteBalance += item.Balance
		e.lastSeen = now
		rec.items[item.Key] += item.Balance
	}
	t.imports[origin] = rec
}

// expireImportsLocked drops any importRecord past its whenExpires
// deadline, refunding the per-entry remote balance on the way out.
// Mirrors rippled periodicActivity / import expiry. Caller must hold
// t.mu.
func (t *Tracker) expireImportsLocked(now time.Time) {
	for origin, rec := range t.imports {
		if now.Before(rec.whenExpires) {
			continue
		}
		for k, bal := range rec.items {
			if e, ok := t.entries[k]; ok {
				e.remoteBalance -= bal
				if e.remoteBalance < 0 {
					e.remoteBalance = 0
				}
			}
		}
		delete(t.imports, origin)
	}
}

const decayWindowSeconds int64 = int64(DecayWindow / time.Second)

func localBalance(e *entry) int64 {
	return e.balance / decayWindowSeconds
}

func combinedBalance(e *entry) int64 {
	return localBalance(e) + int64(e.remoteBalance)
}

// decayLocked applies the integer decaying-sample update used by rippled.
// Caller must hold t.mu.
func (t *Tracker) decayLocked(e *entry, now time.Time) {
	if e.updated.IsZero() || e.balance == 0 {
		e.updated = now
		return
	}
	dt := now.Sub(e.updated)
	if dt <= 0 {
		return
	}
	elapsed := int64(dt / time.Second)
	if elapsed > 4*decayWindowSeconds {
		e.balance = 0
	} else {
		for range elapsed {
			e.balance -= (e.balance + decayWindowSeconds - 1) / decayWindowSeconds
			if e.balance == 0 {
				break
			}
		}
	}
	e.updated = now
}

// sweepLocked evicts entries idle longer than EntryExpiration. Caller
// must hold t.mu. Walks the whole map; cheap because entries are at
// most a few thousand IPs in practice.
func (t *Tracker) sweepLocked(now time.Time) {
	for k, e := range t.entries {
		if now.Sub(e.lastSeen) >= EntryExpiration {
			delete(t.entries, k)
		}
	}
}
