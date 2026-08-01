package nodestore

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

func TestStoreBatchPersistsNodes(t *testing.T) {
	database := testDatabase(t, memorydb.New(), noCacheConfig())
	nodes := []*Node{
		testNode(NodeLedger, []byte("ledger"), 10),
		testNode(NodeAccount, []byte("account"), 11),
		testNode(NodeTransaction, []byte("transaction"), 12),
	}
	if err := database.StoreBatch(t.Context(), nodes); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}
	for _, want := range nodes {
		got, err := database.Fetch(t.Context(), want.Hash)
		if err != nil {
			t.Fatalf("Fetch(%x): %v", want.Hash[:4], err)
		}
		if got == nil || got.Type != want.Type || got.Hash != want.Hash ||
			got.LedgerSeq != want.LedgerSeq || !bytes.Equal(got.Data, want.Data) {
			t.Fatalf("Fetch(%x) = %#v, want %#v", want.Hash[:4], got, want)
		}
	}
}

func TestStoreRejectsInvalidNodes(t *testing.T) {
	valid := testNode(NodeAccount, []byte("valid"), 1)
	tests := []struct {
		name string
		node *Node
	}{
		{name: "nil"},
		{name: "unknown type", node: &Node{Type: NodeUnknown, Hash: valid.Hash, Data: valid.Data}},
		{name: "unsupported type", node: &Node{Type: 2, Hash: valid.Hash, Data: valid.Data}},
		{name: "out of range type", node: &Node{Type: 257, Hash: valid.Hash, Data: valid.Data}},
		{name: "zero hash", node: &Node{Type: NodeAccount, Data: valid.Data}},
		{name: "empty payload", node: &Node{Type: NodeAccount, Hash: valid.Hash}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, store := range []func(*KVDatabase) error{
				func(database *KVDatabase) error {
					return database.Store(context.Background(), test.node)
				},
				func(database *KVDatabase) error {
					return database.StoreBatch(context.Background(), []*Node{test.node})
				},
			} {
				backend := memorydb.New()
				database := testDatabase(t, backend, noCacheConfig())
				if err := store(database); !errors.Is(err, ErrInvalidNode) {
					t.Fatalf("error = %v, want ErrInvalidNode", err)
				}
			}
		})
	}
}

func TestStoreBatchValidationIsAtomic(t *testing.T) {
	backend := memorydb.New()
	database := testDatabase(t, backend, noCacheConfig())
	valid := testNode(NodeAccount, []byte("valid"), 1)
	invalid := &Node{Type: NodeAccount, Data: []byte("invalid")}
	if err := database.StoreBatch(t.Context(), []*Node{valid, invalid}); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("StoreBatch error = %v, want ErrInvalidNode", err)
	}
	if _, err := backend.Get(valid.Hash[:]); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("valid prefix persisted after invalid batch: %v", err)
	}
}

func TestFetchRejectsMalformedRecords(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "empty"},
		{name: "short header", data: []byte{byte(NodeAccount), 0, 0, 0}},
		{name: "unknown type", data: []byte{0, 0, 0, 0, 1, 1}},
		{name: "unsupported type", data: []byte{2, 0, 0, 0, 1, 1}},
		{name: "empty payload", data: []byte{byte(NodeAccount), 0, 0, 0, 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := memorydb.New()
			database := testDatabase(t, backend, noCacheConfig())
			hash := testHash([]byte(test.name))
			if err := backend.Put(hash[:], test.data); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Fetch(t.Context(), hash); !errors.Is(err, ErrDataCorrupt) {
				t.Fatalf("Fetch error = %v, want ErrDataCorrupt", err)
			}
			if _, err := database.FetchDataUncached(t.Context(), hash); !errors.Is(err, ErrDataCorrupt) {
				t.Fatalf("FetchDataUncached error = %v, want ErrDataCorrupt", err)
			}
		})
	}
}

type databaseErrorStore struct {
	kvstore.KeyValueStore
	getErr      error
	putErr      error
	newBatchErr error
}

func (s *databaseErrorStore) Get([]byte) ([]byte, error) {
	return nil, s.getErr
}

func (s *databaseErrorStore) Put([]byte, []byte) error {
	return s.putErr
}

func (s *databaseErrorStore) NewBatch() (kvstore.Batch, error) {
	if s.newBatchErr != nil {
		return nil, s.newBatchErr
	}
	return s.KeyValueStore.NewBatch()
}

func TestDatabasePropagatesBackendErrors(t *testing.T) {
	backend := &databaseErrorStore{KeyValueStore: memorydb.New()}
	database := testDatabase(t, backend, noCacheConfig())
	node := testNode(NodeAccount, []byte("node"), 1)

	backend.putErr = errors.New("put failed")
	if err := database.Store(t.Context(), node); !errors.Is(err, backend.putErr) {
		t.Fatalf("Store error = %v, want put error", err)
	}
	backend.putErr = nil
	backend.getErr = errors.New("get failed")
	if _, err := database.Fetch(t.Context(), node.Hash); !errors.Is(err, backend.getErr) {
		t.Fatalf("Fetch error = %v, want get error", err)
	}
	backend.getErr = nil
	backend.newBatchErr = errors.New("batch failed")
	if err := database.StoreBatch(t.Context(), []*Node{node}); !errors.Is(err, backend.newBatchErr) {
		t.Fatalf("StoreBatch error = %v, want batch error", err)
	}
}

func TestStoreBatchAlwaysClosesBatch(t *testing.T) {
	putErr := errors.New("put failed")
	writeErr := errors.New("write failed")
	closeErr := errors.New("close failed")
	tests := []struct {
		name    string
		batch   *pruneFaultBatch
		wantErr error
	}{
		{name: "put error", batch: &pruneFaultBatch{putErr: putErr}, wantErr: putErr},
		{name: "write error", batch: &pruneFaultBatch{writeErr: writeErr}, wantErr: writeErr},
		{name: "close error", batch: &pruneFaultBatch{closeErr: closeErr}, wantErr: closeErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &pruneFaultStore{
				KeyValueStore: memorydb.New(),
				batch:         test.batch,
			}
			database := testDatabase(t, store, noCacheConfig())
			err := database.StoreBatch(t.Context(), []*Node{
				testNode(NodeAccount, []byte(test.name), 1),
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("StoreBatch error = %v, want %v", err, test.wantErr)
			}
			if test.batch.closes != 1 {
				t.Fatalf("batch Close calls = %d, want 1", test.batch.closes)
			}
		})
	}
}

func TestDatabaseConfigValidation(t *testing.T) {
	if _, err := NewKVDatabase(nil, DatabaseConfig{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil store error = %v, want ErrInvalidConfig", err)
	}
	for _, config := range []DatabaseConfig{
		{PositiveCache: CacheConfig{MaxEntries: 1, TTL: testCacheTTL}},
		{PositiveCache: CacheConfig{Enabled: true, TTL: testCacheTTL}},
		{PositiveCache: CacheConfig{Enabled: true, MaxEntries: 1}},
		{NegativeCache: CacheConfig{Enabled: true, MaxEntries: -1, TTL: testCacheTTL}},
	} {
		store := memorydb.New()
		_, err := NewKVDatabase(store, config)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("config %#v error = %v, want ErrInvalidConfig", config, err)
		}
		if closeErr := store.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}
