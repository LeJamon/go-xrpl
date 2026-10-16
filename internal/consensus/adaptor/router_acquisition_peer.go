package adaptor

import (
	"slices"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

const (
	peerAvailabilityTTL   = 10 * time.Second
	peerAvailabilityLimit = 256
)

const (
	peerUnavailable = iota
	peerBehind
	peerUnknown
	peerAdvertised
)

type peerAvailabilityMiss struct {
	hash       [32]byte
	seq        uint32
	last       uint32
	closed     uint32
	advertised bool
	newer      bool
	expiresAt  time.Time
}

func (c *catchupReplayCoordinator) resolveAcquisitionPeer(seq uint32, hash [32]byte, preferred uint64) (uint64, bool) {
	return c.chooseAcquisitionPeer(seq, hash, preferred, nil, false)
}

func (c *catchupReplayCoordinator) resolveReplayPeer(seq uint32, hash [32]byte, preferred uint64) (uint64, bool) {
	if hash == ([32]byte{}) {
		if entry, ok := c.lookupSeqHash(seq); ok {
			hash = entry.hash
		}
	}
	return c.chooseAcquisitionPeer(seq, hash, preferred, nil, true)
}

// Availability outranks both the forwarding hint and replay support. Unknown
// advertisements remain usable; a lagging topology can still use standard fetches.
func (c *catchupReplayCoordinator) chooseAcquisitionPeer(seq uint32, hash [32]byte, preferred uint64, excluded map[uint64]struct{}, replayOnly bool) (uint64, bool) {
	c.peersMu.RLock()
	ids := make([]uint64, 0, len(c.peerStates)+1)
	for id := range c.peerStates {
		ids = append(ids, uint64(id))
	}
	c.peersMu.RUnlock()
	ids = appendUniquePeer(ids, preferred)
	skip := make([]uint64, 0, len(excluded))
	for id := range excluded {
		skip = append(skip, id)
		if replayOnly {
			ids = append(ids, id)
		}
	}
	ids = append(ids, c.acquisition.SelectLedgerPeers(hash, seq, skip, replayAvailabilityPeerSelectionLimit)...)
	if replayOnly {
		ids = append(ids, c.acquisition.ReplayCapablePeersExcluding(skip, replayAvailabilityPeerSelectionLimit)...)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	bestRank, bestReplayRank := -1, -1
	var best, bestReplay uint64
	now := time.Now()
	for _, id := range ids {
		if id == 0 {
			continue
		}
		_, skip := excluded[id]
		if skip && !replayOnly {
			continue
		}
		if c.peerSessions != nil && !c.peerSessions.IsPeerConnected(peermanagement.PeerID(id)) {
			continue
		}
		rank := c.acquisitionPeerRank(id, seq, hash, now)
		if rank > bestRank || rank == bestRank && id == preferred {
			best, bestRank = id, rank
		}
		if replayOnly && !skip && c.acquisition.PeerSupportsReplay(id) &&
			(rank > bestReplayRank || rank == bestReplayRank && id == preferred) {
			bestReplay, bestReplayRank = id, rank
		}
	}
	if replayOnly {
		return bestReplay, bestReplay != 0 && bestReplayRank >= peerUnknown && bestReplayRank >= bestRank
	}
	return best, best != 0
}

func (c *catchupReplayCoordinator) acquisitionPeerRank(peerID uint64, seq uint32, hash [32]byte, now time.Time) int {
	has, first, last := c.acquisition.PeerLedgerAvailability(peerID, hash, seq)
	c.peersMu.Lock()
	defer c.peersMu.Unlock()
	var state peerLedgerState
	if st := c.peerStates[peermanagement.PeerID(peerID)]; st != nil {
		state = *st
	}
	advertised := has || hash != ([32]byte{}) && (hash == state.LedgerHash || state.haveParent && hash == state.parentHash)
	if miss, ok := c.peerAvailabilityMisses[peerID]; ok {
		newAdvertisement := advertised &&
			(hash == miss.hash && !miss.advertised || miss.newer && seq > miss.seq)
		advanced := last > miss.last || state.LedgerSeq > miss.closed || newAdvertisement
		if !now.Before(miss.expiresAt) || advanced {
			delete(c.peerAvailabilityMisses, peerID)
		} else if hash == miss.hash || seq != 0 && (seq == miss.seq || miss.newer && seq > miss.seq) {
			return peerUnavailable
		}
	}
	if advertised {
		return peerAdvertised
	}
	if seq != 0 && first != 0 && last != 0 {
		if seq < first || seq > last {
			return peerBehind
		}
		return peerUnknown
	}
	if seq != 0 && state.LedgerSeq != 0 && state.LedgerSeq < seq {
		return peerBehind
	}
	return peerUnknown
}

func (c *catchupReplayCoordinator) recordPeerAvailabilityMiss(peerID uint64, seq uint32, hash [32]byte, reply message.ReplyError, now time.Time) {
	advertised, _, last := c.acquisition.PeerLedgerAvailability(peerID, hash, seq)
	c.peersMu.Lock()
	defer c.peersMu.Unlock()
	if c.peerAvailabilityMisses == nil {
		c.peerAvailabilityMisses = make(map[uint64]peerAvailabilityMiss)
	}
	var oldest uint64
	var oldestExpiry time.Time
	for id, miss := range c.peerAvailabilityMisses {
		if !now.Before(miss.expiresAt) {
			delete(c.peerAvailabilityMisses, id)
			continue
		}
		if oldest == 0 || miss.expiresAt.Before(oldestExpiry) {
			oldest, oldestExpiry = id, miss.expiresAt
		}
	}
	if _, exists := c.peerAvailabilityMisses[peerID]; !exists && len(c.peerAvailabilityMisses) >= peerAvailabilityLimit {
		delete(c.peerAvailabilityMisses, oldest)
	}
	var closed uint32
	if st := c.peerStates[peermanagement.PeerID(peerID)]; st != nil {
		closed = st.LedgerSeq
		advertised = advertised || hash == st.LedgerHash || st.haveParent && hash == st.parentHash
	}
	if prior, ok := c.peerAvailabilityMisses[peerID]; ok && prior.newer && seq < prior.seq &&
		last <= prior.last && closed <= prior.closed {
		return
	}
	c.peerAvailabilityMisses[peerID] = peerAvailabilityMiss{
		hash: hash, seq: seq, last: last, closed: closed, advertised: advertised,
		newer:     reply == message.ReplyErrorNoLedger && seq >= last && seq >= closed,
		expiresAt: now.Add(peerAvailabilityTTL),
	}
}
