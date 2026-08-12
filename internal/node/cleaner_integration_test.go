package node

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	ledgercleaner "github.com/LeJamon/go-xrpl/internal/ledger/cleaner"
	ledgerservice "github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
)

func TestLedgerCleanerProductionBoundaryRepairsDurableTransactionRoot(t *testing.T) {
	ctx := t.Context()
	store := memorydb.New()
	db, err := nodestore.NewKVDatabase(store, nodestore.DatabaseConfig{
		PositiveCache: nodestore.CacheConfig{
			Enabled:    true,
			MaxEntries: 2000,
			TTL:        time.Hour,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	family := backend.New(db)
	repositories, err := sqlitedb.NewRepositoryManager(ctx, t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repositories.Close()) })

	svc, err := ledgerservice.New(ledgerservice.Config{
		Standalone:   true,
		NodeStore:    db,
		SHAMapFamily: family,
		RelationalDB: repositories,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	for env.LedgerSeq() <= svc.GetValidatedLedger().Sequence() {
		env.Close()
	}
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()
	closed := env.LastClosedLedger()
	require.NotNil(t, closed)
	stateMap, err := closed.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := closed.TxMapSnapshot()
	require.NoError(t, err)
	hdr := closed.Header()
	require.NotZero(t, hdr.AccountHash)
	require.NotZero(t, hdr.TxHash)
	var transactionHash [32]byte
	require.NoError(t, closed.ForEachTransaction(func(hash [32]byte, _ []byte) bool {
		transactionHash = hash
		return false
	}))
	require.NotZero(t, transactionHash)

	require.NoError(t, svc.AdoptLedgerWithState(ctx, &hdr, stateMap, txMap))
	svc.SetValidatedLedger(hdr.LedgerIndex, hdr.Hash)
	require.Equal(t, hdr.Hash, svc.GetValidatedLedger().Hash())
	svc.FlushPersists()
	transaction, _, err := repositories.Transaction().GetTransaction(
		ctx, relationaldb.Hash(transactionHash), nil,
	)
	require.NoError(t, err)
	require.NotNil(t, transaction)
	accountOptions := relationaldb.AccountTxPageOptions{
		Account:   relationaldb.AccountID(alice.ID),
		MinLedger: relationaldb.LedgerIndex(hdr.LedgerIndex),
		MaxLedger: relationaldb.LedgerIndex(hdr.LedgerIndex),
		Limit:     20,
	}
	accountTransactions, err := repositories.AccountTransaction().GetNewestAccountTxsPage(ctx, accountOptions)
	require.NoError(t, err)
	require.NotEmpty(t, accountTransactions.Transactions)
	require.NoError(t, repositories.Transaction().DeleteTransactionsByLedgerSeq(
		ctx, relationaldb.LedgerIndex(hdr.LedgerIndex),
	))
	transaction, _, err = repositories.Transaction().GetTransaction(
		ctx, relationaldb.Hash(transactionHash), nil,
	)
	require.NoError(t, err)
	require.Nil(t, transaction)
	accountTransactions, err = repositories.AccountTransaction().GetNewestAccountTxsPage(ctx, accountOptions)
	require.NoError(t, err)
	require.Empty(t, accountTransactions.Transactions)

	stateData, err := family.FetchDurable(ctx, hdr.AccountHash)
	require.NoError(t, err)
	require.NotEmpty(t, stateData)
	txData, err := family.FetchDurable(ctx, hdr.TxHash)
	require.NoError(t, err)
	require.NotEmpty(t, txData)
	cachedTxData, err := family.Fetch(ctx, hdr.TxHash)
	require.NoError(t, err)
	require.Equal(t, txData, cachedTxData)

	source := &ledgerCleanerSource{svc: svc, family: family}
	stored, ok, err := source.Ledger(ctx, hdr.LedgerIndex)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, hdr.AccountHash, stored.StateRoot)
	require.Equal(t, hdr.TxHash, stored.TxRoot)
	require.Same(t, family, source.Family())

	encodedTxRoot, err := store.Get(hdr.TxHash[:])
	require.NoError(t, err)
	batch, err := store.NewBatch()
	require.NoError(t, err)
	require.NoError(t, batch.Delete(hdr.TxHash[:]))
	require.NoError(t, batch.Write())
	require.NoError(t, batch.Close())
	cachedTxData, err = family.Fetch(ctx, hdr.TxHash)
	require.NoError(t, err)
	require.Equal(t, txData, cachedTxData)
	durableTxData, err := family.FetchDurable(ctx, hdr.TxHash)
	require.NoError(t, err)
	require.Empty(t, durableTxData)

	reacquired := make(chan uint32, 1)
	var restoreOnce sync.Once
	source.SetReacquire(func(ctx context.Context, seq uint32) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var restoreErr error
		restoreOnce.Do(func() {
			restoreErr = store.Put(hdr.TxHash[:], encodedTxRoot)
			if restoreErr == nil {
				reacquired <- seq
			}
		})
		return restoreErr
	})

	cleaner := ledgercleaner.New(source, nil)
	cleaner.Start()
	t.Cleanup(cleaner.Stop)
	var configurations atomic.Int32
	services := types.NewTestServiceGraph(&types.ServiceContainer{
		LedgerCleanerConfigure: func(p types.LedgerCleanerParams) types.LedgerCleanerStatus {
			configurations.Add(1)
			return toCleanerStatus(cleaner.Clean(ledgercleaner.Params{
				Ledger:     p.Ledger,
				MinLedger:  p.MinLedger,
				MaxLedger:  p.MaxLedger,
				Full:       p.Full,
				CheckNodes: p.CheckNodes,
				FixTxns:    p.FixTxns,
				Stop:       p.Stop,
			}))
		},
		LedgerCleanerStatusFn: func() types.LedgerCleanerStatus {
			return toCleanerStatus(cleaner.Status())
		},
	})
	rpcCtx := &types.RpcContext{
		Context:    ctx,
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}
	method := &handlers.LedgerCleanerMethod{}
	params, err := json.Marshal(map[string]any{"ledger": hdr.LedgerIndex, "full": true})
	require.NoError(t, err)
	result, rpcErr := method.Handle(rpcCtx, params)
	require.Nil(t, rpcErr)
	configured := result.(map[string]any)
	require.Equal(t, "running", configured["status"])
	require.Equal(t, true, configured["check_nodes"])
	require.Equal(t, true, configured["fix_txns"])
	require.Equal(t, "Cleaner configured", configured["message"])

	select {
	case seq := <-reacquired:
		require.Equal(t, hdr.LedgerIndex, seq)
	case <-time.After(2 * time.Second):
		t.Fatal("cleaner did not request transaction-tree reacquisition")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := cleaner.Status()
		if status.State == "idle" && status.LedgersChecked == 1 {
			require.Zero(t, status.MinLedger)
			require.Zero(t, status.MaxLedger)
			require.Zero(t, status.Failures)
			require.Positive(t, status.MissingNodes)
			require.Greater(t, status.NodesChecked, uint64(2))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, "idle", cleaner.Status().State, "status=%+v", cleaner.Status())

	result, rpcErr = method.Handle(rpcCtx, nil)
	require.Nil(t, rpcErr)
	status := result.(map[string]any)
	require.Equal(t, "idle", status["status"])
	require.Equal(t, int32(1), configurations.Load())
	root, err := family.FetchDurable(ctx, hdr.TxHash)
	require.NoError(t, err)
	require.NotEmpty(t, root)
	transaction, _, err = repositories.Transaction().GetTransaction(
		ctx, relationaldb.Hash(transactionHash), nil,
	)
	require.NoError(t, err)
	require.NotNil(t, transaction)
	accountTransactions, err = repositories.AccountTransaction().GetNewestAccountTxsPage(ctx, accountOptions)
	require.NoError(t, err)
	require.NotEmpty(t, accountTransactions.Transactions)
}
