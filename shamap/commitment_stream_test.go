package shamap

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"sort"
	"testing"
)

func TestCommitmentPathPrefixEqMatchesNibbleWalk(t *testing.T) {
	rng := rand.New(rand.NewSource(2050))
	for i := 0; i < 2000; i++ {
		var a, b [32]byte
		if _, err := rng.Read(a[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := rng.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		for depth := 0; depth <= maxDepth; depth++ {
			if got, want := commitmentPathPrefixEq(a, b, depth), pathPrefixEq(a, b, depth); got != want {
				t.Fatalf("prefix mismatch at iteration %d depth %d: got %v want %v", i, depth, got, want)
			}
		}
	}
}

func TestBuildChangesEmptyParentRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(2050))
	for round := 0; round < 80; round++ {
		family := newMemoryFamily()
		expected := New(TypeState)
		changes := make([]Change, 0, 48)
		used := make(map[[32]byte]struct{}, cap(changes))
		for len(changes) < cap(changes) {
			var key [32]byte
			if _, err := rng.Read(key[:]); err != nil {
				t.Fatal(err)
			}
			key[0] |= 1
			if _, found := used[key]; found {
				continue
			}
			used[key] = struct{}{}
			change := Change{Key: key, Data: commitmentTestData(byte(rng.Intn(256)))}
			if rng.Intn(3) == 0 {
				change = Change{Key: key, Delete: true}
			} else if err := expected.Put(key, change.Data); err != nil {
				t.Fatalf("round %d expected.Put: %v", round, err)
			}
			changes = append(changes, change)
		}
		sort.Slice(changes, func(i, j int) bool { return bytes.Compare(changes[i].Key[:], changes[j].Key[:]) < 0 })
		wantRoot, err := expected.Hash()
		if err != nil {
			t.Fatalf("round %d expected.Hash: %v", round, err)
		}

		idx := 0
		result, err := BuildChanges(context.Background(), TypeState, [32]byte{}, 42, family, func() (Change, error) {
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

func BenchmarkBuildChangesEmptyParent(b *testing.B) {
	changes := make([]Change, 288)
	for i := range changes {
		var key [32]byte
		key[0] = byte(i >> 8)
		key[1] = byte(i)
		key[31] = byte(i) ^ 0xa5
		changes[i] = Change{Key: key, Data: commitmentTestData(byte(i))}
	}
	family := newMemoryFamily()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := 0
		result, err := BuildChanges(context.Background(), TypeState, [32]byte{}, 1, family, func() (Change, error) {
			if idx == len(changes) {
				return Change{}, io.EOF
			}
			change := changes[idx]
			idx++
			return change, nil
		}, func(FlushEntry) error { return nil })
		if err != nil {
			b.Fatal(err)
		}
		if result.Root == ([32]byte{}) {
			b.Fatal("empty result root")
		}
	}
}
