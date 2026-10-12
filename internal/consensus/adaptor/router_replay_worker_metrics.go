package adaptor

import "fmt"

func (c *catchupReplayCoordinator) logStandardReplayDrainResult(result *standardReplayDrainResult) {
	if c.logger == nil {
		return
	}
	parent, child := result.parentStats, result.ledgerStats
	state, transactions := child.State, child.Transactions
	c.logger.Info("applied standard transaction replay pipeline entry",
		"seq", result.entry.seq,
		"hash", fmt.Sprintf("%x", result.entry.hash[:8]),
		"acquire_us", durationMicros(result.entry.readyAt.Sub(result.entry.requestedAt)),
		"ready_wait_us", durationMicros(result.startedAt.Sub(result.entry.readyAt)),
		"prepare_us", durationMicros(result.preparationDuration),
		"apply_us", durationMicros(result.applyDuration),
		"admission_us", durationMicros(parent.Elapsed+child.Elapsed),
		"snapshot_flush_us", durationMicros(parent.SnapshotElapsed+child.SnapshotElapsed),
		"parent_verify_us", durationMicros(parent.State.Elapsed+parent.Transactions.Elapsed),
		"state_verify_us", durationMicros(state.Elapsed),
		"tx_verify_us", durationMicros(transactions.Elapsed),
		"commit_lock_wait_us", durationMicros(result.lockWaitDuration),
		"persist_us", durationMicros(result.persistDuration),
		"result_queue_us", durationMicros(result.queueDuration),
		"parent_durable_reads", parent.State.DurableReads+parent.Transactions.DurableReads,
		"state_durable_reads", state.DurableReads,
		"tx_durable_reads", transactions.DurableReads,
		"state_nodes_loaded", state.NodesLoaded,
		"tx_nodes_loaded", transactions.NodesLoaded,
		"state_equal_subtrees_skipped", state.EqualSubtreesSkipped,
		"state_verify_workers", state.Workers,
		"state_verify_workspace_bytes", state.WorkspaceBytes,
		"parent_proof_cache_hit", parent.ProofCacheHit,
		"parent_proof_cache_miss", parent.ProofCacheMiss,
		"parent_proof_used", child.ParentProofUsed,
		"generation_changed", parent.GenerationChanged || child.GenerationChanged,
	)
}
