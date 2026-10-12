package node

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

func (r *nodeRuntime) configureExecution(cfg *service.Config) error {
	if !r.appConfig.State.Enabled {
		return nil
	}
	if r.nodeFamily == nil || r.nodeStore == nil || r.memory == nil || r.memory.persistence == nil {
		return errors.New("bounded execution requires persistent node storage and memory admission")
	}
	state := r.appConfig.State.Resolved()
	storageBytes := r.memory.stateStorage.Capacity()
	if !r.memory.stateStorage.TryReserve(storageBytes) {
		return errors.New("flat state storage memory admission failed")
	}
	dir := r.appConfig.LocalStateDir()
	if dir == "" {
		dir = filepath.Dir(r.appConfig.NodeDB.Path)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		r.memory.stateStorage.Release(storageBytes)
		return err
	}
	flat, err := flatstate.Open(filepath.Join(dir, "execution-state"), flatstate.Options{
		CacheBytes: state.CacheMB << 20, MemTableSize: state.MemTableMB << 20,
		OutboxMaxCount: 64, OutboxMaxBytes: 1 << 20, MemoryBudget: r.memory.persistence,
	})
	if err != nil {
		r.memory.stateStorage.Release(storageBytes)
		return err
	}
	scratch := filepath.Join(dir, "execution-state", "scratch")
	// The Pebble lock excludes another owner before abandoned runs are removed.
	if err := os.RemoveAll(scratch); err != nil {
		_ = flat.Close()
		r.memory.stateStorage.Release(storageBytes)
		return err
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		_ = flat.Close()
		r.memory.stateStorage.Release(storageBytes)
		return err
	}
	execution, err := ledger.NewExecutionStore(ledger.ExecutionConfig{
		Family: r.nodeFamily, Workspace: r.memory.persistence, RootMemory: r.memory.stateRoots,
		MaxRootLeases: 4096, Flat: flat,
		Overlay: overlay.Config{MemoryBudget: r.memory.state, DiskBudget: r.memory.stateDisk,
			MaxMemoryBytes: uint64(state.MemoryMB) << 20, MaxDiskBytes: uint64(state.ScratchMB) << 20, SpillDir: scratch},
	})
	if err != nil {
		_ = flat.Close()
		_ = os.Remove(scratch)
		r.memory.stateStorage.Release(storageBytes)
		return err
	}
	r.executionCleanup = sync.OnceValue(func() error {
		if err := execution.Close(); err != nil {
			return err
		}
		err := errors.Join(flat.Close(), os.RemoveAll(scratch))
		r.memory.stateStorage.Release(storageBytes)
		return err
	})
	cfg.Execution, cfg.FlatState = execution, flat
	r.serverLog.Info("Bounded state execution enabled", "overlay_mb", state.MemoryMB,
		"scratch_mb", state.ScratchMB, "flat_path", filepath.Join(dir, "execution-state"))
	return nil
}
