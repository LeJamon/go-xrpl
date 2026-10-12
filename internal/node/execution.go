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
	scratch, err := os.MkdirTemp(dir, "execution-scratch-")
	if err != nil {
		r.memory.stateStorage.Release(storageBytes)
		return err
	}
	flat, err := flatstate.Open(filepath.Join(dir, "execution-state"), flatstate.Options{
		CacheBytes: state.CacheMB << 20, MemTableSize: state.MemTableMB << 20,
		OutboxMaxCount: 64, OutboxMaxBytes: 1 << 20, MemoryBudget: r.memory.persistence,
	})
	if err != nil {
		_ = os.Remove(scratch)
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
	r.executionCleanup = sync.OnceFunc(func() {
		if err := execution.Close(); err != nil {
			r.serverLog.Error("close execution root leases", "err", err)
		}
		if err := flat.Close(); err != nil {
			r.serverLog.Error("close flat state", "err", err)
		}
		if err := os.Remove(scratch); err != nil {
			r.serverLog.Error("remove execution scratch directory", "err", err)
		}
		r.memory.stateStorage.Release(storageBytes)
	})
	cfg.Execution, cfg.FlatState = execution, flat
	r.serverLog.Info("Bounded state execution enabled", "overlay_mb", state.MemoryMB,
		"scratch_mb", state.ScratchMB, "flat_path", filepath.Join(dir, "execution-state"))
	return nil
}
