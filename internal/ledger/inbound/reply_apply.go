package inbound

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

// ApplyMissingReplyContext verifies and attaches the unconsumed suffix of one
// peer reply. Normal replies release the matching peer's send credit even when
// they are partial; unresolved leases are requeued for another peer. A memory
// admission failure leaves the current suffix owned by the peer so the caller
// can resume it after a checkpoint.
func (l *Ledger) ApplyMissingReplyContext(ctx context.Context, peerID uint64, transaction bool, nodes []message.LedgerNode) (MissingReplyResult, error) {
	result := MissingReplyResult{Received: len(nodes)}
	if err := l.retirementError(); err != nil {
		return result, err
	}
	if len(nodes) == 0 {
		l.mu.Lock()
		result.Complete = l.state == StateComplete
		l.mu.Unlock()
		return result, nil
	}
	if err := ValidateReplyNodeCount(nodes); err != nil {
		return result, err
	}

	l.workMu.Lock()
	defer l.finishWork()
	if err := l.retirementError(); err != nil {
		return result, err
	}

	l.mu.Lock()
	if l.state == StateComplete || (transaction && l.haveTx) || (!transaction && l.haveState) {
		result.Complete = l.state == StateComplete
		l.mu.Unlock()
		return result, nil
	}
	if l.state != StateWantState {
		state := l.state
		l.mu.Unlock()
		return result, fmt.Errorf("unexpected state %d for missing reply", state)
	}
	m := l.txMap
	label := "tx"
	if !transaction {
		m = l.stateMap
		label = "state"
	}
	if m == nil {
		l.mu.Unlock()
		return result, fmt.Errorf("missing %s map", label)
	}
	result, stored, matchedPeer, applyErr := l.applyMissingReplyNodesLocked(ctx, peerID, transaction, m, nodes, label)
	if transaction {
		l.txRecv += uint64(result.Consumed)
		l.txUseful += uint64(result.Useful)
	} else {
		l.stateRecv += uint64(result.Consumed)
		l.stateUseful += uint64(result.Useful)
	}
	if result.Useful > 0 {
		l.markProgressLocked()
	}
	l.publishSnapshotLocked()
	l.mu.Unlock()

	if persistErr := l.persistReceived(ctx, stored, label+" nodes"); persistErr != nil {
		return result, persistErr
	}
	if applyErr != nil && !errors.Is(applyErr, shamap.ErrAcquisitionMemory) {
		l.mu.Lock()
		if !errors.Is(applyErr, ErrInvalidPeerNode) && l.state != StateComplete {
			l.state = StateFailed
			l.err = applyErr
		}
		l.mu.Unlock()
		return result, applyErr
	}
	if result.Useful > 0 && applyErr == nil {
		l.finishReceivedTree(ctx, m, transaction)
	}

	l.mu.Lock()
	if err := l.retirementError(); err != nil {
		l.mu.Unlock()
		return result, err
	}
	if applyErr == nil && result.Consumed == len(nodes) && matchedPeer {
		result.Unfulfilled = l.countPeerLeasesLocked(peerID, transaction)
		l.releaseRequestSlotLocked(peerID, transaction, SystemClock.Now())
	} else {
		result.Unfulfilled = l.countPeerLeasesLocked(peerID, transaction)
	}
	l.refreshRequestPeersLocked()
	l.recomputeComplete()
	result.Complete = l.state == StateComplete
	l.mu.Unlock()
	return result, applyErr
}

// applyMissingReplyNodesLocked returns matchedPeer when at least one
// validated reply node belonged to the currently leased batch for peerID. A
// late node can fulfill a current lease owned by another peer, but it must not
// release that newer peer's unrelated send credit.
func (l *Ledger) applyMissingReplyNodesLocked(
	ctx context.Context,
	peerID uint64,
	transaction bool,
	m *shamap.SHAMap,
	nodes []message.LedgerNode,
	label string,
) (MissingReplyResult, []shamap.FlushEntry, bool, error) {
	result := MissingReplyResult{Received: len(nodes)}
	stored := make([]shamap.FlushEntry, 0, len(nodes))
	matchedPeer := false
	for i := range nodes {
		node := nodes[i]
		parsedID, err := node.SHAMapNodeID()
		if err != nil {
			l.rejectCount++
			l.lastRejectErr = err.Error()
			result.Consumed = i + 1
			return result, stored, matchedPeer, fmt.Errorf("%w: %s node reference: %v", ErrInvalidPeerNode, label, err)
		}
		if parsedID.IsRoot() {
			result.Consumed++
			continue
		}

		key := requestNodeKey{transaction: transaction, nodeID: parsedID}
		leaseHash, hasLease := l.requestByNode[key]
		lease, leaseExists := l.requestLeases[leaseHash]
		if !hasLease || !leaseExists || lease.transaction != transaction {
			hasLease = false
		}
		if hasLease && lease.peerID == peerID {
			matchedPeer = true
		}

		res, entry, addErr := m.AddKnownNodeByIDWithEntryContext(ctx, parsedID, node.NodeData)
		if errors.Is(addErr, context.Canceled) || errors.Is(addErr, context.DeadlineExceeded) {
			return result, stored, matchedPeer, addErr
		}
		if errors.Is(addErr, shamap.ErrAcquisitionMemory) {
			// The current node has not been consumed. The owner and offset stay
			// intact so the worker can resume this exact suffix after checkpoint.
			return result, stored, matchedPeer, addErr
		}
		result.Consumed++

		switch res {
		case shamap.NodeUseful:
			result.Useful++
			if addErr != nil {
				return result, stored, matchedPeer, fmt.Errorf("prepare verified %s node for persistence: %w", label, addErr)
			}
			stored = append(stored, entry)
			if hasLease {
				l.removeLeaseLocked(leaseHash)
				result.Fulfilled++
			}
			l.removeMissingNodeLocked(transaction, parsedID)
		case shamap.NodeDuplicate:
			if hasLease {
				l.removeLeaseLocked(leaseHash)
				result.Fulfilled++
			}
			l.removeMissingNodeLocked(transaction, parsedID)
		case shamap.NodeReRequest:
			// This is a valid node ahead of a hash-only ancestor. It was
			// processed, but the requested frontier remains unfulfilled and is
			// retried by the next scan.
		default:
			l.rejectCount++
			if addErr != nil {
				l.lastRejectErr = addErr.Error()
			}
			return result, stored, matchedPeer, fmt.Errorf("%w: %s node rejected", ErrInvalidPeerNode, label)
		}
	}
	return result, stored, matchedPeer, nil
}

func (l *Ledger) countPeerLeasesLocked(peerID uint64, transaction bool) int {
	count := 0
	for _, lease := range l.requestLeases {
		if lease.peerID == peerID && lease.transaction == transaction {
			count++
		}
	}
	return count
}
