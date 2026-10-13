package replaytool

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	ledgertestutil "github.com/LeJamon/go-xrpl/internal/ledger/testutil"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

func newTestExecutionStore(t testing.TB) *ledger.ExecutionStore {
	t.Helper()
	return ledgertestutil.NewExecutionStore(t, nil)
}

func TestReplayExecutionUsesColdDiskStateFamily(t *testing.T) {
	ctx := context.Background()
	entries, root := syntheticEntries(t, 4)
	basePath := filepath.Join(t.TempDir(), "base")
	base, err := backend.OpenPebble(basePath, 8, 1024)
	if err != nil {
		t.Fatalf("open base: %v", err)
	}
	seed, err := shamap.NewBacked(shamap.TypeState, base)
	if err != nil {
		base.Close()
		t.Fatalf("create seed map: %v", err)
	}
	for _, entry := range entries {
		if err := seed.Put(entry.Index, entry.Data); err != nil {
			base.Close()
			t.Fatalf("seed state: %v", err)
		}
	}
	if got, err := seed.Hash(); err != nil || got != root {
		base.Close()
		t.Fatalf("seed root = %x, want %x (err %v)", got[:8], root[:8], err)
	}
	if err := seed.StoreDirty(func(entries []shamap.FlushEntry) error {
		return base.StoreBatch(ctx, entries)
	}); err != nil {
		base.Close()
		t.Fatalf("persist seed: %v", err)
	}
	if err := base.Sync(ctx); err != nil {
		base.Close()
		t.Fatalf("sync base: %v", err)
	}
	if err := base.Close(); err != nil {
		t.Fatalf("close writable base: %v", err)
	}

	base, err = backend.OpenPebbleReadOnly(basePath, 8, 1024)
	if err != nil {
		t.Fatalf("reopen base read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := base.Close(); err != nil {
			t.Errorf("close read-only base: %v", err)
		}
	})
	overlayPath := filepath.Join(t.TempDir(), "overlay")
	overlay, err := backend.OpenPebble(overlayPath, 8, 1024)
	if err != nil {
		t.Fatalf("open overlay: %v", err)
	}
	t.Cleanup(func() {
		if err := overlay.Close(); err != nil {
			t.Errorf("close overlay: %v", err)
		}
	})
	family := shamap.NewOverlayFamily(base, overlay)
	stateMap, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, root, family)
	if err != nil {
		t.Fatalf("open cold state map: %v", err)
	}
	rules, err := loadRulesFromState(stateMap)
	if err != nil {
		t.Fatalf("load replay rules: %v", err)
	}
	execution, err := newReplayExecutionSession(family)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	t.Cleanup(func() {
		if err := execution.Close(); err != nil {
			t.Errorf("close execution session: %v", err)
		}
	})

	if _, err := executeBlock(ctx, blockExecution{
		StateMap:            stateMap,
		Store:               execution.store,
		LedgerIndex:         1,
		TotalCoins:          1_000_000,
		Fees:                drops.Fees{Base: 10, Reserve: 20, Increment: 5},
		Rules:               rules,
		CloseTime:           time.Unix(1, 0),
		ParentCloseTime:     time.Unix(0, 0),
		CloseTimeResolution: 1,
	}); err != nil {
		t.Fatalf("execute block from cold disk state: %v", err)
	}
}
