package testutil

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

func NewExecutionStore(t testing.TB, family shamap.Family) *ledger.ExecutionStore {
	t.Helper()
	if family == nil {
		owned := backend.NewMemory()
		t.Cleanup(func() {
			if err := owned.Close(); err != nil {
				t.Error(err)
			}
		})
		family = owned
	}
	pool := membudget.New(1 << 30)
	workspace, err := pool.NewAccount("test_execution_workspace", 128<<20, 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := pool.NewAccount("test_execution_overlay", 0, 256<<20)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := pool.NewAccount("test_execution_spill", 0, 512<<20)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ledger.NewExecutionStore(ledger.ExecutionConfig{
		Family: family, Workspace: workspace,
		Overlay: overlay.Config{MemoryBudget: memory, DiskBudget: disk, MaxMemoryBytes: 16 << 20, MaxDiskBytes: 512 << 20, SpillDir: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close execution store: %v", err)
		}
	})
	return store
}
