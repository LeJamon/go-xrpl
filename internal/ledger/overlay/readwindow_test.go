package overlay

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/membudget"
)

type readWindowRecord struct {
	value   []byte
	deleted bool
}

func readWindowKey(n int) [32]byte {
	var key [32]byte
	binary.BigEndian.PutUint64(key[24:], uint64(n))
	return key
}

func writeReadWindowSegment(t *testing.T, owner *budget, records []readWindowRecord) *spillSegment {
	t.Helper()
	rootDir := t.TempDir()
	var root *treeNode
	for i, record := range records {
		key := readWindowKey(i + 1)
		next, err := insertTreeNode(owner, root, key, record.value, record.deleted)
		if err != nil {
			releaseTreeNode(root)
			t.Fatalf("insert record %d: %v", i, err)
		}
		releaseTreeNode(root)
		root = next
	}
	segment, err := writeTreeSegment(context.Background(), owner, rootDir, root)
	releaseTreeNode(root)
	if err != nil {
		t.Fatalf("write segment: %v", err)
	}
	return segment
}

func newReadWindowOverlay(t *testing.T, owner *budget, segment *spillSegment) *Overlay {
	t.Helper()
	return &Overlay{
		base:     emptyReader{},
		cfg:      Config{MaxMemoryBytes: 4 << 20, MaxDiskBytes: 8 << 20, SpillDir: t.TempDir()},
		budget:   owner,
		segments: []*spillSegment{segment},
	}
}

func TestOverlaySpillReadWindowBoundariesAndEviction(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 4 << 20, MaxDiskBytes: 8 << 20})
	records := make([]readWindowRecord, 420)
	for i := range records {
		records[i].value = bytes.Repeat([]byte{byte(i)}, 96)
		if i%37 == 0 {
			records[i].deleted = true
			records[i].value = nil
		}
	}
	segment := writeReadWindowSegment(t, owner, records)
	o := newReadWindowOverlay(t, owner, segment)
	defer o.Close()

	workspacePool := membudget.New(1 << 20)
	workspace, err := workspacePool.NewAccount("read-window", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	it, err := o.Changes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	it.EnableReadBuffers(workspace)

	var saved []byte
	sawWindow := false
	windowStarts := make(map[int64]struct{})
	for i, want := range records {
		change, done, err := it.Next(context.Background())
		if err != nil || done {
			t.Fatalf("record %d = done %v, err %v", i, done, err)
		}
		if change.Key != readWindowKey(i+1) || change.Deleted != want.deleted || !bytes.Equal(change.Value, want.value) {
			t.Fatalf("record %d = key %x, value length %d, deleted %v", i, change.Key, len(change.Value), change.Deleted)
		}
		if i == 1 {
			saved = change.Value
		}
		if workspace.Snapshot().Used != 0 {
			sawWindow = true
			if cursor, ok := it.cursors[0].(*segmentCursor); ok && cursor.readWindow != nil {
				windowStarts[cursor.windowStart] = struct{}{}
			}
		}
		if i == 10 {
			it.ReleaseReadBuffers()
			if got := workspace.Snapshot().Used; got != 0 {
				t.Fatalf("eviction retained %d workspace bytes", got)
			}
			if !bytes.Equal(saved, records[1].value) {
				t.Fatal("eviction changed an already returned value")
			}
			it.EnableReadBuffers(nil)
			if cursor, ok := it.cursors[0].(*segmentCursor); !ok || cursor.readBudget != nil {
				t.Fatal("nil workspace did not disable read-window admission")
			}
			it.EnableReadBuffers(workspace)
		}
	}
	if !sawWindow {
		t.Fatal("sequential changes never admitted a read window")
	}
	if len(windowStarts) < 2 {
		t.Fatalf("sequential changes used %d read window, want multiple windows", len(windowStarts))
	}
	if !bytes.Equal(saved, records[1].value) {
		t.Fatal("refilling the window changed an already returned value")
	}
	if _, done, err := it.Next(context.Background()); err != nil || !done {
		t.Fatalf("final iterator result = done %v, err %v", done, err)
	}
	if got := workspace.Snapshot().Used; got != 0 {
		t.Fatalf("final iterator retained %d workspace bytes", got)
	}
}

func TestOverlaySpillReadWindowLargeValueAndTombstone(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 8 << 20})
	records := []readWindowRecord{
		{value: []byte("small")},
		{value: bytes.Repeat([]byte{0x7a}, 40<<10)},
		{deleted: true},
		{value: []byte("after-large")},
	}
	segment := writeReadWindowSegment(t, owner, records)
	o := newReadWindowOverlay(t, owner, segment)
	defer o.Close()
	workspacePool := membudget.New(1 << 20)
	workspace, err := workspacePool.NewAccount("read-window", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	it, err := o.Changes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	it.EnableReadBuffers(workspace)
	for i, want := range records {
		change, done, err := it.Next(context.Background())
		if err != nil || done {
			t.Fatalf("record %d = done %v, err %v", i, done, err)
		}
		if change.Key != readWindowKey(i+1) || change.Deleted != want.deleted || !bytes.Equal(change.Value, want.value) {
			t.Fatalf("record %d = key %x, value length %d, deleted %v", i, change.Key, len(change.Value), change.Deleted)
		}
	}
	if _, done, err := it.Next(context.Background()); err != nil || !done {
		t.Fatalf("final iterator result = done %v, err %v", done, err)
	}
	if got := workspace.Snapshot().Used; got != 0 {
		t.Fatalf("large-value iterator retained %d workspace bytes", got)
	}
}

func TestOverlaySpillReadWindowDeniedAdmissionFallsBack(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 8 << 20})
	records := []readWindowRecord{{value: []byte("first")}, {value: []byte("second")}, {value: []byte("third")}}
	segment := writeReadWindowSegment(t, owner, records)
	o := newReadWindowOverlay(t, owner, segment)
	defer o.Close()
	workspacePool := membudget.New(1)
	workspace, err := workspacePool.NewAccount("read-window", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !workspace.TryReserve(1) {
		t.Fatal("failed to occupy read-window workspace")
	}
	defer workspace.Release(1)
	it, err := o.Changes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	it.EnableReadBuffers(workspace)
	for i, want := range records {
		change, done, err := it.Next(context.Background())
		if err != nil || done || change.Key != readWindowKey(i+1) || !bytes.Equal(change.Value, want.value) {
			t.Fatalf("record %d = key %x, value %q, done %v, err %v", i, change.Key, change.Value, done, err)
		}
	}
	if _, done, err := it.Next(context.Background()); err != nil || !done {
		t.Fatalf("final iterator result = done %v, err %v", done, err)
	}
	if cursor, ok := it.cursors[0].(*segmentCursor); !ok || cursor.readWindow != nil {
		t.Fatal("denied read-window admission retained a cache")
	}
}

func TestOverlaySpillReadWindowTruncationStillErrors(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 8 << 20})
	segment := writeReadWindowSegment(t, owner, []readWindowRecord{
		{value: []byte("first")},
		{value: []byte("second")},
	})
	o := newReadWindowOverlay(t, owner, segment)
	defer o.Close()
	it, err := o.Changes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	truncatedSize := int64(segmentHeaderSize + recordHeaderSize + uint64(len("first")))
	if err := os.Truncate(segment.path, truncatedSize); err != nil {
		t.Fatal(err)
	}
	workspacePool := membudget.New(1 << 20)
	workspace, err := workspacePool.NewAccount("read-window", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	it.EnableReadBuffers(workspace)
	if _, _, err := it.Next(context.Background()); !errors.Is(err, ErrCorruptSegment) {
		t.Fatalf("truncated iterator error = %v, want ErrCorruptSegment", err)
	}
	if got := workspace.Snapshot().Used; got != 0 {
		t.Fatalf("truncated iterator retained %d workspace bytes", got)
	}
}

func TestOverlaySpillReadWindowCancellationReleases(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 8 << 20})
	records := make([]readWindowRecord, 80)
	for i := range records {
		records[i].value = bytes.Repeat([]byte{byte(i)}, 96)
	}
	segment := writeReadWindowSegment(t, owner, records)
	o := newReadWindowOverlay(t, owner, segment)
	defer o.Close()
	workspacePool := membudget.New(1 << 20)
	workspace, err := workspacePool.NewAccount("read-window", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	it, err := o.Changes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	it.EnableReadBuffers(workspace)
	for i := 0; i < 2; i++ {
		if _, done, err := it.Next(context.Background()); err != nil || done {
			t.Fatalf("iterator result %d = done %v, err %v", i, done, err)
		}
	}
	if got := workspace.Snapshot().Used; got == 0 {
		t.Fatal("first iterator result did not admit a read window")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := it.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled iterator error = %v, want context.Canceled", err)
	}
	if got := workspace.Snapshot().Used; got != 0 {
		t.Fatalf("canceled iterator retained %d workspace bytes", got)
	}
	if err := it.Close(); err != nil {
		t.Fatal(err)
	}
}
