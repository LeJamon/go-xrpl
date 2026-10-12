package rpc

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
	"github.com/stretchr/testify/require"
)

type trackingPathFindView struct {
	types.LedgerStateView
	closed atomic.Int32
}

func (v *trackingPathFindView) Close() error {
	v.closed.Add(1)
	return nil
}

func TestPathFindRefreshClosesQueuedAndCompletedViews(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	target, ok := connections[0].snapshotPathFindUpdate()
	require.True(t, ok)

	manager.mu.Lock()
	manager.generation = target.generation
	manager.mu.Unlock()
	queued := &trackingPathFindView{LedgerStateView: testLedgerView()}
	manager.enqueueJob(pathFindRefreshJob{target: target, view: queued, generation: target.generation}, target.generation)
	manager.invalidate()
	require.Eventually(t, func() bool { return queued.closed.Load() == 1 }, time.Second, time.Millisecond)

	completed := &trackingPathFindView{LedgerStateView: testLedgerView()}
	finished := make(chan struct{})
	connections[0].pathFindSession.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		close(finished)
		return &pathfinder.PathRequestResult{}
	}
	manager.mu.Lock()
	manager.generation = target.generation
	manager.mu.Unlock()
	go manager.runJob(pathFindRefreshJob{target: target, view: completed, generation: target.generation})
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("path-find refresh did not run")
	}
	require.Eventually(t, func() bool { return completed.closed.Load() == 1 }, time.Second, time.Millisecond)
}

func TestPathFindRefreshClosesViewsOnCancelAndShutdown(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	target, ok := connections[0].snapshotPathFindUpdate()
	require.True(t, ok)
	manager.mu.Lock()
	manager.generation = target.generation
	manager.mu.Unlock()
	canceled := &trackingPathFindView{LedgerStateView: testLedgerView()}
	manager.enqueueJob(pathFindRefreshJob{target: target, view: canceled, generation: target.generation}, target.generation)
	manager.cancel(target.connection, target.session, target.generation)
	require.Equal(t, int32(1), canceled.closed.Load())

	ws, connections = newPathFindRefreshTestServer(t, 1)
	manager = ws.ensurePathFindRefreshManager()
	target, ok = connections[0].snapshotPathFindUpdate()
	require.True(t, ok)
	manager.mu.Lock()
	manager.generation = target.generation
	manager.mu.Unlock()
	shutDown := &trackingPathFindView{LedgerStateView: testLedgerView()}
	manager.enqueueJob(pathFindRefreshJob{target: target, view: shutDown, generation: target.generation}, target.generation)
	manager.shutdown()
	require.Equal(t, int32(1), shutDown.closed.Load())
}
