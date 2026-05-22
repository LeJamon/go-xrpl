// Package loadtrack implements a per-client-IP load tracker that
// mirrors rippled's Resource::Manager / LoadFeeTrack approach: each
// inbound RPC method is assigned a Charge (a numeric cost), the cost
// accumulates against a per-IP balance, balances decay exponentially
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
	"math"
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
)

// LoadKind names the cost bucket a handler falls into. The numeric
// value is the charge applied per invocation.
type LoadKind uint32

const (
	// LoadReference is the default — a lightweight RPC (ping, fee, server_info).
	LoadReference LoadKind = LoadKind(ChargeReference)
	// LoadMedium is a moderately expensive RPC that does ledger work
	// (account_lines, gateway_balances, book_offers).
	LoadMedium LoadKind = LoadKind(ChargeMedium)
	// LoadHeavy is a very expensive RPC: pathfinding, account_tx scans,
	// large ledger_data dumps.
	LoadHeavy LoadKind = LoadKind(ChargeHeavy)
	// LoadMalformed is charged for invalidParams / methodNotFound — bad
	// input that should not be a cheap probe (rippled feeMalformedRPC).
	LoadMalformed LoadKind = LoadKind(ChargeMalformed)
	// LoadException is charged when a handler returns rpcINTERNAL.
	// Numerically equal to LoadMalformed but reported as a distinct
	// label, mirroring rippled's separate feeExceptionRPC charge
	// (Fees.cpp).
	LoadException LoadKind = LoadKind(ChargeException)
)

// Thresholds — copied from rippled Tuning.h.
const (
	WarningThreshold = 5000
	DropThreshold    = 25000
	// DecayWindow is the exponential half-life window used to decay
	// the per-IP balance toward zero.
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
	// OutcomeWarn means the request may proceed but the client has
	// crossed the warning threshold — callers should attach a
	// "warning" envelope to the response (rippled's feeWarning emit).
	OutcomeWarn
	// OutcomeDrop means the request must be rejected with rpcSlowDown;
	// the client has crossed the drop threshold.
	OutcomeDrop
)

type entry struct {
	// balance is the locally accumulated, exponentially-decayed
	// charge — mirrors rippled Entry.local_balance.
	balance float64
	// remoteBalance is the sum of imported gossip contributions for
	// this key across all peer origins — mirrors
	// rippled Entry.remote_balance. Not decayed; it changes only on
	// import refresh / expiration.
	remoteBalance int
	updated       time.Time
	lastSeen      time.Time
}

// Gossip is the snapshot exchanged with peers — see
// rippled/include/xrpl/resource/Gossip.h.
//
// NOTE on wire compatibility: rippled's Gossip::Item keys consumers by
// `beast::IP::Endpoint`; goXRPL keys by an opaque string (a client IP
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

// Tracker is the per-IP load accountant. The zero value is ready to
// use but callers should typically use New() so the clock can be set.
type Tracker struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]*entry
	imports map[string]*importRecord

	// lastSweep is updated by sweep() to amortise the cost of LRU
	// eviction across Charge() calls.
	lastSweep time.Time
}

// New returns a Tracker that reads the wall clock.
func New() *Tracker {
	return &Tracker{
		now:     time.Now,
		entries: make(map[string]*entry),
		imports: make(map[string]*importRecord),
	}
}

// newWithClock is used by tests to inject a fake clock.
func newWithClock(now func() time.Time) *Tracker {
	return &Tracker{
		now:     now,
		entries: make(map[string]*entry),
		imports: make(map[string]*importRecord),
	}
}

// Charge debits the configured charge against key (typically a client
// IP) and returns the verdict. An empty key is treated as anonymous /
// untracked and always returns OutcomeOK so unit-test fixtures that
// supply no ClientIP keep working.
//
// The threshold check uses local + remote balance, matching rippled
// Entry.h:74's balance(now) = local_balance.value(now) + remote_balance.
func (t *Tracker) Charge(key string, kind LoadKind) Outcome {
	if key == "" {
		return OutcomeOK
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e, ok := t.entries[key]
	if !ok {
		e = &entry{}
		t.entries[key] = e
	}
	t.decayLocked(e, now)
	e.balance += float64(kind)
	e.updated = now
	e.lastSeen = now
	if now.Sub(t.lastSweep) >= EntryExpiration {
		t.sweepLocked(now)
		t.lastSweep = now
	}
	t.expireImportsLocked(now)
	combined := e.balance + float64(e.remoteBalance)
	switch {
	case combined >= DropThreshold:
		return OutcomeDrop
	case combined >= WarningThreshold:
		return OutcomeWarn
	default:
		return OutcomeOK
	}
}

// Balance reports the current combined (local + remote) balance for a
// key. Returns 0 if the key is unknown.
func (t *Tracker) Balance(key string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		return 0
	}
	t.decayLocked(e, t.now())
	return e.balance + float64(e.remoteBalance)
}

// LocalBalance reports just the locally-decayed component, ignoring
// any remote gossip contributions. Useful for diagnostics and for
// constructing Export() snapshots.
func (t *Tracker) LocalBalance(key string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		return 0
	}
	t.decayLocked(e, t.now())
	return e.balance
}

// OverDropThreshold reports whether the combined balance for key is
// already at or above DropThreshold. Used by the pre-dispatch gate to
// reject before the handler runs — mirrors rippled's
// Resource::Consumer::disconnect() check at ServerHandler.cpp:735.
// An empty key is treated as anonymous and is never over-threshold.
func (t *Tracker) OverDropThreshold(key string) bool {
	if key == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		return false
	}
	t.decayLocked(e, t.now())
	return e.balance+float64(e.remoteBalance) >= DropThreshold
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
// remote client load. goXRPL's tracker currently has no
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
	now := t.now()
	g := Gossip{}
	for k, e := range t.entries {
		if k == "" {
			continue
		}
		t.decayLocked(e, now)
		if e.balance >= MinimumGossipBalance {
			g.Items = append(g.Items, GossipItem{Key: k, Balance: int(e.balance)})
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
	now := t.now()

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
		rec.items[item.Key] = item.Balance
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

// decayLocked applies an exponential decay with half-life DecayWindow
// to e.balance based on the elapsed time since e.updated. Caller must
// hold t.mu.
func (t *Tracker) decayLocked(e *entry, now time.Time) {
	if e.updated.IsZero() || e.balance == 0 {
		e.updated = now
		return
	}
	dt := now.Sub(e.updated)
	if dt <= 0 {
		return
	}
	// Exponential decay with half-life = DecayWindow.
	factor := math.Pow(0.5, dt.Seconds()/DecayWindow.Seconds())
	e.balance *= factor
	if e.balance < 1 {
		e.balance = 0
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
