package shamap

import (
	"errors"
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

// AcquisitionBudget coordinates bounded resident memory across maps and their
// acquisition owners. A reservation is only a conservative accounting charge;
// callers remain responsible for releasing exactly the bytes they reserved.
// A zero requested limit is replaced with the bounded default.
type AcquisitionBudget struct {
	limit       uint64
	used        atomic.Uint64
	peak        atomic.Uint64
	cursorUsed  atomic.Uint64
	pendingUsed atomic.Uint64
	cursorPeak  atomic.Uint64
	loadedUsed  atomic.Uint64
	loadedPeak  atomic.Uint64
}

// NewAcquisitionBudget creates a shared byte budget. The limit is immutable so
// all participating owners observe the same admission rule.
func NewAcquisitionBudget(limit uint64) *AcquisitionBudget {
	if limit == 0 {
		limit = defaultAcquisitionBudgetBytes
	}
	return &AcquisitionBudget{limit: limit}
}

// TryReserve admits bytes if the reservation fits within the budget.
func (b *AcquisitionBudget) TryReserve(bytes uint64) bool {
	if b == nil || bytes == 0 {
		return b != nil || bytes == 0
	}
	for {
		used := b.used.Load()
		if used > b.limit || bytes > b.limit-used {
			return false
		}
		if b.used.CompareAndSwap(used, used+bytes) {
			for {
				peak := b.peak.Load()
				if used+bytes <= peak || b.peak.CompareAndSwap(peak, used+bytes) {
					break
				}
			}
			return true
		}
	}
}

// Release returns a previous reservation to the budget. Releasing more bytes
// than are currently reserved indicates an ownership bug and panics rather
// than stealing another owner's credits.
func (b *AcquisitionBudget) Release(bytes uint64) {
	if b == nil || bytes == 0 {
		return
	}
	for {
		used := b.used.Load()
		if bytes > used {
			panic("shamap: acquisition budget release exceeds reservation")
		}
		if b.used.CompareAndSwap(used, used-bytes) {
			return
		}
	}
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
	for {
		cursorUsed := b.cursorUsed.Load()
		if cursorUsed > limit || bytes > limit-cursorUsed {
			return false
		}
		used := b.used.Load()
		if used > b.limit || bytes > b.limit-used {
			return false
		}
		if !b.cursorUsed.CompareAndSwap(cursorUsed, cursorUsed+bytes) {
			continue
		}
		if !b.used.CompareAndSwap(used, used+bytes) {
			b.cursorUsed.Add(^(bytes - 1))
			continue
		}
		for {
			peak := b.peak.Load()
			if used+bytes <= peak || b.peak.CompareAndSwap(peak, used+bytes) {
				break
			}
		}
		for {
			peak := b.cursorPeak.Load()
			if cursorUsed+bytes <= peak || b.cursorPeak.CompareAndSwap(peak, cursorUsed+bytes) {
				break
			}
		}
		return true
	}
}

func (b *AcquisitionBudget) releaseCursor(bytes uint64) {
	if b == nil || bytes == 0 {
		return
	}
	for {
		used := b.cursorUsed.Load()
		if bytes > used {
			panic("shamap: acquisition cursor release exceeds reservation")
		}
		if b.cursorUsed.CompareAndSwap(used, used-bytes) {
			b.Release(bytes)
			return
		}
	}
}
