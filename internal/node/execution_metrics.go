package node

import (
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/observability"
)

// installExecutionMetrics publishes bounded execution, flat-state and spill
// accounting snapshots for the node metrics registry. The callbacks only read
// in-memory counters at scrape time; Pebble inventory remains on transition
// paths that refresh flatstate.Stats.
func (r *nodeRuntime) installExecutionMetrics(
	execution *ledger.ExecutionStore,
	flat *flatstate.Store,
	spill *membudget.Account,
) func() {
	if execution == nil && flat == nil && spill == nil {
		return func() {}
	}
	provider := observability.StateProvider{}
	if execution != nil {
		provider.Execution = func() observability.ExecutionSnapshot {
			stats := execution.Stats()
			return observability.ExecutionSnapshot{
				RootBorrowers:  stats.RootBorrowers,
				BuildTimeNanos: uint64(stats.RootBuildTime),
				BuildNodes:     stats.RootNodes,
				BuildErrors:    stats.BuildErrors,
				Frontier:       stats.Frontier,
				FlatHits:       stats.FlatHits,
				RootFallbacks:  stats.FlatFallbacks,
			}
		}
	}
	if flat != nil {
		provider.Flat = func() observability.FlatSnapshot {
			stats := flat.Stats()
			return observability.FlatSnapshot{
				OutboxCount: stats.OutboxCount,
				OutboxBytes: stats.OutboxBytes,
				OutboxAge:   stats.OutboxAge,
				Advancing:   stats.Advancing,
				Failures:    stats.Failures,
			}
		}
	}
	if spill != nil {
		spillSnapshot := spill.Snapshot()
		provider.SpillAccount = spillSnapshot.Name
		provider.Spill = func() observability.SpillSnapshot {
			stats := spill.Snapshot()
			return observability.SpillSnapshot{
				Used:   stats.Used,
				Peak:   stats.Peak,
				Denied: stats.Denied,
			}
		}
	}
	return observability.SetStateProvider(provider)
}
