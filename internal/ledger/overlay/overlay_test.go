package overlay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/membudget"
)

type testBase struct {
	mu     sync.RWMutex
	values map[[32]byte][]byte
}

type testCursor struct {
	keys   [][32]byte
	values map[[32]byte][]byte
	index  int
	closed bool
}

func newTestBase(values map[[32]byte][]byte) *testBase {
	copyValues := make(map[[32]byte][]byte, len(values))
	for key, value := range values {
		copyValues[key] = append([]byte(nil), value...)
	}
	return &testBase{values: copyValues}
}

func (b *testBase) Get(ctx context.Context, key [32]byte) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	value, ok := b.values[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func (b *testBase) OpenCursor(ctx context.Context, after *[32]byte) (Cursor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.RLock()
	keys := make([][32]byte, 0, len(b.values))
	for key := range b.values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return string(keys[i][:]) < string(keys[j][:]) })
	values := make(map[[32]byte][]byte, len(b.values))
	for key, value := range b.values {
		values[key] = append([]byte(nil), value...)
	}
	b.mu.RUnlock()
	cursor := &testCursor{keys: keys, values: values}
	if after != nil {
		for cursor.index < len(cursor.keys) && string(cursor.keys[cursor.index][:]) <= string(after[:]) {
			cursor.index++
		}
	}
	return cursor, nil
}

func (c *testCursor) Next(ctx context.Context) ([32]byte, []byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return [32]byte{}, nil, false, err
	}
	if c.closed || c.index >= len(c.keys) {
		return [32]byte{}, nil, false, nil
	}
	key := c.keys[c.index]
	c.index++
	return key, c.values[key], true, nil
}

func (c *testCursor) Close() error {
	c.closed = true
	return nil
}

func testKey(n byte) [32]byte {
	var key [32]byte
	key[31] = n
	return key
}

func TestOverlayReadsMergesAndOwnsValues(t *testing.T) {
	base := newTestBase(map[[32]byte][]byte{
		testKey(1): []byte("base-1"),
		testKey(3): []byte("base-3"),
		testKey(5): []byte("base-5"),
	})
	o, err := New(base, Config{MaxMemoryBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if err := o.Set(testKey(1), []byte("new-1")); err != nil {
		t.Fatal(err)
	}
	if err := o.Delete(testKey(3)); err != nil {
		t.Fatal(err)
	}
	if err := o.Set(testKey(2), []byte("new-2")); err != nil {
		t.Fatal(err)
	}
	value, ok, err := o.Get(context.Background(), testKey(1))
	if err != nil || !ok || string(value) != "new-1" {
		t.Fatalf("Get override = %q, %v, %v", value, ok, err)
	}
	value[0] = 'X'
	value, ok, err = o.Get(context.Background(), testKey(1))
	if err != nil || !ok || string(value) != "new-1" {
		t.Fatalf("Get returned an alias: %q, %v, %v", value, ok, err)
	}
	if _, ok, err := o.Get(context.Background(), testKey(3)); err != nil || ok {
		t.Fatalf("deleted base value was visible: %v, %v", ok, err)
	}

	var got []string
	if err := o.Iterate(context.Background(), nil, func(key [32]byte, value []byte) bool {
		got = append(got, fmt.Sprintf("%d=%s", key[31], value))
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1=new-1", "2=new-2", "5=base-5"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("merged iteration = %v, want %v", got, want)
	}
	value, ok, err = o.Get(context.Background(), testKey(2))
	if err != nil || !ok || string(value) != "new-2" {
		t.Fatalf("iteration changed overlay storage: %q, %v, %v", value, ok, err)
	}

	key := testKey(1)
	var succ []byte
	if err := o.Iterate(context.Background(), &key, func(_ [32]byte, value []byte) bool {
		succ = append(succ, value...)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if string(succ) != "new-2"+"base-5" {
		t.Fatalf("strict successor iteration = %q", succ)
	}
}

func TestOverlayForkSnapshotAndCloseAccounting(t *testing.T) {
	pool := membudget.New(1 << 20)
	account, err := pool.NewAccount("overlay", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	o, err := New(nil, Config{MemoryBudget: account, MaxMemoryBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Set(testKey(1), []byte("parent")); err != nil {
		t.Fatal(err)
	}
	child, err := o.Fork()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Set(testKey(1), []byte("child")); err != nil {
		t.Fatal(err)
	}
	if err := o.Set(testKey(2), []byte("parent-only")); err != nil {
		t.Fatal(err)
	}
	value, ok, err := child.Get(context.Background(), testKey(1))
	if err != nil || !ok || string(value) != "child" {
		t.Fatalf("child override = %q, %v, %v", value, ok, err)
	}
	if _, ok, err := child.Get(context.Background(), testKey(2)); err != nil || ok {
		t.Fatalf("child observed parent mutation: %v, %v", ok, err)
	}
	snapshot, err := o.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := o.Get(context.Background(), testKey(1)); !errors.Is(err, ErrClosed) || ok {
		t.Fatalf("closed overlay Get = %v, %v", ok, err)
	}
	value, ok, err = snapshot.Get(context.Background(), testKey(2))
	if err != nil || !ok || string(value) != "parent-only" {
		t.Fatalf("snapshot lost parent state: %q, %v, %v", value, ok, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if got := account.Snapshot().Used; got != 0 {
		t.Fatalf("memory credits leaked after all owners closed: %d", got)
	}
}

func TestOverlaySpillCompactsAndChangesAreCoalesced(t *testing.T) {
	dir := t.TempDir()
	pool := membudget.New(4 << 20)
	memory, err := pool.NewAccount("memory", 0, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	diskPool := membudget.New(4 << 20)
	disk, err := diskPool.NewAccount("disk", 0, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	o, err := New(newTestBase(map[[32]byte][]byte{testKey(1): []byte("base")}), Config{
		MemoryBudget: memory, DiskBudget: disk, MaxMemoryBytes: 4 << 10, MaxDiskBytes: 1 << 20,
		SpillDir: dir, MaxSegments: 2, MaxMergeFanIn: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	for i := byte(1); i < 25; i++ {
		if err := o.Set(testKey(i), []byte{'a' + i, 'b' + i}); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
	if err := o.Delete(testKey(3)); err != nil {
		t.Fatal(err)
	}
	if err := o.Set(testKey(3), []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if err := o.Delete(testKey(4)); err != nil {
		t.Fatal(err)
	}
	if len(o.segments) > 2 {
		t.Fatalf("spill segment bound exceeded: %d", len(o.segments))
	}
	if len(o.segments) == 0 {
		t.Fatal("test did not exercise spilling")
	}
	value, ok, err := o.Get(context.Background(), testKey(3))
	if err != nil || !ok || string(value) != "replacement" {
		t.Fatalf("spilled replacement = %q, %v, %v", value, ok, err)
	}
	if _, ok, err := o.Get(context.Background(), testKey(4)); err != nil || ok {
		t.Fatalf("spilled tombstone leaked: %v, %v", ok, err)
	}
	var changes []Change
	it, err := o.Changes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	for {
		change, done, nextErr := it.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if done {
			break
		}
		changes = append(changes, change)
	}
	for i := 1; i < len(changes); i++ {
		if string(changes[i-1].Key[:]) >= string(changes[i].Key[:]) {
			t.Fatalf("changes are not strictly ordered: %v then %v", changes[i-1].Key, changes[i].Key)
		}
	}
	if len(changes) != 24 || string(changes[2].Value) != "replacement" || !changes[3].Deleted {
		t.Fatalf("unexpected coalesced changes: %+v", changes)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.run"))
	if err != nil || len(files) == 0 {
		t.Fatalf("spill files were not created: %v files=%v", err, files)
	}
}

func TestOverlaySpillAdmissionAndCancellation(t *testing.T) {
	o, err := New(nil, Config{MaxMemoryBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if err := o.Set(testKey(1), make([]byte, 1<<10)); !errors.Is(err, ErrSpillDisabled) {
		t.Fatalf("oversized write error = %v, want ErrSpillDisabled", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := o.SetContext(ctx, testKey(2), []byte("value")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Set = %v", err)
	}
	if err := o.Iterate(ctx, nil, func([32]byte, []byte) bool { return true }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Iterate = %v", err)
	}
}

func TestOverlayNoSpillFilesOnClose(t *testing.T) {
	dir := t.TempDir()
	o, err := New(nil, Config{MaxMemoryBytes: 4 << 10, MaxDiskBytes: 1 << 20, SpillDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for i := byte(0); i < 20; i++ {
		if err := o.Set(testKey(i), []byte("value")); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("close left spill files: %v", files)
	}
}

func TestOverlayConcurrentForkAndSnapshotReads(t *testing.T) {
	o, err := New(nil, Config{MaxMemoryBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if err := o.Set(testKey(0), []byte("initial")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := byte(1); i < 30; i++ {
				child, forkErr := o.Fork()
				if forkErr != nil {
					t.Errorf("worker %d Fork: %v", worker, forkErr)
					return
				}
				_, _, _ = child.Get(context.Background(), testKey(0))
				if setErr := child.Set(testKey(i), []byte{byte(worker), i}); setErr != nil {
					t.Errorf("worker %d Set: %v", worker, setErr)
				}
				_ = child.Close()
			}
		}(worker)
	}
	for i := byte(30); i < 60; i++ {
		if err := o.Set(testKey(i), []byte("parent")); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if _, ok, err := o.Get(context.Background(), testKey(0)); err != nil || !ok {
		t.Fatalf("parent state lost during concurrent forks: %v, %v", ok, err)
	}
}

func TestOverlayConcurrentSpilledReads(t *testing.T) {
	pool := membudget.New(1 << 20)
	memory, err := pool.NewAccount("overlay-memory", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := pool.NewAccount("overlay-disk", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	o, err := New(nil, Config{
		MemoryBudget: memory, DiskBudget: disk,
		MaxMemoryBytes: 16 << 10, MaxDiskBytes: 1 << 20,
		SpillDir: t.TempDir(), MaxSegments: 8, MaxMergeFanIn: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := byte(0); i < 64; i++ {
		value := make([]byte, 512)
		value[0] = i
		if err := o.Set(testKey(i), value); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	snapshot, err := o.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 128; i++ {
				key := byte((worker*17 + i) % 64)
				value, ok, getErr := snapshot.Get(context.Background(), testKey(key))
				if getErr != nil || !ok || len(value) != 512 || value[0] != key {
					t.Errorf("worker %d get %d = len %d first %d ok %v err %v", worker, key, len(value), func() byte {
						if len(value) == 0 {
							return 0
						}
						return value[0]
					}(), ok, getErr)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if got := memory.Snapshot().Used; got != 0 {
		t.Fatalf("memory credits leaked after spilled reads: %d", got)
	}
	if got := disk.Snapshot().Used; got != 0 {
		t.Fatalf("disk credits leaked after spilled reads: %d", got)
	}
}

func TestOverlaySpilledReaderCapDoesNotDeadlock(t *testing.T) {
	pool := membudget.New(2 << 20)
	memory, err := pool.NewAccount("overlay-memory", 0, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := pool.NewAccount("overlay-disk", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	owner := newBudget(Config{
		MemoryBudget: memory, DiskBudget: disk,
		MaxMemoryBytes: 2 << 20, MaxDiskBytes: 1 << 20,
	})
	dir := t.TempDir()
	const segmentCount = maxSegmentReaders + 16
	segments := make([]*spillSegment, 0, segmentCount)
	releaseSegments := func() {
		for _, segment := range segments {
			releaseSegment(segment)
		}
		segments = nil
	}
	defer func() {
		releaseSegments()
	}()
	for i := 0; i < segmentCount; i++ {
		segment, writeErr := writeOneSegment(context.Background(), owner, dir, testKey(byte(i)), []byte{byte(i)}, false)
		if writeErr != nil {
			t.Fatalf("write segment %d: %v", i, writeErr)
		}
		segments = append(segments, segment)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const workers = 4
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cursors := make([]*segmentCursor, 0, len(segments))
			var firstErr error
			defer func() {
				for _, cursor := range cursors {
					if closeErr := cursor.close(); firstErr == nil && closeErr != nil {
						firstErr = closeErr
					}
				}
				errs <- firstErr
			}()
			for _, segment := range segments {
				cursor, openErr := segment.cursor(ctx, owner, nil)
				if openErr != nil {
					firstErr = openErr
					return
				}
				cursors = append(cursors, cursor)
			}
			for i, cursor := range cursors {
				key, value, deleted, ok, nextErr := cursor.next(ctx)
				if nextErr != nil {
					firstErr = nextErr
					return
				}
				if !ok || deleted || key != testKey(byte(i)) || len(value) != 1 || value[0] != byte(i) {
					firstErr = fmt.Errorf("cursor %d returned key %x, value %x, deleted=%v, ok=%v", i, key, value, deleted, ok)
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("reader cap test did not complete: %v", ctx.Err())
	}
	for worker := 0; worker < workers; worker++ {
		if err := <-errs; err != nil {
			t.Fatalf("reader worker %d: %v", worker, err)
		}
	}
	releaseSegments()
	if got := memory.Snapshot().Used; got != 0 {
		t.Fatalf("memory credits leaked after reader cap test: %d", got)
	}
	if got := disk.Snapshot().Used; got != 0 {
		t.Fatalf("disk credits leaked after reader cap test: %d", got)
	}
}

func TestOverlaySpillTruncatedSkippedValue(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
	dir := t.TempDir()
	targetKey := testKey(2)
	firstValue := make([]byte, 64)
	var root *treeNode
	var err error
	for i := byte(1); i <= 17; i++ {
		value := []byte("other")
		if i == 1 {
			value = firstValue
		} else if i == 2 {
			value = []byte("target")
		}
		root, err = insertTreeNode(owner, root, testKey(i), value, false)
		if err != nil {
			releaseTreeNode(root)
			t.Fatal(err)
		}
	}
	segment, err := writeTreeSegment(context.Background(), owner, dir, root)
	releaseTreeNode(root)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSegment(segment)
	truncatedSize := int64(segmentHeaderSize + recordHeaderSize + uint64(len(firstValue)/2))
	if err := os.Truncate(segment.path, truncatedSize); err != nil {
		t.Fatal(err)
	}
	cursor, err := segment.cursor(context.Background(), owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.close()
	_, _, err = cursor.get(context.Background(), targetKey)
	if !errors.Is(err, ErrCorruptSegment) {
		t.Fatalf("truncated skipped value error = %v, want ErrCorruptSegment", err)
	}
}
