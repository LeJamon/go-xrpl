package adaptor

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
)

func (c *catchupReplayCoordinator) reportAcquisitionProgress(ledger *inbound.Ledger, yielded bool) {
	snapshot, due := ledger.ProgressSnapshot(time.Now())
	if !due {
		return
	}
	c.acquisitionMu.Lock()
	identity := c.standardReplayIdentityLocked()
	occupancy := len(c.standardReplay.entries)
	c.acquisitionMu.Unlock()
	c.acquisitionWorkMu.RLock()
	store := c.acquisitionStore
	c.acquisitionWorkMu.RUnlock()
	var persistenceBytes, persistencePeak uint64
	if store != nil {
		persistenceBytes = store.bytes.Load()
		persistencePeak = store.peakBytes.Load()
	}
	purpose := "consensus"
	switch ledger.Reason() {
	case inbound.ReasonHistory:
		purpose = "history"
	case inbound.ReasonRecovery:
		purpose = "replay_recovery"
	case inbound.ReasonGeneric:
		purpose = "rpc"
	}
	c.logger.Info("inbound ledger acquisition progress",
		"purpose", purpose,
		"seq", snapshot.Seq, "hash", fmt.Sprintf("%x", snapshot.Hash[:8]),
		"phase", snapshot.Phase(), "yielded", yielded,
		"timeouts", snapshot.Timeouts,
		"state_nodes_descended", snapshot.StateNodesDescended,
		"state_durable_reads", snapshot.StateDurableReads,
		"state_nodes_loaded", snapshot.StateNodesLoaded,
		"state_equal_subtrees_skipped", snapshot.StateEqualSubtreesSkipped,
		"state_missing_discovered", snapshot.StateMissingDiscovered,
		"acquisition_memory_limit", snapshot.AcquisitionMemory.Limit,
		"acquisition_cursor_limit", snapshot.AcquisitionMemory.Limit/4,
		"acquisition_reply_reserve", snapshot.AcquisitionMemory.Limit/8*3,
		"acquisition_memory_used", snapshot.AcquisitionMemory.Used,
		"acquisition_memory_peak", snapshot.AcquisitionMemory.Peak,
		"acquisition_cursor_bytes", snapshot.AcquisitionMemory.CursorUsed,
		"acquisition_cursor_peak_bytes", snapshot.AcquisitionMemory.CursorPeak,
		"state_cursor_blocked", snapshot.StateWalk.BlockedItems,
		"state_cursor_frames", snapshot.StateWalk.ActiveFrames,
		"state_cursor_proofs", snapshot.StateWalk.ProofEntries,
		"state_loaded_bytes_charged", snapshot.StateWalk.RetainedBytes,
		"state_loaded_peak_bytes_charged", snapshot.StateWalk.RetainedPeakBytes,
		"tx_cursor_blocked", snapshot.TxWalk.BlockedItems,
		"tx_cursor_frames", snapshot.TxWalk.ActiveFrames,
		"tx_cursor_proofs", snapshot.TxWalk.ProofEntries,
		"tx_loaded_bytes_charged", snapshot.TxWalk.RetainedBytes,
		"tx_loaded_peak_bytes_charged", snapshot.TxWalk.RetainedPeakBytes,
		"acquisition_memory_wait", snapshot.MemoryWaiting,
		"acquisition_memory_checkpoints", snapshot.MemoryCheckpoints,
		"acquisition_persistence_bytes", persistenceBytes,
		"acquisition_persistence_peak_bytes", persistencePeak,
		"tx_nodes_descended", snapshot.TxNodesDescended,
		"tx_durable_reads", snapshot.TxDurableReads,
		"tx_nodes_loaded", snapshot.TxNodesLoaded,
		"needed_state", len(snapshot.NeededState), "needed_tx", len(snapshot.NeededTx),
		"peers", snapshot.Peers, "request_peers", snapshot.RequestPeers,
		"state_received", snapshot.StateReceived, "state_useful", snapshot.StateUseful,
		"tx_received", snapshot.TxReceived, "tx_useful", snapshot.TxUseful,
		"pivot_seq", identity.pivotSeq, "pivot_ready", identity.pivotReady,
		"prepared_occupancy", occupancy, "prepared_limit", standardReplayPreparedLimit,
	)
}
