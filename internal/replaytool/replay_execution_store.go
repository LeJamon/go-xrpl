package replaytool

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
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

// replayExecutionSession owns every resource used by one replay invocation.
// Ledger views created during the invocation share store; the session closes
// the store before its backing family and spill directory.
type replayExecutionSession struct {
	mu       sync.Mutex
	store    *ledger.ExecutionStore
	family   *backend.NodeStore
	spillDir string
	closed   bool
}

func newReplayExecutionSession() (*replayExecutionSession, error) {
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
	family := backend.NewMemory(backend.NodeStoreOptions{CacheMemoryBudget: cacheBudget})
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
		_ = family.Close()
		_ = os.RemoveAll(spillDir)
		return nil, fmt.Errorf("create replay execution store: %w", err)
	}
	return &replayExecutionSession{store: store, family: family, spillDir: spillDir}, nil
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
	closeErr := errors.Join(s.family.Close(), os.RemoveAll(s.spillDir))
	if closeErr == nil {
		s.closed = true
	}
	return closeErr
}
