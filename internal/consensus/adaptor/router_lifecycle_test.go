package adaptor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/require"
)

func startLifecycleTestRouter(t *testing.T) (*Router, chan *peermanagement.InboundMessage, <-chan struct{}) {
	t.Helper()
	inbox := make(chan *peermanagement.InboundMessage)
	router := NewRouter(&mockEngine{}, newTestAdaptor(t), inbox)
	done := make(chan struct{})
	go func() {
		router.Run(context.Background())
		close(done)
	}()
	inbox <- &peermanagement.InboundMessage{}
	return router, inbox, done
}

func TestRouterRunJoinsOwnedTasksOnInboxClose(t *testing.T) {
	router, inbox, done := startLifecycleTestRouter(t)
	started := make(chan struct{})
	release := make(chan struct{})
	require.True(t, router.runLifecycleTask(func(context.Context) {
		close(started)
		<-release
	}))
	<-started

	close(inbox)
	select {
	case <-done:
		t.Fatal("Run returned while router-owned work was still active")
	default:
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after router-owned work exited")
	}

	router.lifecycleMu.RLock()
	require.Equal(t, routerLifecycleStopped, router.lifecycleState)
	require.Nil(t, router.txJobs)
	require.Nil(t, router.serveJobs)
	router.lifecycleMu.RUnlock()
	require.ErrorIs(t, router.lifecycleContext().Err(), context.Canceled)
}

func TestRouterRejectsWorkAfterTerminalStop(t *testing.T) {
	router, inbox, done := startLifecycleTestRouter(t)
	close(inbox)
	<-done

	var ran atomic.Bool
	require.False(t, router.runLifecycleTask(func(context.Context) {
		ran.Store(true)
	}))
	require.False(t, ran.Load())

	dropped := router.DroppedTxJobs()
	router.submitTxJob(&peermanagement.InboundMessage{})
	require.Equal(t, dropped+1, router.DroppedTxJobs())

	droppedServe := router.DroppedServeJobs()
	router.submitServeJob(&peermanagement.InboundMessage{})
	require.Equal(t, droppedServe+1, router.DroppedServeJobs())
	require.False(t, ran.Load())
}

func TestRouterRejectsTrackedTasksBeforeRun(t *testing.T) {
	router := NewRouter(&mockEngine{}, newTestAdaptor(t), nil)
	var ran atomic.Bool

	require.False(t, router.runLifecycleTask(func(context.Context) {
		ran.Store(true)
	}))
	require.False(t, ran.Load())
	require.NoError(t, router.lifecycleContext().Err())
}

func TestRouterRunJoinsOwnedTasksOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inbox := make(chan *peermanagement.InboundMessage)
	router := NewRouter(&mockEngine{}, newTestAdaptor(t), inbox)
	done := make(chan struct{})
	go func() {
		router.Run(ctx)
		close(done)
	}()
	inbox <- &peermanagement.InboundMessage{}

	started := make(chan struct{})
	release := make(chan struct{})
	require.True(t, router.runLifecycleTask(func(context.Context) {
		close(started)
		<-release
	}))
	<-started

	cancel()
	select {
	case <-done:
		t.Fatal("Run returned while router-owned work was still active")
	default:
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after router-owned work exited")
	}
}

func TestRouterRunJoinsSignaturePrewarm(t *testing.T) {
	engine := &mockEngine{}
	inbox := make(chan *peermanagement.InboundMessage)
	router := NewRouter(engine, newTestAdaptor(t), inbox)
	done := make(chan struct{})
	go func() {
		router.Run(context.Background())
		close(done)
	}()
	inbox <- &peermanagement.InboundMessage{}

	started := make(chan struct{})
	release := make(chan struct{})
	router.prewarmSignatures = func(context.Context, [][]byte) {
		close(started)
		<-release
	}
	txSetID := consensus.TxSetID{1}
	router.submitTxSetToEngine(txSetID, [][]byte{{1}})
	<-started

	engine.mu.Lock()
	txSets := append([]consensus.TxSetID(nil), engine.txSets...)
	engine.mu.Unlock()
	require.Equal(t, []consensus.TxSetID{txSetID}, txSets)

	close(inbox)
	select {
	case <-done:
		t.Fatal("Run returned while signature prewarm was active")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after signature prewarm exited")
	}
}

func TestRouterLifecycleAdmissionRacesShutdown(t *testing.T) {
	const submitters = 128

	inbox := make(chan *peermanagement.InboundMessage)
	router := NewRouter(&mockEngine{}, newTestAdaptor(t), inbox)
	var returned atomic.Bool
	done := make(chan struct{})
	go func() {
		router.Run(context.Background())
		returned.Store(true)
		close(done)
	}()
	inbox <- &peermanagement.InboundMessage{}

	start := make(chan struct{})
	var submissions sync.WaitGroup
	var accepted atomic.Int32
	var ran atomic.Int32
	var ranAfterReturn atomic.Bool
	for range submitters {
		submissions.Add(1)
		go func() {
			defer submissions.Done()
			<-start
			if router.runLifecycleTask(func(context.Context) {
				if returned.Load() {
					ranAfterReturn.Store(true)
				}
				ran.Add(1)
			}) {
				accepted.Add(1)
			}
		}()
	}

	close(start)
	close(inbox)
	submissions.Wait()
	<-done
	require.Equal(t, accepted.Load(), ran.Load())
	require.False(t, ranAfterReturn.Load())
}
