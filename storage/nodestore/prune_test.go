package nodestore

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

type resurrectOnIterate struct {
	*memorydb.MemDatabase
	once sync.Once
	fn   func()
}

func (s *resurrectOnIterate) NewIterator(prefix, start []byte) (kvstore.Iterator, error) {
	iterator, err := s.MemDatabase.NewIterator(prefix, start)
	if err == nil && s.fn != nil {
		s.once.Do(s.fn)
	}
	return iterator, err
}

func storeNodeAt(t *testing.T, database *KVDatabase, seq uint32, tag byte) Hash256 {
	t.Helper()
	node := testNode(NodeAccount, []byte{tag, byte(seq), byte(seq >> 8), byte(seq >> 16), byte(seq >> 24)}, seq)
	if err := database.Store(t.Context(), node); err != nil {
		t.Fatalf("Store(seq=%d): %v", seq, err)
	}
	return node.Hash
}

func fetchPresent(t *testing.T, database *KVDatabase, hash Hash256) bool {
	t.Helper()
	node, err := database.Fetch(t.Context(), hash)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return node != nil
}

func TestDeleteBeforeRemovesOnlyOlderNodes(t *testing.T) {
	database := testDatabase(t, memorydb.New(), positiveCacheConfig(100))
	keys := make(map[uint32]Hash256)
	for seq := uint32(1); seq <= 10; seq++ {
		keys[seq] = storeNodeAt(t, database, seq, 0xaa)
	}
	deleted, err := database.DeleteBefore(t.Context(), 6, 2)
	if err != nil {
		t.Fatalf("DeleteBefore: %v", err)
	}
	if deleted != 5 {
		t.Fatalf("deleted=%d, want 5", deleted)
	}
	for seq, hash := range keys {
		if got, want := fetchPresent(t, database, hash), seq >= 6; got != want {
			t.Errorf("sequence %d present=%t, want %t", seq, got, want)
		}
	}
}

func TestDeleteBeforeResurrectedNodeSurvives(t *testing.T) {
	store := &resurrectOnIterate{MemDatabase: memorydb.New()}
	database := testDatabase(t, store, noCacheConfig())
	node := testNode(NodeAccount, []byte("resurrected"), 3)
	if err := database.Store(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	store.fn = func() {
		live := node.Clone()
		live.LedgerSeq = 50
		if err := database.Store(context.Background(), live); err != nil {
			t.Errorf("resurrect Store: %v", err)
		}
	}
	deleted, err := database.DeleteBefore(t.Context(), 40, 10)
	if err != nil {
		t.Fatalf("DeleteBefore: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted=%d, want 0", deleted)
	}
	got, err := database.Fetch(t.Context(), node.Hash)
	if err != nil || got == nil || got.LedgerSeq != 50 {
		t.Fatalf("resurrected node=%#v, err=%v", got, err)
	}
}

type pruneFaultStore struct {
	kvstore.KeyValueStore
	iterator       kvstore.Iterator
	newIteratorErr error
	getErr         error
	batch          kvstore.Batch
	newBatchErr    error
}

func (s *pruneFaultStore) Get(key []byte) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.KeyValueStore.Get(key)
}

func (s *pruneFaultStore) NewIterator(prefix, start []byte) (kvstore.Iterator, error) {
	if s.newIteratorErr != nil {
		return nil, s.newIteratorErr
	}
	if s.iterator != nil {
		return s.iterator, nil
	}
	return s.KeyValueStore.NewIterator(prefix, start)
}

func (s *pruneFaultStore) NewBatch() (kvstore.Batch, error) {
	if s.newBatchErr != nil {
		return nil, s.newBatchErr
	}
	if s.batch != nil {
		return s.batch, nil
	}
	return s.KeyValueStore.NewBatch()
}

type pruneFaultBatch struct {
	putErr    error
	deleteErr error
	writeErr  error
	closeErr  error
	deletes   int
	writes    int
	closes    int
}

func (b *pruneFaultBatch) Put([]byte, []byte) error { return b.putErr }
func (b *pruneFaultBatch) Delete([]byte) error {
	b.deletes++
	return b.deleteErr
}
func (b *pruneFaultBatch) ValueSize() int { return 0 }
func (b *pruneFaultBatch) Write() error {
	b.writes++
	return b.writeErr
}
func (b *pruneFaultBatch) Reset() {}
func (b *pruneFaultBatch) Close() error {
	b.closes++
	return b.closeErr
}

type iteratorPair struct {
	key   []byte
	value []byte
}

type pruneFaultIterator struct {
	pairs    []iteratorPair
	position int
	next     int
	onNext   func(int)
	err      error
	closeErr error
}

func (i *pruneFaultIterator) Next() bool {
	i.next++
	if i.onNext != nil {
		i.onNext(i.next)
	}
	if i.position >= len(i.pairs) {
		return false
	}
	i.position++
	return true
}
func (i *pruneFaultIterator) Key() []byte   { return i.pairs[i.position-1].key }
func (i *pruneFaultIterator) Value() []byte { return i.pairs[i.position-1].value }
func (i *pruneFaultIterator) Error() error  { return i.err }
func (i *pruneFaultIterator) Close() error  { return i.closeErr }

func encodedPair(node *Node) iteratorPair {
	encoded := encodeNodeData(node)
	value := append([]byte(nil), encoded...)
	releaseEncodeBuf(encoded)
	return iteratorPair{key: append([]byte(nil), node.Hash[:]...), value: value}
}

func TestDeleteBeforeFaults(t *testing.T) {
	readErr := errors.New("read failed")
	deleteErr := errors.New("delete failed")
	writeErr := errors.New("write failed")
	batchErr := errors.New("batch failed")
	iteratorErr := errors.New("iterator failed")
	tests := []struct {
		name       string
		configure  func(*pruneFaultStore, *pruneFaultBatch)
		wantErr    error
		wantDelete int
		wantWrite  int
	}{
		{
			name: "iterator construction",
			configure: func(store *pruneFaultStore, _ *pruneFaultBatch) {
				store.newIteratorErr = iteratorErr
			},
			wantErr: iteratorErr,
		},
		{
			name: "current value disappeared",
			configure: func(store *pruneFaultStore, _ *pruneFaultBatch) {
				store.getErr = kvstore.ErrNotFound
			},
		},
		{
			name: "current read",
			configure: func(store *pruneFaultStore, _ *pruneFaultBatch) {
				store.getErr = readErr
			},
			wantErr: readErr,
		},
		{
			name: "batch construction",
			configure: func(store *pruneFaultStore, _ *pruneFaultBatch) {
				store.newBatchErr = batchErr
			},
			wantErr: batchErr,
		},
		{
			name: "batch delete",
			configure: func(_ *pruneFaultStore, batch *pruneFaultBatch) {
				batch.deleteErr = deleteErr
			},
			wantErr:    deleteErr,
			wantDelete: 1,
		},
		{
			name: "batch write",
			configure: func(_ *pruneFaultStore, batch *pruneFaultBatch) {
				batch.writeErr = writeErr
			},
			wantErr:    writeErr,
			wantDelete: 1,
			wantWrite:  1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := memorydb.New()
			store := &pruneFaultStore{KeyValueStore: base}
			database := testDatabase(t, store, noCacheConfig())
			node := testNode(NodeAccount, []byte(test.name), 1)
			if err := database.Store(t.Context(), node); err != nil {
				t.Fatal(err)
			}
			batch := &pruneFaultBatch{}
			store.batch = batch
			test.configure(store, batch)

			deleted, err := database.DeleteBefore(t.Context(), 10, 10)
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("DeleteBefore: %v", err)
				}
			} else if !errors.Is(err, test.wantErr) {
				t.Fatalf("DeleteBefore error=%v, want %v", err, test.wantErr)
			}
			if deleted != 0 {
				t.Fatalf("deleted=%d, want 0", deleted)
			}
			if batch.deletes != test.wantDelete || batch.writes != test.wantWrite {
				t.Fatalf("batch deletes=%d writes=%d, want %d/%d",
					batch.deletes, batch.writes, test.wantDelete, test.wantWrite)
			}
		})
	}
}

func TestDeleteBeforeJoinsScanAndFlushErrors(t *testing.T) {
	scanErr := errors.New("scan failed")
	writeErr := errors.New("write failed")
	node := testNode(NodeAccount, []byte("pending"), 1)
	base := memorydb.New()
	if err := base.Put(node.Hash[:], encodedPair(node).value); err != nil {
		t.Fatal(err)
	}
	batch := &pruneFaultBatch{writeErr: writeErr}
	store := &pruneFaultStore{
		KeyValueStore: base,
		iterator: &pruneFaultIterator{
			pairs: []iteratorPair{encodedPair(node)},
			err:   scanErr,
		},
		batch: batch,
	}
	database := testDatabase(t, store, noCacheConfig())
	deleted, err := database.DeleteBefore(t.Context(), 10, 10)
	if deleted != 0 || !errors.Is(err, scanErr) || !errors.Is(err, writeErr) {
		t.Fatalf("deleted=%d error=%v, want joined scan/write errors", deleted, err)
	}
}

func TestDeleteBeforeJoinsCancellationAndFlushErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	node := testNode(NodeAccount, []byte("pending"), 1)
	base := memorydb.New()
	encoded := encodedPair(node)
	if err := base.Put(node.Hash[:], encoded.value); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	iterator := &pruneFaultIterator{
		pairs: []iteratorPair{encoded, encoded},
		onNext: func(call int) {
			if call == 2 {
				cancel()
			}
		},
	}
	store := &pruneFaultStore{
		KeyValueStore: base,
		iterator:      iterator,
		batch:         &pruneFaultBatch{writeErr: writeErr},
	}
	database := testDatabase(t, store, noCacheConfig())
	deleted, err := database.DeleteBefore(ctx, 10, 10)
	if deleted != 0 || !errors.Is(err, context.Canceled) || !errors.Is(err, writeErr) {
		t.Fatalf("deleted=%d error=%v, want joined cancellation/write errors", deleted, err)
	}
}

func TestDeleteBeforeReturnsCleanupErrors(t *testing.T) {
	batchCloseErr := errors.New("batch close failed")
	iteratorCloseErr := errors.New("iterator close failed")
	node := testNode(NodeAccount, []byte("pending"), 1)
	base := memorydb.New()
	encoded := encodedPair(node)
	if err := base.Put(node.Hash[:], encoded.value); err != nil {
		t.Fatal(err)
	}
	store := &pruneFaultStore{
		KeyValueStore: base,
		iterator: &pruneFaultIterator{
			pairs:    []iteratorPair{encoded},
			closeErr: iteratorCloseErr,
		},
		batch: &pruneFaultBatch{closeErr: batchCloseErr},
	}
	database := testDatabase(t, store, noCacheConfig())
	deleted, err := database.DeleteBefore(t.Context(), 10, 10)
	if deleted != 1 || !errors.Is(err, batchCloseErr) || !errors.Is(err, iteratorCloseErr) {
		t.Fatalf("deleted=%d error=%v, want committed delete and joined cleanup errors", deleted, err)
	}
}

func TestDeleteBeforeRejectsCorruptCurrentRecord(t *testing.T) {
	base := memorydb.New()
	store := &pruneFaultStore{KeyValueStore: base}
	database := testDatabase(t, store, noCacheConfig())
	node := testNode(NodeAccount, []byte("corrupt"), 1)
	if err := database.Store(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	store.iterator = &pruneFaultIterator{pairs: []iteratorPair{encodedPair(node)}}
	if err := base.Put(node.Hash[:], []byte{byte(NodeAccount)}); err != nil {
		t.Fatal(err)
	}
	deleted, err := database.DeleteBefore(t.Context(), 10, 10)
	if deleted != 0 || !errors.Is(err, ErrDataCorrupt) {
		t.Fatalf("deleted=%d error=%v, want ErrDataCorrupt", deleted, err)
	}
}

func TestDeleteBeforeCanceledBeforeStart(t *testing.T) {
	database := testDatabase(t, memorydb.New(), noCacheConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := database.DeleteBefore(ctx, 10, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteBefore error=%v, want context.Canceled", err)
	}
}
