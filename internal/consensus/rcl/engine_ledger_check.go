package rcl

import (
	"bytes"
	"fmt"
	"log/slog"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// run is the main consensus loop on a single global heartbeat. It also
// detects ticks time.Ticker silently coalesced (gap > 2× interval) and
// logs them — observational only; the next tick still runs.
func (e *Engine) run() {
	defer e.wg.Done()

	// Heartbeat cadence = ledgerGRANULARITY (1s), floored by LedgerMinClose
	// so sub-granularity test configs keep up.
	interval := e.timing.LedgerGranularity
	if interval <= 0 {
		interval = time.Second
	}
	if e.timing.LedgerMinClose > 0 && e.timing.LedgerMinClose < interval {
		interval = e.timing.LedgerMinClose
	}
	e.heartbeat = time.NewTicker(interval)
	defer e.heartbeat.Stop()

	last := time.Now()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-e.heartbeat.C:
			if ping := e.stallPing.Load(); ping != nil {
				(*ping)()
			}
			now := time.Now()
			if gap := now.Sub(last); gap > 2*interval {
				missed := int64(gap/interval) - 1
				if missed > 0 {
					e.missedHeartbeats.Add(uint64(missed))
					slog.Warn("heartbeat ticks missed",
						"t", "consensus",
						"event", "tick-missed",
						"missed", missed,
						"gap_ms", gap.Milliseconds(),
						"interval_ms", interval.Milliseconds(),
						"total_missed", e.missedHeartbeats.Load(),
					)
				}
			}
			last = now
			e.timerEntry()
		}
	}
}

// timerEntry is the single heartbeat dispatch; runs each
// ledgerGRANULARITY and dispatches on current phase.
func (e *Engine) timerEntry() {
	tickStart := time.Now()
	e.mu.Lock()
	e.deferBroadcasts++
	var pending []func()
	defer func() {
		e.deferBroadcasts--
		pending = e.takePendingBroadcastsLocked()
		e.mu.Unlock()
		flushBroadcasts(pending)
		// 50ms threshold — the 250ms heartbeat needs headroom.
		dur := time.Since(tickStart)
		if dur > 50*time.Millisecond {
			slog.Info("timer tick slow",
				"t", "consensus",
				"event", "tick-slow",
				"dur_ms", dur.Milliseconds(),
				"phase", e.phase.String(),
				"mode", e.mode.String(),
			)
		}
	}()

	// Phase work runs in every non-disconnected mode; the proposing gate is
	// per-round (closeLedger/sendValidation gate on ModeProposing). Without
	// observer-mode advancement a genesis bootstrap deadlocks at
	// OpModeConnected — no round closes, so auto-promote never fires.
	if e.adaptor.GetOperatingMode() == consensus.OpModeDisconnected {
		return
	}

	// A blocked node can no longer participate safely: latch the operating
	// mode down so it stops claiming to be synced.
	if e.adaptor.GetOperatingMode() > consensus.OpModeConnected &&
		(e.adaptor.IsAmendmentBlocked() || e.adaptor.IsUNLBlocked()) {
		e.adaptor.SetOperatingMode(consensus.OpModeConnected)
	}
	if e.mode == consensus.ModeProposing &&
		e.adaptor.GetOperatingMode() != consensus.OpModeFull {
		e.leaveConsensusLocked()
	}

	// A peer-triggered accept may be applying the LCL off e.mu on another
	// goroutine; don't drive rounds until its commit tail runs (rippled parks
	// the timer thread while the jtACCEPT job holds no lock).
	if e.buildInProgress {
		return
	}

	// Sweep validations that aged past the isCurrent window off the steering
	// indexes each tick (rippled doSweep → current()); a silent validator
	// must not keep steering preferred-ledger selection through a stall.
	if e.validationTracker != nil {
		e.validationTracker.FlushStale()
	}

	// checkLedger runs in every non-disconnected mode — the Syncing/Tracking
	// → Full recovery path; gating on Full would wedge us after a wrongLedger
	// demotion.
	if e.phase != consensus.PhaseAccepted {
		e.checkLedger()
	}

	switch e.phase {
	case consensus.PhaseOpen:
		e.phaseOpen()
	case consensus.PhaseEstablish:
		e.phaseEstablish()
	case consensus.PhaseAccepted:
		e.checkAndStartRoundInner()
		// Evaluate the new phase in the same tick after starting a round.
		if e.phase == consensus.PhaseOpen {
			e.phaseOpen()
		}
	}
}

// checkAndStartRoundInner is the fallback round-start when acceptLedger's
// auto-advance didn't fire (e.g. first round). Caller must hold e.mu.
func (e *Engine) checkAndStartRoundInner() {
	if e.phase != consensus.PhaseAccepted {
		return
	}
	if e.mode == consensus.ModeWrongLedger {
		return
	}

	ledger, err := e.adaptor.GetLastClosedLedger()
	if err != nil {
		return
	}

	// Buffered proposals → start immediately (peer pressure closes open
	// phase); otherwise wait for the idle interval.
	ledgerID := ledger.ID()
	hasBufferedProposals := e.proposalTracker.HasBufferedFor(ledgerID)

	if !hasBufferedProposals {
		timeSinceClose := e.adaptor.Now().Sub(ledger.CloseTime())
		if timeSinceClose < e.timing.LedgerIdleInterval {
			return
		}
	}

	proposing := e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull

	// Refresh prevLedger — an InboundLedger adoption may have changed the LCL.
	e.prevLedger = ledger

	// Normal idle-timeout round start (not recovery).
	round := consensus.RoundID{
		Seq:        ledger.Seq() + 1,
		ParentHash: ledger.ID(),
	}
	e.startRoundLocked(round, proposing, false)
}

// checkLedger compares prevLedger against the network-preferred ledger
// and calls handleWrongLedger on a mismatch.
func (e *Engine) checkLedger() {
	if e.prevLedger == nil {
		return
	}
	ourID := e.prevLedger.ID()
	// A recovery switch changes consensus' previous ledger before the
	// adaptor accepts a replacement LCL. Do not interpret that expected gap
	// as an external ledger change and undo the switch.
	if e.mode != consensus.ModeSwitchedLedger {
		if closed, err := e.adaptor.GetLastClosedLedger(); err == nil && closed != nil && closed.ID() != ourID {
			if e.acceptedLCL == (consensus.LedgerID{}) || closed.ID() != e.acceptedLCL {
				closedID := closed.ID()
				slog.Warn("Closed ledger changed during consensus",
					"t", "consensus",
					"event", "closed-ledger-changed",
					"our", fmt.Sprintf("%x", ourID[:8]),
					"closed", fmt.Sprintf("%x", closedID[:8]),
				)
				e.demoteForLedgerChange()
				e.handleWrongLedger(closedID, closed)
				return
			}
		}
	}
	if e.mode == consensus.ModeWrongLedger {
		validatedID := e.adaptor.GetValidatedLedgerHash()
		if validatedID != (consensus.LedgerID{}) && validatedID != ourID {
			if validated := e.resolveTargetLedger(validatedID); validated != nil &&
				e.canSwitchToLedgerLocked(validated) {
				slog.Info("Switching to held validated recovery ledger",
					"t", "consensus",
					"event", "validated-recovery-lcl",
					"seq", validated.Seq(),
					"hash", fmt.Sprintf("%x", validatedID[:8]),
				)
				e.demoteForLedgerChange()
				e.handleWrongLedger(validatedID, validated)
				return
			}
		}
	}
	netLgr := e.getNetworkLedger()
	if netLgr != ourID {
		// Network is on our parent: we're ahead, not wrong — wait, don't
		// switch back.
		if netLgr == e.prevLedger.ParentID() {
			return
		}

		// Already targeting this hash: re-resolve once in case it became
		// locally available (held adoption that didn't fire OnLedger) and
		// complete the switch. Still missing, retry the adaptor request; its
		// acquisition deadline suppresses duplicates until the retry window opens.
		var target consensus.Ledger
		if e.mode == consensus.ModeWrongLedger && e.wrongLedgerID == netLgr {
			if target = e.resolveTargetLedger(netLgr); target == nil {
				e.adaptor.RequestLedger(netLgr)
				return
			}
		}
		slog.Warn("Consensus view changed",
			"phase", e.phase,
			"mode", e.mode,
			"our", fmt.Sprintf("%x", ourID[:8]),
			"net", fmt.Sprintf("%x", netLgr[:8]),
		)
		e.demoteForLedgerChange()
		e.handleWrongLedger(netLgr, target)
	}
}

func (e *Engine) demoteForLedgerChange() {
	mode := e.adaptor.GetOperatingMode()
	if mode == consensus.OpModeFull || mode == consensus.OpModeTracking {
		e.adaptor.SetOperatingMode(consensus.OpModeConnected)
	}
}

// getNetworkLedger returns the network-preferred prevLedger. Trusted
// validations decide first. Without a validation preference, every connected
// peer's reported LCL is counted independently, with the larger ledger ID
// breaking equal-count ties.
func (e *Engine) getNetworkLedger() consensus.LedgerID {
	if e.prevLedger == nil {
		return consensus.LedgerID{}
	}
	ourID := e.prevLedger.ID()
	if e.prevLedger.Seq() == 0 {
		return ourID
	}

	if id, ok := e.validationPreferredLocked(); ok {
		return id
	}

	counts := map[consensus.LedgerID]uint32{ourID: 0}
	if e.adaptor.GetOperatingMode() >= consensus.OpModeTracking {
		counts[ourID]++
	}
	for _, id := range e.adaptor.PeerReportedLedgers() {
		if id != (consensus.LedgerID{}) {
			counts[id]++
		}
	}

	bestID := ourID
	for id, count := range counts {
		if count > counts[bestID] ||
			(count == counts[bestID] && bytes.Compare(id[:], bestID[:]) > 0) {
			bestID = id
		}
	}
	return bestID
}

// validationPreferredLocked derives the network-preferred prevLedger from
// trusted validations, mirroring rippled getPreferred (Validations.h:849-917):
// trie tip then the stay/switch rules, gated so the result never rewinds
// behind the validated index. ok=false when no trusted validation signal
// exists. Caller holds e.mu.
func (e *Engine) validationPreferredLocked() (consensus.LedgerID, bool) {
	return e.validationPreferredForLedgerLocked(e.prevLedger)
}

func (e *Engine) validationPreferredForLedgerLocked(ledger consensus.Ledger) (consensus.LedgerID, bool) {
	if ledger == nil || e.validationTracker == nil {
		return consensus.LedgerID{}, false
	}
	minSeq := e.validatedSeqLocked()
	id, seq, ok := e.validationTracker.GetPreferred(e.ourLastValidatedSeq)
	if !ok {
		id, seq, ok = e.validationTracker.PreferredFromValidations(minSeq)
	}
	if !ok {
		return consensus.LedgerID{}, false
	}

	ourID := ledger.ID()
	ourSeq := ledger.Seq()
	if id == ourID {
		return ourID, true
	}
	if seq == ourSeq+1 {
		if l, err := e.adaptor.GetLedger(id); err == nil && l != nil && l.ParentID() == ourID {
			return ourID, true
		}
	}
	if seq < minSeq {
		return ourID, true
	}
	if seq > ourSeq {
		return id, true
	}
	if e.ancestorFromLedgerLocked(ledger, seq) != id {
		return id, true
	}
	return ourID, true
}

// isCurrentPreferredLCLLocked reports whether ledger remains the correct LCL
// under the trusted-validation preference rules. No validation signal means
// there is nothing to switch to. A directly preferred child also keeps its
// parent current, matching the stay-on-parent rule used by getNetworkLedger.
func (e *Engine) isCurrentPreferredLCLLocked(ledger consensus.Ledger) bool {
	if ledger == nil {
		return true
	}
	if e.validatedSeqLocked() > ledger.Seq() {
		return false
	}
	id, ok := e.validationPreferredForLedgerLocked(ledger)
	return !ok || id == ledger.ID()
}

// ancestorFromLedgerLocked resolves ledger's chain ID at targetSeq by walking
// locally-held parents. Caller holds e.mu.
func (e *Engine) ancestorFromLedgerLocked(ledger consensus.Ledger, targetSeq uint32) consensus.LedgerID {
	if ledger == nil {
		return consensus.LedgerID{}
	}
	const maxWalk = 256 // rippled's skip-list reach
	seq := ledger.Seq()
	if targetSeq > seq || seq-targetSeq > maxWalk {
		return consensus.LedgerID{}
	}
	if targetSeq == seq {
		return ledger.ID()
	}
	cur := ledger.ParentID()
	for s := seq - 1; s > targetSeq; s-- {
		l, err := e.adaptor.GetLedger(cur)
		if err != nil || l == nil {
			return consensus.LedgerID{}
		}
		cur = l.ParentID()
	}
	return cur
}

// resolveTargetLedger returns the locally-held ledger for id (by-hash
// store, then the just-adopted LCL), or nil if not held yet.
func (e *Engine) resolveTargetLedger(id consensus.LedgerID) consensus.Ledger {
	if l, err := e.adaptor.GetLedger(id); err == nil && l != nil {
		return l
	}
	if lcl, err := e.adaptor.GetLastClosedLedger(); err == nil && lcl != nil && lcl.ID() == id {
		return lcl
	}
	return nil
}

// handleWrongLedger switches to the network's preferred ledger. target is
// an already-resolved ledger (nil to resolve here).
func (e *Engine) handleWrongLedger(netLedgerID consensus.LedgerID, target consensus.Ledger) {
	// Resolve and verify BEFORE mutating any round state, so a refused
	// switch leaves the in-progress round untouched (rippled verifies with
	// canBeCurrent/isCompatible before switching, NetworkOPs.cpp:1948-1962).
	// An unresolvable target is verified later, at adoption (OnLedger).
	newLedger := target
	if newLedger == nil {
		newLedger = e.resolveTargetLedger(netLedgerID)
	}
	if newLedger != nil && !e.canSwitchToLedgerLocked(newLedger) {
		// Off the validated chain or implausibly timed/sequenced — refuse the
		// switch and stay on our ledger.
		if e.lastRefusedSwitch != netLedgerID {
			e.lastRefusedSwitch = netLedgerID
			slog.Info("Refusing switch to incompatible network ledger",
				"t", "consensus",
				"event", "switch-refused",
				"seq", newLedger.Seq(),
				"hash", fmt.Sprintf("%x", netLedgerID[:8]),
			)
		}
		return
	}

	// Stop proposing.
	if e.mode == consensus.ModeProposing {
		e.setMode(consensus.ModeObserving)
	}

	// Clear consensus state and replay (only for a new target ledger).
	if e.prevLedger == nil || netLedgerID != e.prevLedger.ID() {
		e.proposalTracker.ResetProposals()
		e.disputeTracker = newDisputeTracker()
		e.acquiredTxSets = make(map[consensus.TxSetID]consensus.TxSet)
		e.comparesTxSets = make(map[consensus.TxSetID]struct{})
		e.peerUnchangedCounter = 0
		e.establishCounter = 0
		e.closeTime.haveConsensus = false
		if e.state != nil {
			e.state.CloseTimes.Peers = make(map[time.Time]int)
		}

		// Replay proposals for the new ledger; close-time votes only if a
		// round state exists.
		closeTimes, _, relay := e.proposalTracker.Replay(netLedgerID, e.adaptor.IsTrusted)
		if e.state != nil {
			for _, ct := range closeTimes {
				e.state.CloseTimes.Peers[ct]++
			}
		}

		for _, p := range relay {
			e.adaptor.RelayProposal(p, 0)
		}
	}

	if newLedger != nil {
		// Found — restart with recovering=true so we enter switchedLedger for
		// one round (suppress our proposal/validation to avoid poisoning
		// convergence with a stale view); the next round promotes back normally.
		slog.Info("Switching to network ledger",
			"t", "consensus",
			"event", "switch-lcl",
			"seq", newLedger.Seq(),
			"hash", fmt.Sprintf("%x", netLedgerID[:8]),
		)
		e.prevLedger = newLedger
		e.wrongLedgerID = consensus.LedgerID{}
		if e.state != nil {
			e.state.HaveCorrectLCL = true
		}
		nextRound := consensus.RoundID{
			Seq:        newLedger.Seq() + 1,
			ParentHash: newLedger.ID(),
		}
		proposing := e.adaptor.IsValidator() &&
			e.adaptor.GetOperatingMode() == consensus.OpModeFull
		e.startRoundLocked(nextRound, proposing, true)
	} else {
		// Not found — request from peers and remain pinned until the preferred
		// ledger is acquired or a topology change invalidates the request.
		e.adaptor.RequestLedger(netLedgerID)
		slog.Info("Cannot acquire network ledger, entering wrongLedger mode",
			"t", "consensus",
			"event", "wrong-lcl",
			"hash", fmt.Sprintf("%x", netLedgerID[:8]),
		)
		if e.state != nil {
			e.state.HaveCorrectLCL = false
		}
		e.wrongLedgerID = netLedgerID
		e.setMode(consensus.ModeWrongLedger)
	}
}

// OnLedgerAcquireFailed reports that an acquisition was invalidated by a
// topology change. If pinned in wrongLedger on id, un-pin so the router can
// re-resolve and retry without resuming consensus on the stale LCL.
func (e *Engine) OnLedgerAcquireFailed(id consensus.LedgerID) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.mode != consensus.ModeWrongLedger || e.wrongLedgerID != id {
		return
	}

	e.wrongLedgerID = consensus.LedgerID{}
	slog.Warn("wrongLedger acquisition failed; will re-resolve and retry",
		"t", "consensus",
		"event", "wrong-lcl-retry",
		"hash", fmt.Sprintf("%x", id[:8]),
	)
}
