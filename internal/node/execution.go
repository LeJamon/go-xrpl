package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

func (r *nodeRuntime) configureExecution(cfg *service.Config) error {
	if cfg == nil || cfg.SHAMapFamily == nil {
		return errors.New("bounded execution requires a SHAMap family")
	}
	if r.memory == nil || r.memory.persistence == nil || r.memory.state == nil ||
		r.memory.stateRoots == nil || r.memory.stateDisk == nil {
		return errors.New("bounded execution requires memory admission")
	}
	state := r.appConfig.State.Resolved()
	persistent := r.nodeStore != nil && r.nodeFamily != nil
	var (
		flat            *flatstate.Store
		flatPath        string
		scratch         string
		storageBytes    uint64
		storageReserved bool
	)
	closeSetup := func() {
		if flat != nil {
			_ = flat.Close()
		}
		if scratch != "" {
			_ = os.RemoveAll(scratch)
		}
		if storageReserved {
			r.memory.stateStorage.Release(storageBytes)
		}
	}

	if persistent {
		if r.memory.stateStorage == nil {
			return errors.New("persistent bounded execution requires flat-state memory admission")
		}
		storageBytes = r.memory.stateStorage.Capacity()
		if !r.memory.stateStorage.TryReserve(storageBytes) {
			return errors.New("flat state storage memory admission failed")
		}
		storageReserved = true
		dir := r.appConfig.LocalStateDir()
		if dir == "" {
			dir = filepath.Dir(r.appConfig.NodeDB.Path)
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			closeSetup()
			return err
		}
		flatPath = filepath.Join(dir, "execution-state")
		var err error
		flat, err = flatstate.Open(flatPath, flatstate.Options{
			CacheBytes: state.CacheMB << 20, MemTableSize: state.MemTableMB << 20,
			OutboxMaxCount: 64, OutboxMaxBytes: 1 << 20, MemoryBudget: r.memory.persistence,
		})
		if err != nil {
			closeSetup()
			return err
		}
		scratch = filepath.Join(flatPath, "scratch")
		// The Pebble lock excludes another owner before abandoned runs are removed.
		if err := os.RemoveAll(scratch); err != nil {
			closeSetup()
			return err
		}
		if err := os.MkdirAll(scratch, 0700); err != nil {
			closeSetup()
			return err
		}
	} else {
		var err error
		scratch, err = os.MkdirTemp("", "goxrpl-execution-")
		if err != nil {
			return fmt.Errorf("create bounded execution scratch directory: %w", err)
		}
	}
	execution, err := ledger.NewExecutionStore(ledger.ExecutionConfig{
		Family: cfg.SHAMapFamily, Workspace: r.memory.persistence, RootMemory: r.memory.stateRoots,
		MaxRootLeases: 4096, Flat: flat,
		Overlay: overlay.Config{MemoryBudget: r.memory.state, DiskBudget: r.memory.stateDisk,
			MaxMemoryBytes: uint64(state.MemoryMB) << 20, MaxDiskBytes: uint64(state.ScratchMB) << 20, SpillDir: scratch},
	})
	if err != nil {
		closeSetup()
		return err
	}
	stopMetrics := r.installExecutionMetrics(execution, flat, r.memory.stateDisk)
	var cleanupMu sync.Mutex
	cleaned := false
	r.executionCleanup = func() error {
		cleanupMu.Lock()
		defer cleanupMu.Unlock()
		if cleaned {
			return nil
		}
		if err := execution.Close(); err != nil {
			return err
		}
		stopMetrics()
		var errs []error
		if flat != nil {
			errs = append(errs, flat.Close())
		}
		if scratch != "" {
			errs = append(errs, os.RemoveAll(scratch))
		}
		if storageReserved {
			r.memory.stateStorage.Release(storageBytes)
			storageReserved = false
		}
		err := errors.Join(errs...)
		cleaned = err == nil
		return err
	}
	cfg.Execution, cfg.FlatState = execution, flat
	r.serverLog.Info("Bounded execution store opened", "overlay_mb", state.MemoryMB,
		"scratch_mb", state.ScratchMB, "flat_path", flatPath)
	return nil
}
