package shamap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestBuildChangesMatchesSHAMap(t *testing.T) {
	for _, mapType := range []Type{TypeState, TypeTransaction} {
		t.Run(mapType.String(), func(t *testing.T) {
			family := newMemoryFamily()
			parent := New(mapType)
			nodeType := NodeType(0)
			if mapType == TypeTransaction {
				nodeType = NodeTypeTransactionWithMeta
			}
			initial := []byte{0x01, 0x22, 0x7f, 0xc0}
			for i, b := range initial {
				key := commitmentTestKey(byte(i*37 + 3))
				var err error
				if nodeType == NodeType(0) {
					err = parent.Put(key, commitmentTestData(b))
				} else {
					err = parent.PutWithNodeType(key, commitmentTestData(b), nodeType)
				}
				if err != nil {
					t.Fatalf("parent.Put: %v", err)
				}
			}
			if err := flushToFamily(parent, family); err != nil {
				t.Fatalf("flush parent: %v", err)
			}
			parentRoot, err := parent.Hash()
			if err != nil {
				t.Fatalf("parent.Hash: %v", err)
			}

			changes := []Change{
				{Key: commitmentTestKey(3), Data: commitmentTestData(0xf1), Type: nodeType},
				{Key: commitmentTestKey(11), Data: commitmentTestData(0xf2), Type: nodeType},
				{Key: commitmentTestKey(40), Data: commitmentTestData(0xf3), Type: nodeType},
				{Key: commitmentTestKey(77), Delete: true},
			}
			expected, err := parent.SnapshotMutable()
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			for _, change := range changes {
				if change.Delete {
					if err := expected.Delete(change.Key); err != nil && !errors.Is(err, ErrItemNotFound) {
						t.Fatalf("expected.Delete: %v", err)
					}
					continue
				}
				var err error
				if nodeType == NodeType(0) {
					err = expected.Put(change.Key, change.Data)
				} else {
					err = expected.PutWithNodeType(change.Key, change.Data, nodeType)
				}
				if err != nil {
					t.Fatalf("expected.Put: %v", err)
				}
			}
			wantRoot, err := expected.Hash()
			if err != nil {
				t.Fatalf("expected.Hash: %v", err)
			}
			var emitted []FlushEntry
			idx := 0
			result, err := BuildChanges(context.Background(), mapType, parentRoot, 42, family, func() (Change, error) {
				if idx == len(changes) {
					return Change{}, io.EOF
				}
				change := changes[idx]
				idx++
				return change, nil
			}, func(entry FlushEntry) error {
				copyEntry := entry
				copyEntry.Data = bytes.Clone(entry.Data)
				emitted = append(emitted, copyEntry)
				return family.StoreBatch(context.Background(), []FlushEntry{copyEntry})
			})
			if err != nil {
				t.Fatalf("BuildChanges: %v", err)
			}
			if result.Root != wantRoot {
				t.Fatalf("root mismatch: got %x want %x (emitted=%d)", result.Root, wantRoot, len(emitted))
			}
			if result.Nodes != uint64(len(emitted)) {
				t.Fatalf("emitted count mismatch: stats=%d entries=%d", result.Nodes, len(emitted))
			}
			reader := NewRootReader(mapType, result.Root, family)
			assertReaderMatchesMap(t, reader, expected)
		})
	}
}

func TestBuildChangesRawTransactionLeaf(t *testing.T) {
	family := newMemoryFamily()
	parent := New(TypeTransaction)
	dataA := commitmentTestData(0x11)
	dataB := commitmentTestData(0x33)
	keyA := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), dataA)
	keyB := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), dataB)
	if err := parent.PutWithNodeType(keyA, dataA, NodeTypeTransactionNoMeta); err != nil {
		t.Fatalf("parent.PutWithNodeType(A): %v", err)
	}
	if err := parent.PutWithNodeType(keyB, dataB, NodeTypeTransactionNoMeta); err != nil {
		t.Fatalf("parent.PutWithNodeType(B): %v", err)
	}
	if err := flushToFamily(parent, family); err != nil {
		t.Fatalf("flush parent: %v", err)
	}
	parentRoot, err := parent.Hash()
	if err != nil {
		t.Fatalf("parent.Hash: %v", err)
	}

	dataC := commitmentTestData(0x77)
	keyC := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), dataC)
	changes := []Change{
		{Key: keyA, Delete: true, Type: NodeTypeTransactionNoMeta},
		{Key: keyC, Data: dataC, Type: NodeTypeTransactionNoMeta},
	}
	if bytes.Compare(changes[0].Key[:], changes[1].Key[:]) > 0 {
		changes[0], changes[1] = changes[1], changes[0]
	}
	expected, err := parent.SnapshotMutable()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for _, change := range changes {
		if change.Delete {
			if err := expected.Delete(change.Key); err != nil {
				t.Fatalf("expected.Delete: %v", err)
			}
		} else if err := expected.PutWithNodeType(change.Key, change.Data, change.Type); err != nil {
			t.Fatalf("expected.PutWithNodeType: %v", err)
		}
	}
	wantRoot, err := expected.Hash()
	if err != nil {
		t.Fatalf("expected.Hash: %v", err)
	}
	idx := 0
	result, err := BuildChanges(context.Background(), TypeTransaction, parentRoot, 42, family, func() (Change, error) {
		if idx == len(changes) {
			return Change{}, io.EOF
		}
		change := changes[idx]
		idx++
		return change, nil
	}, func(entry FlushEntry) error {
		return family.StoreBatch(context.Background(), []FlushEntry{entry})
	})
	if err != nil {
		t.Fatalf("BuildChanges: %v", err)
	}
	if result.Root != wantRoot {
		t.Fatalf("root mismatch: got %x want %x", result.Root, wantRoot)
	}
	reader := NewRootReader(TypeTransaction, result.Root, family)
	if item, nodeType, found, err := reader.GetLeaf(context.Background(), keyC); err != nil || !found || nodeType != NodeTypeTransactionNoMeta || !bytes.Equal(item.Data(), dataC) {
		t.Fatalf("raw leaf = item=%v type=%v found=%v err=%v", item, nodeType, found, err)
	}
}

func TestBuildChangesRejectsInvalidRawTransactionKey(t *testing.T) {
	family := newMemoryFamily()
	data := commitmentTestData(0x91)
	key := commitmentTestKey(0x91)
	called := false
	idx := 0
	_, err := BuildChanges(context.Background(), TypeTransaction, [32]byte{}, 1, family, func() (Change, error) {
		if idx != 0 {
			return Change{}, io.EOF
		}
		idx++
		return Change{Key: key, Data: data, Type: NodeTypeTransactionNoMeta}, nil
	}, func(FlushEntry) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("BuildChanges accepted a raw transaction with a mismatched key")
	}
	if called {
		t.Fatal("sink called for an invalid raw transaction key")
	}
}

func TestRootReaderDiffMatchesSHAMap(t *testing.T) {
	family := newMemoryFamily()
	oldMap := New(TypeState)
	initial := map[[32]byte]byte{
		commitmentTestKey(0x10): 0x10,
		commitmentTestKey(0x21): 0x21,
		commitmentTestKey(0xa0): 0xa0,
	}
	for key, value := range initial {
		if err := oldMap.Put(key, commitmentTestData(value)); err != nil {
			t.Fatalf("oldMap.Put: %v", err)
		}
	}
	if err := flushToFamily(oldMap, family); err != nil {
		t.Fatalf("flush old map: %v", err)
	}
	oldRoot, err := oldMap.Hash()
	if err != nil {
		t.Fatalf("oldMap.Hash: %v", err)
	}

	targetMap, err := oldMap.SnapshotMutable()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	deleteKey := commitmentTestKey(0xa0)
	insertKey := commitmentTestKey(0x11)
	replaceKey := commitmentTestKey(0x21)
	if err := targetMap.Delete(deleteKey); err != nil {
		t.Fatalf("targetMap.Delete: %v", err)
	}
	if err := targetMap.Put(insertKey, commitmentTestData(0x11)); err != nil {
		t.Fatalf("targetMap.Put(insert): %v", err)
	}
	if err := targetMap.Put(replaceKey, commitmentTestData(0x91)); err != nil {
		t.Fatalf("targetMap.Put(replace): %v", err)
	}
	if err := targetMap.Put(commitmentTestKey(0xf0), commitmentTestData(0xf0)); err != nil {
		t.Fatalf("targetMap.Put(disjoint): %v", err)
	}
	if err := flushToFamily(targetMap, family); err != nil {
		t.Fatalf("flush target map: %v", err)
	}
	targetRoot, err := targetMap.Hash()
	if err != nil {
		t.Fatalf("targetMap.Hash: %v", err)
	}

	reader := NewRootReader(TypeState, oldRoot, family)
	var got []Change
	if err := reader.Diff(context.Background(), targetRoot, func(change Change) error {
		change.Data = bytes.Clone(change.Data)
		got = append(got, change)
		return nil
	}); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	want := diffChangesForMaps(t, oldMap, targetMap)
	if len(got) != len(want) {
		t.Fatalf("Diff count=%d want=%d; got=%v want=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Key != want[i].Key || got[i].Delete != want[i].Delete || got[i].Type != want[i].Type || !bytes.Equal(got[i].Data, want[i].Data) {
			t.Fatalf("Diff[%d]=%+v want %+v", i, got[i], want[i])
		}
	}
	for i := 1; i < len(got); i++ {
		if bytes.Compare(got[i-1].Key[:], got[i].Key[:]) >= 0 {
			t.Fatalf("Diff keys are not strictly sorted at %d", i)
		}
	}

	var noChanges []Change
	if err := NewRootReader(TypeState, targetRoot, family).Diff(context.Background(), targetRoot, func(change Change) error {
		noChanges = append(noChanges, change)
		return nil
	}); err != nil {
		t.Fatalf("equal Diff: %v", err)
	}
	if len(noChanges) != 0 {
		t.Fatalf("equal Diff emitted %d changes", len(noChanges))
	}

	collapsedMap, err := targetMap.SnapshotMutable()
	if err != nil {
		t.Fatalf("collapsed snapshot: %v", err)
	}
	if err := collapsedMap.Delete(commitmentTestKey(0x10)); err != nil {
		t.Fatalf("collapsedMap.Delete: %v", err)
	}
	if err := flushToFamily(collapsedMap, family); err != nil {
		t.Fatalf("flush collapsed map: %v", err)
	}
	collapsedRoot, err := collapsedMap.Hash()
	if err != nil {
		t.Fatalf("collapsedMap.Hash: %v", err)
	}
	var collapsedChanges []Change
	if err := NewRootReader(TypeState, targetRoot, family).Diff(context.Background(), collapsedRoot, func(change Change) error {
		collapsedChanges = append(collapsedChanges, change)
		return nil
	}); err != nil {
		t.Fatalf("Diff collapse: %v", err)
	}
	wantCollapsed := diffChangesForMaps(t, targetMap, collapsedMap)
	if len(collapsedChanges) != len(wantCollapsed) || len(collapsedChanges) != 1 || collapsedChanges[0].Key != wantCollapsed[0].Key || !collapsedChanges[0].Delete {
		t.Fatalf("Diff collapse=%+v want=%+v", collapsedChanges, wantCollapsed)
	}
}

func TestRootReaderDiffEmptyAndCancellation(t *testing.T) {
	family := newMemoryFamily()
	oldMap := New(TypeState)
	if err := oldMap.Put(commitmentTestKey(1), commitmentTestData(1)); err != nil {
		t.Fatalf("oldMap.Put: %v", err)
	}
	if err := flushToFamily(oldMap, family); err != nil {
		t.Fatalf("flush old map: %v", err)
	}
	oldRoot, _ := oldMap.Hash()
	empty := New(TypeState)
	newRoot, _ := empty.Hash()
	var got []Change
	if err := NewRootReader(TypeState, oldRoot, family).Diff(context.Background(), newRoot, func(change Change) error {
		got = append(got, change)
		return nil
	}); err != nil {
		t.Fatalf("Diff to empty: %v", err)
	}
	if len(got) != 1 || !got[0].Delete || got[0].Key != commitmentTestKey(1) {
		t.Fatalf("Diff to empty = %+v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewRootReader(TypeState, oldRoot, family).Diff(ctx, newRoot, func(Change) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Diff error=%v", err)
	}
}

func TestRootReaderDiffRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(2050 ^ 0xd1ff))
	for round := 0; round < 60; round++ {
		family := newMemoryFamily()
		oldMap := New(TypeState)
		keys := make([][32]byte, 0, 24)
		for len(keys) < 24 {
			key := commitmentTestKey(byte(rng.Intn(256)))
			if _, found := findCommitmentKey(keys, key); found {
				continue
			}
			keys = append(keys, key)
			if err := oldMap.Put(key, commitmentTestData(byte(rng.Intn(256)))); err != nil {
				t.Fatalf("round %d oldMap.Put: %v", round, err)
			}
		}
		if err := flushToFamily(oldMap, family); err != nil {
			t.Fatalf("round %d flush old map: %v", round, err)
		}
		oldRoot, err := oldMap.Hash()
		if err != nil {
			t.Fatalf("round %d oldMap.Hash: %v", round, err)
		}
		targetMap, err := oldMap.SnapshotMutable()
		if err != nil {
			t.Fatalf("round %d snapshot: %v", round, err)
		}
		for i := 0; i < 16; i++ {
			key := commitmentTestKey(byte(rng.Intn(256)))
			switch rng.Intn(3) {
			case 0:
				if err := targetMap.Delete(key); err != nil && !errors.Is(err, ErrItemNotFound) {
					t.Fatalf("round %d targetMap.Delete: %v", round, err)
				}
			default:
				if err := targetMap.Put(key, commitmentTestData(byte(rng.Intn(256)))); err != nil {
					t.Fatalf("round %d targetMap.Put: %v", round, err)
				}
			}
		}
		if err := flushToFamily(targetMap, family); err != nil {
			t.Fatalf("round %d flush target map: %v", round, err)
		}
		targetRoot, err := targetMap.Hash()
		if err != nil {
			t.Fatalf("round %d targetMap.Hash: %v", round, err)
		}
		var got []Change
		if err := NewRootReader(TypeState, oldRoot, family).Diff(context.Background(), targetRoot, func(change Change) error {
			got = append(got, change)
			return nil
		}); err != nil {
			t.Fatalf("round %d Diff: %v", round, err)
		}
		want := diffChangesForMaps(t, oldMap, targetMap)
		if len(got) != len(want) {
			t.Fatalf("round %d Diff count=%d want=%d", round, len(got), len(want))
		}
		for i := range want {
			if got[i].Key != want[i].Key || got[i].Delete != want[i].Delete || !bytes.Equal(got[i].Data, want[i].Data) {
				t.Fatalf("round %d Diff[%d]=%+v want %+v", round, i, got[i], want[i])
			}
		}
	}
}

func diffChangesForMaps(t *testing.T, oldMap, targetMap *SHAMap) []Change {
	t.Helper()
	oldItems := make(map[[32]byte][]byte)
	if err := oldMap.ForEach(func(item *Item) bool {
		oldItems[item.Key()] = item.Data()
		return true
	}); err != nil {
		t.Fatalf("oldMap.ForEach: %v", err)
	}
	targetItems := make(map[[32]byte][]byte)
	if err := targetMap.ForEach(func(item *Item) bool {
		targetItems[item.Key()] = item.Data()
		return true
	}); err != nil {
		t.Fatalf("targetMap.ForEach: %v", err)
	}
	keys := make([][32]byte, 0, len(oldItems)+len(targetItems))
	seen := make(map[[32]byte]struct{}, len(oldItems)+len(targetItems))
	for key := range oldItems {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range targetItems {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	changes := make([]Change, 0, len(keys))
	for _, key := range keys {
		oldData, oldOK := oldItems[key]
		targetData, targetOK := targetItems[key]
		switch {
		case oldOK && !targetOK:
			changes = append(changes, Change{Key: key, Delete: true, Type: NodeTypeAccountState})
		case !oldOK && targetOK:
			changes = append(changes, Change{Key: key, Data: targetData, Type: NodeTypeAccountState})
		case oldOK && targetOK && !bytes.Equal(oldData, targetData):
			changes = append(changes, Change{Key: key, Data: targetData, Type: NodeTypeAccountState})
		}
	}
	return changes
}

func TestBuildChangesRandomizedDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(2050))
	for round := 0; round < 80; round++ {
		family := newMemoryFamily()
		parent := New(TypeState)
		keys := make([][32]byte, 0, 20)
		for i := 0; i < 20; i++ {
			key := commitmentTestKey(byte(rng.Intn(256)))
			if _, found := findCommitmentKey(keys, key); found {
				i--
				continue
			}
			keys = append(keys, key)
			if err := parent.Put(key, commitmentTestData(byte(rng.Intn(256)))); err != nil {
				t.Fatalf("round %d parent.Put: %v", round, err)
			}
		}
		if err := flushToFamily(parent, family); err != nil {
			t.Fatalf("round %d flush parent: %v", round, err)
		}
		parentRoot, _ := parent.Hash()

		changes := make([]Change, 0, 12)
		used := make(map[[32]byte]struct{})
		for len(changes) < 12 {
			key := commitmentTestKey(byte(rng.Intn(256)))
			if _, found := used[key]; found {
				continue
			}
			used[key] = struct{}{}
			change := Change{Key: key, Data: commitmentTestData(byte(rng.Intn(256)))}
			if rng.Intn(3) == 0 {
				change = Change{Key: key, Delete: true}
			}
			changes = append(changes, change)
		}
		sort.Slice(changes, func(i, j int) bool { return bytes.Compare(changes[i].Key[:], changes[j].Key[:]) < 0 })

		expected, err := parent.SnapshotMutable()
		if err != nil {
			t.Fatalf("round %d snapshot: %v", round, err)
		}
		for _, change := range changes {
			if change.Delete {
				if err := expected.Delete(change.Key); err != nil && !errors.Is(err, ErrItemNotFound) {
					t.Fatalf("round %d expected.Delete: %v", round, err)
				}
			} else if err := expected.Put(change.Key, change.Data); err != nil {
				t.Fatalf("round %d expected.Put: %v", round, err)
			}
		}
		wantRoot, _ := expected.Hash()
		idx := 0
		result, err := BuildChanges(context.Background(), TypeState, parentRoot, 42, family, func() (Change, error) {
			if idx == len(changes) {
				return Change{}, io.EOF
			}
			change := changes[idx]
			idx++
			return change, nil
		}, func(entry FlushEntry) error {
			return family.StoreBatch(context.Background(), []FlushEntry{entry})
		})
		if err != nil {
			t.Fatalf("round %d BuildChanges: %v", round, err)
		}
		if result.Root != wantRoot {
			t.Fatalf("round %d root mismatch: got %x want %x", round, result.Root, wantRoot)
		}
		if result.MaxFrontier > maxDepth+1 {
			t.Fatalf("round %d frontier=%d exceeds depth bound", round, result.MaxFrontier)
		}
	}
}

func BenchmarkBuildChangesDisjoint(b *testing.B) {
	family := newCountingFamily()
	parent := New(TypeState)
	for i := 0; i < 512; i++ {
		key := commitmentTestKey(byte(i))
		key[1] = byte(i >> 8)
		if err := parent.Put(key, commitmentTestData(byte(i))); err != nil {
			b.Fatalf("parent.Put(%d): %v", i, err)
		}
	}
	if err := flushToFamily(parent, family.inner.(*memoryFamily)); err != nil {
		b.Fatalf("flush parent: %v", err)
	}
	parentRoot, err := parent.Hash()
	if err != nil {
		b.Fatalf("parent.Hash: %v", err)
	}
	changeKey := commitmentTestKey(0x7f)
	changeKey[1] = 0x01
	changeData := commitmentTestData(0xe1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		family.reset()
		idx := 0
		result, err := BuildChanges(context.Background(), TypeState, parentRoot, 1, family, func() (Change, error) {
			if idx != 0 {
				return Change{}, io.EOF
			}
			idx++
			return Change{Key: changeKey, Data: changeData}, nil
		}, func(FlushEntry) error { return nil })
		if err != nil {
			b.Fatal(err)
		}
		if result.MaxFrontier > maxDepth+1 {
			b.Fatalf("frontier=%d exceeds depth bound", result.MaxFrontier)
		}
		b.ReportMetric(float64(family.count()), "fetches/op")
	}
}

func TestBuildChangesShapeTransitions(t *testing.T) {
	keyA := commitmentTestKey(0x10)
	keyB := commitmentTestKey(0x11)
	tests := []struct {
		name   string
		parent []shapeTestEntry
		change []Change
	}{
		{
			name:   "empty to single",
			change: []Change{{Key: keyA, Data: commitmentTestData(1)}},
		},
		{
			name:   "single to collision inner",
			parent: []shapeTestEntry{{key: keyA, data: commitmentTestData(1)}},
			change: []Change{{Key: keyB, Data: commitmentTestData(2)}},
		},
		{
			name:   "collision inner to single",
			parent: []shapeTestEntry{{key: keyA, data: commitmentTestData(1)}, {key: keyB, data: commitmentTestData(2)}},
			change: []Change{{Key: keyA, Delete: true}},
		},
		{
			name:   "single to empty",
			parent: []shapeTestEntry{{key: keyA, data: commitmentTestData(1)}},
			change: []Change{{Key: keyA, Delete: true}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			family := newMemoryFamily()
			parent := New(TypeState)
			for _, entry := range test.parent {
				if err := parent.Put(entry.key, entry.data); err != nil {
					t.Fatalf("parent.Put: %v", err)
				}
			}
			if err := flushToFamily(parent, family); err != nil {
				t.Fatalf("flush parent: %v", err)
			}
			parentRoot, _ := parent.Hash()
			expected, err := parent.SnapshotMutable()
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			for _, change := range test.change {
				if change.Delete {
					if err := expected.Delete(change.Key); err != nil {
						t.Fatalf("expected.Delete: %v", err)
					}
				} else if err := expected.Put(change.Key, change.Data); err != nil {
					t.Fatalf("expected.Put: %v", err)
				}
			}
			wantRoot, _ := expected.Hash()
			idx := 0
			got, err := BuildChanges(context.Background(), TypeState, parentRoot, 7, family, func() (Change, error) {
				if idx == len(test.change) {
					return Change{}, io.EOF
				}
				change := test.change[idx]
				idx++
				return change, nil
			}, func(entry FlushEntry) error {
				return family.StoreBatch(context.Background(), []FlushEntry{entry})
			})
			if err != nil {
				t.Fatalf("BuildChanges: %v", err)
			}
			if got.Root != wantRoot {
				t.Fatalf("root=%x want=%x", got.Root, wantRoot)
			}
		})
	}
}

func TestBuildChangesRootClosingDeletion(t *testing.T) {
	family := newMemoryFamily()
	parent := New(TypeState)
	keys := [][32]byte{commitmentTestKey(0x10), commitmentTestKey(0x20), commitmentTestKey(0x30)}
	for _, key := range keys {
		if err := parent.Put(key, commitmentTestData(key[0])); err != nil {
			t.Fatalf("parent.Put: %v", err)
		}
	}
	if err := flushToFamily(parent, family); err != nil {
		t.Fatalf("flush parent: %v", err)
	}
	parentRoot, err := parent.Hash()
	if err != nil {
		t.Fatalf("parent.Hash: %v", err)
	}
	expected, err := parent.SnapshotMutable()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	changes := make([]Change, 0, len(keys))
	for _, key := range keys {
		if err := expected.Delete(key); err != nil {
			t.Fatalf("expected.Delete: %v", err)
		}
		changes = append(changes, Change{Key: key, Delete: true, Type: NodeTypeAccountState})
	}
	wantRoot, err := expected.Hash()
	if err != nil {
		t.Fatalf("expected.Hash: %v", err)
	}
	idx := 0
	result, err := BuildChanges(context.Background(), TypeState, parentRoot, 1, family, func() (Change, error) {
		if idx == len(changes) {
			return Change{}, io.EOF
		}
		change := changes[idx]
		idx++
		return change, nil
	}, func(entry FlushEntry) error {
		return family.StoreBatch(context.Background(), []FlushEntry{entry})
	})
	if err != nil {
		t.Fatalf("BuildChanges: %v", err)
	}
	if result.Root != wantRoot || result.Root != ([32]byte{}) {
		t.Fatalf("root after closing deletion = %x, want empty root", result.Root)
	}
	if result.Nodes != 0 {
		t.Fatalf("closing deletion emitted %d nodes, want none", result.Nodes)
	}
	if err := NewRootReader(TypeState, result.Root, family).IterateLeaves(context.Background(), nil, func(*Item, NodeType) bool { return true }); err != nil {
		t.Fatalf("empty root reader: %v", err)
	}
}

func TestRootReaderRejectsLeafOnWrongAuthenticatedPath(t *testing.T) {
	family := newMemoryFamily()
	parent := New(TypeState)
	keyA := commitmentTestKey(0x10)
	keyB := commitmentTestKey(0x20)
	for _, key := range [][32]byte{keyA, keyB} {
		if err := parent.Put(key, commitmentTestData(key[0])); err != nil {
			t.Fatalf("parent.Put: %v", err)
		}
	}
	if err := flushToFamily(parent, family); err != nil {
		t.Fatalf("flush parent: %v", err)
	}
	parentRoot, err := parent.Hash()
	if err != nil {
		t.Fatalf("parent.Hash: %v", err)
	}
	rootData, err := family.Fetch(context.Background(), parentRoot)
	if err != nil {
		t.Fatalf("family.Fetch(root): %v", err)
	}
	decoded, err := decodeAndVerifyPrefixNode(rootData, parentRoot)
	if err != nil {
		t.Fatalf("decode root: %v", err)
	}
	rootNode, ok := decoded.(*innerNode)
	if !ok {
		t.Fatalf("decoded root type = %T", decoded)
	}
	rootNode.mu.RLock()
	hashes, branches := rootNode.hashes, rootNode.isBranch
	rootNode.mu.RUnlock()
	branchA := getBranchAtDepth(keyA, 0)
	branchB := getBranchAtDepth(keyB, 0)
	if branchA == branchB || branches&(1<<branchA) == 0 || branches&(1<<branchB) == 0 {
		t.Fatalf("test keys did not produce distinct root leaves: branches=%x", branches)
	}
	hashes[branchA] = hashes[branchB]
	malformed := &innerNode{hashes: hashes, isBranch: branches}
	if err := malformed.UpdateHash(); err != nil {
		t.Fatalf("malformed.UpdateHash: %v", err)
	}
	malformedData, err := malformed.SerializeWithPrefix()
	if err != nil {
		t.Fatalf("malformed.SerializeWithPrefix: %v", err)
	}
	malformedRoot := malformed.Hash()
	if err := family.StoreBatch(context.Background(), []FlushEntry{{Hash: malformedRoot, Data: malformedData, MapType: TypeState}}); err != nil {
		t.Fatalf("store malformed root: %v", err)
	}
	reader := NewRootReader(TypeState, malformedRoot, family)
	if _, _, _, err := reader.GetLeaf(context.Background(), keyA); err == nil {
		t.Fatal("GetLeaf accepted leaf on the wrong branch")
	}
	if err := reader.IterateLeaves(context.Background(), nil, func(*Item, NodeType) bool { return true }); err == nil {
		t.Fatal("IterateLeaves accepted leaf on the wrong branch")
	}
	if err := reader.Diff(context.Background(), parentRoot, func(Change) error { return nil }); err == nil {
		t.Fatal("Diff accepted leaf on the wrong branch")
	}
	idx := 0
	if _, err := BuildChanges(context.Background(), TypeState, malformedRoot, 1, family, func() (Change, error) {
		if idx != 0 {
			return Change{}, io.EOF
		}
		idx++
		return Change{Key: keyA, Data: commitmentTestData(0x99)}, nil
	}, func(FlushEntry) error { return nil }); err == nil {
		t.Fatal("BuildChanges accepted leaf on the wrong branch")
	}
}

type shapeTestEntry struct {
	key  [32]byte
	data []byte
}

func TestBuildChangesErrorsAndCancellation(t *testing.T) {
	family := newMemoryFamily()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := BuildChanges(ctx, TypeState, [32]byte{}, 1, family, func() (Change, error) {
		return Change{}, io.EOF
	}, func(FlushEntry) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	key := commitmentTestKey(1)
	_, err = BuildChanges(context.Background(), TypeState, [32]byte{}, 1, family, func() (Change, error) {
		return Change{Key: key, Data: commitmentTestData(1)}, nil
	}, func(FlushEntry) error { return errors.New("sink failed") })
	if err == nil || !strings.Contains(err.Error(), "sink failed") {
		t.Fatalf("sink error = %v", err)
	}
}

func TestCommitmentBudgetAdmissionAndRelease(t *testing.T) {
	family := newMemoryFamily()
	data := commitmentTestData(0x44)
	key := commitmentTestKey(0x44)
	tooSmall := &testCommitmentBudget{capacity: commitmentNodeCharge(data) - 1}
	called := false
	_, err := BuildChanges(context.Background(), TypeState, [32]byte{}, 1, family, func() (Change, error) {
		return Change{Key: key, Data: data}, nil
	}, func(FlushEntry) error {
		called = true
		return nil
	}, WithCommitmentBudget(tooSmall))
	if !errors.Is(err, ErrCommitmentBudget) {
		t.Fatalf("oversized valid change error=%v", err)
	}
	if called {
		t.Fatal("sink called after budget admission failure")
	}
	if got := tooSmall.used.Load(); got != 0 {
		t.Fatalf("budget retained %d bytes after admission failure", got)
	}

	parent := New(TypeState)
	if err := parent.Put(key, data); err != nil {
		t.Fatalf("parent.Put: %v", err)
	}
	if err := flushToFamily(parent, family); err != nil {
		t.Fatalf("flush parent: %v", err)
	}
	parentRoot, _ := parent.Hash()
	readerBudget := &testCommitmentBudget{capacity: 1 << 20}
	reader := NewRootReader(TypeState, parentRoot, family, WithCommitmentBudget(readerBudget))
	if _, _, found, err := reader.GetLeaf(context.Background(), key); err != nil || !found {
		t.Fatalf("budgeted reader.Get found=%v err=%v", found, err)
	}
	if got := readerBudget.used.Load(); got != 0 {
		t.Fatalf("reader retained %d bytes", got)
	}
}

func TestBuildChangesBudgetReleasesAncestorBeforeChild(t *testing.T) {
	family := newMemoryFamily()
	parent := New(TypeState)
	key := commitmentTestKey(0x23)
	if err := parent.Put(key, commitmentTestData(0x11)); err != nil {
		t.Fatalf("parent.Put: %v", err)
	}
	if err := flushToFamily(parent, family); err != nil {
		t.Fatalf("flush parent: %v", err)
	}
	parentRoot, err := parent.Hash()
	if err != nil {
		t.Fatalf("parent.Hash: %v", err)
	}

	// The root inner record itself fits, but the root and its changed child do
	// not fit simultaneously. The builder must release frame payload metadata
	// before descending so this valid update still has a progress path.
	budget := &testCommitmentBudget{capacity: commitmentNodeCharge(make([]byte, fullInnerSerializedSize))}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	idx := 0
	result, err := BuildChanges(ctx, TypeState, parentRoot, 1, family, func() (Change, error) {
		if idx != 0 {
			return Change{}, io.EOF
		}
		idx++
		return Change{Key: key, Data: commitmentTestData(0x99)}, nil
	}, func(entry FlushEntry) error {
		return family.StoreBatch(context.Background(), []FlushEntry{entry})
	}, WithCommitmentBudget(budget))
	if err != nil {
		t.Fatalf("BuildChanges: %v", err)
	}
	if result.Root == ([32]byte{}) {
		t.Fatal("BuildChanges returned empty root")
	}
	if got := budget.used.Load(); got != 0 {
		t.Fatalf("budget retained %d bytes", got)
	}
}

type testCommitmentBudget struct {
	capacity uint64
	used     atomic.Uint64
	wake     chan struct{}
}

func (b *testCommitmentBudget) TryReserve(bytes uint64) bool {
	for {
		used := b.used.Load()
		if bytes > b.capacity-used {
			return false
		}
		if b.used.CompareAndSwap(used, used+bytes) {
			return true
		}
	}
}

func (b *testCommitmentBudget) Release(bytes uint64) {
	b.used.Add(^bytes + 1)
	if b.wake != nil {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
}

func (b *testCommitmentBudget) Changed() <-chan struct{} {
	if b.wake == nil {
		b.wake = make(chan struct{}, 1)
	}
	return b.wake
}

func (b *testCommitmentBudget) Capacity() uint64 { return b.capacity }

func TestRootReaderOperations(t *testing.T) {
	family := newMemoryFamily()
	sm := New(TypeState)
	for i := byte(1); i <= 20; i++ {
		if err := sm.Put(commitmentTestKey(i), commitmentTestData(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := flushToFamily(sm, family); err != nil {
		t.Fatalf("flush: %v", err)
	}
	root, _ := sm.Hash()
	reader := NewRootReader(TypeState, root, family)
	for _, key := range [][32]byte{commitmentTestKey(1), commitmentTestKey(20), commitmentTestKey(99)} {
		got, _, found, err := reader.GetLeaf(context.Background(), key)
		if err != nil {
			t.Fatalf("Get(%x): %v", key[:1], err)
		}
		want, wantFound, err := sm.Get(key)
		if err != nil {
			t.Fatalf("SHAMap.Get: %v", err)
		}
		if found != wantFound || (found && !bytes.Equal(got.Data(), want.Data())) {
			t.Fatalf("Get(%x) = (%v,%v), want (%v,%v)", key[:1], got, found, want, wantFound)
		}
	}
	var gotKeys [][32]byte
	if err := reader.IterateLeaves(context.Background(), nil, func(item *Item, _ NodeType) bool {
		gotKeys = append(gotKeys, item.Key())
		return true
	}); err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if len(gotKeys) != sm.Size() {
		t.Fatalf("ForEach count = %d, want %d", len(gotKeys), sm.Size())
	}
	for i := 1; i < len(gotKeys); i++ {
		if bytes.Compare(gotKeys[i-1][:], gotKeys[i][:]) >= 0 {
			t.Fatalf("ForEach order is not strict at %d", i)
		}
	}
	for start := 0; start < len(gotKeys); start++ {
		after := gotKeys[start]
		var iterated [][32]byte
		if err := reader.IterateLeaves(context.Background(), &after, func(item *Item, _ NodeType) bool {
			iterated = append(iterated, item.Key())
			return true
		}); err != nil {
			t.Fatalf("IterateLeaves(%d): %v", start, err)
		}
		want := gotKeys[start+1:]
		if len(iterated) != len(want) {
			t.Fatalf("IterateLeaves(%d) count=%d want=%d", start, len(iterated), len(want))
		}
		for i := range want {
			if iterated[i] != want[i] {
				t.Fatalf("IterateLeaves(%d)[%d]=%x want=%x", start, i, iterated[i][:4], want[i][:4])
			}
		}
	}
	for i := byte(0); i < 21; i++ {
		key := commitmentTestKey(i)
		var got *Item
		err := reader.IterateLeaves(context.Background(), &key, func(item *Item, _ NodeType) bool {
			got = item
			return false
		})
		found := got != nil
		if err != nil {
			t.Fatalf("first IterateLeaves(%d): %v", i, err)
		}
		var want *Item
		for _, candidate := range gotKeys {
			if bytes.Compare(candidate[:], key[:]) > 0 {
				want, _, _ = sm.Get(candidate)
				break
			}
		}
		if (want != nil) != found || (found && !bytes.Equal(got.Data(), want.Data())) {
			t.Fatalf("first IterateLeaves(%d) found=%v item=%v want=%v", i, found, got, want)
		}
	}
}

func assertReaderMatchesMap(t *testing.T, reader *RootReader, want *SHAMap) {
	t.Helper()
	var got [][32]byte
	if err := reader.IterateLeaves(context.Background(), nil, func(item *Item, _ NodeType) bool {
		got = append(got, item.Key())
		return true
	}); err != nil {
		t.Fatalf("reader.IterateLeaves: %v", err)
	}
	var expected [][32]byte
	if err := want.ForEach(func(item *Item) bool {
		expected = append(expected, item.Key())
		return true
	}); err != nil {
		t.Fatalf("map.ForEach: %v", err)
	}
	if len(got) != len(expected) {
		t.Fatalf("reader count=%d expected=%d", len(got), len(expected))
	}
	for i, key := range expected {
		if got[i] != key {
			t.Fatalf("reader key %d=%x expected=%x", i, got[i][:4], key[:4])
		}
		item, _, found, err := reader.GetLeaf(context.Background(), key)
		if err != nil || !found {
			t.Fatalf("reader.GetLeaf(%x): found=%v err=%v", key[:4], found, err)
		}
		wantItem, _, err := want.Get(key)
		if err != nil || !bytes.Equal(item.Data(), wantItem.Data()) {
			t.Fatalf("reader.GetLeaf(%x) data mismatch", key[:4])
		}
	}
}

func commitmentTestKey(value byte) [32]byte {
	var key [32]byte
	key[0] = value
	key[31] = value ^ 0xa5
	return key
}

func commitmentTestData(value byte) []byte {
	data := make([]byte, 12)
	for i := range data {
		data[i] = value + byte(i)
	}
	return data
}

func findCommitmentKey(keys [][32]byte, key [32]byte) (int, bool) {
	for i, candidate := range keys {
		if candidate == key {
			return i, true
		}
	}
	return 0, false
}
