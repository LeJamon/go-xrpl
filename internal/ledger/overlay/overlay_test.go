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

	"github.com/LeJamon/go-xrpl/internal/membudget"
)

type testBase struct {
	mu     sync.RWMutex
	values map[[32]byte][]byte
	err    error
	mutate bool
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
		return nil, false, b.err
	}
	return append([]byte(nil), value...), true, b.err
}

func (b *testBase) Iterate(ctx context.Context, after *[32]byte, fn func([32]byte, []byte) bool) error {
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
	err := b.err
	b.mu.RUnlock()
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		if after != nil && string(key[:]) <= string(after[:]) {
			continue
		}
		if !fn(key, values[key]) {
			break
		}
	}
	return err
}

func testKey(n byte) [32]byte {
	var key [32]byte
	key[31] = n
	return key
}

func collectReader(t *testing.T, reader Reader) map[[32]byte][]byte {
	t.Helper()
	values := make(map[[32]byte][]byte)
	if err := reader.Iterate(context.Background(), nil, func(key [32]byte, value []byte) bool {
		values[key] = append([]byte(nil), value...)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return values
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
	snapshot, err := o.Retain()
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
		if err := o.Set(testKey(i), []byte{byte('a' + i), byte('b' + i)}); err != nil {
			t.Fatalf("Set %d: %v stats=%+v", i, err, o.Stats())
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
	if o.Stats().Segments > 2 {
		t.Fatalf("spill segment bound exceeded: %+v", o.Stats())
	}
	if o.Stats().Segments == 0 {
		t.Fatalf("test did not exercise spilling: %+v", o.Stats())
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
			t.Fatalf("set %d: %v stats=%+v", i, err, o.Stats())
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
