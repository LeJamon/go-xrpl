package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
)

type serviceStreamingPublisher struct {
	relationaldb.RepositoryManager

	calls            int
	completed        int
	seen             []int
	commitIDs        [][32]byte
	publicationKinds []string
	cancelAfter      int
	cancel           context.CancelFunc
}

func (p *serviceStreamingPublisher) PersistLedgerStream(
	ctx context.Context,
	_ relationaldb.LedgerInfo,
	commitID [32]byte,
	publicationKind string,
	next relationaldb.IndexedTransactionStream,
) error {
	p.calls++
	p.commitIDs = append(p.commitIDs, commitID)
	p.publicationKinds = append(p.publicationKinds, publicationKind)
	count := 0
	for {
		_, ok, err := next(ctx)
		if err != nil {
			p.seen = append(p.seen, count)
			return err
		}
		if !ok {
			p.completed++
			p.seen = append(p.seen, count)
			return nil
		}
		count++
		if p.cancelAfter > 0 && count == p.cancelAfter && p.cancel != nil {
			p.cancel()
		}
	}
}

func TestPersistToRelationalDBStreamingRejectsMalformedRowsWithoutPublication(t *testing.T) {
	validMeta := &tx.Metadata{TransactionIndex: 0, TransactionResult: ter.TesSUCCESS}
	badRaw, badHash := validRelationalTestTransaction(t, 1)
	badHash[0] ^= 0xff
	tests := []struct {
		name string
		raw  []byte
		meta *tx.Metadata
		hash [32]byte
	}{
		{
			name: "bad transaction hash",
			raw:  badRaw,
			meta: validMeta,
			hash: badHash,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			combined, err := tx.CreateTxWithMetaBlob(test.raw, test.meta)
			if err != nil {
				t.Fatalf("CreateTxWithMetaBlob: %v", err)
			}
			l := relationalTestLedger(t, test.hash, combined)
			publisher := &serviceStreamingPublisher{}
			service := &Service{relationalDB: publisher}

			err = service.persistToRelationalDB(context.Background(), l)
			if err == nil {
				t.Fatal("persistToRelationalDB returned nil")
			}
			if publisher.calls != 1 || publisher.completed != 0 || len(publisher.seen) != 1 || publisher.seen[0] != 0 {
				t.Fatalf("stream publication state = calls %d completed %d seen %v, want one empty unsuccessful stream", publisher.calls, publisher.completed, publisher.seen)
			}
		})
	}

	raw, hash := validRelationalTestTransaction(t, 1)
	meta := encodeRelationalTestMap(t, map[string]any{
		"AffectedNodes":    []any{},
		"TransactionIndex": uint32(0),
	})
	combined := combineRelationalTestBlobs(t, raw, meta)
	l := relationalTestLedger(t, hash, combined)
	publisher := &serviceStreamingPublisher{}
	service := &Service{relationalDB: publisher}

	if err := service.persistToRelationalDB(context.Background(), l); err == nil {
		t.Fatal("persistToRelationalDB returned nil for missing TransactionResult")
	}
	if publisher.calls != 1 || publisher.completed != 0 || len(publisher.seen) != 1 || publisher.seen[0] != 0 {
		t.Fatalf("missing metadata publication state = calls %d completed %d seen %v, want one empty unsuccessful stream", publisher.calls, publisher.completed, publisher.seen)
	}
}

func TestPersistToRelationalDBStreamingMalformedRowsRollbackSQL(t *testing.T) {
	tests := []struct {
		name string
		make func(t *testing.T) *ledger.Ledger
	}{
		{
			name: "bad transaction hash",
			make: func(t *testing.T) *ledger.Ledger {
				raw, hash := validRelationalTestTransaction(t, 1)
				hash[0] ^= 0xff
				combined, err := tx.CreateTxWithMetaBlob(raw, &tx.Metadata{
					TransactionIndex:  0,
					TransactionResult: ter.TesSUCCESS,
				})
				if err != nil {
					t.Fatalf("CreateTxWithMetaBlob: %v", err)
				}
				return relationalTestLedger(t, hash, combined)
			},
		},
		{
			name: "missing transaction result",
			make: func(t *testing.T) *ledger.Ledger {
				raw, hash := validRelationalTestTransaction(t, 1)
				meta := encodeRelationalTestMap(t, map[string]any{
					"AffectedNodes":    []any{},
					"TransactionIndex": uint32(0),
				})
				return relationalTestLedger(t, hash, combineRelationalTestBlobs(t, raw, meta))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, err := sqlitedb.NewRepositoryManager(context.Background(), t.TempDir(), sqlitedb.Settings{})
			if err != nil {
				t.Fatalf("NewRepositoryManager: %v", err)
			}
			t.Cleanup(func() {
				if err := manager.Close(); err != nil {
					t.Errorf("RepositoryManager.Close: %v", err)
				}
			})
			l := test.make(t)
			service := &Service{relationalDB: manager}
			if err := service.persistToRelationalDB(context.Background(), l); err == nil {
				t.Fatal("persistToRelationalDB returned nil")
			}
			if _, err := manager.Ledger().GetLedgerInfoBySeq(context.Background(), relationaldb.LedgerIndex(l.Sequence())); !errors.Is(err, relationaldb.ErrLedgerNotFound) {
				t.Fatalf("malformed ledger publication lookup error = %v, want ErrLedgerNotFound", err)
			}
		})
	}
}

func TestPersistToRelationalDBStreamingCommitIDIsStableAcrossReplay(t *testing.T) {
	raw, hash := validRelationalTestTransaction(t, 1)
	combined, err := tx.CreateTxWithMetaBlob(raw, &tx.Metadata{
		TransactionIndex:  0,
		TransactionResult: ter.TesSUCCESS,
	})
	if err != nil {
		t.Fatalf("CreateTxWithMetaBlob: %v", err)
	}
	l := relationalTestLedger(t, hash, combined)
	publisher := &serviceStreamingPublisher{}
	service := &Service{relationalDB: publisher}

	for i := 0; i < 2; i++ {
		if err := service.persistToRelationalDB(context.Background(), l); err != nil {
			t.Fatalf("persistToRelationalDB replay %d: %v", i, err)
		}
	}
	if publisher.calls != 2 || publisher.completed != 2 || len(publisher.seen) != 2 || publisher.seen[0] != 1 || publisher.seen[1] != 1 {
		t.Fatalf("replay publication state = calls %d completed %d seen %v, want two complete one-row streams", publisher.calls, publisher.completed, publisher.seen)
	}
	if publisher.publicationKinds[0] != validatedLedgerPublicationKind || publisher.publicationKinds[1] != validatedLedgerPublicationKind {
		t.Fatalf("publication kinds = %q, want %q twice", publisher.publicationKinds, validatedLedgerPublicationKind)
	}
	if publisher.commitIDs[0] != publisher.commitIDs[1] {
		t.Fatalf("replay commit IDs differ: %x and %x", publisher.commitIDs[0], publisher.commitIDs[1])
	}
	wantCommitID := flatstate.SemanticCommitID(l.Hash(), validatedLedgerPublicationKind)
	if publisher.commitIDs[0] != [32]byte(wantCommitID) {
		t.Fatalf("commit ID = %x, want %x", publisher.commitIDs[0], wantCommitID)
	}
}

func TestPersistToRelationalDBStreamingCancellationStopsSource(t *testing.T) {
	raw, hash := validRelationalTestTransaction(t, 1)
	combined, err := tx.CreateTxWithMetaBlob(raw, &tx.Metadata{
		TransactionIndex:  0,
		TransactionResult: ter.TesSUCCESS,
	})
	if err != nil {
		t.Fatalf("CreateTxWithMetaBlob: %v", err)
	}
	l := relationalTestLedger(t, hash, combined)
	ctx, cancel := context.WithCancel(context.Background())
	publisher := &serviceStreamingPublisher{cancelAfter: 1, cancel: cancel}
	service := &Service{relationalDB: publisher}

	err = service.persistToRelationalDB(ctx, l)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("persistToRelationalDB error = %v, want context.Canceled", err)
	}
	if publisher.completed != 0 || len(publisher.seen) != 1 || publisher.seen[0] != 1 {
		t.Fatalf("canceled publication state = completed %d seen %v, want one consumed row without completion", publisher.completed, publisher.seen)
	}
}

func TestPersistToRelationalDBStreamsGeneratedTransactions(t *testing.T) {
	const transactionCount = 128
	l := relationalTestLedgerWithTransactions(t, transactionCount)
	publisher := &serviceStreamingPublisher{}
	service := &Service{relationalDB: publisher}

	if err := service.persistToRelationalDB(context.Background(), l); err != nil {
		t.Fatalf("persistToRelationalDB: %v", err)
	}
	if publisher.completed != 1 || len(publisher.seen) != 1 || publisher.seen[0] != transactionCount {
		t.Fatalf("generated transaction stream state = completed %d seen %v, want one complete stream of %d", publisher.completed, publisher.seen, transactionCount)
	}
}

func relationalTestLedgerWithTransactions(t *testing.T, count int) *ledger.Ledger {
	t.Helper()
	g, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}
	parent, err := ledger.FromGenesis(g.Header, g.StateMap, g.TxMap, drops.DefaultFees(), newTestExecutionStore(t))
	if err != nil {
		t.Fatalf("ledger.FromGenesis: %v", err)
	}
	t.Cleanup(parent.Discard)
	l, err := ledger.NewOpen(parent, g.Header.CloseTime.Add(time.Second))
	if err != nil {
		t.Fatalf("ledger.NewOpen: %v", err)
	}
	t.Cleanup(l.Discard)
	for i := 0; i < count; i++ {
		raw, hash := validRelationalTestTransaction(t, uint32(i+1))
		combined, err := tx.CreateTxWithMetaBlob(raw, &tx.Metadata{
			TransactionIndex:  uint32(i),
			TransactionResult: ter.TesSUCCESS,
		})
		if err != nil {
			t.Fatalf("CreateTxWithMetaBlob(%d): %v", i, err)
		}
		if err := l.AddTransactionWithMeta(hash, combined); err != nil {
			t.Fatalf("AddTransactionWithMeta(%d): %v", i, err)
		}
	}
	if err := l.Close(g.Header.CloseTime.Add(time.Second), 0); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return l
}
