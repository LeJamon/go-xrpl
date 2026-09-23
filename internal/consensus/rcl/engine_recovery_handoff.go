package rcl

import (
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// deferAcquiredChildLocked applies the same stay-on-parent principle as
// validationPreferredForLedgerLocked to explicit recovery completions. Merely
// having a quorum-validated replay result does not mean a healthy proposing
// round should be abandoned just before its acceptance heartbeat.
//
// This is a bounded handoff deferral, not a build/acquisition reservation:
// acquisition remains useful and actual accept-job ownership stays separate.
// Caller holds e.mu. Only immutable ledger identity and in-memory state are
// inspected here; never walk ancestry or perform storage I/O for this policy.
func (e *Engine) deferAcquiredChildLocked(candidate consensus.Ledger) bool {
	if candidate == nil || e.prevLedger == nil || e.state == nil ||
		e.mode != consensus.ModeProposing ||
		(e.phase != consensus.PhaseOpen && e.phase != consensus.PhaseEstablish) ||
		e.adaptor.GetOperatingMode() != consensus.OpModeFull ||
		!e.state.HaveCorrectLCL || e.roundStartTime.IsZero() {
		return false
	}
	if candidate.Seq() != e.state.Round.Seq ||
		candidate.ParentID() != e.prevLedger.ID() ||
		e.state.Round.ParentHash != e.prevLedger.ID() ||
		candidate.Seq() <= e.prevLedger.Seq() || candidate.Seq()-e.prevLedger.Seq() != 1 {
		return false
	}
	if frontier, ok := e.adaptor.(interface{ NetworkValidatedLedgerSeq() uint32 }); ok &&
		frontier.NetworkValidatedLedgerSeq() > candidate.Seq() {
		return false // Genuine growing lag must not be hidden by the grace period.
	}
	return e.now().Sub(e.roundStartTime) < e.recoveryHandoffGraceLocked()
}

// The deadline uses the engine's convergence clock, reset once on close, never
// by recovery retries or newly arriving proposals. Idle open time must not use
// up the minimum establish interval. Allow the previous convergence duration
// plus two heartbeat ticks for opening/scheduling, bounded by the existing
// consensus soft deadline.
func (e *Engine) recoveryHandoffGraceLocked() time.Duration {
	granularity := e.timing.LedgerGranularity
	if granularity <= 0 {
		granularity = time.Second
	}
	limit := e.timing.LedgerMaxConsensus
	if limit <= 0 {
		limit = consensus.DefaultTiming().LedgerMaxConsensus
	}
	return min(limit, max(e.prevRoundTime, e.timing.LedgerMinConsensus)+2*granularity)
}
