package rpc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingLedgerIndex struct {
	relationaldb.RepositoryManager
	failSeq atomic.Uint32
}

func (r *failingLedgerIndex) PersistValidatedLedger(ctx context.Context, validated relationaldb.ValidatedLedger) error {
	if uint32(validated.Ledger.Sequence) == r.failSeq.Load() {
		return errors.New("injected relational failure")
	}
	return r.RepositoryManager.PersistValidatedLedger(ctx, validated)
}

func TestServerInfoReportsLedgerPersistenceFailures(t *testing.T) {
	ctx := context.Background()
	db, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{
		PositiveCache: nodestore.CacheConfig{Enabled: true, MaxEntries: 10_000, TTL: time.Hour},
	})
	require.NoError(t, err)
	rm, err := sqlitedb.NewRepositoryManager(ctx, t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	relational := &failingLedgerIndex{RepositoryManager: rm}
	svc, err := service.New(service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  relational,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(func() {
		svc.Stop()
		require.NoError(t, rm.Close())
		require.NoError(t, db.Close())
	})
	rpcCtx := &types.RpcContext{
		Context:  t.Context(),
		Services: types.NewTestServiceGraph(types.NewServiceContainer(rpcadapter.NewLedgerServiceAdapter(svc))),
	}

	failed := svc.GetClosedLedgerIndex() + 1
	relational.failSeq.Store(failed)
	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"consecutive_failures": uint32(1),
		"last_failed_seq":      failed,
	}, callServerStatus(t, rpcCtx, true)["ledger_persistence"])

	relational.failSeq.Store(0)
	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	assert.NotContains(t, callServerStatus(t, rpcCtx, false), "ledger_persistence")
}
