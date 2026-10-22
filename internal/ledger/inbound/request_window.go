package inbound

import (
	"context"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
)

// Request metadata is deliberately charged to the same acquisition budget as
// map/cursor state. A 1 KiB slot charge covers the map bucket slack for the
// lease, wire-shadow, node-index, recent-owner, peer, and ready-set indexes plus the
// retained ready slice. At the current 896-slot bound this reserves 917,504
// bytes before any request container grows.
const missingRequestMetadataBytes = uint64(1024)

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

func (l *Ledger) enqueueReadyLeaseLocked(ready readyMissingNode) bool {
	if l.retired.Load() {
		return false
	}
	if len(l.readyRequests) >= missingRequestReadyReserve {
		return false
	}
	if _, reserved := l.requestLeases[ready.node.Hash]; reserved {
		return false
	}
	if _, queued := l.readySet[ready.node.Hash]; queued {
		return false
	}
	if !l.reserveRequestMetadataLocked() {
		return false
	}
	l.readyRequests = append(l.readyRequests, ready)
	l.readySet[ready.node.Hash] = struct{}{}
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

// fulfillReadyNodeLocked consumes a cached ready node. A failed hedge may have
// moved the logical lease into readyRequests while its original wire frame is
// still outstanding; retain that peer credit as a wire-only shadow.
func (l *Ledger) fulfillReadyNodeLocked(transaction bool, hash [32]byte) {
	now := SystemClock.Now()
	for i := range l.readyRequests {
		ready := l.readyRequests[i]
		if ready.transaction != transaction || ready.node.Hash != hash {
			continue
		}
		l.readyRequests = append(l.readyRequests[:i], l.readyRequests[i+1:]...)
		delete(l.readySet, hash)
		if ready.previousPeerID != 0 && ready.previousExpires.After(now) {
			l.requestLeases[hash] = missingRequestLease{
				previousPeerID:  ready.previousPeerID,
				previousExpires: ready.previousExpires,
				wireShadow:      true,
				transaction:     transaction,
				nodeID:          ready.node.NodeID,
				started:         ready.started,
				expires:         now,
				retries:         ready.retries,
				hedges:          ready.hedges,
			}
		}
		return
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
	l.requestLeases[node.Hash] = missingRequestLease{
		peerID:      peerID,
		transaction: transaction,
		nodeID:      node.NodeID,
		started:     now,
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
			lease := l.requestLeases[ready.node.Hash]
			lease.started = ready.started
			if lease.started.IsZero() {
				lease.started = now
			}
			lease.previousPeerID = ready.previousPeerID
			lease.previousExpires = ready.previousExpires
			lease.retries = ready.retries
			if !ready.started.IsZero() && lease.retries != ^uint32(0) {
				lease.retries++
			}
			lease.hedges = ready.hedges
			l.requestLeases[ready.node.Hash] = lease
			return ready.node, true
		}
		l.enqueueReadyLeaseLocked(ready)
		return shamap.MissingNode{}, false
	}
	if !claimOrphan {
		return shamap.MissingNode{}, false
	}
	for hash, lease := range l.requestLeases {
		if lease.transaction != transaction || lease.peerID != 0 {
			continue
		}
		if lease.wireShadow {
			continue
		}
		lease.peerID = peerID
		lease.expires = now.Add(missingRequestLeaseTTL)
		if lease.started.IsZero() {
			lease.started = now
		} else if lease.retries != ^uint32(0) {
			lease.retries++
		}
		l.requestLeases[hash] = lease
		l.recentNodes[hash] = peerID
		return shamap.MissingNode{Hash: hash, NodeID: lease.nodeID}, true
	}
	return shamap.MissingNode{}, false
}

func (l *Ledger) removeLeaseLocked(hash [32]byte) (missingRequestLease, bool) {
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
	return lease, true
}

// fulfillLeaseLocked removes a logical reservation after a verified local or
// peer placement. A hedge can satisfy the node while either wire frame is
// still outstanding; retain those peer credits in the bounded lease map until
// each reply boundary or expiry.
func (l *Ledger) fulfillLeaseLocked(hash [32]byte, replyPeerID uint64) {
	lease, ok := l.requestLeases[hash]
	if !ok {
		return
	}
	now := SystemClock.Now()
	keepPrevious := lease.previousPeerID != 0 && lease.previousPeerID != replyPeerID && lease.previousExpires.After(now)
	keepCurrent := lease.hedges != 0 && lease.peerID != 0 && lease.peerID != replyPeerID && lease.expires.After(now)
	if keepPrevious || keepCurrent {
		delete(l.recentNodes, hash)
		key := requestNodeKey{transaction: lease.transaction, nodeID: lease.nodeID}
		if current, exists := l.requestByNode[key]; exists && current == hash {
			delete(l.requestByNode, key)
		}
		if !keepPrevious {
			lease.previousPeerID = 0
			lease.previousExpires = time.Time{}
		}
		if !keepCurrent {
			lease.peerID = 0
			lease.expires = now
		}
		lease.wireShadow = true
		l.requestLeases[hash] = lease
		return
	}
	l.removeLeaseLocked(hash)
}

// requeueLeaseLocked releases a peer's send ownership while retaining the
// unresolved node. The ready reserve absorbs common partial replies; overflow
// stays as an unowned lease and is claimed directly by the next planner pass.
func (l *Ledger) requeueLeaseLocked(hash [32]byte, now time.Time) {
	lease, ok := l.requestLeases[hash]
	if !ok {
		return
	}
	if lease.wireShadow {
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
		if removed, ok := l.removeLeaseLocked(hash); ok {
			l.enqueueReadyLeaseLocked(readyMissingNode{
				node:            shamap.MissingNode{Hash: hash, NodeID: removed.nodeID},
				transaction:     removed.transaction,
				previousPeerID:  removed.previousPeerID,
				previousExpires: removed.previousExpires,
				started:         removed.started,
				retries:         removed.retries,
				hedges:          removed.hedges,
			})
		}
	}
}

func (l *Ledger) refreshPeerLeaseLocked(peerID uint64) {
	if peerID == 0 {
		return
	}
	now := SystemClock.Now()
	for _, lease := range l.requestLeases {
		if lease.peerID == peerID {
			l.requestPeers[peerID] = struct{}{}
			return
		}
		if lease.previousPeerID == peerID && lease.previousExpires.After(now) {
			l.requestPeers[peerID] = struct{}{}
			return
		}
	}
	delete(l.requestPeers, peerID)
}

func (l *Ledger) refreshRequestPeersLocked() {
	l.refreshRequestPeersAtLocked(SystemClock.Now())
}

func (l *Ledger) refreshRequestPeersAtLocked(now time.Time) {
	if l.retired.Load() {
		return
	}
	clear(l.requestPeers)
	for hash, lease := range l.requestLeases {
		if lease.wireShadow {
			if lease.peerID != 0 && !lease.expires.After(now) {
				lease.peerID = 0
				lease.expires = now
			}
			if lease.previousPeerID != 0 && !lease.previousExpires.After(now) {
				lease.previousPeerID = 0
				lease.previousExpires = time.Time{}
			}
			if lease.peerID == 0 && lease.previousPeerID == 0 {
				l.removeLeaseLocked(hash)
				continue
			}
			l.requestLeases[hash] = lease
		}
		if lease.previousPeerID != 0 && (lease.previousExpires.IsZero() || !lease.previousExpires.After(now)) {
			lease.previousPeerID = 0
			lease.previousExpires = time.Time{}
			l.requestLeases[hash] = lease
		}
		if lease.peerID != 0 {
			l.requestPeers[lease.peerID] = struct{}{}
		}
		if lease.previousPeerID != 0 {
			l.requestPeers[lease.previousPeerID] = struct{}{}
		}
	}
	for i, ready := range l.readyRequests {
		if ready.previousPeerID != 0 && (ready.previousExpires.IsZero() || !ready.previousExpires.After(now)) {
			ready.previousPeerID = 0
			ready.previousExpires = time.Time{}
			l.readyRequests[i] = ready
		}
		if ready.previousPeerID != 0 {
			l.requestPeers[ready.previousPeerID] = struct{}{}
		}
	}
}

func (l *Ledger) clearHedgePeerLocked(peerID uint64) {
	if peerID == 0 {
		return
	}
	for hash, lease := range l.requestLeases {
		if lease.wireShadow {
			if lease.previousPeerID == peerID {
				lease.previousPeerID = 0
				lease.previousExpires = time.Time{}
			}
			if lease.peerID == peerID {
				lease.peerID = 0
				lease.expires = time.Time{}
			}
			if lease.peerID == 0 && lease.previousPeerID == 0 {
				l.removeLeaseLocked(hash)
			} else {
				l.requestLeases[hash] = lease
			}
			continue
		}
		if lease.previousPeerID != peerID {
			continue
		}
		lease.previousPeerID = 0
		lease.previousExpires = time.Time{}
		l.requestLeases[hash] = lease
	}
	for i, ready := range l.readyRequests {
		if ready.previousPeerID != peerID {
			continue
		}
		ready.previousPeerID = 0
		ready.previousExpires = time.Time{}
		l.readyRequests[i] = ready
	}
}

func (l *Ledger) expireMissingLeasesLocked(now time.Time, force bool) {
	for hash, lease := range l.requestLeases {
		if lease.wireShadow {
			if lease.peerID != 0 && !lease.expires.After(now) {
				lease.peerID = 0
			}
			if lease.previousPeerID != 0 && !lease.previousExpires.After(now) {
				lease.previousPeerID = 0
			}
			if lease.peerID == 0 && lease.previousPeerID == 0 {
				l.removeLeaseLocked(hash)
			} else {
				l.requestLeases[hash] = lease
			}
			continue
		}
		if lease.peerID == 0 {
			if lease.previousPeerID != 0 && lease.previousExpires.After(now) {
				// A failed hedge can overflow the ready reserve. Keep its
				// shadow lease until the original wire request expires.
				continue
			}
			if force || !now.Before(lease.expires) {
				l.removeLeaseLocked(hash)
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
			l.removeLeaseLocked(hash)
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
