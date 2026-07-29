package shamapstore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// defaultDeleteBatch is the per-batch deletion size used when delete_batch is
// left unconfigured. It bounds the work done between context checks so a prune
// pass stays responsive to shutdown.
const (
	defaultDeleteBatch = 65536
	defaultBackOff     = 100 * time.Millisecond
)

// NodePruner deletes stored nodes below a retention boundary. It is satisfied
// by the nodestore's PrunableDatabase. boundary is exclusive: nodes with a
// ledger sequence strictly below it are removed.
type NodePruner interface {
	DeleteBefore(ctx context.Context, boundary uint32, batchSize int) (deleted uint64, err error)
}

// NodeGenerationRotator retires whole storage generations and exposes the
// boundary durably committed with the active pair.
type NodeGenerationRotator interface {
	RotateGeneration(
		ctx context.Context,
		lastRotated, minimumOnline uint32,
	) (committed bool, err error)
	GenerationState() (lastRotated, minimumOnline uint32)
}

// RelationalPruner deletes ledger and transaction index rows below a retention
// boundary. It is the go-xrpl equivalent of rippled's clearSql over the
// Ledgers / Transactions / AccountTransactions tables. A nil RelationalPruner
// is tolerated (relational indexing is optional).
type RelationalPruner interface {
	DeleteLedgersBefore(ctx context.Context, boundary uint32) error
}

// StateRefresh preserves the live state at or above minimumSeq. Rotating stores
// promote it into the writable generation; legacy stores re-stamp it in place.
// checkpoint must be called periodically while walking the state tree. The
// returned sequence identifies the validated ledger that was preserved.
type StateRefresh func(ctx context.Context, minimumSeq uint32, checkpoint func(time.Duration) error) (uint32, error)

// RotationConfig carries the node_db online-delete settings the rotator needs.
type RotationConfig struct {
	// DeleteInterval is node_db online_delete: rotate (and delete) once the
	// validated ledger has advanced this many sequences past the last rotation.
	// Zero disables rotation entirely.
	DeleteInterval uint32

	// DeleteBatch is node_db delete_batch: the maximum number of records removed
	// per backend batch. Zero selects a default.
	DeleteBatch int

	// BackOff is the cooperative pause between live-state promotion checkpoints.
	// Zero selects the rippled node_db default.
	BackOff time.Duration
}

// Rotator runs the online-delete rotation: every DeleteInterval validated
// ledgers it retires the oldest node-store generation (or prunes a legacy
// single store) and removes old relational rows, advancing the advisory-delete
// state's lastRotated and minimum-online boundary.
//
// It mirrors the decision logic of rippled's SHAMapStoreImp::run: the first
// notification seeds lastRotated; thereafter a rotation fires when the
// validated sequence has advanced a full DeleteInterval past lastRotated and,
// under advisory_delete, the operator-set can_delete boundary permits it.
//
// Notifications are dispatched to a single background worker so deletion never
// blocks the consensus / ledger-accept path; an in-flight rotation coalesces
// further notifications to the newest validated sequence.
type Rotator struct {
	store      *Store
	nodes      NodePruner
	rel        RelationalPruner
	cfg        RotationConfig
	logger     xrpllog.Logger
	hooksMu    sync.RWMutex
	refresh    StateRefresh
	advance    func(uint32)
	beginPrune func() func()
	healthMu   sync.RWMutex
	healthy    func() bool
	recovery   time.Duration

	notifyCh chan uint32
	stopCh   chan struct{}
	lifeMu   sync.Mutex
	life     rotatorLifecycle
	workers  sync.WaitGroup

	// minimumOnline is the lowest ledger sequence the node still retains in
	// full. Acquisition / fetch-pack serving must not reach below it. Zero
	// until the first rotation.
	minimumOnline atomic.Uint32
}

type rotatorLifecycle uint8

const (
	rotatorNew rotatorLifecycle = iota
	rotatorRunning
	rotatorStopped
)

// SetHealthCheck gates rotation on the node being fully synchronized.
func (r *Rotator) SetHealthCheck(healthy func() bool, recoveryWait time.Duration) {
	if r == nil {
		return
	}
	if recoveryWait <= 0 {
		recoveryWait = 5 * time.Second
	}
	r.healthMu.Lock()
	r.healthy = healthy
	r.recovery = recoveryWait
	r.healthMu.Unlock()
}

// SetStateRefresh installs the live-state refresh, acquisition-floor advance,
// and exclusive cache guard required around node-store pruning.
func (r *Rotator) SetStateRefresh(refresh StateRefresh, advance func(uint32), beginPrune func() func()) {
	if r == nil {
		return
	}
	r.hooksMu.Lock()
	r.refresh = refresh
	r.advance = advance
	r.beginPrune = beginPrune
	r.hooksMu.Unlock()
}

// NewRotator constructs a Rotator. store and nodes are required; rel may be nil
// when no relational index is configured. A nil logger is replaced with a
// discard logger. NewRotator returns nil when rotation is disabled
// (cfg.DeleteInterval == 0) so callers can treat a nil *Rotator as "online
// delete off".
func NewRotator(store *Store, nodes NodePruner, rel RelationalPruner, cfg RotationConfig, logger xrpllog.Logger) *Rotator {
	if cfg.DeleteInterval == 0 || store == nil || nodes == nil {
		return nil
	}
	if logger == nil {
		logger = xrpllog.Discard()
	}
	if cfg.DeleteBatch <= 0 {
		cfg.DeleteBatch = defaultDeleteBatch
	}
	if cfg.BackOff <= 0 {
		cfg.BackOff = defaultBackOff
	}
	r := &Rotator{
		store:    store,
		nodes:    nodes,
		rel:      rel,
		cfg:      cfg,
		logger:   logger,
		notifyCh: make(chan uint32, 1),
		stopCh:   make(chan struct{}),
	}
	r.minimumOnline.Store(store.GetMinimumOnline())
	return r
}

// ReconcileGenerationState repairs advisory-delete bookkeeping after a crash
// that durably published a generation swap but stopped before SetRotation.
func (r *Rotator) ReconcileGenerationState() error {
	if r == nil {
		return nil
	}
	generations, ok := r.nodes.(NodeGenerationRotator)
	if !ok {
		return nil
	}
	lastRotated, minimumOnline := generations.GenerationState()
	if lastRotated <= r.store.GetLastRotated() {
		return nil
	}
	if minimumOnline == 0 {
		return fmt.Errorf("generation %d has no minimum-online boundary", lastRotated)
	}
	if err := r.store.SetRotation(lastRotated, minimumOnline); err != nil {
		return err
	}
	r.minimumOnline.Store(minimumOnline)
	return nil
}

// Start launches the background rotation worker. Repeated calls and calls
// after Stop are no-ops.
func (r *Rotator) Start() {
	if r == nil {
		return
	}
	r.lifeMu.Lock()
	if r.life != rotatorNew {
		r.lifeMu.Unlock()
		return
	}
	r.life = rotatorRunning
	r.workers.Add(1)
	r.lifeMu.Unlock()
	go r.run()
}

// Stop signals the worker to exit and waits for it to finish. Idempotent.
func (r *Rotator) Stop() {
	if r == nil {
		return
	}
	r.lifeMu.Lock()
	if r.life == rotatorNew {
		r.life = rotatorStopped
		close(r.stopCh)
		r.lifeMu.Unlock()
		return
	}
	if r.life == rotatorRunning {
		r.life = rotatorStopped
		close(r.stopCh)
	}
	r.lifeMu.Unlock()
	r.workers.Wait()
}

// Notify reports a newly validated ledger sequence. It never blocks: if a
// rotation is already pending or in flight, the latest sequence supersedes the
// queued one (a coalescing send), mirroring rippled where only the newest
// validated ledger drives the run loop.
func (r *Rotator) Notify(validatedSeq uint32) {
	if r == nil || validatedSeq == 0 {
		return
	}
	for {
		select {
		case r.notifyCh <- validatedSeq:
			return
		case <-r.notifyCh:
			// Drop the stale queued sequence and retry with the newer one.
		case <-r.stopCh:
			return
		}
	}
}

// MinimumOnline returns the lowest ledger sequence still retained in full.
// Ledgers below it have been (or are being) deleted and must not be served or
// re-acquired. Zero before the first rotation. Mirrors rippled's
// SHAMapStore::minimumOnline().
func (r *Rotator) MinimumOnline() uint32 {
	if r == nil {
		return 0
	}
	return r.minimumOnline.Load()
}

// SetMinimumOnlineFloor durably advances the retained-ledger floor. It is used
// at startup to migrate older state from the relational ledger range and before
// each destructive rotation so a crash cannot reopen below deleted data.
func (r *Rotator) SetMinimumOnlineFloor(seq uint32) error {
	if r == nil || seq == 0 {
		return nil
	}
	if err := r.store.SetMinimumOnline(seq); err != nil {
		return err
	}
	for {
		current := r.minimumOnline.Load()
		if seq <= current || r.minimumOnline.CompareAndSwap(current, seq) {
			return nil
		}
	}
}

func (r *Rotator) run() {
	defer r.workers.Done()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-r.stopCh
		cancel()
	}()

	for {
		select {
		case <-r.stopCh:
			return
		case validatedSeq := <-r.notifyCh:
			r.maybeRotate(ctx, validatedSeq)
		}
	}
}

// maybeRotate applies the rippled readyToRotate predicate for validatedSeq and,
// when it holds, deletes complete ledgers below the rotation boundary.
func (r *Rotator) maybeRotate(ctx context.Context, validatedSeq uint32) {
	if err := r.ReconcileGenerationState(); err != nil {
		r.logger.Warn("online delete: generation reconciliation failed", "err", err)
		return
	}
	lastRotated := r.store.GetLastRotated()

	// First validated ledger seeds the boundary without deleting anything,
	// matching rippled (lastRotated = validatedSeq on the first run).
	if lastRotated == 0 {
		if err := r.store.SetLastRotated(validatedSeq); err != nil {
			r.logger.Warn("online delete: failed to persist initial lastRotated", "seq", validatedSeq, "err", err)
			return
		}
		return
	}

	if validatedSeq < lastRotated+r.cfg.DeleteInterval {
		return
	}

	// Under advisory delete, the operator's can_delete boundary must permit
	// removing everything below lastRotated (rippled: canDelete_ >= lastRotated-1).
	// lastRotated >= 1 here, so lastRotated-1 cannot underflow; comparing this
	// way (rather than canDelete+1) also avoids overflow when can_delete is set
	// to "always" (max uint32).
	if r.store.AdvisoryDelete() && r.store.GetCanDelete() < lastRotated-1 {
		return
	}
	if !r.waitHealthy(ctx) {
		return
	}

	r.rotate(ctx, validatedSeq, lastRotated)
}

func (r *Rotator) waitHealthy(ctx context.Context) bool {
	for {
		r.healthMu.RLock()
		healthy := r.healthy
		wait := r.recovery
		r.healthMu.RUnlock()
		if healthy == nil || healthy() {
			return true
		}
		if wait <= 0 {
			wait = 5 * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func (r *Rotator) refreshCheckpoint(ctx context.Context, work time.Duration) error {
	if !r.waitHealthy(ctx) {
		return ctx.Err()
	}
	pause := min(work, r.cfg.BackOff)
	if pause > 0 {
		timer := time.NewTimer(pause)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if !r.waitHealthy(ctx) {
		return ctx.Err()
	}
	return nil
}

// rotate retires everything below lastRotated, then advances the boundary to
// validatedSeq. A rotating store first promotes the live state into its current
// writable generation and then drops the former archive. A legacy store retains
// the sequence-stamp pruning path.
func (r *Rotator) rotate(ctx context.Context, validatedSeq, lastRotated uint32) {
	r.logger.Info("online delete: rotating",
		"validatedSeq", validatedSeq, "lastRotated", lastRotated,
		"deleteInterval", r.cfg.DeleteInterval)

	r.hooksMu.RLock()
	refresh := r.refresh
	advance := r.advance
	beginPrune := r.beginPrune
	r.hooksMu.RUnlock()

	if refresh == nil {
		r.logger.Warn("online delete: live-state refresh is not configured")
		return
	}
	minimumOnline := lastRotated + 1
	if err := r.SetMinimumOnlineFloor(minimumOnline); err != nil {
		r.logger.Warn("online delete: failed to persist minimum online ledger", "seq", minimumOnline, "err", err)
		return
	}
	if advance != nil {
		advance(minimumOnline)
	}
	refreshedSeq, err := refresh(ctx, validatedSeq, func(work time.Duration) error {
		return r.refreshCheckpoint(ctx, work)
	})
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("online delete: live-state refresh failed", "seq", validatedSeq, "err", err)
		}
		return
	}
	if refreshedSeq < validatedSeq {
		r.logger.Warn("online delete: live-state refresh returned stale ledger",
			"requestedSeq", validatedSeq, "refreshedSeq", refreshedSeq)
		return
	}
	if !r.waitHealthy(ctx) {
		return
	}

	if r.rel != nil {
		if err := r.rel.DeleteLedgersBefore(ctx, lastRotated); err != nil {
			if ctx.Err() != nil {
				return
			}
			r.logger.Warn("online delete: relational prune failed", "boundary", lastRotated, "err", err)
		}
	}
	if !r.waitHealthy(ctx) {
		return
	}

	deleted, committed, err := func() (uint64, bool, error) {
		if beginPrune != nil {
			unlock := beginPrune()
			defer unlock()
		}
		if generations, ok := r.nodes.(NodeGenerationRotator); ok {
			committed, err := generations.RotateGeneration(ctx, refreshedSeq, minimumOnline)
			return 0, committed, err
		}
		deleted, err := r.nodes.DeleteBefore(ctx, lastRotated, r.cfg.DeleteBatch)
		return deleted, err == nil, err
	}()
	if err != nil {
		if committed {
			r.logger.Warn("online delete: retired generation cleanup failed", "err", err)
		} else {
			if ctx.Err() != nil {
				return
			}
			r.logger.Warn("online delete: nodestore rotation failed", "boundary", lastRotated, "deleted", deleted, "err", err)
			return
		}
	}
	if !committed {
		if ctx.Err() != nil {
			return
		}
		r.logger.Warn("online delete: nodestore rotation did not commit", "boundary", lastRotated)
		return
	}
	if err := r.store.SetRotation(refreshedSeq, minimumOnline); err != nil {
		r.logger.Warn("online delete: failed to persist lastRotated", "seq", refreshedSeq, "err", err)
		return
	}

	r.logger.Info("online delete: rotation finished",
		"validatedSeq", refreshedSeq, "nodesDeleted", deleted, "minimumOnline", minimumOnline)
}
