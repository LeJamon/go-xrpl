package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

func TestService_RefreshValidatedStateSurvivesPruning(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "refresh", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	family := shamap.NewNodeStoreFamily(db)
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  family,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	seq, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	latest := svc.GetValidatedLedger()
	require.Equal(t, seq, latest.Sequence())
	root, err := latest.StateMapHash()
	require.NoError(t, err)

	require.NoError(t, svc.RefreshValidatedState(ctx, seq))
	_, err = db.DeleteBefore(ctx, seq, 1024)
	require.NoError(t, err)
	require.NoError(t, svc.verifyStoredSHAMap(ctx, root, shamap.TypeState))
}
