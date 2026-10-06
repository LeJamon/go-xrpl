package shamap

import (
	"errors"
	"sync"
	"sync/atomic"
)

const defaultAcquisitionBudgetBytes = 512 << 20

// ErrAcquisitionMemory reports a resumable acquisition walk that could not
// admit more cursor work under its shared byte budget. It is deliberately
// distinct from ErrTraversalBudget: callers should make room (usually by
// flushing and acknowledging persisted replies) before retrying this walk.
var ErrAcquisitionMemory = errors.New("SHAMap acquisition memory budget exhausted")

// AcquisitionBudgetStats is a point-in-time view of a shared acquisition
// budget. Used and Peak include reservations made by every owner attached to
// the budget, including SHAMap cursors and persistence staging.
type AcquisitionBudgetStats struct {
	Limit      uint64
	Used       uint64
	Peak       uint64
	CursorUsed uint64
	CursorPeak uint64
	LoadedUsed uint64
	LoadedPeak uint64
}

// AcquisitionMemoryAccount is the narrow parent-account contract used by
// SHAMap acquisition. The account owns the node-wide allowance; this package
// only asks whether a charge fits and returns it when the associated owner is
// gone. Changed is a level-triggered wakeup for callers that must retry after
// another owner releases memory.
type AcquisitionMemoryAccount interface {
	TryReserve(uint64) bool
	Release(uint64)
	Changed() <-chan struct{}
}

// AcquisitionBudget coordinates bounded resident memory across maps and their
// acquisition owners. A reservation is only a conservative accounting charge;
// callers remain responsible for releasing exactly the bytes they reserved.
// A zero requested limit is replaced with the bounded default.
type AcquisitionBudget struct {
	mu         sync.Mutex
	limit      uint64
	parent     AcquisitionMemoryAccount
	changed    chan struct{}
	used       atomic.Uint64
	peak       atomic.Uint64
	cursorUsed atomic.Uint64
	cursorPeak atomic.Uint64
	loadedUsed atomic.Uint64
	loadedPeak atomic.Uint64
}

// NewAcquisitionBudget creates a shared byte budget. The limit is immutable so
// all participating owners observe the same admission rule.
func NewAcquisitionBudget(limit uint64) *AcquisitionBudget {
	return newAcquisitionBudget(limit, nil)
}

// NewAcquisitionBudgetWithParent creates an acquisition budget with the same
// local cursor/loaded limits as NewAcquisitionBudget while charging every
// reservation to parent as well. A failed parent admission never consumes a
// local credit, and a successful local admission is always rolled back if the
// parent cannot be charged.
//
// A zero limit retains the standalone constructor's bounded default. Callers
// that want the parent account to provide the complete ceiling should pass a
// local limit large enough for that account; the parent remains the final
// node-wide admission authority.
func NewAcquisitionBudgetWithParent(limit uint64, parent AcquisitionMemoryAccount) *AcquisitionBudget {
	return newAcquisitionBudget(limit, parent)
}

func newAcquisitionBudget(limit uint64, parent AcquisitionMemoryAccount) *AcquisitionBudget {
	if limit == 0 {
		limit = defaultAcquisitionBudgetBytes
	}
	return &AcquisitionBudget{limit: limit, parent: parent, changed: make(chan struct{})}
}

// Changed returns a wakeup channel that is closed and replaced whenever this
// budget releases memory. Parent-backed budgets return the parent's channel so
// waits also wake when another sibling account returns shared capacity.
func (b *AcquisitionBudget) Changed() <-chan struct{} {
	if b == nil {
		return nil
	}
	if b.parent != nil {
		return b.parent.Changed()
	}
	b.mu.Lock()
	changed := b.changed
	b.mu.Unlock()
	return changed
}

func (b *AcquisitionBudget) signalChangedLocked() {
	if b.parent != nil || b.changed == nil {
		return
	}
	close(b.changed)
	b.changed = make(chan struct{})
}

// TryReserve admits bytes if the reservation fits within the budget.
func (b *AcquisitionBudget) TryReserve(bytes uint64) bool {
	if b == nil || bytes == 0 {
		return b != nil || bytes == 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.tryReserveGeneralLocked(bytes) {
		return false
	}
	return true
}

func (b *AcquisitionBudget) tryReserveGeneralLocked(bytes uint64) bool {
	used := b.used.Load()
	if used > b.limit || bytes > b.limit-used {
		return false
	}
	if b.parent != nil && !b.parent.TryReserve(bytes) {
		return false
	}
	b.used.Store(used + bytes)
	for peak := b.peak.Load(); used+bytes > peak && !b.peak.CompareAndSwap(peak, used+bytes); peak = b.peak.Load() {
	}
	return true
}

// Release returns a previous reservation to the budget. Releasing more bytes
// than are currently reserved indicates an ownership bug and panics rather
// than stealing another owner's credits.
func (b *AcquisitionBudget) Release(bytes uint64) {
	if b == nil || bytes == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	used := b.used.Load()
	if bytes > used {
		panic("shamap: acquisition budget release exceeds reservation")
	}
	b.used.Store(used - bytes)
	if b.parent != nil {
		b.parent.Release(bytes)
	}
	b.signalChangedLocked()
}

// Snapshot returns the current shared-budget counters.
func (b *AcquisitionBudget) Snapshot() AcquisitionBudgetStats {
	if b == nil {
		return AcquisitionBudgetStats{}
	}
	return AcquisitionBudgetStats{
		Limit:      b.limit,
		Used:       b.used.Load(),
		Peak:       b.peak.Load(),
		CursorUsed: b.cursorUsed.Load(),
		CursorPeak: b.cursorPeak.Load(),
		LoadedUsed: b.loadedUsed.Load(),
		LoadedPeak: b.loadedPeak.Load(),
	}
}

const acquisitionCursorBudgetFraction = 4

func (b *AcquisitionBudget) tryReserveCursor(bytes uint64) bool {
	if b == nil || bytes == 0 {
		return b == nil || bytes == 0
	}
	limit := b.limit / acquisitionCursorBudgetFraction
	b.mu.Lock()
	defer b.mu.Unlock()
	cursorUsed := b.cursorUsed.Load()
	if cursorUsed > limit || bytes > limit-cursorUsed {
		return false
	}
	if !b.tryReserveGeneralLocked(bytes) {
		return false
	}
	b.cursorUsed.Store(cursorUsed + bytes)
	for peak := b.cursorPeak.Load(); cursorUsed+bytes > peak && !b.cursorPeak.CompareAndSwap(peak, cursorUsed+bytes); peak = b.cursorPeak.Load() {
	}
	return true
}

func (b *AcquisitionBudget) releaseCursor(bytes uint64) {
	if b == nil || bytes == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	cursorUsed := b.cursorUsed.Load()
	if bytes > cursorUsed {
		panic("shamap: acquisition cursor release exceeds reservation")
	}
	used := b.used.Load()
	if bytes > used {
		panic("shamap: acquisition cursor release exceeds reservation")
	}
	b.cursorUsed.Store(cursorUsed - bytes)
	b.used.Store(used - bytes)
	if b.parent != nil {
		b.parent.Release(bytes)
	}
	b.signalChangedLocked()
}
