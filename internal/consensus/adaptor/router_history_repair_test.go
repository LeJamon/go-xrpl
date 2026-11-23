package adaptor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

// failingLedgerIndex fails relational writes of selected ledgers; a negative
// count fails every attempt.
type failingLedgerIndex struct {
	relationaldb.RepositoryManager
	mu        sync.Mutex
	remaining map[uint32]int
	writes    map[uint32]int
}

func (r *failingLedgerIndex) fail(seq uint32, times int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.remaining == nil {
		r.remaining = make(map[uint32]int)
	}
	r.remaining[seq] = times
}

func (r *failingLedgerIndex) attempts(seq uint32) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writes[seq]
}

func (r *failingLedgerIndex) PersistValidatedLedger(ctx context.Context, validated relationaldb.ValidatedLedger) error {
	seq := uint32(validated.Ledger.Sequence)
	r.mu.Lock()
	if r.writes == nil {
		r.writes = make(map[uint32]int)
	}
	r.writes[seq]++
	remaining := r.remaining[seq]
	if remaining > 0 {
		r.remaining[seq]--
	}
	r.mu.Unlock()
	if remaining != 0 {
		return errors.New("injected relational failure")
	}
	return r.RepositoryManager.PersistValidatedLedger(ctx, validated)
}

// failingHeaderStore fails one write of the selected ledger's header.
type failingHeaderStore struct {
	nodestore.Database
	failSeq atomic.Uint32
}

func (d *failingHeaderStore) Store(ctx context.Context, node *nodestore.Node) error {
	if node.Type == nodestore.NodeLedger && node.LedgerSeq != 0 && d.failSeq.CompareAndSwap(node.LedgerSeq, 0) {
		return errors.New("injected node-store failure")
	}
	return d.Database.Store(ctx, node)
}

func newHistoryRepairStores(t *testing.T) (nodestore.Database, *sqlitedb.RepositoryManager) {
	t.Helper()
	db, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{
		PositiveCache: nodestore.CacheConfig{Enabled: true, MaxEntries: 10_000, TTL: time.Hour},
	})
	require.NoError(t, err)
	rm, err := sqlitedb.NewRepositoryManager(context.Background(), t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, rm.Close())
		require.NoError(t, db.Close())
	})
	return db, rm
}

func startHistoryRepairService(t *testing.T, cfg service.Config) *service.Service {
	t.Helper()
	cfg.GenesisConfig = genesis.DefaultConfig()
	if cfg.NodeStore != nil {
		cfg.SHAMapFamily = backend.New(cfg.NodeStore)
	}
	svc, err := service.New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	return svc
}

func acceptUntil(t *testing.T, svc *service.Service, seq uint32) {
	t.Helper()
	for svc.GetValidatedLedgerIndex() < seq {
		_, err := svc.AcceptLedger(context.Background())
		require.NoError(t, err)
		svc.FlushPersists()
	}
}

func newHistoryRepairRouter(t *testing.T, svc *service.Service, depth uint32) (*Router, *recordingSender) {
	t.Helper()
	a, sender := newRecordingAdaptor(t, svc)
	r := newTestRouter(nil, a, make(chan *peermanagement.InboundMessage, 1))
	r.catchupReplay.configureHistoryBackfill(true, depth, false)
	trackCatchupPeer(r, 7, svc.GetValidatedLedgerIndex())
	return r, sender
}

func runHistoryBackfill(svc *service.Service, r *Router, done func() bool) {
	for range 32 {
		if done() {
			return
		}
		r.catchupReplay.armHistoryBackfill()
		svc.FlushPersists()
	}
}

// writeLedgerHistory persists ledgers up to tip with a standalone writer that
// the caller stops before restarting on the same stores.
func writeLedgerHistory(t *testing.T, db nodestore.Database, relational relationaldb.RepositoryManager, tip uint32) *service.Service {
	t.Helper()
	writer, err := service.New(service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  relational,
	})
	require.NoError(t, err)
	require.NoError(t, writer.Start())
	acceptUntil(t, writer, tip)
	return writer
}

func restartFastLoad(t *testing.T, db nodestore.Database, relational relationaldb.RepositoryManager) *service.Service {
	t.Helper()
	svc := startHistoryRepairService(t, service.Config{
		NodeStore:    db,
		RelationalDB: relational,
		FastLoad:     true,
	})
	require.True(t, svc.IsFastLoadProvisional())
	return svc
}

// A resident ledger whose history persist failed during the restart walk is
// retargeted on a later tick and repaired without a network request.
func TestHistoryBackfillRetargetsResidentHoleLocally(t *testing.T) {
	for _, nodeStoreFailure := range []bool{false, true} {
		name := "relational"
		if nodeStoreFailure {
			name = "node store"
		}
		t.Run(name, func(t *testing.T) {
			db, rm := newHistoryRepairStores(t)
			writeLedgerHistory(t, db, rm, 10).Stop()

			const hole = 7
			relational := &failingLedgerIndex{RepositoryManager: rm}
			store := &failingHeaderStore{Database: db}
			if nodeStoreFailure {
				store.failSeq.Store(hole)
			} else {
				relational.fail(hole, 1)
			}
			svc := restartFastLoad(t, store, relational)
			tip := svc.GetValidatedLedgerIndex()
			r, sender := newHistoryRepairRouter(t, svc, tip-2)

			runHistoryBackfill(svc, r, func() bool {
				for seq := uint32(2); seq < tip; seq++ {
					if seq != hole && !svc.HasCompleteLedger(seq) {
						return false
					}
				}
				return true
			})
			for seq := uint32(2); seq < tip; seq++ {
				require.Equalf(t, seq != hole, svc.HasCompleteLedger(seq), "ledger %d", seq)
			}

			runHistoryBackfill(svc, r, func() bool { return svc.HasCompleteLedger(hole) })
			require.True(t, svc.HasCompleteLedger(hole))
			require.Empty(t, sender.legacyCalls())
		})
	}
}

// A hole that leaves memory after the initial walk is acquired from the
// network once validated persists succeed again.
func TestHistoryBackfillAcquiresEvictedHole(t *testing.T) {
	db, rm := newHistoryRepairStores(t)
	relational := &failingLedgerIndex{RepositoryManager: rm}
	svc := startHistoryRepairService(t, service.Config{
		Standalone:      true,
		LedgerCacheSize: 3,
		NodeStore:       db,
		RelationalDB:    relational,
	})
	acceptUntil(t, svc, 8)
	r, sender := newHistoryRepairRouter(t, svc, 6)
	r.catchupReplay.armHistoryBackfill()
	require.Empty(t, sender.legacyCalls())

	hole := svc.GetValidatedLedgerIndex() + 1
	relational.fail(hole, -1)
	relational.fail(hole+1, 1)
	acceptUntil(t, svc, hole+1)
	r.catchupReplay.armHistoryBackfill()
	svc.FlushPersists()
	require.Equal(t, 1, relational.attempts(hole), "no history work while validated persists fail")

	acceptUntil(t, svc, hole+3)
	relational.fail(hole, 0)
	acceptUntil(t, svc, hole+5)
	_, err := svc.AdoptedLedgerBySequence(hole)
	require.Error(t, err)
	require.False(t, svc.HasCompleteLedger(hole))

	trackCatchupPeer(r, 7, svc.GetValidatedLedgerIndex())
	r.catchupReplay.armHistoryBackfill()
	calls := sender.legacyCalls()
	require.Len(t, calls, 1)
	require.Equal(t, hole, calls[0].seq)
}

// After a restart the walk requests the unindexed ledger, and installing the
// acquired copy re-indexes it.
func TestHistoryBackfillReindexesHoleAfterRestart(t *testing.T) {
	ctx := context.Background()
	db, rm := newHistoryRepairStores(t)
	relational := &failingLedgerIndex{RepositoryManager: rm}
	const hole = 7
	relational.fail(hole, -1)
	writer := writeLedgerHistory(t, db, relational, 10)
	acquired, err := writer.GetLedgerBySequence(hole)
	require.NoError(t, err)
	writer.Stop()
	_, err = rm.Ledger().GetLedgerInfoBySeq(ctx, hole)
	require.ErrorIs(t, err, relationaldb.ErrLedgerNotFound)

	svc := restartFastLoad(t, db, rm)
	tip := svc.GetValidatedLedgerIndex()
	r, sender := newHistoryRepairRouter(t, svc, tip-2)
	runHistoryBackfill(svc, r, func() bool { return len(sender.legacyCalls()) != 0 })
	calls := sender.legacyCalls()
	require.Len(t, calls, 1)
	require.Equal(t, uint32(hole), calls[0].seq)
	require.Equal(t, acquired.Hash(), calls[0].hash)

	h := acquired.Header()
	stateMap, err := acquired.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := acquired.TxMapSnapshot()
	require.NoError(t, err)
	require.NoError(t, svc.IngestHistoricalLedgerWithState(ctx, &h, stateMap, txMap))
	svc.FlushPersists()
	require.True(t, svc.HasCompleteLedger(hole))
	info, err := rm.Ledger().GetLedgerInfoBySeq(ctx, hole)
	require.NoError(t, err)
	require.Equal(t, acquired.Hash(), [32]byte(info.Hash))
}
