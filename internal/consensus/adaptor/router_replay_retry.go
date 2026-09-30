package adaptor

import "time"

type standardReplayFailureClass uint8

const (
	standardReplayFailureNone standardReplayFailureClass = iota
	standardReplayFailureAvailability
	standardReplayFailureInvalidData
	standardReplayFailurePersistence
	standardReplayFailureExecution
)

const (
	standardReplayAvailabilityRetryLimit uint8 = 3
	standardReplayAvailabilityWaitWindow       = standardReplayProgressWindow
)

var standardReplayAvailabilityRetryDelays = [...]time.Duration{
	time.Second,
	2 * time.Second,
	4 * time.Second,
}

type standardReplayAvailabilityRetryResult uint8

const (
	standardReplayAvailabilityRetryNone standardReplayAvailabilityRetryResult = iota
	standardReplayAvailabilityRetryWaiting
	standardReplayAvailabilityRetryStarted
	standardReplayAvailabilityRetryExhausted
)

func (c *catchupReplayCoordinator) standardReplayHasAvailabilityBlockLocked() bool {
	if !c.standardReplay.active || c.standardReplay.replacement != nil {
		return false
	}
	entry := c.standardReplay.entries[c.standardReplay.anchorSeq+1]
	return entry != nil && entry.failureClass == standardReplayFailureAvailability &&
		(entry.availabilityPending || entry.availabilityRetrying || entry.availabilityExhausted)
}

func (c *catchupReplayCoordinator) standardReplayAvailabilityHeadLocked() *standardReplayEntry {
	if !c.standardReplay.active || !c.standardReplay.pivotReady {
		return nil
	}
	entry := c.standardReplay.entries[c.standardReplay.anchorSeq+1]
	if entry == nil || entry.generation != c.standardReplay.generation ||
		entry.seq != c.standardReplay.anchorSeq+1 || entry.acquisition != nil ||
		!entry.availabilityPending || entry.availabilityRetrying || entry.availabilityExhausted ||
		!entry.readyAt.IsZero() {
		return nil
	}
	return entry
}

func (c *catchupReplayCoordinator) retryStandardReplayAvailability(now time.Time) standardReplayAvailabilityRetryResult {
	if c.stoppedForShutdown() {
		return standardReplayAvailabilityRetryNone
	}
	c.acquisitionMu.Lock()
	entry := c.standardReplayAvailabilityHeadLocked()
	if entry == nil {
		c.acquisitionMu.Unlock()
		return standardReplayAvailabilityRetryNone
	}
	if entry.availabilityRetries >= standardReplayAvailabilityRetryLimit {
		entry.availabilityPending = false
		entry.availabilityRetrying = false
		entry.availabilityExhausted = true
		entry.availabilityNextRetryAt = time.Time{}
		c.acquisitionMu.Unlock()
		return standardReplayAvailabilityRetryExhausted
	}
	if entry.availabilityDeadlineAt.IsZero() {
		entry.availabilityDeadlineAt = now.Add(standardReplayAvailabilityWaitWindow)
		entry.availabilityNextRetryAt = now.Add(standardReplayAvailabilityRetryDelays[entry.availabilityRetries])
	}
	if !entry.availabilityNextRetryAt.IsZero() && now.Before(entry.availabilityNextRetryAt) {
		c.acquisitionMu.Unlock()
		return standardReplayAvailabilityRetryWaiting
	}
	if !entry.availabilityDeadlineAt.IsZero() && !now.Before(entry.availabilityDeadlineAt) {
		entry.availabilityPending = false
		entry.availabilityRetrying = false
		entry.availabilityExhausted = true
		entry.availabilityNextRetryAt = time.Time{}
		c.acquisitionMu.Unlock()
		return standardReplayAvailabilityRetryExhausted
	}
	if c.acquisition == nil {
		c.acquisitionMu.Unlock()
		return standardReplayAvailabilityRetryWaiting
	}

	tried := append([]uint64(nil), entry.availabilityTriedPeers...)
	var peerID uint64
	for _, candidate := range c.acquisition.SelectLedgerPeers(entry.hash, entry.seq, tried, 1) {
		if candidate != 0 && !standardReplayContainsPeer(tried, candidate) {
			peerID = candidate
			break
		}
	}
	if peerID == 0 {
		for _, candidate := range c.acquisition.SelectLedgerPeers(entry.hash, entry.seq, nil, 1) {
			if candidate != 0 {
				peerID = candidate
				break
			}
		}
	}
	if peerID == 0 {
		c.acquisitionMu.Unlock()
		return standardReplayAvailabilityRetryWaiting
	}

	il, _ := c.startLedgerReplayAcquisitionLegacyLocked(entry.seq, entry.hash, peerID)
	if il == nil || !il.TransactionOnly() {
		c.acquisitionMu.Unlock()
		return standardReplayAvailabilityRetryWaiting
	}
	entry.acquisition = il
	entry.peerID = peerID
	entry.requestedAt = now
	entry.availabilityPending = false
	entry.availabilityRetrying = true
	entry.availabilityRetries++
	entry.availabilityNextRetryAt = time.Time{}
	entry.availabilityTriedPeers = appendUniquePeers(entry.availabilityTriedPeers, il.Peers()...)
	entry.availabilityTriedPeers = appendUniquePeer(entry.availabilityTriedPeers, peerID)
	c.logger.Debug("retrying unavailable replay head", "generation", entry.generation,
		"anchor_seq", c.standardReplay.anchorSeq, "seq", entry.seq, "peer", peerID,
		"attempt", entry.availabilityRetries, "retained_entries", len(c.standardReplay.entries))
	c.acquisitionMu.Unlock()
	return standardReplayAvailabilityRetryStarted
}

func (c *catchupReplayCoordinator) standardReplayAvailabilityExhaustedState() (uint64, uint32, [32]byte, uint64, bool) {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	if !c.standardReplay.active || !c.standardReplay.pivotReady {
		return 0, 0, [32]byte{}, 0, false
	}
	entry := c.standardReplay.entries[c.standardReplay.anchorSeq+1]
	if entry == nil || entry.generation != c.standardReplay.generation ||
		entry.seq != c.standardReplay.anchorSeq+1 || entry.failureClass != standardReplayFailureAvailability ||
		!entry.availabilityExhausted || entry.acquisition != nil {
		return 0, 0, [32]byte{}, 0, false
	}
	return entry.generation, entry.seq, entry.hash, entry.peerID, true
}

func (c *catchupReplayCoordinator) standardReplayAvailabilityRetryActiveLocked(now time.Time) bool {
	if !c.standardReplay.active {
		return false
	}
	entry := c.standardReplay.entries[c.standardReplay.anchorSeq+1]
	if entry == nil || entry.failureClass != standardReplayFailureAvailability || entry.availabilityExhausted ||
		(!entry.availabilityPending && !entry.availabilityRetrying) {
		return false
	}
	return !entry.availabilityDeadlineAt.IsZero() && now.Before(entry.availabilityDeadlineAt)
}

func appendUniquePeers(peers []uint64, additions ...uint64) []uint64 {
	for _, peerID := range additions {
		peers = appendUniquePeer(peers, peerID)
	}
	return peers
}

func standardReplayContainsPeer(peers []uint64, peerID uint64) bool {
	for _, existing := range peers {
		if existing == peerID {
			return true
		}
	}
	return false
}
