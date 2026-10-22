package adaptor

import (
	"cmp"
	"slices"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
)

func (c *catchupReplayCoordinator) retryReplayHeadTail(now time.Time) {
	c.acquisitionMu.Lock()
	pipeline := &c.standardReplay
	entry := pipeline.entries[pipeline.anchorSeq+1]
	var ledger *inbound.Ledger
	if pipeline.active && pipeline.pivotReady && entry != nil && !entry.failed && !entry.durable {
		ledger = entry.acquisition
	}
	c.acquisitionMu.Unlock()
	if ledger == nil || c.fetchTracker.Find(ledger.Hash()) != ledger {
		return
	}
	if lane := c.currentAcquisitionWork(); lane != nil && lane.has(ledger) {
		return
	}
	if !ledger.TxOnlyTailHedgeDue(now) {
		return
	}
	peers := c.replayTailCandidates(ledger, now)
	if len(peers) > 0 {
		c.submitAcquisitionWork(ledger, acquisitionWorkEvent{kind: acquisitionWorkTail, peers: peers, at: now})
	}
}

func (c *catchupReplayCoordinator) replayTailCandidates(ledger *inbound.Ledger, now time.Time) []uint64 {
	type candidate struct {
		id   uint64
		rank int
		rtt  time.Duration
	}
	available := acquisitionRequestCandidates(nil, ledger.Peers())
	candidates := make([]candidate, 0, len(available))
	for _, peerID := range available {
		if !c.acquisitionPeerConnected(peerID) {
			continue
		}
		rank := c.acquisitionPeerRank(peerID, ledger.Seq(), ledger.Hash(), now)
		if rank < peerUnknown {
			continue
		}
		rtt, measured := c.acquisition.PeerLatency(peerID)
		if !measured {
			rtt = 250 * time.Millisecond
		}
		candidates = append(candidates, candidate{id: peerID, rank: rank, rtt: rtt})
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int {
		if rank := cmp.Compare(b.rank, a.rank); rank != 0 {
			return rank
		}
		return cmp.Compare(a.rtt, b.rtt)
	})
	peers := make([]uint64, 0, len(candidates))
	for _, candidate := range candidates {
		peers = append(peers, candidate.id)
	}
	return peers
}
