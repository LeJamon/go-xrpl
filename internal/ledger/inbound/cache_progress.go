package inbound

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/shamap"
)

// Consume the bounded leased frontier first. A fresh walk's first 256 missing
// nodes need not include any of the outstanding peer batches. Scanning only
// that prefix would leave already received replies stranded in the cache.
// Requests retain verified NodeIDs, so these placements need no discovery walk.
func (l *Ledger) fillFromCacheContext(ctx context.Context, m *shamap.SHAMap, transaction bool, fetch func([32]byte) ([]byte, bool)) (bool, []shamap.FlushEntry, []shamap.MissingNode, error) {
	l.mu.Lock()
	frontier := make([]shamap.MissingNode, 0, len(l.requestLeases)+len(l.readyRequests))
	for hash, lease := range l.requestLeases {
		if lease.transaction == transaction && !lease.wireShadow {
			frontier = append(frontier, shamap.MissingNode{Hash: hash, NodeID: lease.nodeID})
		}
	}
	for _, ready := range l.readyRequests {
		if ready.transaction == transaction {
			frontier = append(frontier, ready.node)
		}
	}
	l.mu.Unlock()
	var stored []shamap.FlushEntry
	for _, node := range frontier {
		if err := ctx.Err(); err != nil {
			return len(stored) > 0, stored, nil, err
		}
		data, ok := fetch(node.Hash)
		if !ok || !shamap.VerifyFetchPackNode(node.Hash, data) {
			continue
		}
		result, entry, err := m.AddKnownNodeFromPrefixWithEntryContext(ctx, node.NodeID, data)
		if errors.Is(err, shamap.ErrAcquisitionMemory) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return len(stored) > 0, stored, nil, err
		}
		if err != nil {
			continue
		}
		if result == shamap.NodeUseful {
			stored = append(stored, entry)
		} else if result == shamap.NodeDuplicate {
			l.mu.Lock()
			l.fulfillCacheNodeLocked(transaction, node.Hash)
			l.refreshRequestPeersLocked()
			l.mu.Unlock()
		}
	}
	progressed, more, missing, err := fillFromLocalContext(ctx, m, fetch)
	return len(stored) > 0 || progressed, append(stored, more...), missing, err
}

// recordCachePlacements accounts only verified NodeUseful placements, not
// arbitrary cache hits or durable-store reads. Received/useful totals include
// these accepted cache deliveries; the cache-specific subset lets callers
// distinguish them from ordinary LedgerData replies. This is not a wire-byte
// or unique-network-object counter (a fetch pack may have been cached earlier).
//
// Called under workMu immediately after placement, including before a traversal
// yield or persistence error. A resumed pass cannot count the same placement
// twice. Fulfilling leases here is essential: by-hash replies have no NodeID
// and do not pass through ApplyMissingReplyContext.
func (l *Ledger) recordCachePlacements(transaction bool, stored []shamap.FlushEntry) {
	if len(stored) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := uint64(len(stored))
	if transaction {
		l.txRecv += n
		l.txUseful += n
		l.txCacheUseful += n
	} else {
		l.stateRecv += n
		l.stateUseful += n
		l.stateCacheUseful += n
	}
	for _, entry := range stored {
		l.fulfillCacheNodeLocked(transaction, entry.Hash)
	}
	l.refreshRequestPeersLocked()
	l.markProgressLocked()
}

func (l *Ledger) fulfillCacheNodeLocked(transaction bool, hash [32]byte) {
	if lease, ok := l.requestLeases[hash]; ok && lease.transaction == transaction {
		l.fulfillLeaseLocked(hash, 0)
		l.removeMissingNodeLocked(transaction, lease.nodeID)
	} else {
		l.fulfillReadyNodeLocked(transaction, hash)
	}
	// A fetch pack can also fulfill an unleased cached frontier.
	missing := l.missingState
	if transaction {
		missing = l.missingTx
	}
	for _, node := range missing {
		if node.Hash == hash {
			l.removeMissingNodeLocked(transaction, node.NodeID)
			break
		}
	}
}
