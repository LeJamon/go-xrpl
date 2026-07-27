package backend

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

func TestOpenPebbleRejectsOverflowingBlockCache(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("overflowing MiB value does not fit in int")
	}
	overflowingMiB := int64(math.MaxInt64/(1<<20) + 1)
	blockCacheMB := int(overflowingMiB)
	if _, err := OpenPebble(t.TempDir(), blockCacheMB, 1); err == nil {
		t.Fatal("OpenPebble succeeded with an overflowing block cache, want error")
	}
}

type blockingNodeStore struct {
	nodestore.Database
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *blockingNodeStore) StoreBatch(ctx context.Context, nodes []*nodestore.Node) error {
	blocked := false
	s.once.Do(func() {
		blocked = true
		close(s.entered)
	})
	if blocked {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Database.StoreBatch(ctx, nodes)
}

type controlledFetchNodeStore struct {
	nodestore.Database
	calls    atomic.Int32
	entered  chan struct{}
	release  chan struct{}
	firstErr error
	data     []byte
	once     sync.Once
}

func (s *controlledFetchNodeStore) Fetch(ctx context.Context, hash nodestore.Hash256) (*nodestore.Node, error) {
	call := s.calls.Add(1)
	if call == 1 {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if s.firstErr != nil {
			return nil, s.firstErr
		}
	}
	return &nodestore.Node{Hash: hash, Data: s.data}, nil
}

type observedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func newObservedContext(ctx context.Context) (*observedContext, <-chan struct{}) {
	observed := make(chan struct{})
	return &observedContext{Context: ctx, observed: observed}, observed
}

func TestNodeStoreRejectsWritesBelowMinimumLedger(t *testing.T) {
	ctx := t.Context()
	family := NewMemory()
	hash := [32]byte{0x91}
	entry := shamap.FlushEntry{
		Hash: hash, Data: []byte("node"), LedgerSeq: 300, MapType: shamap.TypeState,
	}
	if err := family.StoreBatch(ctx, []shamap.FlushEntry{entry}); err != nil {
		t.Fatal(err)
	}

	family.SetMinimumLedgerSeq(250)
	entry.LedgerSeq = 200
	if err := family.StoreBatch(ctx, []shamap.FlushEntry{entry}); !errors.Is(err, ErrStoreBelowMinimum) {
		t.Fatalf("below-floor store error = %v, want %v", err, ErrStoreBelowMinimum)
	}
	stored, err := family.db.Fetch(ctx, nodestore.Hash256(hash))
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.LedgerSeq != 300 {
		t.Fatalf("stored sequence = %v, want 300", stored)
	}

	family.SetMinimumLedgerSeq(100)
	belowFloor := [32]byte{0x92}
	entry.Hash = belowFloor
	entry.LedgerSeq = 225
	if err := family.StoreBatch(ctx, []shamap.FlushEntry{entry}); !errors.Is(err, ErrStoreBelowMinimum) {
		t.Fatalf("monotonic floor store error = %v, want %v", err, ErrStoreBelowMinimum)
	}
	stored, err = family.db.Fetch(ctx, nodestore.Hash256(belowFloor))
	if err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		t.Fatalf("minimum ledger sequence regressed: stored %+v", stored)
	}
}

func TestNodeStoreMinimumWaitsForInFlightStore(t *testing.T) {
	ctx := t.Context()
	base := NewMemory().db
	store := &blockingNodeStore{
		Database: base,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	family := New(store)
	oldHash := [32]byte{0xA1}
	storeDone := make(chan error, 1)
	go func() {
		storeDone <- family.StoreBatch(ctx, []shamap.FlushEntry{{
			Hash: oldHash, Data: []byte("old"), LedgerSeq: 100, MapType: shamap.TypeState,
		}})
	}()
	<-store.entered

	floorDone := make(chan struct{})
	go func() {
		family.SetMinimumLedgerSeq(200)
		close(floorDone)
	}()
	select {
	case <-floorDone:
		t.Fatal("minimum advanced before the in-flight store completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(store.release)
	if err := <-storeDone; err != nil {
		t.Fatal(err)
	}
	<-floorDone
	stored, err := base.Fetch(ctx, nodestore.Hash256(oldHash))
	if err != nil || stored == nil {
		t.Fatalf("in-flight store = %v, %v", stored, err)
	}

	newHash := [32]byte{0xA2}
	if err := family.StoreBatch(ctx, []shamap.FlushEntry{{
		Hash: newHash, Data: []byte("new"), LedgerSeq: 100, MapType: shamap.TypeState,
	}}); !errors.Is(err, ErrStoreBelowMinimum) {
		t.Fatalf("post-advance store error = %v, want %v", err, ErrStoreBelowMinimum)
	}
	stored, err = base.Fetch(ctx, nodestore.Hash256(newHash))
	if err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		t.Fatalf("below-floor store after advance = %+v", stored)
	}
}

func TestNodeStoreFetchDurableCoalescesConcurrentHash(t *testing.T) {
	store := &controlledFetchNodeStore{
		Database: NewMemory().db,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		data:     []byte("shared durable node"),
	}
	family := New(store)
	hash := [32]byte{0xB1}

	const callers = 16
	type result struct {
		data []byte
		err  error
	}
	results := make(chan result, callers)
	for range callers {
		ctx, observed := newObservedContext(t.Context())
		go func() {
			data, err := family.FetchDurable(ctx, hash)
			results <- result{data: data, err: err}
		}()
		<-observed
	}
	<-store.entered
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("backend fetches while shared read is blocked = %d, want 1", got)
	}
	close(store.release)

	for range callers {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if string(got.data) != string(store.data) {
			t.Fatalf("data = %q, want %q", got.data, store.data)
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("backend fetches = %d, want 1", got)
	}
}

func TestNodeStoreFetchDurableWaiterCancellationDoesNotCancelRead(t *testing.T) {
	store := &controlledFetchNodeStore{
		Database: NewMemory().db,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		data:     []byte("durable after cancellation"),
	}
	family := New(store)
	hash := [32]byte{0xB2}

	ctx, cancel := context.WithCancel(t.Context())
	firstCtx, firstObserved := newObservedContext(ctx)
	first := make(chan error, 1)
	go func() {
		_, err := family.FetchDurable(firstCtx, hash)
		first <- err
	}()
	<-firstObserved
	<-store.entered

	type result struct {
		data []byte
		err  error
	}
	secondCtx, secondObserved := newObservedContext(t.Context())
	second := make(chan result, 1)
	go func() {
		data, err := family.FetchDurable(secondCtx, hash)
		second <- result{data: data, err: err}
	}()
	<-secondObserved

	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
	select {
	case got := <-second:
		t.Fatalf("shared read finished before backend release: %+v", got)
	default:
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("backend fetches after waiter cancellation = %d, want 1", got)
	}

	close(store.release)
	got := <-second
	if got.err != nil {
		t.Fatal(got.err)
	}
	if string(got.data) != string(store.data) {
		t.Fatalf("data = %q, want %q", got.data, store.data)
	}
}

func TestNodeStoreFetchDurableErrorPropagatesAndRetries(t *testing.T) {
	fetchErr := errors.New("durable fetch failed")
	store := &controlledFetchNodeStore{
		Database: NewMemory().db,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		firstErr: fetchErr,
		data:     []byte("retry succeeded"),
	}
	family := New(store)
	hash := [32]byte{0xB3}

	const callers = 8
	results := make(chan error, callers)
	for range callers {
		ctx, observed := newObservedContext(t.Context())
		go func() {
			_, err := family.FetchDurable(ctx, hash)
			results <- err
		}()
		<-observed
	}
	<-store.entered
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("backend fetches while shared read is blocked = %d, want 1", got)
	}
	close(store.release)
	for range callers {
		if err := <-results; !errors.Is(err, fetchErr) {
			t.Fatalf("coalesced error = %v, want %v", err, fetchErr)
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("backend fetches after shared error = %d, want 1", got)
	}

	data, err := family.FetchDurable(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(store.data) {
		t.Fatalf("retry data = %q, want %q", data, store.data)
	}
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("backend fetches after retry = %d, want 2", got)
	}
}
