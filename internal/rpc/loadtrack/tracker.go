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
//
// The implementation here is intentionally smaller than rippled's
// (no gossip, no inbound/outbound endpoint distinction) — it is the
// minimum needed so that path_find / account_tx / ripple_path_find
// cannot be hammered at the same cost as ping.
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
	// LoadMalformed is charged when the request itself was malformed so a
	// client cannot use bad input as a cheap probe.
	LoadMalformed LoadKind = LoadKind(ChargeMalformed)
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
	balance  float64
	updated  time.Time
	lastSeen time.Time
}

// Tracker is the per-IP load accountant. The zero value is ready to
// use but callers should typically use New() so the clock can be set.
type Tracker struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]*entry

	// lastSweep is updated by sweep() to amortise the cost of LRU
	// eviction across Charge() calls.
	lastSweep time.Time
}

// New returns a Tracker that reads the wall clock.
func New() *Tracker {
	return &Tracker{
		now:     time.Now,
		entries: make(map[string]*entry),
	}
}

// newWithClock is used by tests to inject a fake clock.
func newWithClock(now func() time.Time) *Tracker {
	return &Tracker{now: now, entries: make(map[string]*entry)}
}

// Charge debits the configured charge against key (typically a client
// IP) and returns the verdict. An empty key is treated as anonymous /
// untracked and always returns OutcomeOK so unit-test fixtures that
// supply no ClientIP keep working.
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
	switch {
	case e.balance >= DropThreshold:
		return OutcomeDrop
	case e.balance >= WarningThreshold:
		return OutcomeWarn
	default:
		return OutcomeOK
	}
}

// Balance reports the current balance for a key (after decay applied
// up to "now"). Returns 0 if the key is unknown.
func (t *Tracker) Balance(key string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		return 0
	}
	t.decayLocked(e, t.now())
	return e.balance
}

// OverDropThreshold reports whether the (decayed) balance for key is
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
	return e.balance >= DropThreshold
}

// Reset removes a key from the tracker; used by tests.
func (t *Tracker) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
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
