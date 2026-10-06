package shamap

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

const defaultAcquisitionBudgetBytes = 512 << 20

const (
	acquisitionCursorMinimumBytes      = 64 << 20
	acquisitionLoadedMinimumBytes      = 64 << 20
	acquisitionPersistenceMinimumBytes = 64 << 20
	acquisitionProcessingMinimumBytes  = 192 << 20
	acquisitionReplayHeadMinimumBytes  = 128 << 20
)

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
	mu               sync.Mutex
	limit            uint64
	parent           AcquisitionMemoryAccount
	changed          chan struct{}
	protected        bool
	closed           bool
	parentMinimum    uint64
	used             atomic.Uint64
	peak             atomic.Uint64
	cursorUsed       atomic.Uint64
	cursorPeak       atomic.Uint64
	loadedUsed       atomic.Uint64
	loadedPeak       atomic.Uint64
	persistenceUsed  atomic.Uint64
	processingUsed   atomic.Uint64
	replayHeadUsed   atomic.Uint64
	replayFutureUsed atomic.Uint64
}

// NewAcquisitionBudget creates a shared byte budget. The limit is immutable so
// all participating owners observe the same admission rule.
func NewAcquisitionBudget(limit uint64) *AcquisitionBudget {
	if limit == 0 {
		limit = defaultAcquisitionBudgetBytes
	}
	return &AcquisitionBudget{limit: limit}
}

// NewAcquisitionBudgetWithParent creates an acquisition budget that charges
// every reservation to parent as well. For a ceiling that can support it, the
// constructor commits the fixed progress minimum before returning. A failed
// parent admission never consumes a local credit, and a successful local
// admission is always rolled back if the parent cannot be charged.
//
// A zero limit retains the standalone constructor's bounded default. Callers
// that want the parent account to provide the complete ceiling should pass a
// local limit large enough for that account; the parent remains the final
// node-wide admission authority.
func NewAcquisitionBudgetWithParent(limit uint64, parent AcquisitionMemoryAccount) (*AcquisitionBudget, error) {
	if limit == 0 {
		limit = defaultAcquisitionBudgetBytes
	}
	b := &AcquisitionBudget{limit: limit, parent: parent}
	if parent != nil && limit >= defaultAcquisitionBudgetBytes {
		if parent.TryReserve(defaultAcquisitionBudgetBytes) {
			b.protected = true
			b.parentMinimum = defaultAcquisitionBudgetBytes
		} else {
			return nil, fmt.Errorf("shamap: parent cannot reserve protected acquisition minimum (%d bytes)", defaultAcquisitionBudgetBytes)
		}
	}
	return b, nil
}

// Changed returns a wakeup channel that is closed and replaced whenever a
// parent-backed budget's shared capacity changes. Standalone budgets use their
// own release channel; parent-backed callers can select LocalChanged as well
// when they need to observe releases below a protected category minimum.
func (b *AcquisitionBudget) Changed() <-chan struct{} {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	parent := b.parent
	if parent != nil {
		b.mu.Unlock()
		return parent.Changed()
	}
	if b.changed == nil {
		b.changed = make(chan struct{})
	}
	changed := b.changed
	b.mu.Unlock()
	return changed
}

// LocalChanged returns a wakeup channel for releases owned by this account.
// Parent-backed callers that wait on shared capacity should use Changed;
// callers coordinating category-local work can also select LocalChanged.
func (b *AcquisitionBudget) LocalChanged() <-chan struct{} {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.changed == nil {
		b.changed = make(chan struct{})
	}
	changed := b.changed
	b.mu.Unlock()
	return changed
}

func (b *AcquisitionBudget) signalChangedLocked() {
	if b.changed != nil {
		close(b.changed)
		b.changed = nil
	}
}

type acquisitionMemoryCategory uint8

const (
	acquisitionCategoryPersistence acquisitionMemoryCategory = iota
	acquisitionCategoryCursor
	acquisitionCategoryLoaded
	acquisitionCategoryProcessing
	acquisitionCategoryReplayHead
	acquisitionCategoryReplayFuture
)

func maxAcquisitionBytes(used, minimum uint64) uint64 {
	if used < minimum {
		return minimum
	}
	return used
}

func (b *AcquisitionBudget) categoryUsedLocked(category acquisitionMemoryCategory) uint64 {
	switch category {
	case acquisitionCategoryPersistence:
		return b.persistenceUsed.Load()
	case acquisitionCategoryCursor:
		return b.cursorUsed.Load()
	case acquisitionCategoryLoaded:
		return b.loadedUsed.Load()
	case acquisitionCategoryProcessing:
		return b.processingUsed.Load()
	case acquisitionCategoryReplayHead:
		return b.replayHeadUsed.Load()
	case acquisitionCategoryReplayFuture:
		return b.replayFutureUsed.Load()
	default:
		panic("shamap: unknown acquisition memory category")
	}
}

func (b *AcquisitionBudget) setCategoryUsedLocked(category acquisitionMemoryCategory, used uint64) {
	switch category {
	case acquisitionCategoryPersistence:
		b.persistenceUsed.Store(used)
	case acquisitionCategoryCursor:
		b.cursorUsed.Store(used)
	case acquisitionCategoryLoaded:
		b.loadedUsed.Store(used)
	case acquisitionCategoryProcessing:
		b.processingUsed.Store(used)
	case acquisitionCategoryReplayHead:
		b.replayHeadUsed.Store(used)
	case acquisitionCategoryReplayFuture:
		b.replayFutureUsed.Store(used)
	default:
		panic("shamap: unknown acquisition memory category")
	}
}

func (b *AcquisitionBudget) categoryMinimumLocked(category acquisitionMemoryCategory) uint64 {
	if !b.protected {
		return 0
	}
	switch category {
	case acquisitionCategoryPersistence:
		return acquisitionPersistenceMinimumBytes
	case acquisitionCategoryCursor:
		return acquisitionCursorMinimumBytes
	case acquisitionCategoryLoaded:
		return acquisitionLoadedMinimumBytes
	case acquisitionCategoryProcessing:
		return acquisitionProcessingMinimumBytes
	case acquisitionCategoryReplayHead:
		return acquisitionReplayHeadMinimumBytes
	default:
		return 0
	}
}

func (b *AcquisitionBudget) effectiveUsedLocked() uint64 {
	if !b.protected {
		return b.used.Load()
	}
	var total uint64
	for _, category := range []acquisitionMemoryCategory{
		acquisitionCategoryPersistence,
		acquisitionCategoryCursor,
		acquisitionCategoryLoaded,
		acquisitionCategoryProcessing,
		acquisitionCategoryReplayHead,
		acquisitionCategoryReplayFuture,
	} {
		amount := maxAcquisitionBytes(b.categoryUsedLocked(category), b.categoryMinimumLocked(category))
		if amount > ^uint64(0)-total {
			return ^uint64(0)
		}
		total += amount
	}
	return total
}

func (b *AcquisitionBudget) reserveCategoryLocked(category acquisitionMemoryCategory, bytes uint64) bool {
	if bytes == 0 {
		return true
	}
	if b.closed {
		return false
	}
	before := b.effectiveUsedLocked()
	used := b.categoryUsedLocked(category)
	if used > ^uint64(0)-bytes {
		return false
	}
	afterUsed := used + bytes
	oldEffective := maxAcquisitionBytes(used, b.categoryMinimumLocked(category))
	newEffective := maxAcquisitionBytes(afterUsed, b.categoryMinimumLocked(category))
	if newEffective >= oldEffective {
		delta := newEffective - oldEffective
		if before > b.limit || delta > b.limit-before {
			return false
		}
		if delta != 0 && b.parent != nil && !b.parent.TryReserve(delta) {
			return false
		}
	}
	b.setCategoryUsedLocked(category, afterUsed)
	b.used.Add(bytes)
	if usedTotal := b.used.Load(); usedTotal > b.peak.Load() {
		b.peak.Store(usedTotal)
	}
	return true
}

func (b *AcquisitionBudget) releaseCategoryLocked(category acquisitionMemoryCategory, bytes uint64) {
	if bytes == 0 {
		return
	}
	used := b.categoryUsedLocked(category)
	if bytes > used {
		panic("shamap: acquisition category release exceeds reservation")
	}
	before := b.effectiveUsedLocked()
	afterUsed := used - bytes
	after := before - maxAcquisitionBytes(used, b.categoryMinimumLocked(category)) +
		maxAcquisitionBytes(afterUsed, b.categoryMinimumLocked(category))
	b.setCategoryUsedLocked(category, afterUsed)
	actual := b.used.Load()
	if bytes > actual {
		panic("shamap: acquisition budget release exceeds reservation")
	}
	b.used.Store(actual - bytes)
	if before > after && b.parent != nil {
		b.parent.Release(before - after)
	}
	b.signalChangedLocked()
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
	return b.reserveCategoryLocked(acquisitionCategoryPersistence, bytes)
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
	b.releaseCategoryLocked(acquisitionCategoryPersistence, bytes)
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
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := b.limit / acquisitionCursorBudgetFraction
	cursorUsed := b.cursorUsed.Load()
	if cursorUsed > limit || bytes > limit-cursorUsed {
		return false
	}
	if !b.reserveCategoryLocked(acquisitionCategoryCursor, bytes) {
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
	b.releaseCategoryLocked(acquisitionCategoryCursor, bytes)
}

func (b *AcquisitionBudget) tryReserveCategory(category acquisitionMemoryCategory, bytes uint64) bool {
	if b == nil || bytes == 0 {
		return b != nil || bytes == 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reserveCategoryLocked(category, bytes)
}

func (b *AcquisitionBudget) releaseCategory(category acquisitionMemoryCategory, bytes uint64) {
	if b == nil || bytes == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.releaseCategoryLocked(category, bytes)
}

// TryReserveReplayHead admits the bounded in-flight replay head. Its protected
// category remains available even while elastic prepared replay data grows.
func (b *AcquisitionBudget) TryReserveReplayHead(bytes uint64) bool {
	return b.tryReserveCategory(acquisitionCategoryReplayHead, bytes)
}

// PromoteReplayHead transfers an existing future reservation to the replay
// head without temporarily charging both owners. Total retained bytes do not
// change; entering protected head capacity can return parent credits.
func (b *AcquisitionBudget) PromoteReplayHead(bytes uint64) bool {
	if b == nil || bytes == 0 {
		return b != nil || bytes == 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	future, head := b.replayFutureUsed.Load(), b.replayHeadUsed.Load()
	if b.closed || bytes > future || bytes > ^uint64(0)-head {
		return false
	}
	before := b.effectiveUsedLocked()
	b.replayFutureUsed.Store(future - bytes)
	b.replayHeadUsed.Store(head + bytes)
	after := b.effectiveUsedLocked()
	if before > after {
		if b.parent != nil {
			b.parent.Release(before - after)
		}
		b.signalChangedLocked()
	}
	return true
}

// ReleaseReplayHead returns replay-head bytes to the acquisition budget.
func (b *AcquisitionBudget) ReleaseReplayHead(bytes uint64) {
	b.releaseCategory(acquisitionCategoryReplayHead, bytes)
}

// TryReserveReplayProcessing admits response decode and apply workspace.
func (b *AcquisitionBudget) TryReserveReplayProcessing(bytes uint64) bool {
	return b.tryReserveCategory(acquisitionCategoryProcessing, bytes)
}

// ReplayProcessingCapacity returns the processing ceiling after accounting
// for the other protected working sets and a parent's known capacity.
func (b *AcquisitionBudget) ReplayProcessingCapacity() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	capacity := b.limit
	if parent, ok := b.parent.(interface{ Capacity() uint64 }); ok {
		capacity = min(capacity, parent.Capacity())
	}
	if b.protected {
		const others = defaultAcquisitionBudgetBytes - acquisitionProcessingMinimumBytes
		if capacity < others {
			return 0
		}
		capacity -= others
	}
	return capacity
}

// ReleaseReplayProcessing returns replay-processing bytes to the acquisition budget.
func (b *AcquisitionBudget) ReleaseReplayProcessing(bytes uint64) {
	b.releaseCategory(acquisitionCategoryProcessing, bytes)
}

// TryReserveReplay admits retained prepared replay data from the elastic
// remainder. It has no idle minimum and therefore cannot displace the replay
// head or processing reserves.
func (b *AcquisitionBudget) TryReserveReplay(bytes uint64) bool {
	return b.tryReserveCategory(acquisitionCategoryReplayFuture, bytes)
}

// ReleaseReplay returns retained replay bytes to the acquisition budget.
func (b *AcquisitionBudget) ReleaseReplay(bytes uint64) {
	b.releaseCategory(acquisitionCategoryReplayFuture, bytes)
}

// Close releases the fixed parent commitment after all category owners have
// drained. It is safe to call more than once; a close with outstanding owners
// returns false and leaves the commitment intact.
func (b *AcquisitionBudget) Close() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return true
	}
	if b.used.Load() != 0 {
		b.mu.Unlock()
		return false
	}
	b.closed = true
	b.signalChangedLocked()
	parent := b.parent
	minimum := b.parentMinimum
	b.parent = nil
	b.parentMinimum = 0
	b.protected = false
	b.mu.Unlock()
	if parent != nil && minimum != 0 {
		parent.Release(minimum)
	}
	return true
}
