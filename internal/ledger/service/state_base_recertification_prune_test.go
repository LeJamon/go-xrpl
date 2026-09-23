package service

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestStateBaseRecertificationAfterUncommittedPrune(t *testing.T) {
	for _, cancelPrune := range []bool{false, true} {
		name := "no nodes deleted"
		if cancelPrune {
			name = "canceled after invalidation"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			db := newTestNodeStore(t, 100_000)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			svc := newFastLoadCheckpointService(t, db, newTestRepositories(t, ctx), true)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)
			svc.StopStateBaseRecertification()
			_, err := svc.AcceptLedger(ctx)
			require.NoError(t, err)
			svc.FlushPersists()
			require.NoError(t, svc.recertifyValidatedStateBase(ctx))
			before, found := svc.currentValidatedStateBaseProof()
			require.True(t, found)

			pruneCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			deleted, err := db.DeleteBeforeWithPrune(pruneCtx, 1, 16, func() func() {
				svc.InvalidateFastLoadCheckpointEligibility()
				finish := svc.shamapFamily.(*backend.NodeStore).BeginPrune()
				if cancelPrune {
					cancel()
				}
				return finish
			})
			if cancelPrune {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			require.Zero(t, deleted)
			svc.markFastLoadCheckpointEligible()
			require.Equal(t, uint32(fastLoadCheckpointInvalidated), svc.fastLoadCheckpointState.Load())
			_, release, available, err := svc.AcquireValidatedStateBase(ctx)
			if release != nil {
				release()
			}
			require.False(t, available)
			require.ErrorContains(t, err, "no matching completeness proof")

			require.NoError(t, svc.recertifyValidatedStateBase(ctx))
			after, found := svc.currentValidatedStateBaseProof()
			require.True(t, found)
			fingerprint, err := db.DurableFingerprint(ctx)
			require.NoError(t, err)
			require.Equal(t, fingerprint, after.nodeStoreFingerprint)
			require.Equal(t, before.ledgerHash, after.ledgerHash)
			if cancelPrune {
				require.Equal(t, before.nodeStoreFingerprint, fingerprint)
			} else {
				require.NotEqual(t, before.nodeStoreFingerprint, fingerprint)
			}
			root, release, available, err := svc.AcquireValidatedStateBase(ctx)
			require.NoError(t, err)
			require.True(t, available)
			require.Equal(t, after.stateRoot, root)
			release()
			svc.Stop()
			prepared, err := svc.PrepareFastLoadCheckpoint(ctx)
			require.NoError(t, err)
			require.True(t, prepared)
		})
	}
}
