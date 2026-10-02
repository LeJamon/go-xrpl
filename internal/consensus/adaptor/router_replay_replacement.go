package adaptor

import (
	"context"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/shamap"
)

type standardReplayReplacement struct {
	generation  uint64
	seq         uint32
	hash        [32]byte
	peerID      uint64
	acquisition *inbound.Ledger
	retryAt     time.Time
	installing  bool
}

const standardReplayLocalCandidateCheckTimeout = time.Second

// localReplayReplacementCandidate returns a complete, root-checked local
// ledger suitable for the replacement install path. A hash lookup alone is
// insufficient: the service may retain an unvalidated or partially materialized
// ledger by hash, so callers must receive usable state and transaction maps.
func (c *catchupReplayCoordinator) localReplayReplacementCandidate(
	seq uint32,
	hash [32]byte,
) (*header.LedgerHeader, *shamap.SHAMap, *shamap.SHAMap, bool) {
	if seq == 0 || hash == ([32]byte{}) || c.adaptor == nil {
		return nil, nil, nil, false
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return nil, nil, nil, false
	}
	local, err := svc.GetLedgerByHash(hash)
	if err != nil || local == nil || !local.IsClosed() || local.Sequence() != seq || local.Hash() != hash {
		return nil, nil, nil, false
	}
	h := local.Header()
	if h.LedgerIndex != seq || h.Hash != hash || header.CalculateHash(h) != hash {
		return nil, nil, nil, false
	}
	stateMap, err := local.StateMapSnapshot()
	if err != nil || stateMap == nil {
		return nil, nil, nil, false
	}
	txMap, err := local.TxMapSnapshot()
	if err != nil || txMap == nil {
		return nil, nil, nil, false
	}

	ctx := c.lifecycleContext()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, standardReplayLocalCandidateCheckTimeout)
	defer cancel()
	if !localReplayMapComplete(ctx, stateMap) || !localReplayMapComplete(ctx, txMap) {
		return nil, nil, nil, false
	}
	stateRoot, err := stateMap.Hash()
	if err != nil || stateRoot != h.AccountHash {
		return nil, nil, nil, false
	}
	txRoot, err := txMap.Hash()
	if err != nil || txRoot != h.TxHash {
		return nil, nil, nil, false
	}
	return &h, stateMap, txMap, true
}

func localReplayMapComplete(ctx context.Context, m *shamap.SHAMap) bool {
	if m == nil {
		return false
	}
	result, err := m.CheckComplete(ctx)
	return err == nil && result != nil && len(result.Missing) == 0 && len(result.Corrupt) == 0
}

func (c *catchupReplayCoordinator) reserveStandardReplayReplacement(generation uint64, seq uint32, hash [32]byte, peerID uint64, now time.Time) bool {
	c.acquisitionMu.Lock()
	if c.stoppedForShutdown() || !c.standardReplay.active || !c.standardReplay.pivotReady ||
		c.standardReplay.generation != generation || seq <= c.standardReplay.anchorSeq || hash == ([32]byte{}) {
		c.acquisitionMu.Unlock()
		return false
	}
	if c.standardReplay.replacement != nil {
		c.acquisitionMu.Unlock()
		return false
	}
	if seq >= c.standardReplay.targetSeq {
		c.standardReplay.targetSeq, c.standardReplay.targetHash = seq, hash
	}
	c.standardReplay.replacement = &standardReplayReplacement{generation: generation, seq: seq, hash: hash, peerID: peerID}
	c.replayPipelineFallbacks.Add(1)
	c.logger.Info("retaining replay anchor while replacing unavailable replay", "generation", generation,
		"anchor_seq", c.standardReplay.anchorSeq, "replacement_seq", seq, "replacement_hash", fmt.Sprintf("%x", hash[:8]),
		"retained_entries", len(c.standardReplay.entries))
	c.acquisitionMu.Unlock()
	c.retryStandardReplayReplacement(now)
	return true
}

func (c *catchupReplayCoordinator) retryStandardReplayReplacement(now time.Time) {
	c.acquisitionMu.Lock()
	replacement := c.standardReplay.replacement
	if c.stoppedForShutdown() || !c.standardReplay.active || replacement == nil ||
		replacement.generation != c.standardReplay.generation || replacement.installing || now.Before(replacement.retryAt) {
		c.acquisitionMu.Unlock()
		return
	}
	if replacement.acquisition != nil {
		if c.fetchTracker.Find(replacement.hash) == replacement.acquisition {
			c.acquisitionMu.Unlock()
			return
		}
		replacement.acquisition = nil
	}
	generation, seq, hash, hint := replacement.generation, replacement.seq, replacement.hash, replacement.peerID
	anchorSeq, targetSeq, targetHash := c.standardReplay.anchorSeq, c.standardReplay.targetSeq, c.standardReplay.targetHash
	c.acquisitionMu.Unlock()
	if localHeader, stateMap, txMap, ok := c.localReplayReplacementCandidate(seq, hash); ok {
		if c.completeLocalStandardReplayReplacement(generation, replacement, localHeader, stateMap, txMap) {
			return
		}
	}
	var survivor *inbound.Ledger
	for _, candidate := range c.fetchTracker.Active() {
		if candidate.TransactionOnly() || candidate.Reason() != inbound.ReasonConsensus || candidate.Seq() <= anchorSeq {
			continue
		}
		if candidate.Hash() == hash && candidate.Seq() == seq ||
			c.canAdoptKnownFrozenPivot(candidate.Seq(), candidate.Hash(), targetSeq, targetHash) {
			survivor = candidate
			seq, hash, hint = candidate.Seq(), candidate.Hash(), candidate.PeerID()
			break
		}
	}
	peerID := hint
	if survivor == nil {
		var ok bool
		peerID, ok = c.resolveAcquisitionPeer(seq, hint)
		if !ok {
			return
		}
	}
	c.acquisitionMu.Lock()
	if c.stoppedForShutdown() || !c.standardReplay.active || c.standardReplay.generation != generation ||
		c.standardReplay.replacement != replacement || replacement.acquisition != nil ||
		c.standardReplay.targetSeq != targetSeq || c.standardReplay.targetHash != targetHash ||
		(survivor != nil && c.fetchTracker.Find(hash) != survivor) {
		c.acquisitionMu.Unlock()
		return
	}
	if c.standardReplay.anchorSeq >= seq {
		if c.consensusRecovery.stepHash == replacement.hash {
			c.consensusRecovery.stepHash = [32]byte{}
		}
		c.standardReplay.replacement = nil
		c.acquisitionMu.Unlock()
		return
	}
	var retired []*inbound.Ledger
	if old := c.fetchTracker.Find(hash); old != nil && old.TransactionOnly() {
		if c.discardInboundAcquisitionLocked(old) {
			retired = append(retired, old)
			if entry := c.standardReplay.entries[seq]; entry != nil && entry.acquisition == old {
				entry.acquisition = nil
			}
		}
	}
	if c.canAdmitCatchupLocked(hash, maxConcurrentCatchup) {
		admission := c.startFrozenPivotReplacementLocked(seq, hash, peerID)
		if il := admission.acquisition; (admission.outcome == fullStateAdmissionStarted || admission.outcome == fullStateAdmissionJoined) &&
			il != nil && !il.TransactionOnly() && il.Reason() == inbound.ReasonConsensus && il.Seq() == seq {
			replacement.seq, replacement.hash = seq, hash
			replacement.acquisition = il
			replacement.peerID = peerID
			if c.consensusRecovery.targetHash != ([32]byte{}) {
				c.consensusRecovery.stepHash = hash
			}
		}
	}
	c.acquisitionMu.Unlock()
	c.retireLegacyAcquisitions(retired)
}

func (c *catchupReplayCoordinator) failStandardReplayReplacement(il *inbound.Ledger, cause error) bool {
	c.acquisitionMu.Lock()
	replacement := c.standardReplay.replacement
	if replacement == nil || replacement.acquisition != il || replacement.generation != c.standardReplay.generation {
		c.acquisitionMu.Unlock()
		return false
	}
	replacement.acquisition = nil
	replacement.installing = false
	replacement.retryAt = time.Now().Add(catchupFailureCooldown)
	c.logger.Warn("replacement acquisition failed; replay anchor retained", "seq", replacement.seq,
		"hash", fmt.Sprintf("%x", replacement.hash[:8]), "anchor_seq", c.standardReplay.anchorSeq,
		"retry_at", replacement.retryAt, "error", cause)
	c.acquisitionMu.Unlock()
	return true
}

func (c *catchupReplayCoordinator) completeStandardReplayReplacement(il *inbound.Ledger, h *header.LedgerHeader, stateMap, txMap *shamap.SHAMap) bool {
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	replacement := c.standardReplay.replacement
	if !c.standardReplay.active || replacement == nil || replacement.acquisition != il ||
		replacement.generation != c.standardReplay.generation {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return false
	}
	if c.stoppedForShutdown() || h == nil || h.LedgerIndex != replacement.seq || h.Hash != replacement.hash ||
		!c.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), true) {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		c.retireAcquisitionStore(c.lifecycleContext(), il)
		return true
	}
	replacement.installing = true
	c.acquisitionMu.Unlock()
	initial, err := c.adaptor.LedgerService().BootstrapLedgerWithState(c.lifecycleContext(), h, stateMap, txMap)
	if err != nil {
		c.replayCommitMu.Unlock()
		c.recordReplayAcquisitionFailure(il, err)
		c.failStandardReplayReplacement(il, err)
		c.retireAcquisitionStore(c.lifecycleContext(), il)
		return true
	}
	c.acquisitionMu.Lock()
	retirement, installed := c.installStandardReplayReplacementLocked(replacement, h, initial)
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	if !installed {
		return true
	}
	return c.finishStandardReplayReplacement(replacement, h, initial, retirement)
}

// The candidate must pass localReplayReplacementCandidate before publication.
func (c *catchupReplayCoordinator) completeLocalStandardReplayReplacement(
	generation uint64,
	replacement *standardReplayReplacement,
	h *header.LedgerHeader,
	stateMap, txMap *shamap.SHAMap,
) bool {
	if replacement == nil || h == nil || stateMap == nil || txMap == nil {
		return false
	}
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	if c.stoppedForShutdown() || !c.standardReplay.active || c.standardReplay.generation != generation ||
		c.standardReplay.replacement != replacement || replacement.generation != generation ||
		replacement.acquisition != nil || replacement.installing || replacement.seq != h.LedgerIndex ||
		replacement.hash != h.Hash || c.standardReplay.anchorSeq >= h.LedgerIndex {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return false
	}
	replacement.installing = true
	c.acquisitionMu.Unlock()
	initial, err := c.adaptor.LedgerService().BootstrapLedgerWithState(c.lifecycleContext(), h, stateMap, txMap)
	if err != nil {
		c.acquisitionMu.Lock()
		if c.standardReplay.active && c.standardReplay.generation == generation &&
			c.standardReplay.replacement == replacement && replacement.installing {
			replacement.installing = false
		}
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		c.logger.Warn("verified local replacement candidate could not be bootstrapped", "seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]), "error", err)
		return false
	}
	c.acquisitionMu.Lock()
	retirement, installed := c.installStandardReplayReplacementLocked(replacement, h, initial)
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	if !installed {
		return false
	}
	return c.finishStandardReplayReplacement(replacement, h, initial, retirement)
}

// Caller holds replayCommitMu and acquisitionMu after successful Bootstrap.
func (c *catchupReplayCoordinator) installStandardReplayReplacementLocked(
	replacement *standardReplayReplacement,
	h *header.LedgerHeader,
	initial bool,
) (standardReplayRetirement, bool) {
	if replacement == nil || h == nil || !c.standardReplay.active ||
		c.standardReplay.replacement != replacement || replacement.generation != c.standardReplay.generation ||
		replacement.seq != h.LedgerIndex || replacement.hash != h.Hash {
		return standardReplayRetirement{}, false
	}
	var retired []*inbound.Ledger
	if c.standardReplay.anchorSeq < h.LedgerIndex {
		lastSeq, lastHash := h.LedgerIndex, h.Hash
		for {
			entry := c.standardReplay.entries[lastSeq+1]
			if entry == nil || entry.parentHash != lastHash {
				break
			}
			lastSeq, lastHash = entry.seq, entry.hash
		}
		for seq, entry := range c.standardReplay.entries {
			if seq <= h.LedgerIndex || seq > lastSeq {
				if entry.acquisition != nil && c.discardInboundAcquisitionLocked(entry.acquisition) {
					retired = append(retired, entry.acquisition)
				}
				if c.consensusRecovery.stepHash == entry.hash {
					c.consensusRecovery.stepHash = [32]byte{}
				}
				delete(c.standardReplay.entries, seq)
				c.replayPipelineDiscarded.Add(1)
			}
		}
		c.standardReplay.pivotSeq, c.standardReplay.pivotHash = h.LedgerIndex, h.Hash
		c.standardReplay.anchorSeq, c.standardReplay.anchorHash = h.LedgerIndex, h.Hash
		c.standardReplay.collectSeq, c.standardReplay.collectHash = lastSeq, lastHash
		c.standardReplay.initialCandidate = initial
		c.standardReplay.progressSampleAt = time.Now()
		c.standardReplay.sampleAnchorSeq = h.LedgerIndex
		c.standardReplay.stalledSamples = 0
		c.standardReplay.retargetAttemptAt = time.Time{}
		c.standardReplay.headBlockedAt = time.Time{}
		if c.consensusRecovery.targetHash != ([32]byte{}) {
			c.consensusRecovery.anchorSeq, c.consensusRecovery.anchorHash = h.LedgerIndex, h.Hash
			c.consensusRecovery.stepHash = h.Hash
		}
	}
	c.standardReplay.replacement = nil
	delete(c.replayFallbackRequired, h.Hash)
	reachedTarget := c.standardReplay.targetSeq == h.LedgerIndex && c.standardReplay.targetHash == h.Hash
	retirement := standardReplayRetirement{ledgers: retired}
	if reachedTarget {
		retirement.baseLedger, retirement.release = c.standardReplay.baseLedger, c.standardReplay.baseRelease
		c.standardReplay.baseLedger, c.standardReplay.baseRelease = nil, nil
		c.standardReplay.active = false
		c.standardReplay.applying = false
		c.standardReplay.backpressured = false
		c.standardReplay.entries = nil
	}
	return retirement, true
}

func (c *catchupReplayCoordinator) finishStandardReplayReplacement(
	replacement *standardReplayReplacement,
	h *header.LedgerHeader,
	initial bool,
	retirement standardReplayRetirement,
) bool {
	c.retireStandardReplay(retirement)
	c.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, initial)
	c.acquisitionMu.Lock()
	current := c.standardReplay.active && c.standardReplay.generation == replacement.generation
	c.acquisitionMu.Unlock()
	if !current {
		return true
	}
	c.refillStandardReplayCollector(replacement.peerID)
	c.acquisitionMu.Lock()
	c.scheduleReadyStandardReplayDrainLocked()
	c.acquisitionMu.Unlock()
	return true
}
