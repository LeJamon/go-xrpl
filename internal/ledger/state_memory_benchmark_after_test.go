package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

func benchmarkRevisionName() string { return "bounded-flatstate" }

func newStateMemoryBenchmarkRuntime(t *testing.T, c StateMemoryBenchmarkConfig) (*stateMemoryBenchmarkRuntime, error) {
	t.Helper()
	root := t.TempDir()
	nodePath := filepath.Join(root, "nodestore")
	flatPath := filepath.Join(root, "flatstate")
	spillPath := filepath.Join(root, "spill")
	if err := os.MkdirAll(spillPath, 0755); err != nil {
		return nil, err
	}
	node, err := backend.OpenPebble(nodePath, c.NodeBlockCacheMiB, c.NodeCacheItems)
	if err != nil {
		return nil, err
	}
	family := &benchmarkFamily{inner: node}
	pool := membudget.New(c.WorkspaceBytes + c.OverlayMemoryBytes + c.OverlayDiskBytes + 32<<20)
	workspace, err := pool.NewAccount("benchmark-workspace", 0, c.WorkspaceBytes)
	if err != nil {
		_ = family.close()
		return nil, err
	}
	overlayMemory, err := pool.NewAccount("benchmark-overlay-memory", 0, c.OverlayMemoryBytes)
	if err != nil {
		_ = family.close()
		return nil, err
	}
	overlayDisk, err := pool.NewAccount("benchmark-overlay-disk", 0, c.OverlayDiskBytes)
	if err != nil {
		_ = family.close()
		return nil, err
	}
	flat, err := flatstate.Open(flatPath, flatstate.Options{
		CacheBytes: c.FlatCacheBytes, MemTableSize: c.FlatMemTableBytes,
		MaxBatchBytes: c.FlatBatchBytes, MaxBatchRecords: c.FlatBatchRecords,
		MemoryBudget: workspace,
	})
	if err != nil {
		_ = family.close()
		return nil, err
	}
	store, err := NewExecutionStore(ExecutionConfig{
		Family: family, Workspace: workspace, Flat: flat,
		Overlay: overlay.Config{
			MemoryBudget: overlayMemory, DiskBudget: overlayDisk,
			MaxMemoryBytes: c.OverlayMemoryBytes, MaxDiskBytes: c.OverlayDiskBytes,
			SpillDir: spillPath, MaxSegments: c.OverlaySegments, MaxMergeFanIn: c.OverlayMergeFanIn,
		},
	})
	if err != nil {
		_ = flat.Close()
		_ = family.close()
		return nil, err
	}
	var flatAdvances atomic.Uint64
	return &stateMemoryBenchmarkRuntime{
		family: family, nodePath: nodePath, flatPath: flatPath,
		initialize: func(h header.LedgerHeader, stateMap, txMap *shamap.SHAMap) (*Ledger, error) {
			return NewOpenWithHeader(h, stateMap, txMap, drops.Fees{}, store)
		},
		persist: func(ctx context.Context, l *Ledger) error { return family.Sync(ctx) },
		changes: benchmarkStateChangesAfter,
		advance: func(ctx context.Context, parent, current *Ledger, changes []benchmarkChange) error {
			target := benchmarkFlatDescriptor(current)
			var expected *flatstate.Descriptor
			if parent != nil {
				descriptor := benchmarkFlatDescriptor(parent)
				expected = &descriptor
			}
			flatChanges := make([]flatstate.Change, len(changes))
			for i, change := range changes {
				flatChanges[i] = flatstate.Change{Key: change.Key, Value: change.Value, Delete: change.Delete}
			}
			authorization := flatstate.RootClosureAuthorization{TargetRoot: target.StateRoot, Token: []byte("state-memory-benchmark-v1")}
			if expected != nil {
				authorization.SourceRoot = expected.StateRoot
			}
			if err := flat.Advance(ctx, flatstate.AdvanceRequest{
				Expected: expected, Target: target, Changes: flatstate.NewSliceChangeIterator(flatChanges),
				Authorization: authorization, PublicationKind: "state-memory-benchmark",
			}); err != nil {
				return err
			}
			if err := flat.AckOutbox(ctx, flatstate.SemanticCommitID(target.LedgerHash, "state-memory-benchmark")); err != nil {
				return err
			}
			flatAdvances.Add(1)
			return nil
		},
		reopen: func(ctx context.Context, l *Ledger) (*Ledger, error) {
			return NewClosedFromRootsContext(ctx, l.Header(), drops.Fees{}, store)
		},
		authenticate: func(ctx context.Context, l *Ledger) error { return l.EstablishBoundedClosure(ctx) },
		flatStats: func() benchmarkFlatStats {
			stats := flat.Stats()
			execution := store.Stats()
			return benchmarkFlatStats{
				Active: true, Advances: flatAdvances.Load(), FlatHits: execution.FlatHits, FlatFallbacks: execution.FlatFallbacks,
				OutboxCount: stats.OutboxCount, OutboxBytes: stats.OutboxBytes, Failures: stats.Failures,
			}
		},
		memoryStats: func() benchmarkMemoryStats {
			return benchmarkMemoryStats{WorkspacePeak: workspace.Snapshot().Peak, OverlayPeak: overlayMemory.Snapshot().Peak, SpillPeak: overlayDisk.Snapshot().Peak}
		},
		resources: func() benchmarkResolvedResources {
			return benchmarkResolvedResources{
				NodeBlockCacheBytes: uint64(c.NodeBlockCacheMiB) << 20, FlatCacheBytes: uint64(c.FlatCacheBytes), FlatMemTableBytes: uint64(c.FlatMemTableBytes),
				WorkspaceBytes: c.WorkspaceBytes, OverlayMemoryBytes: c.OverlayMemoryBytes, OverlayDiskBytes: c.OverlayDiskBytes,
				WorkspacePoolBytes: c.WorkspaceBytes + c.OverlayMemoryBytes + c.OverlayDiskBytes + 32<<20,
			}
		},
		release: func(retained []*Ledger, parent *Ledger) {
			seen := make(map[*Ledger]struct{}, len(retained)+1)
			for _, ledger := range retained {
				if ledger != nil {
					seen[ledger] = struct{}{}
					ledger.Discard()
				}
			}
			if parent != nil {
				if _, ok := seen[parent]; !ok {
					parent.Discard()
				}
			}
		},
		close: func() error {
			return errors.Join(store.Close(), flat.Close(), family.close())
		},
	}, nil
}

func benchmarkFlatDescriptor(l *Ledger) flatstate.Descriptor {
	h := l.Header()
	return flatstate.Descriptor{LedgerHash: h.Hash, ParentHash: h.ParentHash, Sequence: h.LedgerIndex, StateRoot: h.AccountHash, TxRoot: h.TxHash, Schema: 1}
}

// benchmarkStateChangesAfter diffs the persisted roots with RootReader. It does
// not materialize either ledger into a retained SHAMap before flat publication.
func benchmarkStateChangesAfter(ctx context.Context, parent, current *Ledger) ([]benchmarkChange, error) {
	if parent == nil {
		return benchmarkStateChanges(ctx, current)
	}
	state := current.execution.state
	reader := shamap.NewRootReader(shamap.TypeState, parent.Header().AccountHash, state.store.config.Family,
		shamap.WithCommitmentBudget(state.store.config.Workspace))
	result := make([]benchmarkChange, 0)
	if err := reader.Diff(ctx, current.Header().AccountHash, func(change shamap.Change) error {
		result = append(result, benchmarkChange{Key: change.Key, Value: append([]byte(nil), change.Data...), Delete: change.Delete})
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}
