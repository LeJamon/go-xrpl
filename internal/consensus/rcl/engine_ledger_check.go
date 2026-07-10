package rcl

import (
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

// MissedHeartbeats returns the count of dropped heartbeat ticks since
// start.
func (e *Engine) MissedHeartbeats() uint64 {
	return e.missedHeartbeats.Load()
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

	// An amendment-blocked node can no longer build correct ledgers: latch
	// the operating mode down so it stops claiming to be synced.
	if e.adaptor.GetOperatingMode() > consensus.OpModeConnected &&
		e.adaptor.IsAmendmentBlocked() {
		e.adaptor.SetOperatingMode(consensus.OpModeConnected)
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

	// Runs every tick regardless of phase: a WrongLedger pin taken at
	// PhaseAccepted advances no rounds, so the checkLedger path below never runs.
	e.checkStuckWrongLedger()

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

// checkStuckWrongLedger drops to a degraded resync once pinned in
// ModeWrongLedger past wrongLedgerStuckTimeout, backing the clean-failure hatch
// which can't arm under a livelock or moving target. Caller must hold e.mu.
func (e *Engine) checkStuckWrongLedger() {
	if e.mode == consensus.ModeWrongLedger && !e.wrongLedgerSince.IsZero() &&
		e.adaptor.Now().Sub(e.wrongLedgerSince) > wrongLedgerStuckTimeout {
		e.dropToDegradedResync("stuck-timeout")
	}
}

// checkLedger compares prevLedger against the network-preferred ledger
// and calls handleWrongLedger on a mismatch.
func (e *Engine) checkLedger() {
	if e.prevLedger == nil {
		return
	}
	ourID := e.prevLedger.ID()
	netLgr := e.getNetworkLedger()
	if netLgr != ourID {
		// Network is on our parent: we're ahead, not wrong — wait, don't
		// switch back.
		if netLgr == e.prevLedger.ParentID() {
			return
		}

		// Already targeting this hash: re-resolve once in case it became
		// locally available (held adoption that didn't fire OnLedger) and
		// complete the switch; otherwise we'd spin in wrongLedger forever
		// (#724). Still missing → don't spam the acquire.
		var target consensus.Ledger
		if e.mode == consensus.ModeWrongLedger && e.wrongLedgerID == netLgr {
			if target = e.resolveTargetLedger(netLgr); target == nil {
				return
			}
		}
		slog.Warn("Consensus view changed",
			"phase", e.phase,
			"mode", e.mode,
			"our", fmt.Sprintf("%x", ourID[:8]),
			"net", fmt.Sprintf("%x", netLgr[:8]),
		)
		e.handleWrongLedger(netLgr, target)
	}
}

// getNetworkLedger returns the network-preferred prevLedger. Trusted
// validations decide first, like rippled's getPrevLedger (pure
// vals.getPreferred, RCLConsensus.cpp:301-303) — only validations break a
// proposal-count tie between two self-agreeing islands. The proposal+peer-LCL
// majority is the fallback for validation-less phases (boot).
func (e *Engine) getNetworkLedger() consensus.LedgerID {
	if e.prevLedger == nil {
		return consensus.LedgerID{}
	}
	ourID := e.prevLedger.ID()

	if id, ok := e.validationPreferredLocked(); ok {
		return id
	}

	freshness := e.timing.ProposeFreshness
	now := e.adaptor.Now()

	// For each trusted node, take the most recent fresh proposal.
	type vote struct {
		prevLedger consensus.LedgerID
	}
	votes := make(map[consensus.NodeID]vote)
	for nodeID, p := range e.proposalTracker.LatestFresh(e.adaptor.IsTrusted, now, freshness) {
		votes[nodeID] = vote{prevLedger: p.PreviousLedger}
	}

	// Include our own position as a vote: otherwise the >len/2 majority is
	// computed over peers only, so two disagreeing peers flip our LCL where a
	// fair vote (with us) would tie.
	if e.state != nil && e.state.OurPosition != nil {
		pos := e.state.OurPosition
		if now.Sub(pos.Timestamp) <= freshness {
			if key, err := e.adaptor.GetValidatorKey(); err == nil {
				votes[key] = vote{prevLedger: pos.PreviousLedger}
			}
		}
	}

	// Hashes already voted via trusted proposals. Skip peer-LCL votes for
	// these so a validator that's also a peer isn't double-counted.
	proposalHashes := make(map[consensus.LedgerID]struct{}, len(votes))
	for _, v := range votes {
		proposalHashes[v.prevLedger] = struct{}{}
	}

	// Fold in peer-reported LCLs from statusChange (a peer that advanced its
	// LCL but hasn't gossiped a proposal yet). Keyed on a synthetic NodeID so
	// one peer counts once; deduped against trusted-proposer votes. Votes are
	// counted ungated, like rippled's checkLastClosedLedger peer tally
	// (NetworkOPs.cpp:1915-1921); safety against adopting a bogus gossiped
	// hash lives in the acquire-then-verify checks at the switch site
	// (canSwitchToLedgerLocked), not in vote suppression.
	for i, h := range e.adaptor.PeerReportedLedgers() {
		if _, already := proposalHashes[h]; already {
			continue
		}
		var synthKey consensus.NodeID
		// 0xFF is unused by XRPL pubkey encoding, so synthetic keys can't
		// collide with a real validator key.
		synthKey[0] = 0xFF
		synthKey[1] = byte(i >> 8)
		synthKey[2] = byte(i)
		// Fill the rest with the ledger hash so different reported LCLs from
		// the same ordinal slot stay distinguishable.
		copy(synthKey[3:], h[:30])
		votes[synthKey] = vote{prevLedger: h}
	}

	if len(votes) == 0 {
		return ourID
	}

	counts := make(map[consensus.LedgerID]int)
	for _, v := range votes {
		counts[v.prevLedger]++
	}

	var bestID consensus.LedgerID
	bestCount := 0
	for id, count := range counts {
		if count > bestCount {
			bestID = id
			bestCount = count
		}
	}

	if bestID != ourID && bestCount > len(votes)/2 {
		return bestID
	}
	return ourID
}

// validationPreferredLocked derives the network-preferred prevLedger from
// trusted validations, mirroring rippled getPreferred (Validations.h:849-917):
// trie tip then the stay/switch rules, gated so the result never rewinds
// behind the validated index. ok=false when no trusted validation signal
// exists. Caller holds e.mu.
func (e *Engine) validationPreferredLocked() (consensus.LedgerID, bool) {
	if e.validationTracker == nil {
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

	ourID := e.prevLedger.ID()
	ourSeq := e.prevLedger.Seq()
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
	if e.ancestorAtLocked(seq) != id {
		return id, true
	}
	return ourID, true
}

// ancestorAtLocked resolves our chain's ledger ID at targetSeq by walking
// locally-held parents from prevLedger; the zero ID when unresolvable —
// treated as a different chain, like rippled's out-of-skip-list ID{0}
// (RCLValidations.cpp:78-95). Caller holds e.mu.
func (e *Engine) ancestorAtLocked(targetSeq uint32) consensus.LedgerID {
	const maxWalk = 256 // rippled's skip-list reach
	seq := e.prevLedger.Seq()
	if targetSeq > seq || seq-targetSeq > maxWalk {
		return consensus.LedgerID{}
	}
	if targetSeq == seq {
		return e.prevLedger.ID()
	}
	cur := e.prevLedger.ParentID()
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
		e.disputeTracker = NewDisputeTracker()
		e.acquiredTxSets = make(map[consensus.TxSetID]consensus.TxSet)
		e.comparesTxSets = make(map[consensus.TxSetID]struct{})
		e.peerUnchangedCounter = 0
		e.establishCounter = 0
		e.converged = false
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
		e.wrongLedgerAcquireFailures = 0
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
		// Not found — request from peers. Inside the degraded-resync cooldown,
		// stay advancing rather than re-pinning wrongLedger: a pinned node
		// closes no ledgers and makes no progress toward the network tip.
		e.adaptor.RequestLedger(netLedgerID)
		if e.adaptor.Now().Before(e.degradedResyncUntil) {
			slog.Info("Retrying network ledger in degraded resync",
				"t", "consensus",
				"event", "wrong-lcl-degraded-retry",
				"hash", fmt.Sprintf("%x", netLedgerID[:8]),
			)
			return
		}
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

// wrongLedgerAcquireMaxFailures bounds clean acquisition failures before
// dropping to a degraded resync; degradedResyncCooldown is how long it
// then stays unpinned and advancing.
const (
	wrongLedgerAcquireMaxFailures = 3
	degradedResyncCooldown        = 20 * time.Second

	// wrongLedgerStuckTimeout bounds continuous time pinned in ModeWrongLedger.
	// The clean-failure hatch can fail to arm — a livelocked acquisition never
	// times out, and a target moving as the network advances leaves each clean
	// failure on a stale id the hatch ignores — so without this bound the node
	// wedges forever. Set above the clean-failure budget so it only backstops
	// a genuinely stuck node.
	wrongLedgerStuckTimeout = 60 * time.Second
)

// OnLedgerAcquireFailed reports a clean acquisition failure for id. If
// pinned in wrongLedger on id it must not stay frozen (a frozen
// wrongLedger closes no ledgers and never rejoins): each failure un-pins
// so checkLedger re-resolves; at the limit it drops to a degraded resync
// so closes resume while recovery continues.
func (e *Engine) OnLedgerAcquireFailed(id consensus.LedgerID) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.mode != consensus.ModeWrongLedger || e.wrongLedgerID != id {
		return
	}

	e.wrongLedgerAcquireFailures++
	// Un-pin so the next checkLedger re-resolves and re-requests.
	e.wrongLedgerID = consensus.LedgerID{}

	if e.wrongLedgerAcquireFailures < wrongLedgerAcquireMaxFailures {
		slog.Warn("wrongLedger acquisition failed; will re-attempt",
			"t", "consensus",
			"event", "wrong-lcl-retry",
			"hash", fmt.Sprintf("%x", id[:8]),
			"failures", e.wrongLedgerAcquireFailures,
		)
		return
	}

	// Persistent clean failure: validated ledger unacquirable.
	e.dropToDegradedResync("acquire-max-failures")
}

// dropToDegradedResync demotes a node that cannot acquire its wrongLedger
// target: ModeObserving keeps rounds advancing while checkLedger retries, so
// closes resume. Reached from both the clean-failure hatch (at its limit) and
// the stuck-acquisition backstop. Caller must hold e.mu.
func (e *Engine) dropToDegradedResync(reason string) {
	slog.Warn("wrongLedger ledger unacquirable; dropping to degraded resync",
		"t", "consensus",
		"event", "wrong-lcl-degraded",
		"reason", reason,
	)
	e.wrongLedgerAcquireFailures = 0
	// Un-pin so the next checkLedger re-resolves and re-requests.
	e.wrongLedgerID = consensus.LedgerID{}
	e.degradedResyncUntil = e.adaptor.Now().Add(degradedResyncCooldown)
	if e.state != nil {
		e.state.HaveCorrectLCL = false
	}
	e.setMode(consensus.ModeObserving)
	if e.adaptor.GetOperatingMode() == consensus.OpModeFull {
		e.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
}
