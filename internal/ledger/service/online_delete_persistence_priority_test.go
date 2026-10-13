package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestService_RefreshBatchesWaitForLedgerPersistence(t *testing.T) {
	t.Run("resume", func(t *testing.T) { testRefreshPersistencePriority(t, false) })
	t.Run("cancel", func(t *testing.T) { testRefreshPersistencePriority(t, true) })
}

func testRefreshPersistencePriority(t *testing.T, canceled bool) {
	svc, db, seq := newRotatingRefreshFixture(t, 256)
	root, err := svc.GetValidatedLedger().StateMapHash()
	require.NoError(t, err)
	db.headerStoreStart = make(chan struct{})
	db.headerStoreResume = make(chan struct{})
	db.promotionStart = make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(db.headerStoreResume) }) }
	t.Cleanup(release)

	next := buildLedgerWithState(t, seq+1)
	persistDone := make(chan error, 1)
	go func() {
		persistDone <- svc.persistToNodeStore(t.Context(), next, next.Sequence())
	}()
	<-db.headerStoreStart

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- svc.refreshGenerationStateWithBatch(ctx, root, seq, db, nil, 4, 64, 4<<20)
	}()
	<-db.promotionStart
	select {
	case err := <-refreshDone:
		t.Fatalf("refresh bypassed pending persistence: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	require.Zero(t, db.promotionBatches.Load())

	if canceled {
		cancel()
		select {
		case err := <-refreshDone:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("refresh cancellation waited for ledger persistence")
		}
		release()
		require.NoError(t, <-persistDone)
		require.Zero(t, db.promotionBatches.Load())
		return
	}
	release()
	require.NoError(t, <-persistDone)
	require.NoError(t, <-refreshDone)
	require.Positive(t, db.promotionBatches.Load())
}
