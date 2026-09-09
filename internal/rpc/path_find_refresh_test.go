package rpc

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
	"github.com/stretchr/testify/require"
)

func newPathFindRefreshTestServer(t *testing.T, count int) (*WebSocketServer, []*websocketConnection) {
	t.Helper()
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second})
	manager := ws.ensurePathFindRefreshManager()
	connections := make([]*websocketConnection, 0, count)
	ws.connectionsMutex.Lock()
	for i := range count {
		id := fmt.Sprintf("conn-%02d", i)
		conn := &websocketConnection{
			Connection:      subscription.NewConnection(id, make(chan []byte, 8)),
			pathFindRefresh: manager,
		}
		conn.installPathFindSession(&PathFindSession{id: id})
		ws.connections[id] = conn
		connections = append(connections, conn)
	}
	ws.connectionsMutex.Unlock()
	return ws, connections
}

func testLedgerView() types.LedgerStateView {
	return &stubAmendmentsView{}
}

func TestUpdatePathFindSessionsDoesNotBlockLedgerCallback(t *testing.T) {
	ws, _ := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	defer func() { _ = manager.wait(context.Background()) }()

	viewStarted := make(chan struct{})
	releaseView := make(chan struct{})
	updateDone := make(chan struct{})
	go func() {
		ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) {
			close(viewStarted)
			<-releaseView
			return testLedgerView(), nil
		})
		close(updateDone)
	}()

	select {
	case <-viewStarted:
	case <-time.After(time.Second):
		t.Fatal("refresh worker did not start view lookup")
	}
	select {
	case <-updateDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ledger callback waited for view lookup")
	}
	close(releaseView)
}

func TestQueuePathFindSessionsIncludesNewConnection(t *testing.T) {
	ws, _ := newPathFindRefreshTestServer(t, 0)
	manager := ws.ensurePathFindRefreshManager()
	defer func() { _ = manager.wait(context.Background()) }()

	computed := make(chan struct{}, 1)
	conn := &websocketConnection{
		Connection: subscription.NewConnection("new-connection", make(chan []byte, 1)),
	}
	conn.installPathFindSession(&PathFindSession{
		computeFn: func(tx.LedgerView) *pathfinder.PathRequestResult {
			computed <- struct{}{}
			return &pathfinder.PathRequestResult{}
		},
	})

	ws.queuePathFindSessions(func() (types.LedgerStateView, error) {
		return testLedgerView(), nil
	}, conn)

	select {
	case <-computed:
	case <-time.After(time.Second):
		t.Fatal("new path-find session was not queued for a full update")
	}
}

func TestPathFindRefreshMaximumConcurrency(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 3)
	manager := ws.ensurePathFindRefreshManager()
	defer func() { _ = manager.wait(context.Background()) }()

	var running, maximum atomic.Int32
	started := make(chan struct{}, 3)
	finished := make(chan struct{}, 3)
	release := make(chan struct{})
	connections[0].pathFindSession.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		current := running.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		running.Add(-1)
		finished <- struct{}{}
		return &pathfinder.PathRequestResult{}
	}
	for _, connection := range connections[1:] {
		connection.pathFindSession.computeFn = connections[0].pathFindSession.computeFn
	}

	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	for range pathFindRefreshWorkerCount {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	require.Equal(t, int32(pathFindRefreshWorkerCount), maximum.Load())
	require.Equal(t, int32(pathFindRefreshWorkerCount), running.Load())
	select {
	case <-started:
		t.Fatal("path-find refresh exceeded worker cap")
	default:
	}
	close(release)
	for range 3 {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("worker did not finish")
		}
	}
}

func TestPathFindRefreshLatestGenerationWins(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	defer func() { _ = manager.wait(context.Background()) }()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondViewed := make(chan struct{})
	computed := make(chan struct{}, 2)
	var calls atomic.Int32
	connections[0].pathFindSession.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		calls.Add(1)
		computed <- struct{}{}
		return &pathfinder.PathRequestResult{}
	}

	go ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) {
		close(firstStarted)
		<-releaseFirst
		return testLedgerView(), nil
	})
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first view lookup did not start")
	}
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) {
		close(secondViewed)
		return testLedgerView(), nil
	})
	close(releaseFirst)
	select {
	case <-secondViewed:
	case <-time.After(time.Second):
		t.Fatal("latest generation view lookup did not run")
	}
	select {
	case <-computed:
	case <-time.After(time.Second):
		t.Fatal("latest generation was not computed")
	}
	require.Equal(t, int32(1), calls.Load())
	select {
	case <-computed:
		t.Fatal("superseded generation was computed")
	default:
	}
}

func TestPathFindRefreshViewErrorRecovers(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	defer func() { _ = manager.wait(context.Background()) }()

	firstDone := make(chan struct{})
	computed := make(chan struct{}, 1)
	connections[0].pathFindSession.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		computed <- struct{}{}
		return &pathfinder.PathRequestResult{}
	}
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) {
		close(firstDone)
		return nil, errors.New("view unavailable")
	})
	<-firstDone
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	select {
	case <-computed:
	case <-time.After(time.Second):
		t.Fatal("refresh did not recover after a view error")
	}
}

func TestPathFindRefreshCloseSuppressesInFlightResult(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	defer func() { _ = manager.wait(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	connections[0].pathFindSession.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		close(started)
		<-release
		close(finished)
		return &pathfinder.PathRequestResult{}
	}
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	require.NotNil(t, connections[0].clearPathFindSession())
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("in-flight refresh did not finish")
	}
	select {
	case <-connections[0].Outbound():
		t.Fatal("closed session received a stale refresh")
	default:
	}
}

func TestPathFindRefreshReplacementStillRuns(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	defer func() { _ = manager.wait(context.Background()) }()

	old := connections[0].pathFindSession
	replacement := &PathFindSession{id: "replacement"}
	startedOld := make(chan struct{})
	replacementDone := make(chan struct{})
	releaseOld := make(chan struct{})
	old.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		close(startedOld)
		<-releaseOld
		return &pathfinder.PathRequestResult{}
	}
	replacement.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		close(replacementDone)
		return &pathfinder.PathRequestResult{}
	}
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	<-startedOld
	connections[0].clearPathFindSession()
	connections[0].installPathFindSession(replacement)
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	close(releaseOld)
	select {
	case <-replacementDone:
	case <-time.After(time.Second):
		t.Fatal("replacement session did not refresh")
	}
	select {
	case data := <-connections[0].Outbound():
		require.Contains(t, string(data), "replacement")
	case <-time.After(time.Second):
		t.Fatal("replacement result was not sent")
	}
}

func TestPathFindRefreshCloseJoinsWorkers(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	connections[0].pathFindSession.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		close(started)
		<-release
		close(finished)
		return &pathfinder.PathRequestResult{}
	}
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	<-started
	ws.connectionsMutex.Lock()
	delete(ws.connections, connections[0].ID())
	ws.connectionsMutex.Unlock()
	closeDone := make(chan error, 1)
	go func() { closeDone <- ws.Close(context.Background()) }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before path-find worker joined: %v", err)
	default:
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("worker did not finish")
	}
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not join refresh workers")
	}
}

func TestPathFindRefreshCloseDeadlineWithBlockedView(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	viewStarted := make(chan struct{})
	releaseView := make(chan struct{})
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) {
		close(viewStarted)
		<-releaseView
		return testLedgerView(), nil
	})
	<-viewStarted
	ws.connectionsMutex.Lock()
	delete(ws.connections, connections[0].ID())
	ws.connectionsMutex.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := ws.Close(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
	select {
	case <-manager.doneCh:
		t.Fatal("blocked view unexpectedly completed before release")
	default:
	}
	close(releaseView)
	select {
	case <-manager.doneCh:
	case <-time.After(time.Second):
		t.Fatal("refresh manager did not clean up after view release")
	}
	require.NoError(t, ws.Close(context.Background()))
}

func TestPathFindRefreshCloseDeadlineWithBlockedExecute(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	executeStarted := make(chan struct{})
	releaseExecute := make(chan struct{})
	connections[0].pathFindSession.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		close(executeStarted)
		<-releaseExecute
		return &pathfinder.PathRequestResult{}
	}
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	<-executeStarted
	ws.connectionsMutex.Lock()
	delete(ws.connections, connections[0].ID())
	ws.connectionsMutex.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := ws.Close(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-manager.doneCh:
		t.Fatal("blocked execute unexpectedly completed before release")
	default:
	}
	close(releaseExecute)
	select {
	case <-manager.doneCh:
	case <-time.After(time.Second):
		t.Fatal("refresh worker did not clean up after execute release")
	}
	require.NoError(t, ws.Close(context.Background()))
}

func TestPathFindRefreshDoesNotCommitSupersededResult(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	session := connections[0].pathFindSession
	initial := pathfinder.PathRequestResult{}
	oldResult := pathfinder.PathRequestResult{Alternatives: []pathfinder.PathAlternative{{DestinationAmount: state.NewXRPAmountFromInt(2)}}}
	latestResult := pathfinder.PathRequestResult{Alternatives: []pathfinder.PathAlternative{{DestinationAmount: state.NewXRPAmountFromInt(3)}}}
	session.convertAll = true
	session.CommitResult(&initial, false)
	oldStarted := make(chan struct{})
	latestStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	releaseLatest := make(chan struct{})
	var calls atomic.Int32
	session.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		if calls.Add(1) == 1 {
			close(oldStarted)
			<-releaseOld
			return &oldResult
		}
		close(latestStarted)
		<-releaseLatest
		return &latestResult
	}
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	<-oldStarted
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	close(releaseOld)
	<-latestStarted
	status := session.Status()
	require.Empty(t, status.Alternatives, "a superseded computation must not commit")
	close(releaseLatest)
	select {
	case <-connections[0].Outbound():
	case <-time.After(time.Second):
		t.Fatal("latest refresh was not published")
	}
	status = session.Status()
	require.Len(t, status.Alternatives, 1)
	require.Equal(t, `"3"`, string(status.Alternatives[0].DestinationAmount))
	_ = manager.wait(context.Background())
}

func TestPathFindRefreshSharesPathfindAdmissionPerConnection(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	shedder := types.NewClientLoadShedder()
	ws.services = types.NewTestServiceGraph(&types.ServiceContainer{ClientLoad: shedder})
	firstStarted := make(chan struct{})
	latestStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	connections[0].pathFindSession.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return &pathfinder.PathRequestResult{}
		}
		close(latestStarted)
		return &pathfinder.PathRequestResult{}
	}
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	<-firstStarted
	require.Equal(t, int64(1), shedder.PathfindActive())
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
	select {
	case <-latestStarted:
		t.Fatal("one connection occupied two pathfind workers")
	default:
	}
	close(releaseFirst)
	select {
	case <-latestStarted:
	case <-time.After(time.Second):
		t.Fatal("latest queued connection job did not run")
	}
	select {
	case <-connections[0].Outbound():
	case <-time.After(time.Second):
		t.Fatal("latest queued connection job was not published")
	}
	require.NoError(t, manager.wait(t.Context()))
	require.Equal(t, int64(0), shedder.PathfindActive())
}

func TestPathFindRefreshCancellationStress(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 4)
	manager := ws.ensurePathFindRefreshManager()
	defer func() { _ = manager.wait(context.Background()) }()

	var calls atomic.Int32
	started := make(chan struct{}, 1)
	for _, connection := range connections {
		connection.pathFindSession.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
			calls.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			return &pathfinder.PathRequestResult{}
		}
	}
	for range 100 {
		ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return testLedgerView(), nil })
		if calls.Load() > int32(len(connections)) {
			break
		}
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stress loop did not admit any refresh")
	}
}
