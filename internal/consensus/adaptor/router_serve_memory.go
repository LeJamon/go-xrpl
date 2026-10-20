package adaptor

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/observability"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

// LedgerServeMemoryBytes caps temporary reply construction across serving workers.
const LedgerServeMemoryBytes = 256 << 20

const ledgerServeReplyBytes = 8 << 20

func newLedgerServeMemory() *membudget.Account {
	account, _ := membudget.New(LedgerServeMemoryBytes).NewAccount("peer_serve", 0, LedgerServeMemoryBytes)
	return account
}

// SetLedgerServeMemoryBudget attaches an elastic node-owned account before Run.
// Serving never waits for credits or borrows protected progress reserves.
func (r *Router) SetLedgerServeMemoryBudget(account *membudget.Account) error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.lifecycleState != routerLifecycleInitial || account == nil || r.serveMemory.Snapshot().Used != 0 {
		return errors.New("ledger serving: memory budget must be set before use")
	}
	r.serveMemory = account
	return nil
}

type ledgerServeWorkspace struct {
	account  *membudget.Account
	reserved uint64
	wire     int
	nodes    int
}

func ledgerServeWorkspaceBytes(wireBytes, nodes int) uint64 {
	// Cover prefix/decode/wire/frame copies, overlapping old/new node slices,
	// IDs and depth pointers, protobuf wrappers, and fixed walk/snapshot scratch.
	// The outbound clone has a separate queue reservation, held concurrently
	// with this one until Send returns.
	return 6*uint64(wireBytes) + 512*uint64(nodes) + message.LedgerDataEncodingWorkspaceBytes(nodes) + 1<<20
}

func (r *Router) reserveLedgerServeWorkspace(ctx context.Context, base bool) *ledgerServeWorkspace {
	if ctx.Err() != nil {
		return nil
	}
	wireBytes, nodes := r.serveReplyBytes, txSetHardMaxReplyNodes
	if base {
		wireBytes, nodes = 2048, 3
	}
	workspace := &ledgerServeWorkspace{
		wire: wireBytes, nodes: nodes,
		reserved: ledgerServeWorkspaceBytes(wireBytes, nodes),
	}
	r.lifecycleMu.RLock()
	workspace.account = r.serveMemory
	ok := workspace.account != nil && workspace.account.TryReserve(workspace.reserved)
	r.lifecycleMu.RUnlock()
	if !ok {
		observability.RecordMemoryAdmission("ledger_serve", "denied")
		return nil
	}
	return workspace
}

func (w *ledgerServeWorkspace) close() {
	if w != nil && w.reserved != 0 {
		w.account.Release(w.reserved)
		w.reserved = 0
	}
}

func (w *ledgerServeWorkspace) walkBudget() *shamap.WireBudget {
	// Leave room for the frame/envelope and each protobuf node's tags and
	// lengths. NodeDepth references never exceed the legacy 33-byte NodeID.
	nodes := min(w.nodes, max(0, (w.wire-64)/66))
	return &shamap.WireBudget{
		RemainingNodes:     nodes,
		RemainingWireBytes: max(0, w.wire-64-32*nodes),
		RemainingWork:      txSetHardMaxReplyNodes * (64 + shamap.BranchFactor + 1),
	}
}
