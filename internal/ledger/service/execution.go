package service

import (
	"errors"
	"os"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

func configureServiceExecution(cfg *Config) (func() error, error) {
	if cfg.Execution != nil {
		return nil, nil
	}
	var closeFamily func() error
	configured := false
	defer func() {
		if !configured && closeFamily != nil {
			_ = closeFamily()
		}
	}()
	if cfg.SHAMapFamily == nil {
		if cfg.NodeStore != nil {
			family := backend.New(cfg.NodeStore)
			cfg.SHAMapFamily = family
			closeFamily = func() error { family.FullBelowCache().Close(); return nil }
		} else {
			family := backend.NewMemory()
			cfg.SHAMapFamily = family
			closeFamily = family.Close
		}
	}
	pool := membudget.New(512 << 20)
	if cfg.PersistenceMemory == nil {
		workspace, err := pool.NewAccount("execution_workspace", shamap.DirtyWorkspaceBytes, shamap.DirtyWorkspaceBytes)
		if err != nil {
			return nil, err
		}
		cfg.PersistenceMemory = workspace
	}
	memory, err := pool.NewAccount("execution_overlay", 8<<20, 64<<20)
	if err != nil {
		return nil, err
	}
	roots, err := pool.NewAccount("execution_roots", 1<<20, 1<<20)
	if err != nil {
		return nil, err
	}
	diskPool := membudget.New(2 << 30)
	disk, err := diskPool.NewAccount("execution_scratch", 0, 2<<30)
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "goxrpl-execution-")
	if err != nil {
		return nil, err
	}
	store, err := ledger.NewExecutionStore(ledger.ExecutionConfig{
		Family: cfg.SHAMapFamily, Workspace: cfg.PersistenceMemory, RootMemory: roots,
		Overlay: overlay.Config{MemoryBudget: memory, DiskBudget: disk, MaxMemoryBytes: 16 << 20, MaxDiskBytes: 2 << 30, SpillDir: scratch},
	})
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(scratch))
	}
	cfg.Execution = store
	configured = true
	return func() error {
		if err := store.Close(); err != nil {
			return err
		}
		err := os.RemoveAll(scratch)
		if closeFamily != nil {
			err = errors.Join(err, closeFamily())
		}
		return err
	}, nil
}
