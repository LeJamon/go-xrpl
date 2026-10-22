package node

import "github.com/LeJamon/go-xrpl/internal/observability"

func (r *nodeRuntime) transactionIngressMetrics() observability.TransactionIngressMetricsSnapshot {
	var result observability.TransactionIngressMetricsSnapshot
	if r.ledger != nil {
		s := r.ledger.OpenLedgerIngressMetrics()
		result.PrivateAttempts = s.PrivateAttempts
		result.PrivateAttemptDuration = s.PrivateAttemptDuration
		result.StaleRetries = s.StaleRetries
		result.StaleRetryDuration = s.StaleRetryDuration
		result.Reparses = s.Reparses
		result.ReparseDuration = s.ReparseDuration
	}
	if r.consensus != nil && r.consensus.Router != nil {
		s := r.consensus.Router.TransactionIngressMetrics()
		result.Frames = s.Frames
		result.ParseWork = s.ParseWork
		result.ParseErrors = s.ParseErrors
		result.InputHits = s.InputHits
		result.InputMisses = s.InputMisses
		result.InputBypass = s.InputBypass
		result.Deduped = s.Deduped
		result.ParseWorkUs = s.ParseWorkUs
		result.QueueLatencyUs = s.QueueLatencyUs
		result.QueueLatencySamples = s.QueueLatencySamples
		result.MemoEvictions = s.MemoEvictions
		result.MemoEntries = s.MemoEntries
		result.MemoPayloadBytes = s.MemoPayloadBytes
	}
	return result
}
