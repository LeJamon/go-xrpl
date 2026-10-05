package inbound

import (
	"context"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
)

// Request metadata is deliberately charged to the same acquisition budget as
// map/cursor state. The request window remains bounded even when no budget is
// installed, while the charge keeps a shared budget from admitting a large
// scheduler alongside resident reply nodes.
const missingRequestMetadataBytes = uint64(128)

func scanMissingNodesContext(ctx context.Context, m *shamap.SHAMap, max int, filter shamap.SyncFilter) ([]shamap.MissingNode, shamap.MissingNodesStatus, error) {
	result, err := m.ScanMissingNodesContext(ctx, max, filter)
	return result.Nodes, result.Status, err
}

func (l *Ledger) requestReservationLimit() int {
	return missingRequestWindow + missingRequestReadyReserve
}

func (l *Ledger) requestAvailableLocked() int {
	available := l.requestReservationLimit() - len(l.requestLeases)
	if available < 0 {
		return 0
	}
	return available
}

func (l *Ledger) reserveRequestMetadataLocked() bool {
	if l.requestMetadataBytes != 0 {
		return true
	}
	metadataBytes := uint64(l.requestReservationLimit()) * missingRequestMetadataBytes
	if l.memory != nil && !l.memory.TryReserve(metadataBytes) {
		l.memoryWaiting = true
		return false
	}
	// Keep one reservation for the complete bounded window. Go map and slice
	// buckets retain their high-water capacity after entries are deleted, so
	// releasing one charge per lease would under-account that resident memory.
	l.requestMetadataBytes = metadataBytes
	return true
}

func (l *Ledger) ensureRequestContainersLocked() {
	if l.requestLeases == nil {
		l.requestLeases = make(map[[32]byte]missingRequestLease)
	}
	if l.requestByNode == nil {
		l.requestByNode = make(map[requestNodeKey][32]byte)
	}
	if l.recentNodes == nil {
		l.recentNodes = make(map[[32]byte]uint64)
	}
	if l.requestPeers == nil {
		l.requestPeers = make(map[uint64]struct{})
	}
	if l.readySet == nil {
		l.readySet = make(map[[32]byte]struct{})
	}
}

func (l *Ledger) releaseRequestMetadataLocked() {
	// The bounded-window reservation is released only when the acquisition is
	// retired, after all request maps and slices have become unreachable.
}

func (l *Ledger) enqueueReadyLocked(node shamap.MissingNode, transaction bool) bool {
	if l.retired.Load() {
		return false
	}
	if len(l.readyRequests) >= missingRequestReadyReserve {
		return false
	}
	if _, reserved := l.requestLeases[node.Hash]; reserved {
		return false
	}
	if _, queued := l.readySet[node.Hash]; queued {
		return false
	}
	if l.readySet == nil {
		l.readySet = make(map[[32]byte]struct{})
	}
	l.readyRequests = append(l.readyRequests, readyMissingNode{node: node, transaction: transaction})
	l.readySet[node.Hash] = struct{}{}
	return true
}

func (l *Ledger) removeReadyLocked(hash [32]byte) {
	delete(l.readySet, hash)
	for i := range l.readyRequests {
		if l.readyRequests[i].node.Hash == hash {
			l.readyRequests = append(l.readyRequests[:i], l.readyRequests[i+1:]...)
			return
		}
	}
}

func (l *Ledger) reserveMissingNodeLocked(node shamap.MissingNode, transaction bool, peerID uint64, now time.Time) bool {
	if l.retired.Load() {
		return false
	}
	if len(l.requestLeases) >= l.requestReservationLimit() {
		return false
	}
	if _, reserved := l.requestLeases[node.Hash]; reserved {
		return false
	}
	key := requestNodeKey{transaction: transaction, nodeID: node.NodeID}
	if _, reserved := l.requestByNode[key]; reserved {
		return false
	}
	if !l.reserveRequestMetadataLocked() {
		return false
	}
	l.ensureRequestContainersLocked()
	l.requestLeases[node.Hash] = missingRequestLease{
		peerID:      peerID,
		transaction: transaction,
		nodeID:      node.NodeID,
		expires:     now.Add(missingRequestLeaseTTL),
	}
	l.requestByNode[key] = node.Hash
	l.recentNodes[node.Hash] = peerID
	l.removeReadyLocked(node.Hash)
	return true
}

// claimReadyNodeLocked claims either a prefetched node or an orphaned lease.
// Orphaned leases are retained when the small ready reserve is full, so expiry
// and partial replies cannot lose the unresolved frontier.
func (l *Ledger) claimReadyNodeLocked(transaction bool, peerID uint64, now time.Time, claimOrphan bool) (shamap.MissingNode, bool) {
	for i := range l.readyRequests {
		ready := l.readyRequests[i]
		if ready.transaction != transaction {
			continue
		}
		l.readyRequests = append(l.readyRequests[:i], l.readyRequests[i+1:]...)
		delete(l.readySet, ready.node.Hash)
		if l.reserveMissingNodeLocked(ready.node, transaction, peerID, now) {
			return ready.node, true
		}
		l.enqueueReadyLocked(ready.node, transaction)
		return shamap.MissingNode{}, false
	}
	if !claimOrphan {
		return shamap.MissingNode{}, false
	}
	for hash, lease := range l.requestLeases {
		if lease.transaction != transaction || lease.peerID != 0 {
			continue
		}
		lease.peerID = peerID
		lease.expires = now.Add(missingRequestLeaseTTL)
		l.requestLeases[hash] = lease
		l.recentNodes[hash] = peerID
		return shamap.MissingNode{Hash: hash, NodeID: lease.nodeID}, true
	}
	return shamap.MissingNode{}, false
}

func (l *Ledger) removeLeaseLocked(hash [32]byte, _ bool, _ time.Time) (missingRequestLease, bool) {
	lease, ok := l.requestLeases[hash]
	if !ok {
		return missingRequestLease{}, false
	}
	delete(l.requestLeases, hash)
	delete(l.recentNodes, hash)
	key := requestNodeKey{transaction: lease.transaction, nodeID: lease.nodeID}
	if current, exists := l.requestByNode[key]; exists && current == hash {
		delete(l.requestByNode, key)
	}
	l.releaseRequestMetadataLocked()
	return lease, true
}

// requeueLeaseLocked releases a peer's send ownership while retaining the
// unresolved node. The ready reserve absorbs common partial replies; overflow
// stays as an unowned lease and is claimed directly by the next planner pass.
func (l *Ledger) requeueLeaseLocked(hash [32]byte, now time.Time) {
	lease, ok := l.requestLeases[hash]
	if !ok {
		return
	}
	if lease.peerID == 0 {
		return
	}
	oldPeer := lease.peerID
	lease.peerID = 0
	lease.expires = now
	l.requestLeases[hash] = lease
	l.recentNodes[hash] = 0
	l.refreshPeerLeaseLocked(oldPeer)
	if len(l.readyRequests) < missingRequestReadyReserve {
		if removed, ok := l.removeLeaseLocked(hash, false, now); ok {
			l.enqueueReadyLocked(shamap.MissingNode{Hash: hash, NodeID: removed.nodeID}, removed.transaction)
		}
	}
}

func (l *Ledger) refreshPeerLeaseLocked(peerID uint64) {
	if peerID == 0 {
		return
	}
	for _, lease := range l.requestLeases {
		if lease.peerID == peerID {
			l.requestPeers[peerID] = struct{}{}
			return
		}
	}
	delete(l.requestPeers, peerID)
}

func (l *Ledger) refreshRequestPeersLocked() {
	if l.retired.Load() {
		return
	}
	if l.requestPeers == nil {
		l.requestPeers = make(map[uint64]struct{})
	}
	clear(l.requestPeers)
	for _, lease := range l.requestLeases {
		if lease.peerID != 0 {
			l.requestPeers[lease.peerID] = struct{}{}
		}
	}
}

func (l *Ledger) expireMissingLeasesLocked(now time.Time, force bool) {
	for hash, lease := range l.requestLeases {
		if lease.peerID == 0 {
			if force || !now.Before(lease.expires) {
				l.removeLeaseLocked(hash, false, now)
			}
			continue
		}
		if !force && now.Before(lease.expires) {
			continue
		}
		l.requeueLeaseLocked(hash, now)
	}
	l.refreshRequestPeersLocked()
}

func (l *Ledger) releasePeerLeaseLocked(peerID uint64, hashes [][32]byte, now time.Time, enqueue bool) {
	for _, hash := range hashes {
		lease, ok := l.requestLeases[hash]
		if !ok || lease.peerID != peerID {
			continue
		}
		if enqueue {
			l.requeueLeaseLocked(hash, now)
			continue
		}
		// The caller has observed the peer's reply boundary. Keep its unresolved
		// IDs reserved, but free the peer send credit for another batch.
		lease.peerID = 0
		lease.expires = now.Add(missingRequestLeaseTTL)
		l.requestLeases[hash] = lease
		l.recentNodes[hash] = 0
	}
	l.refreshRequestPeersLocked()
}

func (l *Ledger) releaseAllPeerLeasesLocked(peerID uint64, now time.Time, enqueue bool) {
	hashes := make([][32]byte, 0, len(l.requestLeases))
	for hash, lease := range l.requestLeases {
		if lease.peerID == peerID {
			hashes = append(hashes, hash)
		}
	}
	l.releasePeerLeaseLocked(peerID, hashes, now, enqueue)
}

// releaseRequestSlotLocked is used after a complete, normally consumed reply.
// Every still-leased ID belongs to the same send batch and must be retried
// without waiting for the lease TTL.
func (l *Ledger) releaseRequestSlotLocked(peerID uint64, transaction bool, now time.Time) int {
	hashes := make([][32]byte, 0)
	for hash, lease := range l.requestLeases {
		if lease.peerID == peerID && lease.transaction == transaction {
			hashes = append(hashes, hash)
		}
	}
	for _, hash := range hashes {
		l.requeueLeaseLocked(hash, now)
	}
	l.refreshRequestPeersLocked()
	return len(hashes)
}

func (l *Ledger) clearTreeReservationsLocked(transaction bool, now time.Time) {
	for hash, lease := range l.requestLeases {
		if lease.transaction == transaction {
			l.removeLeaseLocked(hash, false, now)
		}
	}
	for i := 0; i < len(l.readyRequests); {
		if l.readyRequests[i].transaction != transaction {
			i++
			continue
		}
		delete(l.readySet, l.readyRequests[i].node.Hash)
		l.readyRequests = append(l.readyRequests[:i], l.readyRequests[i+1:]...)
	}
	l.refreshRequestPeersLocked()
}

func (l *Ledger) releaseAllRequestMetadataLocked() uint64 {
	bytes := l.requestMetadataBytes
	l.requestMetadataBytes = 0
	// Drop the containers themselves before returning the fixed reservation.
	// clear leaves map buckets and slice backing arrays reachable through the
	// Ledger, so releasing the charge at that point would under-account memory.
	l.requestLeases = nil
	l.requestByNode = nil
	l.recentNodes = nil
	l.requestPeers = nil
	l.readyRequests = nil
	l.readySet = nil
	return bytes
}
