package replaytool

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/shamap"
)

// syntheticEntries returns n state entries with distinct keys and >=12-byte
// data, plus the account_hash they hash to (the in-memory state root).
func syntheticEntries(t *testing.T, n int) ([]statecompare.StateEntry, [32]byte) {
	t.Helper()
	entries := make([]statecompare.StateEntry, n)
	ref := shamap.New(shamap.TypeState)
	for i := range n {
		var key [32]byte
		key[0] = byte(i)
		key[1] = byte(i >> 8)
		key[31] = 0xA5
		data := make([]byte, 24)
		for j := range data {
			data[j] = byte(i + j)
		}
		entries[i] = statecompare.StateEntry{Index: key, Data: data}
		if err := ref.Put(key, data); err != nil {
			t.Fatalf("ref.Put: %v", err)
		}
	}
	root, err := ref.Hash()
	if err != nil {
		t.Fatalf("ref.Hash: %v", err)
	}
	return entries, root
}

// streamAll adapts a fixed slice of entries into the streaming callback
// buildOrOpenLazyState consumes.
func streamAll(entries []statecompare.StateEntry) func(func(statecompare.StateEntry) error) error {
	return func(fn func(statecompare.StateEntry) error) error {
		for _, e := range entries {
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestBuildOrOpenLazyState_ColdBuildThenLazyRead(t *testing.T) {
	ctx := context.Background()
	entries, accountHash := syntheticEntries(t, 250)

	base := shamap.NewMemoryNodeStoreFamily()
	overlay := shamap.NewMemoryNodeStoreFamily()

	loads := 0
	state, err := buildOrOpenLazyState(ctx, base, overlay, accountHash, func(fn func(statecompare.StateEntry) error) error {
		loads++
		return streamAll(entries)(fn)
	})
	if err != nil {
		t.Fatalf("cold build: %v", err)
	}
	if loads != 1 {
		t.Fatalf("expected 1 entry stream on cold build, got %d", loads)
	}

	root, err := state.Hash()
	if err != nil || root != accountHash {
		t.Fatalf("lazy state root %x != account_hash %x (err %v)", root[:8], accountHash[:8], err)
	}

	// Every entry must be readable through the lazy (overlay-over-base) map.
	for _, e := range entries {
		item, found, err := state.Get(e.Index)
		if err != nil || !found {
			t.Fatalf("Get %x: found=%v err=%v", e.Index[:4], found, err)
		}
		if string(item.Data()) != string(e.Data) {
			t.Fatalf("Get %x: data mismatch", e.Index[:4])
		}
	}
}

func TestBuildOrOpenLazyState_WarmOpenSkipsRebuild(t *testing.T) {
	ctx := context.Background()
	entries, accountHash := syntheticEntries(t, 64)

	base := shamap.NewMemoryNodeStoreFamily()
	overlay := shamap.NewMemoryNodeStoreFamily()

	if _, err := buildOrOpenLazyState(ctx, base, overlay, accountHash, streamAll(entries)); err != nil {
		t.Fatalf("cold build: %v", err)
	}

	// A second open over the now-populated base must not rebuild: streamEntries
	// failing the test if called proves the open path is "open the nodestore".
	overlay2 := shamap.NewMemoryNodeStoreFamily()
	state, err := buildOrOpenLazyState(ctx, base, overlay2, accountHash, func(func(statecompare.StateEntry) error) error {
		t.Fatalf("streamEntries called on warm open")
		return nil
	})
	if err != nil {
		t.Fatalf("warm open: %v", err)
	}
	root, err := state.Hash()
	if err != nil || root != accountHash {
		t.Fatalf("warm state root %x != account_hash %x (err %v)", root[:8], accountHash[:8], err)
	}
}

func TestBuildOrOpenLazyState_VerifyGate(t *testing.T) {
	ctx := context.Background()
	entries, accountHash := syntheticEntries(t, 32)

	base := shamap.NewMemoryNodeStoreFamily()
	overlay := shamap.NewMemoryNodeStoreFamily()

	// Claim a wrong account_hash: the built root must not match and the build
	// must fail rather than hand back an unverified seed.
	wrong := accountHash
	wrong[0] ^= 0xFF
	if _, err := buildOrOpenLazyState(ctx, base, overlay, wrong, streamAll(entries)); err == nil {
		t.Fatal("expected account_hash mismatch error, got nil")
	}
}
