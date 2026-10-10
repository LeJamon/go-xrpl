package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// ReplayVerificationWorkspaceBytes bounds one stored-node verification worker,
// which can retain the largest accepted replay payload plus its serialized and
// decoded copies. Shared verification memory is
// reserved per worker before a cold walk starts; the limit keeps this path from
// multiplying the replay payload by the machine's CPU count.
const ReplayVerificationWorkspaceBytes = (128 << 20) + (256 << 10)

const replayAdmissionColdWorkspaceBytes = ReplayVerificationWorkspaceBytes

// ErrReplayAdmissionWorkspace marks a persistence configuration that cannot
// admit even one bounded cold-walk worker. It is distinct from replay payload
// pressure, which belongs to the inbound acquisition budget.
var ErrReplayAdmissionWorkspace = errors.New("replay admission persistence workspace unavailable")

// ReplayAdmissionWorkspaceError reports the minimum shared persistence budget
// required by the cold verification worker and the configured limit.
type ReplayAdmissionWorkspaceError struct {
	Required uint64
	Capacity uint64
}

func (e *ReplayAdmissionWorkspaceError) Error() string {
	if e == nil {
		return ErrReplayAdmissionWorkspace.Error()
	}
	return fmt.Sprintf("%s: requires %d bytes, persistence capacity is %d", ErrReplayAdmissionWorkspace, e.Required, e.Capacity)
}

func (e *ReplayAdmissionWorkspaceError) Unwrap() error { return ErrReplayAdmissionWorkspace }

func (s *Service) verifyReplaySnapshot(
	ctx context.Context,
	name string,
	snapshot *shamap.SHAMap,
	baseRoot [32]byte,
) (ReplayAdmissionMapStats, error) {
	if snapshot == nil {
		return ReplayAdmissionMapStats{}, nil
	}
	if baseRoot != ([32]byte{}) && snapshot.IsBacked() {
		startedAt := time.Now()
		root, err := snapshot.Hash()
		if err != nil {
			return ReplayAdmissionMapStats{}, fmt.Errorf("state admission: hash %s root: %w", name, err)
		}
		if root == baseRoot {
			return ReplayAdmissionMapStats{
				Elapsed:              time.Since(startedAt),
				EqualSubtreesSkipped: 1,
				UsedVerifiedBase:     true,
				Workers:              1,
			}, nil
		}
	}
	if baseRoot == ([32]byte{}) && snapshot.IsBacked() && s.hasDurableFamily() {
		root, err := snapshot.Hash()
		if err != nil {
			return ReplayAdmissionMapStats{}, fmt.Errorf("state admission: hash %s root: %w", name, err)
		}
		if root != ([32]byte{}) {
			if stats, ok, err := s.verifyStoredReplaySnapshot(ctx, name, root, snapshot.Type()); ok {
				return stats, err
			}
		}
	}
	return s.verifyDetachedSnapshotStats(ctx, name, snapshot, baseRoot)
}

func (s *Service) verifyStoredReplaySnapshot(
	ctx context.Context,
	name string,
	root [32]byte,
	mapType shamap.Type,
) (ReplayAdmissionMapStats, bool, error) {
	durable, ok := s.shamapFamily.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	})
	if !ok {
		return ReplayAdmissionMapStats{}, false, nil
	}
	startedAt := time.Now()
	workers, workspaceBytes, releaseWorkspace, err := s.acquireReplayColdWorkspace(ctx)
	if err != nil {
		return ReplayAdmissionMapStats{Elapsed: time.Since(startedAt), Workers: workers, WorkspaceBytes: workspaceBytes}, true, err
	}
	defer releaseWorkspace()

	logger := s.logger
	if logger == nil {
		logger = xrpllog.Discard()
	}
	progress := newStoredSHAMapVerificationProgress(logger, s.nodeStore, root, mapType, startedAt)
	progress.operation = "replay admission verification"
	progress.extraFields = []any{"purpose", name}
	var reads atomic.Uint64
	fetch := func(fetchCtx context.Context, hash nodestore.Hash256) (*nodestore.Node, error) {
		reads.Add(1)
		data, fetchErr := durable.FetchDurable(fetchCtx, [32]byte(hash))
		if fetchErr != nil || data == nil {
			return nil, fetchErr
		}
		return &nodestore.Node{Hash: hash, Data: data}, nil
	}
	ticker := time.NewTicker(storedSHAMapVerificationLogInterval)
	defer ticker.Stop()
	defer func() {
		if (errors.Is(err, context.Canceled) || errors.Is(err, errReplayParentProofAvailable)) &&
			errors.Is(context.Cause(ctx), errReplayParentProofAvailable) {
			logger.Info("replay admission verification superseded by exact completeness proof", progress.fields(time.Now())...)
			return
		}
		progress.finish(time.Now(), err)
	}()
	control := storedSHAMapWalkControl{progress: progress, progressTicks: ticker.C, now: time.Now}
	if batchFetch := s.storedSHAMapVerificationBatchFetch(); batchFetch != nil {
		control.batchFetch = func(ctx context.Context, hashes []nodestore.Hash256, maxBytes int) ([]*nodestore.Node, kvstore.PromotionStats, error) {
			nodes, metrics, err := batchFetch(ctx, hashes, maxBytes)
			reads.Add(uint64(len(nodes)))
			return nodes, metrics, err
		}
		control.batchNodes = storedSHAMapVerificationBatchNodes
		control.batchBytes = storedSHAMapVerificationBatchBytes
	}
	err = s.walkStoredSHAMapConcurrentWithFetch(ctx, root, mapType, fetch, workers, control, nil)
	stats := ReplayAdmissionMapStats{
		Elapsed:        time.Since(startedAt),
		NodesLoaded:    progress.nodesChecked.Load(),
		DurableReads:   reads.Load(),
		Workers:        workers,
		WorkspaceBytes: workspaceBytes,
	}
	if err != nil {
		return stats, true, fmt.Errorf("state admission: walk %s tree: %w", name, err)
	}
	return stats, true, nil
}

func (s *Service) acquireReplayColdWorkspace(ctx context.Context) (int, uint64, func(), error) {
	desired := resolveStoredSHAMapWorkers(s.config.FastLoadWorkers)
	if desired <= 0 {
		desired = 1
	}
	budget := s.config.ReplayVerificationMemory
	if budget == nil {
		budget = s.config.PersistenceMemory
	}
	if budget == nil {
		return 1, 0, func() {}, nil
	}
	capacity := budget.Limit()
	if capacity < replayAdmissionColdWorkspaceBytes {
		return 0, 0, nil, &ReplayAdmissionWorkspaceError{
			Required: replayAdmissionColdWorkspaceBytes,
			Capacity: capacity,
		}
	}
	maxWorkers := desired
	if byBytes := capacity / replayAdmissionColdWorkspaceBytes; byBytes < uint64(maxWorkers) {
		maxWorkers = int(byBytes)
	}
	if maxWorkers < 1 {
		return 0, 0, nil, &ReplayAdmissionWorkspaceError{
			Required: replayAdmissionColdWorkspaceBytes,
			Capacity: capacity,
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, 0, nil, err
		}
		wake := budget.Changed()
		for workers := maxWorkers; workers >= 1; workers-- {
			workspaceBytes := uint64(workers) * replayAdmissionColdWorkspaceBytes
			if budget.TryReserve(workspaceBytes) {
				return workers, workspaceBytes, sync.OnceFunc(func() {
					budget.Release(workspaceBytes)
				}), nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, 0, nil, ctx.Err()
		case <-wake:
		}
	}
}
