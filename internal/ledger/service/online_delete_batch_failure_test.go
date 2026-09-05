package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type failingPromotionDatabase struct {
	nodestore.BatchGenerationDatabase
	failure    error
	cancel     context.CancelFunc
	batchCalls atomic.Int64
	syncCalls  atomic.Int64
}

func (d *failingPromotionDatabase) FetchBatchForPromotion(
	ctx context.Context,
	hashes []nodestore.Hash256,
	maxBytes int,
) ([]*nodestore.Node, kvstore.PromotionStats, error) {
	nodes, stats, err := d.BatchGenerationDatabase.FetchBatchForPromotion(ctx, hashes, maxBytes)
	if err != nil {
		return nil, stats, err
	}
	d.batchCalls.Add(1)
	if d.cancel != nil {
		d.cancel()
	}
	if d.failure != nil {
		return nil, stats, d.failure
	}
	return nodes, stats, nil
}

func (d *failingPromotionDatabase) Sync(ctx context.Context) error {
	d.syncCalls.Add(1)
	return d.BatchGenerationDatabase.Sync(ctx)
}

func TestService_RefreshBatchFailureDoesNotSyncIncompleteTraversal(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "fetch error"
		if canceled {
			name = "canceled after promotion"
		}
		t.Run(name, func(t *testing.T) {
			svc, db, seq := newRotatingRefreshFixture(t, 256)
			root, err := svc.GetValidatedLedger().StateMapHash()
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantErr := errors.New("promotion batch failed")
			probe := &failingPromotionDatabase{BatchGenerationDatabase: db, failure: wantErr}
			if canceled {
				wantErr = context.Canceled
				probe.failure = nil
				probe.cancel = cancel
			}
			refresh := &Service{nodeStore: probe, logger: svc.logger}
			err = refresh.refreshGenerationStateWithBatch(ctx, root, seq, probe, nil, 1, 64, 4<<20)
			require.ErrorIs(t, err, wantErr)
			require.Positive(t, probe.batchCalls.Load())
			require.Zero(t, probe.syncCalls.Load())
			lastRotated, _ := db.GenerationState()
			require.Equal(t, seq, lastRotated)
		})
	}
}
