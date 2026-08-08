package node

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

func peerConnectPendingCount(s *peerConnectScheduler) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

func TestPeerConnectSchedulerBoundsWorkersAndQueue(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var running atomic.Int32
	dial := func(context.Context, string) error {
		running.Add(1)
		started <- struct{}{}
		<-release
		running.Add(-1)
		return nil
	}
	s := newPeerConnectScheduler(context.Background(), dial, 2, 2, nil)
	for i := 0; i < 2; i++ {
		require.NoError(t, s.Enqueue("running-"+string(rune('a'+i))))
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("scheduler worker did not start")
		}
	}
	for i := 0; i < 2; i++ {
		require.NoError(t, s.Enqueue("queued-"+string(rune('a'+i))))
	}
	require.ErrorIs(t, s.Enqueue("overflow"), types.ErrPeerConnectQueueFull)
	require.LessOrEqual(t, running.Load(), int32(2))
	require.Equal(t, 4, peerConnectPendingCount(s))
	close(release)
	require.Eventually(t, func() bool { return peerConnectPendingCount(s) == 0 }, time.Second, time.Millisecond)
	s.Close()
}

func TestPeerConnectSchedulerDeduplicatesRunningAndQueued(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	dial := func(_ context.Context, addr string) error {
		started <- addr
		<-release
		return nil
	}
	s := newPeerConnectScheduler(context.Background(), dial, 1, 2, nil)
	require.NoError(t, s.Enqueue("first"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first dial did not start")
	}
	require.NoError(t, s.Enqueue("first"))
	require.NoError(t, s.Enqueue("second"))
	require.NoError(t, s.Enqueue("second"))
	require.Equal(t, 2, peerConnectPendingCount(s))
	close(release)
	require.Eventually(t, func() bool { return peerConnectPendingCount(s) == 0 }, time.Second, time.Millisecond)
	s.Close()
}

func TestPeerConnectSchedulerRetriesAfterCompletion(t *testing.T) {
	var calls atomic.Int32
	completed := make(chan struct{}, 2)
	dial := func(context.Context, string) error {
		if calls.Add(1) == 1 {
			completed <- struct{}{}
			return errors.New("temporary dial failure")
		}
		completed <- struct{}{}
		return nil
	}
	s := newPeerConnectScheduler(context.Background(), dial, 1, 1, nil)
	require.NoError(t, s.Enqueue("retry"))
	<-completed
	require.Eventually(t, func() bool { return peerConnectPendingCount(s) == 0 }, time.Second, time.Millisecond)
	require.NoError(t, s.Enqueue("retry"))
	<-completed
	require.EqualValues(t, 2, calls.Load())
	s.Close()
}

func TestPeerConnectSchedulerObservesAsyncFailures(t *testing.T) {
	observed := make(chan struct {
		addr string
		err  error
	}, 1)
	wantErr := errors.New("dial failed")
	s := newPeerConnectScheduler(context.Background(), func(context.Context, string) error {
		return wantErr
	}, 1, 1, func(addr string, err error) {
		observed <- struct {
			addr string
			err  error
		}{addr, err}
	})
	require.NoError(t, s.Enqueue("bad:1"))
	select {
	case got := <-observed:
		require.Equal(t, "bad:1", got.addr)
		require.ErrorIs(t, got.err, wantErr)
	case <-time.After(time.Second):
		t.Fatal("failure observer was not called")
	}
	s.Close()
}

func TestPeerConnectSchedulerCloseRejectsAdmissionAndJoinsWorkers(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	s := newPeerConnectScheduler(context.Background(), func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}, 1, 1, nil)
	require.NoError(t, s.Enqueue("blocked"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dial worker did not start")
	}
	closed := make(chan struct{})
	go func() {
		s.Close()
		close(closed)
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel running dial")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not join worker")
	}
	require.ErrorIs(t, s.Enqueue("after-close"), types.ErrPeerConnectClosed)
	require.Zero(t, peerConnectPendingCount(s))
}

func TestPeerConnectSchedulerParentCancellationRejectsAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newPeerConnectScheduler(ctx, func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}, 1, 1, nil)
	cancel()
	require.Eventually(t, func() bool {
		return errors.Is(s.Enqueue("after-cancel"), types.ErrPeerConnectClosed)
	}, time.Second, time.Millisecond)
	s.Close()
}
