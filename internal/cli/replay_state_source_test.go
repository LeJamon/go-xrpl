package cli

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
	ref, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatalf("shamap.New: %v", err)
	}
	for i := 0; i < n; i++ {
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

func TestBuildOrOpenLazyState_ColdBuildThenLazyRead(t *testing.T) {
	ctx := context.Background()
	entries, accountHash := syntheticEntries(t, 250)

	base, err := shamap.NewMemoryNodeStoreFamily()
	if err != nil {
		t.Fatalf("base family: %v", err)
	}
	overlay, err := shamap.NewMemoryNodeStoreFamily()
	if err != nil {
		t.Fatalf("overlay family: %v", err)
	}

	loads := 0
	state, err := buildOrOpenLazyState(ctx, base, overlay, accountHash, func() ([]statecompare.StateEntry, error) {
		loads++
		return entries, nil
	})
	if err != nil {
		t.Fatalf("cold build: %v", err)
	}
	if loads != 1 {
		t.Fatalf("expected 1 entry load on cold build, got %d", loads)
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

	base, err := shamap.NewMemoryNodeStoreFamily()
	if err != nil {
		t.Fatalf("base family: %v", err)
	}
	overlay, err := shamap.NewMemoryNodeStoreFamily()
	if err != nil {
		t.Fatalf("overlay family: %v", err)
	}

	if _, err := buildOrOpenLazyState(ctx, base, overlay, accountHash, func() ([]statecompare.StateEntry, error) {
		return entries, nil
	}); err != nil {
		t.Fatalf("cold build: %v", err)
	}

	// A second open over the now-populated base must not rebuild: loadEntries
	// failing the test if called proves the open path is "open the nodestore".
	overlay2, err := shamap.NewMemoryNodeStoreFamily()
	if err != nil {
		t.Fatalf("overlay2 family: %v", err)
	}
	state, err := buildOrOpenLazyState(ctx, base, overlay2, accountHash, func() ([]statecompare.StateEntry, error) {
		t.Fatalf("loadEntries called on warm open")
		return nil, nil
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

	base, err := shamap.NewMemoryNodeStoreFamily()
	if err != nil {
		t.Fatalf("base family: %v", err)
	}
	overlay, err := shamap.NewMemoryNodeStoreFamily()
	if err != nil {
		t.Fatalf("overlay family: %v", err)
	}

	// Claim a wrong account_hash: the built root must not match and the build
	// must fail rather than hand back an unverified seed.
	wrong := accountHash
	wrong[0] ^= 0xFF
	if _, err := buildOrOpenLazyState(ctx, base, overlay, wrong, func() ([]statecompare.StateEntry, error) {
		return entries, nil
	}); err == nil {
		t.Fatal("expected account_hash mismatch error, got nil")
	}
}
