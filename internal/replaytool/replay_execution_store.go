package replaytool

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

const (
	replayExecutionPoolBytes      = 1 << 30
	replayExecutionWorkspaceBytes = 128 << 20
	replayExecutionOverlayBytes   = 256 << 20
	replayExecutionSpillBytes     = 512 << 20
	replayExecutionCacheBytes     = 64 << 20
	replayExecutionRootBytes      = 1 << 20
	replayExecutionMaxMemory      = 16 << 20
	replayExecutionMaxRoots       = 4096
)

// replayExecutionSession owns the bounded store and spill directory used by
// one replay invocation. A session-created family is closed after the store;
// a family supplied by a state source remains owned by that source.
type replayExecutionSession struct {
	mu          sync.Mutex
	store       *ledger.ExecutionStore
	family      shamap.Family
	closeFamily func() error
	spillDir    string
	closed      bool
}

// newReplayExecutionSession creates a bounded execution store over family. A
// nil family creates a private bounded memory family for standalone replay;
// supplied families remain owned by their state source and are not closed here.
func newReplayExecutionSession(family shamap.Family) (*replayExecutionSession, error) {
	pool := membudget.New(replayExecutionPoolBytes)
	workspace, err := pool.NewAccount(
		"replay-execution-workspace",
		replayExecutionWorkspaceBytes,
		replayExecutionWorkspaceBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("create replay execution workspace budget: %w", err)
	}
	overlayMemory, err := pool.NewAccount("replay-execution-overlay", 0, replayExecutionOverlayBytes)
	if err != nil {
		return nil, fmt.Errorf("create replay execution overlay budget: %w", err)
	}
	spillBudget, err := pool.NewAccount("replay-execution-spill", 0, replayExecutionSpillBytes)
	if err != nil {
		return nil, fmt.Errorf("create replay execution spill budget: %w", err)
	}
	cacheBudget, err := pool.NewAccount("replay-execution-cache", 0, replayExecutionCacheBytes)
	if err != nil {
		return nil, fmt.Errorf("create replay execution cache budget: %w", err)
	}
	rootBudget, err := pool.NewAccount("replay-execution-roots", replayExecutionRootBytes, replayExecutionRootBytes)
	if err != nil {
		return nil, fmt.Errorf("create replay execution root budget: %w", err)
	}
	spillDir, err := os.MkdirTemp("", "goxrpl-replay-execution-")
	if err != nil {
		return nil, fmt.Errorf("create replay execution spill directory: %w", err)
	}
	var closeFamily func() error
	if family == nil {
		owned := backend.NewMemory(backend.NodeStoreOptions{CacheMemoryBudget: cacheBudget})
		family = owned
		closeFamily = owned.Close
	}
	store, err := ledger.NewExecutionStore(ledger.ExecutionConfig{
		Family:        family,
		Workspace:     workspace,
		RootMemory:    rootBudget,
		MaxRootLeases: replayExecutionMaxRoots,
		Overlay: overlay.Config{
			MemoryBudget:   overlayMemory,
			DiskBudget:     spillBudget,
			MaxMemoryBytes: replayExecutionMaxMemory,
			MaxDiskBytes:   replayExecutionSpillBytes,
			SpillDir:       spillDir,
		},
	})
	if err != nil {
		if closeFamily != nil {
			_ = closeFamily()
		}
		_ = os.RemoveAll(spillDir)
		return nil, fmt.Errorf("create replay execution store: %w", err)
	}
	return &replayExecutionSession{store: store, family: family, closeFamily: closeFamily, spillDir: spillDir}, nil
}

// Close releases the session only after all ledger views have released their
// root ownership. If that ownership is still live, the backing family and
// spill directory remain open so callers can release the views and retry.
func (s *replayExecutionSession) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if err := s.store.Close(); err != nil {
		return err
	}
	closeErr := errors.Join(os.RemoveAll(s.spillDir))
	if s.closeFamily != nil {
		closeErr = errors.Join(closeErr, s.closeFamily())
	}
	if closeErr == nil {
		s.closed = true
	}
	return closeErr
}
