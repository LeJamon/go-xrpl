package adaptor

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	// Keep the stage record and a conservative transaction payload allowance
	// charged from admission through publication. The allowance is trimmed to
	// the measured unbacked map size once the map is handed to the pipeline;
	// larger maps must obtain an explicit top-up before they can be retained.
	standardReplayEntryMetadataBytes          = 8 << 10
	standardReplayPreparedPayloadReserveBytes = 8 << 20
	standardReplayProcessingBytes             = 4 << 20
)

const standardReplayUnknownMapBytes = ^uint64(0)

type standardReplayMemoryKind uint8

const (
	standardReplayMemoryFuture standardReplayMemoryKind = iota + 1
	standardReplayMemoryHead
)

type standardReplayMemoryWaitKind uint8

const (
	standardReplayMemoryWaitHead standardReplayMemoryWaitKind = iota + 1
	standardReplayMemoryWaitProcessing
	standardReplayMemoryWaitFuture
)

type standardReplayMemoryReservation struct {
	account      *shamap.AcquisitionBudget
	bytes        uint64
	payloadBytes uint64
	kind         standardReplayMemoryKind
}

func (c *catchupReplayCoordinator) standardReplayMemoryAccount() *shamap.AcquisitionBudget {
	if c == nil {
		return nil
	}
	c.acquisitionWorkMu.RLock()
	account := c.acquisitionMemory
	c.acquisitionWorkMu.RUnlock()
	return account
}

func reserveStandardReplayMemory(account *shamap.AcquisitionBudget, kind standardReplayMemoryKind, bytes uint64) bool {
	if account == nil || bytes == 0 {
		return true
	}
	switch kind {
	case standardReplayMemoryHead:
		return account.TryReserveReplayHead(bytes)
	case standardReplayMemoryFuture:
		return account.TryReserveReplay(bytes)
	}
	return false
}

func releaseStandardReplayMemory(account *shamap.AcquisitionBudget, kind standardReplayMemoryKind, bytes uint64) {
	if account == nil || bytes == 0 {
		return
	}
	switch kind {
	case standardReplayMemoryHead:
		account.ReleaseReplayHead(bytes)
		return
	case standardReplayMemoryFuture:
		account.ReleaseReplay(bytes)
		return
	}
}

func reserveStandardReplayProcessing(account *shamap.AcquisitionBudget, bytes uint64) bool {
	if account == nil || bytes == 0 {
		return true
	}
	return account.TryReserveReplayProcessing(bytes)
}

func releaseStandardReplayProcessing(account *shamap.AcquisitionBudget, bytes uint64) {
	if account == nil || bytes == 0 {
		return
	}
	account.ReleaseReplayProcessing(bytes)
}

func (c *catchupReplayCoordinator) reserveStandardReplayEntryMemory(head bool) (standardReplayMemoryReservation, bool) {
	account := c.standardReplayMemoryAccount()
	kind := standardReplayMemoryFuture
	if head {
		kind = standardReplayMemoryHead
	}
	reservation := standardReplayMemoryReservation{account: account, kind: kind}
	initialBytes := uint64(standardReplayEntryMetadataBytes) + uint64(standardReplayPreparedPayloadReserveBytes)
	if account == nil || reserveStandardReplayMemory(account, kind, initialBytes) {
		reservation.bytes = initialBytes
		reservation.payloadBytes = standardReplayPreparedPayloadReserveBytes
		return reservation, true
	}
	return standardReplayMemoryReservation{}, false
}

func (r *standardReplayMemoryReservation) add(account *shamap.AcquisitionBudget, bytes uint64) bool {
	if r == nil || bytes == 0 {
		return true
	}
	if r.account == nil {
		return true
	}
	if account != nil && account != r.account {
		return false
	}
	if bytes > ^uint64(0)-r.bytes {
		return false
	}
	if !reserveStandardReplayMemory(r.account, r.kind, bytes) {
		return false
	}
	r.bytes += bytes
	return true
}

func (r *standardReplayMemoryReservation) trimPayload(bytes uint64) {
	if r == nil || bytes >= r.payloadBytes {
		return
	}
	r.releaseBytes(r.payloadBytes - bytes)
	r.payloadBytes = bytes
}

func (r *standardReplayMemoryReservation) releaseBytes(bytes uint64) {
	if r == nil || bytes == 0 {
		return
	}
	if bytes > r.bytes {
		bytes = r.bytes
	}
	if r.account != nil {
		releaseStandardReplayMemory(r.account, r.kind, bytes)
	}
	r.bytes -= bytes
}

func (r *standardReplayMemoryReservation) releasePayload() {
	if r == nil {
		return
	}
	r.releaseBytes(r.payloadBytes)
	r.payloadBytes = 0
}

func (r *standardReplayMemoryReservation) release() {
	if r == nil {
		return
	}
	if r.account == nil || r.bytes == 0 {
		r.account = nil
		r.bytes = 0
		r.payloadBytes = 0
		return
	}
	releaseStandardReplayMemory(r.account, r.kind, r.bytes)
	r.account = nil
	r.bytes = 0
	r.payloadBytes = 0
}

func (c *catchupReplayCoordinator) promoteStandardReplayHeadLocked(entry *standardReplayEntry) bool {
	if entry == nil || entry.replayMemory.kind == standardReplayMemoryHead {
		return true
	}
	if entry.replayMemory.account == nil {
		entry.replayMemory.kind = standardReplayMemoryHead
		return true
	}
	if !entry.replayMemory.account.PromoteReplayHead(entry.replayMemory.bytes) {
		return false
	}
	entry.replayMemory.kind = standardReplayMemoryHead
	return true
}

func standardReplayTransactionMapBytes(txMap *shamap.SHAMap) uint64 {
	if txMap == nil {
		return 0
	}
	bytes, err := txMap.RetainedMemoryBytes()
	if err != nil || bytes == standardReplayUnknownMapBytes {
		// A map that cannot be walked must never become an uncharged retained
		// object. The caller treats this as a local admission failure and keeps
		// the peer out of the failure path.
		return standardReplayUnknownMapBytes
	}
	return bytes
}

func (c *catchupReplayCoordinator) installStandardReplayMemoryWake() {
	c.setAcquisitionMemoryWake(c.retryStandardReplayMemory)
}

func (c *catchupReplayCoordinator) retryStandardReplayMemory() {
	account := c.standardReplayMemoryAccount()
	c.acquisitionMu.Lock()
	if c.stoppedForShutdown() {
		c.acquisitionMu.Unlock()
		return
	}
	if c.standardReplay.active && c.standardReplay.memoryWait {
		if !probeStandardReplayMemory(account, c.standardReplay.memoryWaitKind, c.standardReplay.memoryWaitBytes) {
			c.acquisitionMu.Unlock()
			return
		}
		c.standardReplay.memoryWait = false
		c.standardReplay.memoryWaitKind = 0
		c.standardReplay.memoryWaitBytes = 0
	}
	if c.standardReplay.active && (c.standardReplay.applying || c.scheduleReadyStandardReplayDrainLocked()) {
		c.acquisitionMu.Unlock()
		return
	}
	if len(c.standardReplay.legacyMemoryWait) != 0 {
		candidate := c.standardReplay.legacyMemoryWait[0]
		if probeStandardReplayMemory(account, standardReplayMemoryWaitProcessing, candidate.required) {
			queue := c.standardReplay.legacyMemoryWait
			copy(queue, queue[1:])
			queue[len(queue)-1] = nil
			queue = queue[:len(queue)-1]
			if len(queue) == 0 {
				queue = nil
			}
			c.standardReplay.legacyMemoryWait = queue
			c.acquisitionMu.Unlock()
			c.retryStandardReplayLegacyMemory(candidate.ledger)
			return
		}
		c.acquisitionMu.Unlock()
		return
	}
	var retired []*inbound.Ledger
	if c.standardReplay.active && c.standardReplay.memoryFutureWait {
		retired = c.retryStandardReplayMemoryEntriesLocked()
	}
	ready := c.scheduleReadyStandardReplayDrainLocked()
	collectorRetry := c.standardReplay.active && c.standardReplay.memoryCollectorBlocked && !ready
	if head := c.standardReplay.entries[c.standardReplay.anchorSeq+1]; c.standardReplay.pivotReady && head != nil && head.replayMemoryPending {
		collectorRetry = false
	}
	collectorPeer := c.standardReplay.memoryCollectorPeerHint
	c.acquisitionMu.Unlock()
	for _, ledger := range retired {
		c.retireAcquisitionStore(c.lifecycleContext(), ledger)
	}
	if collectorRetry {
		c.refillStandardReplayCollector(collectorPeer)
	}
}

func (c *catchupReplayCoordinator) retainsStandardReplayLegacyLocked(il *inbound.Ledger) bool {
	for _, wait := range c.standardReplay.legacyMemoryWait {
		if wait.ledger == il {
			return true
		}
	}
	return false
}

func (c *catchupReplayCoordinator) retryStandardReplayLegacyMemory(il *inbound.Ledger) {
	if il == nil || c.stoppedForShutdown() {
		if il != nil {
			c.retireAcquisitionStore(c.lifecycleContext(), il)
		}
		return
	}
	h, _, txMap, err := il.Result()
	if err == nil && h != nil {
		c.completeStandardTransactionReplay(il, h, txMap, il.PeerID())
	}
	c.acquisitionMu.Lock()
	retained := c.retainsStandardReplayLegacyLocked(il)
	c.acquisitionMu.Unlock()
	if !retained {
		if err != nil && c.logger != nil {
			hash := il.Hash()
			c.logger.Warn("deferred standard transaction replay result is no longer available",
				"seq", il.Seq(),
				"hash", fmt.Sprintf("%x", hash[:8]),
				"error", err,
			)
		}
		c.retireAcquisitionStore(c.lifecycleContext(), il)
	}
}

func probeStandardReplayMemory(account *shamap.AcquisitionBudget, kind standardReplayMemoryWaitKind, bytes uint64) bool {
	if account == nil || bytes == 0 {
		return true
	}
	var reserved bool
	switch kind {
	case standardReplayMemoryWaitHead:
		reserved = account.TryReserveReplayHead(bytes)
		if reserved {
			account.ReleaseReplayHead(bytes)
		}
	case standardReplayMemoryWaitProcessing:
		reserved = account.TryReserveReplayProcessing(bytes)
		if reserved {
			account.ReleaseReplayProcessing(bytes)
		}
	case standardReplayMemoryWaitFuture:
		reserved = account.TryReserveReplay(bytes)
		if reserved {
			account.ReleaseReplay(bytes)
		}
	}
	return reserved
}

func (c *catchupReplayCoordinator) markStandardReplayMemoryWaitLocked(kind standardReplayMemoryWaitKind, bytes uint64) {
	if kind == standardReplayMemoryWaitFuture {
		c.standardReplay.memoryFutureWait = true
		return
	}
	if c.standardReplay.memoryWait && c.standardReplay.memoryWaitKind == kind &&
		c.standardReplay.memoryWaitBytes >= bytes {
		return
	}
	c.standardReplay.memoryWait = true
	c.standardReplay.memoryWaitKind = kind
	c.standardReplay.memoryWaitBytes = bytes
}

func (c *catchupReplayCoordinator) releaseStandardReplayEntryMemoryLocked(entry *standardReplayEntry) {
	if entry == nil {
		return
	}
	if entry.replayMemoryReleasePending {
		return
	}
	entry.replayMemoryReleasePending = true
	entry.replayMemoryPending = false
	entry.replayPayloadBytes = 0
	if entry.replayMemoryActive != 0 {
		return
	}
	entry.replayMemory.release()
	entry.replayMemoryReleasePending = false
}

func (c *catchupReplayCoordinator) retainStandardReplayEntryMemoryLocked(entry *standardReplayEntry) bool {
	if entry == nil || entry.replayMemoryReleasePending {
		return false
	}
	entry.replayMemoryActive++
	return true
}

func (c *catchupReplayCoordinator) releaseStandardReplayEntryMemory(entry *standardReplayEntry) {
	if entry == nil {
		return
	}
	c.acquisitionMu.Lock()
	if entry.replayMemoryActive != 0 {
		entry.replayMemoryActive--
	}
	if entry.replayMemoryActive == 0 && entry.replayMemoryReleasePending {
		entry.replayMemory.release()
		entry.replayMemoryReleasePending = false
	}
	c.acquisitionMu.Unlock()
}

func (c *catchupReplayCoordinator) blockStandardReplayEntryForMemoryLocked(
	entry *standardReplayEntry,
	h *header.LedgerHeader,
	il *inbound.Ledger,
	txMap *shamap.SHAMap,
	peerID uint64,
	now time.Time,
) (bool, bool, bool) {
	if entry == nil || h == nil {
		return false, false, false
	}
	entry.header = *h
	entry.txMap = txMap
	entry.durable = false
	entry.peerID = peerID
	entry.acquisition = il
	entry.readyAt = time.Time{}
	entry.replayPayloadBytes = standardReplayUnknownMapBytes
	entry.replayMemoryPending = true
	entry.availabilityPending = false
	entry.availabilityRetrying = false
	entry.availabilityExhausted = false
	entry.availabilityRetries = 0
	entry.availabilityNextRetryAt = time.Time{}
	entry.availabilityDeadlineAt = time.Time{}
	entry.availabilityTriedPeers = nil
	if c.logger != nil {
		c.logger.Warn("standard transaction replay entry is waiting for a bounded local memory estimate",
			"seq", entry.seq,
			"hash", fmt.Sprintf("%x", entry.hash[:8]),
			"peer", peerID,
			"configured_bytes", entry.replayMemory.bytes,
		)
	}
	c.markStandardReplayMemoryWaitLocked(standardReplayMemoryWaitFuture, standardReplayPreparedPayloadReserveBytes)
	c.updateStandardReplayHeadBlockLocked(now)
	return true, false, true
}
