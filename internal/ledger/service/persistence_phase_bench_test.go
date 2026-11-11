package service

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

type phaseTimedNodeStore struct {
	nodestore.Database
	write, sync time.Duration
}

func (d *phaseTimedNodeStore) Store(ctx context.Context, node *nodestore.Node) error {
	started := time.Now()
	defer func() { d.write += time.Since(started) }()
	return d.Database.Store(ctx, node)
}

func (d *phaseTimedNodeStore) StoreBatch(ctx context.Context, nodes []*nodestore.Node) error {
	started := time.Now()
	defer func() { d.write += time.Since(started) }()
	return d.Database.StoreBatch(ctx, nodes)
}

func (d *phaseTimedNodeStore) Sync(ctx context.Context) error {
	started := time.Now()
	defer func() { d.sync += time.Since(started) }()
	return d.Database.Sync(ctx)
}

type phaseTimedRepositories struct {
	relationaldb.RepositoryManager
	persist time.Duration
}

func (r *phaseTimedRepositories) PersistValidatedLedger(ctx context.Context, validated relationaldb.ValidatedLedger) error {
	started := time.Now()
	defer func() { r.persist += time.Since(started) }()
	return r.RepositoryManager.PersistValidatedLedger(ctx, validated)
}

// persistPhaseChain closes consecutive ledgers of signed XRP payments from the
// master account to a fixed destination set; the first ledger creates the
// destinations and later ledgers modify them.
type persistPhaseChain struct {
	svc          *Service
	env          *jtx.TestEnv
	master       *jtx.Account
	destinations []*jtx.Account
	sequence     uint32
}

func newPersistPhaseChain(b *testing.B, txPerLedger int) *persistPhaseChain {
	b.Helper()
	svc, err := New(DefaultConfig())
	require.NoError(b, err)
	require.NoError(b, svc.Start())
	b.Cleanup(svc.Stop)
	env := jtx.NewTestEnv(b)
	env.SetVerifySignatures(true)
	chain := &persistPhaseChain{
		svc:          svc,
		env:          env,
		master:       jtx.MasterAccount(),
		destinations: make([]*jtx.Account, txPerLedger),
		sequence:     1,
	}
	for i := range chain.destinations {
		chain.destinations[i] = jtx.NewAccount(fmt.Sprintf("persist-phase-destination-%d", i))
	}
	return chain
}

func (c *persistPhaseChain) next(b *testing.B) *ledger.Ledger {
	b.Helper()
	blobs := make([][]byte, len(c.destinations))
	for i, destination := range c.destinations {
		blobs[i], _ = buildServicePaymentBlob(b, c.env, c.master, destination, 250_000_000, c.sequence)
		c.sequence++
	}
	parent := c.svc.GetClosedLedger()
	_, err := c.svc.AcceptConsensusResult(b.Context(), parent, blobs, nil, parent.CloseTime().Add(10*time.Second), true)
	require.NoError(b, err)
	closed := c.svc.GetClosedLedger()
	require.Equal(b, uint32(len(blobs)), closed.TxCount())
	return closed
}

// BenchmarkPersistLedgerPhases breaks one validated ledger's persistence into
// the phases the persistence worker runs serially, against pebble and SQLite
// in a temporary directory.
func BenchmarkPersistLedgerPhases(b *testing.B) {
	for _, txPerLedger := range []int{50, 200, 1000} {
		b.Run(fmt.Sprintf("tx=%d", txPerLedger), func(b *testing.B) {
			benchmarkPersistLedgerPhases(b, txPerLedger)
		})
	}
}

func benchmarkPersistLedgerPhases(b *testing.B, txPerLedger int) {
	dir := b.TempDir()
	store, err := kvpebble.New(filepath.Join(dir, "nodes"), kvpebble.Options{BlockCacheBytes: 32 << 20, MaxOpenFiles: 200})
	require.NoError(b, err)
	base, err := nodestore.NewKVDatabase(store, nodestore.DefaultDatabaseConfig())
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, base.Close()) })
	repositories, err := sqlitedb.NewRepositoryManager(b.Context(), filepath.Join(dir, "sqlite"), sqlitedb.Settings{})
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, repositories.Close()) })

	nodes := &phaseTimedNodeStore{Database: base}
	relational := &phaseTimedRepositories{RepositoryManager: repositories}
	writer := &Service{nodeStore: nodes, shamapFamily: backend.New(nodes), relationalDB: relational}
	chain := newPersistPhaseChain(b, txPerLedger)
	genesisLedger := chain.svc.GetClosedLedger()
	require.NoError(b, writer.persistToNodeStore(b.Context(), genesisLedger, genesisLedger.Sequence()))

	var nodeSerialize, nodeWrite, nodeSync, relDecode, relSQLite, tip time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		closed := chain.next(b)
		write, sync, persist := nodes.write, nodes.sync, relational.persist
		b.StartTimer()

		started := time.Now()
		require.NoError(b, writer.persistToNodeStore(b.Context(), closed, closed.Sequence()))
		nodeDone := time.Now()
		require.NoError(b, writer.persistToRelationalDB(b.Context(), closed))
		relationalDone := time.Now()
		tipWrite, tipSync := nodes.write, nodes.sync
		require.NoError(b, writer.persistValidatedTipLocked(b.Context(), closed, false))
		tipDone := time.Now()

		b.StopTimer()
		nodeWrite += tipWrite - write
		nodeSync += tipSync - sync
		nodeSerialize += nodeDone.Sub(started) - (tipWrite - write) - (tipSync - sync)
		relSQLite += relational.persist - persist
		relDecode += relationalDone.Sub(nodeDone) - (relational.persist - persist)
		tip += tipDone.Sub(relationalDone)
		b.StartTimer()
	}
	b.StopTimer()
	for _, phase := range []struct {
		name     string
		duration time.Duration
	}{
		{"node-serialize", nodeSerialize},
		{"node-write", nodeWrite},
		{"node-sync", nodeSync},
		{"rel-decode", relDecode},
		{"rel-sqlite", relSQLite},
		{"tip", tip},
	} {
		b.ReportMetric(float64(phase.duration.Nanoseconds())/float64(b.N), phase.name+"-ns/op")
	}
}
