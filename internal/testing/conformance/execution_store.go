package conformance

import (
	"os"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

func newSnapshotExecutionStore() (*ledger.ExecutionStore, func(), error) {
	family := backend.NewMemory()
	spillDir, err := os.MkdirTemp("", "goxrpl-conformance-execution-")
	if err != nil {
		_ = family.Close()
		return nil, nil, err
	}

	pool := membudget.New(1 << 30)
	workspace, err := pool.NewAccount("conformance_execution_workspace", 128<<20, 128<<20)
	if err != nil {
		_ = family.Close()
		_ = os.RemoveAll(spillDir)
		return nil, nil, err
	}
	memory, err := pool.NewAccount("conformance_execution_overlay", 0, 256<<20)
	if err != nil {
		_ = family.Close()
		_ = os.RemoveAll(spillDir)
		return nil, nil, err
	}
	disk, err := pool.NewAccount("conformance_execution_spill", 0, 512<<20)
	if err != nil {
		_ = family.Close()
		_ = os.RemoveAll(spillDir)
		return nil, nil, err
	}
	store, err := ledger.NewExecutionStore(ledger.ExecutionConfig{
		Family:    family,
		Workspace: workspace,
		Overlay:   overlay.Config{MemoryBudget: memory, DiskBudget: disk, MaxMemoryBytes: 16 << 20, MaxDiskBytes: 512 << 20, SpillDir: spillDir},
	})
	if err != nil {
		_ = family.Close()
		_ = os.RemoveAll(spillDir)
		return nil, nil, err
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = store.Close()
			_ = family.Close()
			_ = os.RemoveAll(spillDir)
		})
	}
	return store, cleanup, nil
}
