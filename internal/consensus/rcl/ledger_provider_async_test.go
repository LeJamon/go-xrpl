package rcl

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/require"
)

func TestBackgroundAncestryProvider_ColdLookupDoesNotBlockOrDuplicate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tip, headers := buildChain(5, 'q')
		release := make(chan struct{})
		var calls atomic.Int32
		p := newBackgroundAncestryProvider(func(ctx context.Context, hash [32]byte) (LedgerHeader, error) {
			if hash == tip.hash {
				calls.Add(1)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return headers[hash], nil
		})
		t.Cleanup(p.stop)
		id := consensus.LedgerID(tip.hash)
		returned := make(chan bool, 1)
		go func() { l, ok := p.LedgerByID(id); returned <- l == nil && !ok }()
		synctest.Wait()
		select {
		case unresolved := <-returned:
			require.True(t, unresolved)
		default:
			t.Fatal("consensus lookup waited for blocked storage")
		}
		require.EqualValues(t, 1, calls.Load())
		for range 100 {
			l, ok := p.LedgerByID(id)
			require.False(t, ok)
			require.Nil(t, l)
		}
		require.EqualValues(t, 1, calls.Load())
		close(release)
		synctest.Wait()
		resolved, ok := p.LedgerByID(id)
		require.True(t, ok)
		require.Equal(t, id, resolved.ID())
		require.EqualValues(t, 1, resolved.MinSeq())
		require.EqualValues(t, 1, calls.Load())
	})
}

func TestBackgroundAncestryProvider_BoundedQueueAndCancelableShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var active, peak atomic.Int32
		p := newBackgroundAncestryProvider(func(ctx context.Context, _ [32]byte) (LedgerHeader, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			<-ctx.Done()
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Errorf("ancestry read ended with %v, want shutdown cancellation", ctx.Err())
			}
			return nil, ctx.Err()
		})
		t.Cleanup(p.stop)
		for i := range 1000 {
			var id consensus.LedgerID
			id[0], id[1] = byte(i), byte(i>>8)
			_, _ = p.LedgerByID(id)
		}
		synctest.Wait()
		require.EqualValues(t, ancestryWorkers, active.Load())
		p.mu.Lock()
		pending, queued := len(p.inflight), len(p.background.jobs)
		p.mu.Unlock()
		require.LessOrEqual(t, pending, ancestryQueueCapacity+ancestryWorkers)
		require.LessOrEqual(t, queued, ancestryQueueCapacity)
		p.stop()
		require.Zero(t, active.Load())
		require.LessOrEqual(t, peak.Load(), int32(ancestryWorkers))
		p.mu.Lock()
		pending = len(p.inflight)
		p.mu.Unlock()
		require.Zero(t, pending)
		l, ok := p.LedgerByID(consensus.LedgerID{99})
		require.False(t, ok)
		require.Nil(t, l)
	})
}

func TestBackgroundAncestryProvider_RepairsPartialWithoutBlockingFallback(t *testing.T) {
	tip, headers := buildChain(5, 'p')
	var missing [32]byte
	for hash, h := range headers {
		if h.Sequence() == 3 {
			missing = hash
		}
	}
	saved := headers[missing]
	delete(headers, missing)
	var mu sync.RWMutex
	p := newBackgroundAncestryProvider(func(_ context.Context, hash [32]byte) (LedgerHeader, error) {
		mu.RLock()
		defer mu.RUnlock()
		h, ok := headers[hash]
		if !ok {
			return nil, errors.New("not acquired yet")
		}
		return h, nil
	})
	t.Cleanup(p.stop)
	id := consensus.LedgerID(tip.hash)
	require.Eventually(t, func() bool { l, ok := p.LedgerByID(id); return ok && l.MinSeq() == 3 }, progressTimeout, time.Millisecond)
	mu.Lock()
	headers[missing] = saved
	mu.Unlock()
	require.Eventually(t, func() bool { l, ok := p.LedgerByID(id); return ok && l.MinSeq() == 1 }, progressTimeout, time.Millisecond)
	l, _ := p.LedgerByID(id)
	require.Equal(t, consensus.LedgerID(missing), l.Ancestor(3))
}

func TestBackgroundAncestryProvider_PanicDoesNotStrandWorkerOrFlight(t *testing.T) {
	var panicNext atomic.Bool
	panicNext.Store(true)
	tip, _ := buildChain(1, 'x')
	p := newBackgroundAncestryProvider(func(context.Context, [32]byte) (LedgerHeader, error) {
		if panicNext.Swap(false) {
			panic("injected read panic")
		}
		return tip, nil
	})
	t.Cleanup(p.stop)
	id := consensus.LedgerID(tip.hash)
	require.Eventually(t, func() bool { _, ok := p.LedgerByID(id); return ok }, progressTimeout, time.Millisecond)
}

func TestBackgroundAncestryProvider_EngineOwnsReplacementAndShutdown(t *testing.T) {
	e := NewEngine(newMockAdaptor(), DefaultConfig())
	p := NewAncestryProvider(func(context.Context, [32]byte) (LedgerHeader, error) { return nil, errors.New("unused") })
	require.NotNil(t, p.background, "production provider must use background lookup")
	e.SetLedgerAncestryProvider(p)
	e.SetLedgerAncestryProvider(p) // Reinstalling the same instance must not stop it.
	require.False(t, p.background.stopped)
	require.NoError(t, p.background.ctx.Err())
	e.SetLedgerAncestryProvider(nil)
	require.True(t, p.background.stopped)
	require.ErrorIs(t, p.background.ctx.Err(), context.Canceled)
	q := newBackgroundAncestryProvider(nil)
	e.SetLedgerAncestryProvider(q)
	require.NoError(t, e.Stop())
	require.True(t, q.background.stopped)
	require.ErrorIs(t, q.background.ctx.Err(), context.Canceled)
	r := newBackgroundAncestryProvider(nil)
	e.SetLedgerAncestryProvider(r)
	require.True(t, r.background.stopped, "stopped engine must reject and close new providers")
	require.ErrorIs(t, r.background.ctx.Err(), context.Canceled)
}

func TestBackgroundAncestryProvider_TrackerProgressesDuringBlockedRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tip, headers := buildChain(5, 't')
		release := make(chan struct{})
		p := newBackgroundAncestryProvider(func(ctx context.Context, hash [32]byte) (LedgerHeader, error) {
			select {
			case <-release:
				return headers[hash], nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		t.Cleanup(p.stop)
		vt := NewValidationTracker(2)
		now := time.Now()
		vt.SetNow(func() time.Time { return now })
		first, second := consensus.NodeID{1}, consensus.NodeID{2}
		vt.setTrusted([]consensus.NodeID{first, second})
		vt.setLedgerAncestryProvider(p)
		id := consensus.LedgerID(tip.hash)
		done := make(chan bool, 1)
		go func() {
			one := vt.Add(makeTrustedValidation(first, id, tip.seq, now))
			two := vt.Add(makeTrustedValidation(second, id, tip.seq, now))
			_, _, _ = vt.GetPreferred(0)
			done <- one && two
		}()
		synctest.Wait()
		select {
		case accepted := <-done:
			require.True(t, accepted)
		default:
			t.Fatal("validation ingestion/preference polling waited for storage")
		}
		close(release)
		require.Eventually(t, func() bool {
			got, seq, ok := vt.GetPreferred(0)
			return ok && got == id && seq == tip.seq
		}, progressTimeout, time.Millisecond)
	})
}
