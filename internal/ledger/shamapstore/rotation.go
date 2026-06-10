package shamapstore

import (
	"context"
	"sync"
	"sync/atomic"

	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// defaultDeleteBatch is the per-batch deletion size used when delete_batch is
// left unconfigured. It bounds the work done between context checks so a prune
// pass stays responsive to shutdown.
const defaultDeleteBatch = 65536

// NodePruner deletes stored nodes below a retention boundary. It is satisfied
// by the nodestore's PrunableDatabase. boundary is exclusive: nodes with a
// ledger sequence strictly below it are removed.
type NodePruner interface {
	DeleteBefore(ctx context.Context, boundary uint32, batchSize int) (deleted uint64, err error)
}

// RelationalPruner deletes ledger and transaction index rows below a retention
// boundary. It is the go-xrpl equivalent of rippled's clearSql over the
// Ledgers / Transactions / AccountTransactions tables. A nil RelationalPruner
// is tolerated (relational indexing is optional).
type RelationalPruner interface {
	DeleteLedgersBefore(ctx context.Context, boundary uint32) error
}

// RotationConfig carries the node_db online-delete settings the rotator needs.
type RotationConfig struct {
	// DeleteInterval is node_db online_delete: rotate (and delete) once the
	// validated ledger has advanced this many sequences past the last rotation.
	// Zero disables rotation entirely.
	DeleteInterval uint32

	// DeleteBatch is node_db delete_batch: the maximum number of records removed
	// per backend batch. Zero selects a default.
	DeleteBatch int
}

// Rotator runs the online-delete rotation: every DeleteInterval validated
// ledgers it deletes complete ledgers below the rotation boundary from the
// nodestore and relational stores, advancing the advisory-delete state's
// lastRotated and the minimum-online boundary.
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
	store  *Store
	nodes  NodePruner
	rel    RelationalPruner
	cfg    RotationConfig
	logger xrpllog.Logger

	notifyCh chan uint32
	stopCh   chan struct{}
	stopOnce sync.Once
	doneCh   chan struct{}

	// minimumOnline is the lowest ledger sequence the node still retains in
	// full. Acquisition / fetch-pack serving must not reach below it. Zero
	// until the first rotation.
	minimumOnline atomic.Uint32
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
	r := &Rotator{
		store:    store,
		nodes:    nodes,
		rel:      rel,
		cfg:      cfg,
		logger:   logger,
		notifyCh: make(chan uint32, 1),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	r.minimumOnline.Store(store.GetLastRotated())
	return r
}

// Start launches the background rotation worker. Safe to call once.
func (r *Rotator) Start() {
	if r == nil {
		return
	}
	go r.run()
}

// Stop signals the worker to exit and waits for it to finish. Idempotent.
func (r *Rotator) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stopCh) })
	<-r.doneCh
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

func (r *Rotator) run() {
	defer close(r.doneCh)

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
	lastRotated := r.store.GetLastRotated()

	// First validated ledger seeds the boundary without deleting anything,
	// matching rippled (lastRotated = validatedSeq on the first run).
	if lastRotated == 0 {
		if err := r.store.SetLastRotated(validatedSeq); err != nil {
			r.logger.Warn("online delete: failed to persist initial lastRotated", "seq", validatedSeq, "err", err)
			return
		}
		r.minimumOnline.Store(validatedSeq)
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

	r.rotate(ctx, validatedSeq, lastRotated)
}

// rotate deletes everything below lastRotated, then advances the boundary to
// validatedSeq. Deletion runs below the OLD boundary (lastRotated): the live
// state at validatedSeq is preserved because every live state node was
// re-persisted at the current sequence, so it carries a LedgerSeq at or above
// the retained range and is never matched by the below-boundary scan.
func (r *Rotator) rotate(ctx context.Context, validatedSeq, lastRotated uint32) {
	r.logger.Info("online delete: rotating",
		"validatedSeq", validatedSeq, "lastRotated", lastRotated,
		"deleteInterval", r.cfg.DeleteInterval)

	// Refuse to re-acquire or serve ledgers about to be deleted before any
	// deletion begins (rippled clearPrior: minimumOnline_ = lastRotated + 1).
	r.minimumOnline.Store(lastRotated + 1)

	if r.rel != nil {
		if err := r.rel.DeleteLedgersBefore(ctx, lastRotated); err != nil {
			if ctx.Err() != nil {
				return
			}
			r.logger.Warn("online delete: relational prune failed", "boundary", lastRotated, "err", err)
		}
	}

	deleted, err := r.nodes.DeleteBefore(ctx, lastRotated, r.cfg.DeleteBatch)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		r.logger.Warn("online delete: nodestore prune failed", "boundary", lastRotated, "deleted", deleted, "err", err)
		return
	}

	if err := r.store.SetLastRotated(validatedSeq); err != nil {
		r.logger.Warn("online delete: failed to persist lastRotated", "seq", validatedSeq, "err", err)
		return
	}

	r.logger.Info("online delete: rotation finished",
		"validatedSeq", validatedSeq, "nodesDeleted", deleted, "minimumOnline", lastRotated+1)
}
