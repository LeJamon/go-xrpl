package adaptor

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
)

func (r *Router) reportAcquisitionProgress(ledger *inbound.Ledger, yielded bool) {
	snapshot, due := ledger.ProgressSnapshot(time.Now())
	if !due {
		return
	}
	r.acquisitionMu.Lock()
	identity := r.standardReplayIdentityLocked()
	occupancy := len(r.standardReplay.entries)
	r.acquisitionMu.Unlock()
	r.logger.Info("inbound ledger acquisition progress",
		"seq", snapshot.Seq, "hash", fmt.Sprintf("%x", snapshot.Hash[:8]),
		"phase", snapshot.Phase(), "yielded", yielded,
		"timeouts", snapshot.Timeouts,
		"state_nodes_descended", snapshot.StateNodesDescended,
		"state_durable_reads", snapshot.StateDurableReads,
		"state_nodes_loaded", snapshot.StateNodesLoaded,
		"state_equal_subtrees_skipped", snapshot.StateEqualSubtreesSkipped,
		"state_missing_discovered", snapshot.StateMissingDiscovered,
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
