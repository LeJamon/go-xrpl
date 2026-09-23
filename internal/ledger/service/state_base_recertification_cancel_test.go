package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type recertificationPinTrackingDatabase struct {
	*blockingStateBaseRecertificationDatabase
	pins atomic.Int32
}

func (d *recertificationPinTrackingDatabase) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	fingerprint, release, err := d.checkpointTrackingDatabase.AcquireDurableSnapshot(ctx)
	if err != nil {
		return fingerprint, nil, err
	}
	d.pins.Add(1)
	return fingerprint, func() { release(); d.pins.Add(-1) }, nil
}

func TestStateBaseRecertificationCancelsObsoleteWalkAndReleasesSnapshot(t *testing.T) {
	for _, reason := range []string{"skipped successor", "storage mutation"} {
		t.Run(reason, func(t *testing.T) {
			f := newStateBaseRecertificationFixture(t)
			f.svc.StopStateBaseRecertification()
			f.invalidate(t)
			db := &recertificationPinTrackingDatabase{
				blockingStateBaseRecertificationDatabase: &blockingStateBaseRecertificationDatabase{
					checkpointTrackingDatabase: &checkpointTrackingDatabase{Database: f.db, uncached: f.db},
					target:                     nodestore.Hash256(f.childHash), started: make(chan struct{}), release: make(chan struct{}),
				},
			}
			f.svc.nodeStore = db
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("verification did not join after cancellation")
				}
			})
			go func() {
				defer close(done)
				done <- f.svc.recertifyValidatedStateBase(ctx)
			}()
			select {
			case <-db.started:
			case <-time.After(2 * time.Second):
				t.Fatal("verification did not reach blocked durable read")
			}
			require.EqualValues(t, 1, db.pins.Load())
			if reason == "storage mutation" {
				f.svc.InvalidateFastLoadCheckpointEligibility()
			} else {
				h := f.validated.Header()
				h.Validated = false
				h.LedgerIndex += 2
				h.ParentHash = f.validated.Hash()
				h.Hash = header.CalculateHash(h)
				state, err := f.validated.StateMapSnapshot()
				require.NoError(t, err)
				txs, err := f.validated.TxMapSnapshot()
				require.NoError(t, err)
				next, err := ledger.NewFromHeader(h, state, txs, drops.Fees{})
				require.NoError(t, err)
				require.NoError(t, next.SetValidated())
				f.svc.canonicalPersistMu.Lock()
				f.svc.advanceStateBaseRecertification(t.Context(), next)
				f.svc.canonicalPersistMu.Unlock()
			}
			// The read remains blocked unless its per-attempt context is canceled.
			select {
			case err := <-done:
				require.ErrorIs(t, err, errStateBaseRecertificationSuperseded)
			case <-time.After(2 * time.Second):
				t.Fatal("obsolete walk kept reading instead of canceling")
			}
			require.Zero(t, db.pins.Load())
			_, found := f.svc.currentValidatedStateBaseProof()
			require.False(t, found)
			// A replacement attempt can verify the still-current durable base.
			db.unblock()
			require.NoError(t, f.svc.recertifyValidatedStateBase(t.Context()))
			require.Zero(t, db.pins.Load())
			_, found = f.svc.currentValidatedStateBaseProof()
			require.True(t, found)
		})
	}
}
