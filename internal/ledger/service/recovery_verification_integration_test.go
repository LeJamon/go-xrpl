package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type recoveryVerificationFamily struct {
	*replayAdmissionFamily
	root        [32]byte
	armed       atomic.Bool
	rootReads   atomic.Int32
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (f *recoveryVerificationFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if hash == f.root && f.armed.Load() {
		if f.rootReads.Add(1) == 1 {
			close(f.entered)
		}
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.replayAdmissionFamily.FetchDurable(ctx, hash)
}

func (f *recoveryVerificationFamily) unblock() {
	f.releaseOnce.Do(func() { close(f.release) })
}

func TestStoredPromotionAndPreferredRecoveryShareVerification(t *testing.T) {
	t.Run("promotion first", func(t *testing.T) { testSharedRecoveryVerification(t, false) })
	t.Run("preferred first", func(t *testing.T) { testSharedRecoveryVerification(t, true) })
}

func testSharedRecoveryVerification(t *testing.T, preferredFirst bool) {
	t.Helper()
	family := &recoveryVerificationFamily{
		replayAdmissionFamily: newReplayAdmissionFamily(t),
		entered:               make(chan struct{}),
		release:               make(chan struct{}),
	}
	cfg := DefaultConfig()
	cfg.SHAMapFamily = family
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	t.Cleanup(family.unblock)
	parent := svc.GetClosedLedger()
	child, err := ledger.NewOpen(parent, parent.CloseTime().Add(time.Second))
	require.NoError(t, err)
	for i := range 256 {
		require.NoError(t, child.Insert(keylet.Keylet{Key: [32]byte{byte(i), 0x12, 0x27}}, []byte("recovery state")))
	}
	require.NoError(t, child.Close(parent.CloseTime().Add(time.Second), 0))
	state, err := child.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, state.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}))
	family.root = child.Header().AccountHash
	state, err = shamap.NewFromRootHashContext(t.Context(), shamap.TypeState, family.root, family)
	require.NoError(t, err)
	txs := shamap.New(shamap.TypeTransaction)
	stored, err := ledger.NewFromHeader(child.Header(), state, txs, child.Fees())
	require.NoError(t, err)
	svc.historyComponent.mu.Lock()
	svc.cachePersistedLedgerLocked(stored)
	svc.historyComponent.mu.Unlock()

	family.armed.Store(true)
	promoted := make(chan struct{})
	promote := func() {
		go func() {
			svc.PromoteStoredValidatedLedgerAt(stored.Sequence(), stored.Hash(), time.Time{})
			close(promoted)
		}()
	}
	preferred := make(chan error, 1)
	ctx := t.Context()
	prepare := func() {
		go func() {
			prepared, err := svc.PreparePreferredLedger(ctx, stored)
			if err == nil {
				defer prepared.Release()
				err = prepared.Wait(ctx)
				if err == nil {
					_, err = prepared.TryPublish()
					if err == nil {
						prepared.Finish()
					}
				}
			}
			preferred <- err
		}()
	}
	if preferredFirst {
		prepare()
		awaitPreferredPreparation(t, family.entered)
		promote()
	} else {
		promote()
		awaitPreferredPreparation(t, family.entered)
		prepare()
	}
	require.Eventually(t, func() bool {
		svc.replayVerificationMu.Lock()
		defer svc.replayVerificationMu.Unlock()
		for _, flight := range svc.replayVerificationFlights {
			if flight.waiters == 2 {
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond, "preferred recovery did not join the stored promotion verification")
	require.False(t, svc.preferredTransitionIsActive(), "cold verification must not own the preferred transition")
	family.unblock()
	select {
	case err := <-preferred:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("preferred recovery did not consume the completed verification")
	}
	select {
	case <-promoted:
	case <-time.After(5 * time.Second):
		t.Fatal("stored promotion remained blocked after verification and publication")
	}
	require.Equal(t, int32(1), family.rootReads.Load(), "recovery repeated the full-state walk")
	require.Equal(t, stored.Hash(), svc.GetValidatedLedger().Hash())
	require.Equal(t, stored.Hash(), svc.GetClosedLedger().Hash())
}
