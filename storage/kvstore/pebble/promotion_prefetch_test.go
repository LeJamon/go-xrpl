package pebble

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	p "github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/cockroachdb/pebble/vfs/errorfs"
	"github.com/stretchr/testify/require"
)

type blockingPromotionRead struct {
	armed   atomic.Bool
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingPromotionRead) MaybeError(op errorfs.Op, path string) error {
	if b.armed.Load() && filepath.Ext(path) == ".sst" && (op == errorfs.OpFileRead || op == errorfs.OpFileReadAt) {
		b.once.Do(func() { close(b.entered); <-b.release })
	}
	return nil
}

type promotionReadGate struct {
	armed       atomic.Bool
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func newPromotionReadGate() *promotionReadGate {
	return &promotionReadGate{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *promotionReadGate) arm() { g.armed.Store(true) }

func (g *promotionReadGate) unblock() {
	g.releaseOnce.Do(func() { close(g.release) })
}

type stagedPromotionRead struct {
	gates []*promotionReadGate
}

func (s *stagedPromotionRead) MaybeError(op errorfs.Op, path string) error {
	if filepath.Ext(path) != ".sst" || (op != errorfs.OpFileRead && op != errorfs.OpFileReadAt) {
		return nil
	}
	for _, gate := range s.gates {
		if gate.armed.CompareAndSwap(true, false) {
			close(gate.entered)
			<-gate.release
			return errorfs.ErrInjected
		}
	}
	return nil
}

func TestPromotionColdWritableReadDoesNotBlockPut(t *testing.T) {
	for _, batchWrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batchWrite), func(t *testing.T) {
			testPromotionColdWritableRead(t, batchWrite)
		})
	}
}

func testPromotionColdWritableRead(t *testing.T, batchWrite bool) {
	t.Helper()
	block := &blockingPromotionRead{entered: make(chan struct{}), release: make(chan struct{})}
	store := openPromotionPrefetchStore(t, block)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(block.release) }) })
	block.armed.Store(true)
	type result struct {
		records []kvstore.Promotion
		err     error
	}
	done := make(chan result, 1)
	go func() {
		records, _, err := store.PromoteBatch([][]byte{[]byte("key")}, 1024)
		done <- result{records, err}
	}()
	select {
	case <-block.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("did not reach cold writable read")
	}
	put := make(chan error, 1)
	go func() {
		if !batchWrite {
			put <- store.Put([]byte("key"), []byte("new"))
			return
		}
		batch, err := store.NewBatch()
		if err != nil {
			put <- err
			return
		}
		defer batch.Close()
		if err := batch.Put([]byte("key"), []byte("new")); err != nil {
			put <- err
			return
		}
		put <- batch.Write()
	}()
	select {
	case err := <-put:
		require.NoError(t, err)
	case <-time.After(time.Second):
		release.Do(func() { close(block.release) })
		<-done
		t.Fatal("foreground Put blocked behind promotion disk read")
	}
	release.Do(func() { close(block.release) })
	got := <-done
	require.NoError(t, got.err)
	require.Len(t, got.records, 1)
	require.Equal(t, []byte("new"), got.records[0].Value, "must recheck writes made during prefetch")
}

func openPromotionPrefetchStore(t *testing.T, injector errorfs.Injector) *RotatingStore {
	t.Helper()
	fs := vfs.NewMem()
	require.NoError(t, fs.MkdirAll("writable", 0755))
	require.NoError(t, fs.MkdirAll("archive", 0755))
	db, err := p.Open("writable", &p.Options{FS: fs})
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte("key"), []byte("old"), p.NoSync))
	require.NoError(t, db.Flush())
	require.NoError(t, db.Close())
	cache := p.NewCache(0)
	t.Cleanup(cache.Unref)
	statsLoaded := make(chan struct{})
	var statsOnce sync.Once
	writable, err := p.Open("writable", &p.Options{
		FS: errorfs.Wrap(fs, injector), Cache: cache, DisableAutomaticCompactions: true,
		EventListener: &p.EventListener{TableStatsLoaded: func(p.TableStatsInfo) {
			statsOnce.Do(func() { close(statsLoaded) })
		}},
	})
	require.NoError(t, err)
	archive, err := p.Open("archive", &p.Options{FS: fs, Cache: cache, DisableAutomaticCompactions: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, writable.Close())
		require.NoError(t, archive.Close())
	})
	select {
	case <-statsLoaded:
	case <-time.After(5 * time.Second):
		t.Fatal("initial table statistics did not finish loading")
	}
	return &RotatingStore{writable: &Store{db: writable}, archive: &Store{db: archive}, blockCache: cache}
}

func TestPromotionSecondPrefetchReadStillAllowsPut(t *testing.T) {
	first := newPromotionReadGate()
	second := newPromotionReadGate()
	injector := &stagedPromotionRead{gates: []*promotionReadGate{first, second}}
	store := openPromotionPrefetchStore(t, injector)
	first.arm()
	type result struct {
		records []kvstore.Promotion
		err     error
	}
	done := make(chan result, 1)
	go func() {
		records, _, err := store.PromoteBatch([][]byte{[]byte("key")}, 1024)
		done <- result{records: records, err: err}
	}()
	waitPromotionSignal(t, first.entered, "first writable prefetch")
	require.NoError(t, store.Put([]byte("key"), []byte("new")))
	require.NoError(t, store.writable.db.Flush())
	second.arm()
	first.unblock()
	waitPromotionSignal(t, second.entered, "second writable prefetch")
	require.NoError(t, store.Put([]byte("key"), []byte("newer")))
	require.NoError(t, store.writable.db.Flush())
	second.unblock()
	got := waitPromotionResultValue(t, done, "promotion")
	require.NoError(t, got.err)
	require.Len(t, got.records, 1)
	require.Equal(t, []byte("newer"), got.records[0].Value)
}

func TestPromotionChangedVersionRereadDoesNotBlockSelectedStripePut(t *testing.T) {
	first := newPromotionReadGate()
	second := newPromotionReadGate()
	injector := &stagedPromotionRead{gates: []*promotionReadGate{first, second}}
	store := openPromotionPrefetchStore(t, injector)
	first.arm()
	t.Cleanup(func() {
		first.unblock()
		second.unblock()
	})
	stripe := mutationStripe([]byte("key"))
	require.Greater(t, stripe, 2)
	gate := promotionKeyOnStripe(0, "gate")
	selected := promotionKeyOnStripe(1, "selected")
	control := promotionKeyOnStripe(2, "control")
	collision1 := promotionKeyOnStripe(stripe, "first")
	collision2 := promotionKeyOnStripe(stripe, "second")
	gateHeld := false
	t.Cleanup(func() {
		if gateHeld {
			store.mutations[0].Unlock()
		}
	})
	type result struct {
		records []kvstore.Promotion
		stats   kvstore.PromotionStats
		err     error
	}
	done := make(chan result, 1)
	go func() {
		records, stats, err := store.PromoteBatch([][]byte{[]byte("key"), gate, selected}, 1024)
		done <- result{records, stats, err}
	}()
	waitPromotionSignal(t, first.entered, "initial writable prefetch")
	store.mutations[0].Lock()
	gateHeld = true
	first.unblock()
	iteratorClosed := make(chan struct{})
	go func() {
		store.writable.mu.Lock()
		store.writable.mu.Unlock()
		close(iteratorClosed)
	}()
	waitPromotionSignal(t, iteratorClosed, "completed writable prefetch")
	put1 := make(chan error, 1)
	go func() { put1 <- store.Put(collision1, []byte("first")) }()
	require.NoError(t, waitPromotionResult(t, put1, "first colliding Put"))
	second.arm()
	store.mutations[0].Unlock()
	gateHeld = false
	waitPromotionSignal(t, second.entered, "changed-version writable reread")
	for _, test := range []struct {
		key  []byte
		name string
	}{
		{control, "unselected-stripe Put"},
		{collision2, "colliding-stripe Put"},
		{selected, "other selected-stripe Put"},
	} {
		put := make(chan error, 1)
		go func() { put <- store.Put(test.key, []byte("second")) }()
		require.NoError(t, waitPromotionResult(t, put, test.name))
	}
	second.unblock()
	got := waitPromotionResultValue(t, done, "promotion")
	require.NoError(t, got.err)
	require.Len(t, got.records, 3)
	require.Equal(t, []byte("key"), got.records[0].Key)
	require.Equal(t, []byte("old"), got.records[0].Value)
	require.GreaterOrEqual(t, got.stats.VersionMismatches, 1)
	require.GreaterOrEqual(t, got.stats.Retries, 1)
}

func promotionKeyOnStripe(stripe int, label string) []byte {
	for index := 0; ; index++ {
		candidate := []byte(fmt.Sprintf("promotion/%s/%d", label, index))
		if mutationStripe(candidate) == stripe {
			return candidate
		}
	}
}

func closePromotionGate(channel chan struct{}) {
	select {
	case <-channel:
	default:
		close(channel)
	}
}

func waitPromotionSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("did not reach %s", operation)
	}
}

func waitPromotionResult(t *testing.T, result <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not complete", operation)
		return nil
	}
}

func waitPromotionResultValue[T any](t *testing.T, result <-chan T, operation string) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(5 * time.Second):
		var zero T
		t.Fatalf("%s did not complete", operation)
		return zero
	}
}

type sequencedPromotionRead struct {
	active atomic.Pointer[blockingPromotionRead]
}

func (s *sequencedPromotionRead) MaybeError(op errorfs.Op, path string) error {
	if gate := s.active.Load(); gate != nil {
		return gate.MaybeError(op, path)
	}
	return nil
}

func TestPromotionRetryExhaustionHoldsOnlyCurrentStripe(t *testing.T) {
	injector := &sequencedPromotionRead{}
	store := openPromotionPrefetchStore(t, injector)
	stages := make([]*blockingPromotionRead, promotionRetryLimit+2)
	for index := range stages {
		stages[index] = &blockingPromotionRead{entered: make(chan struct{}), release: make(chan struct{})}
		stages[index].armed.Store(true)
	}
	gateKey := promotionKeyOnStripe(0, "gate")
	stripe := mutationStripe([]byte("key"))
	require.NotZero(t, stripe)
	collision := promotionKeyOnStripe(stripe, "collision")
	gateHeld := false
	t.Cleanup(func() {
		if gateHeld {
			store.mutations[0].Unlock()
		}
		for _, stage := range stages {
			closePromotionGate(stage.release)
		}
	})
	injector.active.Store(stages[0])
	type result struct {
		records []kvstore.Promotion
		stats   kvstore.PromotionStats
		err     error
	}
	done := make(chan result, 1)
	go func() {
		records, stats, err := store.PromoteBatch([][]byte{[]byte("key"), gateKey}, 1024)
		done <- result{records, stats, err}
	}()
	for attempt := 0; attempt <= promotionRetryLimit; attempt++ {
		waitPromotionSignal(t, stages[attempt].entered, "off-lock writable read")
		store.mutations[0].Lock()
		gateHeld = true
		closePromotionGate(stages[attempt].release)
		iteratorClosed := make(chan struct{})
		go func() {
			store.writable.mu.Lock()
			store.writable.mu.Unlock()
			close(iteratorClosed)
		}()
		waitPromotionSignal(t, iteratorClosed, "completed writable prefetch")
		put := make(chan error, 1)
		go func() { put <- store.Put(collision, []byte("changed")) }()
		require.NoError(t, waitPromotionResult(t, put, "colliding mutation"))
		injector.active.Store(stages[attempt+1])
		store.mutations[0].Unlock()
		gateHeld = false
	}
	waitPromotionSignal(t, stages[len(stages)-1].entered, "single-stripe fallback")
	if store.mutations[stripe].TryLock() {
		store.mutations[stripe].Unlock()
		t.Fatal("fallback did not hold the current key's stripe")
	}
	put := make(chan error, 1)
	go func() { put <- store.Put(gateKey, []byte("foreground")) }()
	require.NoError(t, waitPromotionResult(t, put, "other selected-stripe Put"))
	closePromotionGate(stages[len(stages)-1].release)
	got := waitPromotionResultValue(t, done, "bounded promotion")
	require.NoError(t, got.err)
	require.Len(t, got.records, 2)
	require.Equal(t, []byte("old"), got.records[0].Value)
	require.Equal(t, []byte("foreground"), got.records[1].Value)
	require.Equal(t, promotionRetryLimit, got.stats.Retries)
	require.Positive(t, got.stats.Fallbacks)
}

type rejectPromotionReads struct {
	armed atomic.Bool
	reads atomic.Int64
}

func (r *rejectPromotionReads) MaybeError(op errorfs.Op, path string) error {
	if r.armed.Load() && filepath.Ext(path) == ".sst" && (op == errorfs.OpFileRead || op == errorfs.OpFileReadAt) {
		r.reads.Add(1)
		return errors.New("unexpected archive SST read")
	}
	return nil
}

func TestPromotionWritableHitsDoNotReadArchive(t *testing.T) {
	fs := vfs.NewMem()
	for _, generation := range []string{"writable", "archive"} {
		db, err := p.Open(generation, &p.Options{FS: fs})
		require.NoError(t, err)
		require.NoError(t, db.Set([]byte("key"), []byte(generation), p.NoSync))
		require.NoError(t, db.Flush())
		require.NoError(t, db.Close())
	}
	archiveReads := &rejectPromotionReads{}
	writable, err := p.Open("writable", &p.Options{FS: fs})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writable.Close()) })
	archiveLoaded := make(chan struct{})
	archive, err := p.Open("archive", &p.Options{
		FS:            errorfs.Wrap(fs, archiveReads),
		EventListener: &p.EventListener{TableStatsLoaded: func(p.TableStatsInfo) { close(archiveLoaded) }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, archive.Close()) })
	store := &RotatingStore{writable: &Store{db: writable}, archive: &Store{db: archive}}
	waitPromotionTableStats(t, archiveLoaded)
	archiveReads.armed.Store(true)

	records, stats, err := store.PromoteBatch([][]byte{[]byte("key")}, 1024)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, []byte("writable"), records[0].Value)
	require.Zero(t, archiveReads.reads.Load())
	require.Equal(t, 1, stats.ArchiveLookupsAvoided)
	require.Zero(t, stats.ArchiveLookups)
}

func waitPromotionTableStats(t *testing.T, loaded <-chan struct{}) {
	t.Helper()
	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("table statistics did not finish loading")
	}
}
