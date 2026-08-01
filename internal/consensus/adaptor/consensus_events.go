package adaptor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
)

func (a *Adaptor) GetOperatingMode() consensus.OperatingMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.operatingMode
}

func (a *Adaptor) SetOperatingMode(mode consensus.OperatingMode) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// A blocked node is never more than connected: it cannot safely
	// participate in consensus, so it must not claim to be synced.
	if mode > consensus.OpModeConnected && (a.IsAmendmentBlocked() || a.IsUNLBlocked()) {
		mode = consensus.OpModeConnected
	}
	if mode >= consensus.OpModeTracking && consensus.Mode(a.consensusMode.Load()) == consensus.ModeWrongLedger {
		if a.operatingMode < consensus.OpModeTracking {
			mode = a.operatingMode
		} else {
			mode = consensus.OpModeConnected
		}
	}
	a.operatingMode = mode
	if a.stateAcct != nil {
		// Held under a.mu so the field and the accounting transition share one
		// serialization order; the tracker's own mutex never re-enters a.mu.
		a.stateAcct.transition(mode)
	}
}

// StateAccounting returns the snapshot server_info uses for state_accounting
// and the server_state_duration_us / initial_sync_duration_us fields. Zero
// value when constructed without a tracker. stateAcct is set once in New(), so
// no Adaptor lock is needed (the tracker has its own mutex).
func (a *Adaptor) StateAccounting() StateAccountingSnapshot {
	if a.stateAcct == nil {
		return StateAccountingSnapshot{}
	}
	return a.stateAcct.snapshot()
}

// OnConsensusReached logs the close and fires the consensus-phase hook; the
// open-ledger view is already advanced by AcceptConsensusResult.
//
// Does NOT mark the ledger validated — that only happens at trusted-validation
// quorum (OnLedgerFullyValidated). Local consensus != network agreement.
func (a *Adaptor) OnConsensusReached(ledger consensus.Ledger, validations []*consensus.Validation, roundTime time.Duration) {
	a.logger.Info("Consensus reached",
		"ledger_seq", ledger.Seq(),
		"validations", len(validations),
		"round_time", roundTime,
	)

	if a.ledgerService != nil {
		// Feed round duration to the service so TxQ sees the timeLeap flag when
		// consensus crossed the 5s slow-consensus threshold.
		a.ledgerService.SetLastConsensusRoundTime(roundTime)

		a.emitConsensusPhase("accepted")
	}

	a.maybePromoteAfterConsensus(ledger)
	if a.onLedgerBuilt != nil {
		a.onLedgerBuilt(ledger.Seq(), [32]byte(ledger.ID()))
	}
}

// emitConsensusPhase delivers a consensus-phase notification through a single
// ordered dispatcher (started on first use). Enqueue is non-blocking: a slow
// hook drops the (advisory) notification rather than stalling consensus.
func (a *Adaptor) emitConsensusPhase(phase string) {
	if a.ledgerService == nil {
		return
	}
	a.consensusPhaseMu.Lock()
	if a.consensusPhaseStop {
		a.consensusPhaseMu.Unlock()
		return
	}
	if a.consensusPhaseCh == nil {
		a.consensusPhaseCh = make(chan string, 64)
		a.consensusPhaseQuit = make(chan struct{})
		a.consensusPhaseWG.Add(1)
		go a.runConsensusPhaseDispatcher()
	}
	ch := a.consensusPhaseCh
	a.consensusPhaseMu.Unlock()
	select {
	case ch <- phase:
	default:
		slog.Warn("consensus phase hook buffer full; dropping notification",
			"t", "adaptor.emitConsensusPhase", "phase", phase)
	}
}

// runConsensusPhaseDispatcher drains consensus-phase notifications in order
// until StopConsensusPhaseDispatcher signals quit. The notifications are
// advisory, so a shutdown abandons any still buffered rather than draining them.
func (a *Adaptor) runConsensusPhaseDispatcher() {
	defer a.consensusPhaseWG.Done()
	for {
		select {
		case p := <-a.consensusPhaseCh:
			if hooks := a.ledgerService.EventHooks(); hooks != nil && hooks.OnConsensusPhase != nil {
				hooks.OnConsensusPhase(p)
			}
		case <-a.consensusPhaseQuit:
			return
		}
	}
}

// StopConsensusPhaseDispatcher stops the consensus-phase dispatcher goroutine and
// joins it, so an in-process restart cycle doesn't leak one per cycle. Idempotent
// and safe if the dispatcher was never started.
func (a *Adaptor) StopConsensusPhaseDispatcher() {
	a.consensusPhaseMu.Lock()
	if a.consensusPhaseStop {
		a.consensusPhaseMu.Unlock()
		return
	}
	a.consensusPhaseStop = true
	quit := a.consensusPhaseQuit
	a.consensusPhaseMu.Unlock()
	if quit != nil {
		close(quit)
		a.consensusPhaseWG.Wait()
	}
}

// maybePromoteAfterConsensus auto-promotes the operating mode after a
// successful consensus close (a completed round is evidence we're aligned):
//
//	CONNECTED | SYNCING  → TRACKING
//	CONNECTED | TRACKING → FULL when the just-closed ledger is recent
//	                       (now < ledger.CloseTime() + 2 * resolution)
//
// Both branches are gated on !networkLedgerDiffers(ledger). Without this a
// fresh genesis bootstrap would deadlock at OpModeConnected (no peer to acquire
// from fires the normal Tracking transitions).
func (a *Adaptor) maybePromoteAfterConsensus(ledger consensus.Ledger) {
	if ledger == nil {
		return
	}
	current := a.GetOperatingMode()
	if current == consensus.OpModeDisconnected || current == consensus.OpModeFull {
		return
	}

	if a.networkLedgerDiffers(ledger, current) {
		a.logger.Info("operating mode promotion deferred — network prefers a different LCL",
			"mode", current.String(),
			"ledger_seq", ledger.Seq(),
		)
		return
	}

	target := current
	if current == consensus.OpModeConnected || current == consensus.OpModeSyncing {
		target = consensus.OpModeTracking
	}
	if target == consensus.OpModeConnected || target == consensus.OpModeTracking {
		resolution := a.CloseTimeResolution()
		networkSeq := a.networkValidatedSeq.Load()
		if a.Now().Before(ledger.CloseTime().Add(2*resolution)) &&
			!aheadByMoreThan(networkSeq, ledger.Seq(), 1) {
			target = consensus.OpModeFull
		} else if aheadByMoreThan(networkSeq, ledger.Seq(), 1) {
			a.logger.Info("operating mode Full promotion deferred — validated network is ahead",
				"ledger_seq", ledger.Seq(),
				"network_validated_seq", networkSeq,
			)
		}
	}
	if target == current {
		return
	}
	a.SetOperatingMode(target)
	a.logger.Info("operating mode auto-promoted after consensus",
		"from", current.String(),
		"to", target.String(),
		"ledger_seq", ledger.Seq(),
	)
}

// networkLedgerDiffers reports whether the network-preferred LCL differs from
// the one we just closed (the promotion gate's signal). False when the
// preferred LCL is our own.
func (a *Adaptor) networkLedgerDiffers(ledger consensus.Ledger, mode consensus.OperatingMode) bool {
	return a.preferredLCL(ledger, mode) != ledger.ID()
}

// preferredLCL picks the network-preferred last closed ledger, mirroring
// rippled Validations::getPreferredLCL (Validations.h:935-960): the
// trie-preferred ledger first, the most-supported trusted-validation tip
// when the trie has none, and the dominant peer-reported LCL as the last
// fallback. The only sequence gate is the last fully-validated index —
// never rewinding behind it; a preferred ledger at or below our own seq on
// a different chain is still a switch (Validations.h:892-895).
func (a *Adaptor) preferredLCL(ledger consensus.Ledger, mode consensus.OperatingMode) consensus.LedgerID {
	ourLCL := ledger.ID()
	var minSeq uint32
	if a.ledgerService != nil {
		minSeq = a.ledgerService.GetValidatedLedgerIndex()
	}

	if h := a.validationHistorian; h != nil {
		// Dump the trie's support state to diagnose why a branch was preferred.
		// Debug-gated because building it walks and marshals the whole trie.
		if a.logger.Enabled(context.Background(), slog.LevelDebug) {
			if trie := h.GetJSONTrie(); trie != nil {
				if raw, err := json.Marshal(trie); err == nil {
					a.logger.Debug("ValidationTrie", "trie", string(raw))
				}
			}
		}
		if id, seq, ok := h.GetPreferred(a.lastIssuedValidationSeq.Load()); ok {
			id, seq = a.resolvePreferredVsCurrent(id, seq, ledger)
			if seq >= minSeq {
				return id
			}
			return ourLCL
		}
		// No-trie fallback over trusted-validation tips (the acquiring_
		// majority is handled inside GetPreferred); already filtered to
		// seq >= minSeq.
		if id, _, ok := h.PreferredFromValidations(minSeq); ok {
			return id
		}
	}

	// Peer-LCL fallback. Seed our own LCL at zero and increment it when
	// we are already >= TRACKING, then pick the dominant ledger (ties
	// broken by larger ID).
	counts := map[consensus.LedgerID]uint32{ourLCL: 0}
	if mode >= consensus.OpModeTracking {
		counts[ourLCL]++
	}
	for _, p := range a.PeerReportedLedgers() {
		counts[p]++
	}
	best := ourLCL
	for id, c := range counts {
		// Larger count wins; ties break on larger ID.
		if c > counts[best] || (c == counts[best] && bytes.Compare(id[:], best[:]) > 0) {
			best = id
		}
	}
	return best
}

// resolvePreferredVsCurrent applies rippled getPreferred's stay/switch rules
// (Validations.h:881-898) to the trie-preferred tip: our own immediate child
// on our chain is not a switch (we may be about to build it), a tip ahead of
// us always wins, and a tip at or below our seq wins only when our chain's
// ledger at that seq differs (a fork).
func (a *Adaptor) resolvePreferredVsCurrent(prefID consensus.LedgerID, prefSeq uint32, ledger consensus.Ledger) (consensus.LedgerID, uint32) {
	ourLCL := ledger.ID()
	ourSeq := ledger.Seq()
	if prefSeq == ourSeq+1 {
		if l, err := a.GetLedger(prefID); err == nil && l != nil && l.ParentID() == ourLCL {
			return ourLCL, ourSeq
		}
	}
	if prefSeq > ourSeq {
		return prefID, prefSeq
	}
	if a.ancestorOf(ledger, prefSeq) != prefID {
		return prefID, prefSeq
	}
	return ourLCL, ourSeq
}

// ancestorOf resolves our chain's ledger ID at targetSeq, starting from
// ledger's own parent link and walking locally-held parents. Returns the
// zero ID when the ancestry is not locally resolvable — treated as a
// different chain, like rippled's out-of-skip-list ID{0}
// (RCLValidations.cpp:78-95).
func (a *Adaptor) ancestorOf(ledger consensus.Ledger, targetSeq uint32) consensus.LedgerID {
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
		l, err := a.GetLedger(cur)
		if err != nil || l == nil {
			return consensus.LedgerID{}
		}
		cur = l.ParentID()
	}
	return cur
}

// OnLedgerFullyValidated fires at trusted-validation quorum. It advances the
// service's validated_ledger only if our stored ledger at that seq has the
// matching hash (fork safety, keyed on the ledger not seq alone), then refreshes
// LoadFeeTrack's remoteFee from the median sfLoadFee across trusted validations.
func (a *Adaptor) OnLedgerFullyValidated(ledgerID consensus.LedgerID, seq uint32) {
	for {
		current := a.networkValidatedSeq.Load()
		if seq <= current || a.networkValidatedSeq.CompareAndSwap(current, seq) {
			break
		}
	}
	if closed := a.ledgerService.GetClosedLedger(); closed != nil && a.GetOperatingMode() == consensus.OpModeFull &&
		aheadByMoreThan(seq, closed.Sequence(), 1) {
		a.SetOperatingMode(consensus.OpModeConnected)
		a.logger.Info("operating mode demoted — validated network is ahead",
			"closed_seq", closed.Sequence(),
			"network_validated_seq", seq,
		)
	}

	var hash [32]byte
	copy(hash[:], ledgerID[:])
	if a.onLedgerFullyValidated != nil {
		a.onLedgerFullyValidated(seq, hash)
	}
	startupConfirmation := a.ledgerService.NeedsInitialSync() ||
		a.ledgerService.IsFastLoadProvisional()
	a.ledgerService.SetValidatedLedgerAt(seq, hash, a.validatedSignTime(ledgerID, seq))
	if startupConfirmation {
		held, err := a.ledgerService.GetLedgerByHash(hash)
		if err != nil {
			a.sender.CheckTracking(seq)
		} else if closed := a.ledgerService.GetClosedLedger(); held != nil && closed != nil &&
			held.Sequence() == seq && held.Sequence() == closed.Sequence() &&
			held.Hash() == closed.Hash() {
			if err := a.ledgerService.SwitchToPreferredLedger(held); err != nil {
				a.logger.Warn("failed to confirm current network ledger", "seq", seq, "error", err)
			} else if a.GetOperatingMode() < consensus.OpModeTracking {
				a.SetOperatingMode(consensus.OpModeTracking)
			}
		}
	}
	a.logger.Info("trusted validation quorum observed",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
	)
}

func (a *Adaptor) validatedSignTime(ledgerID consensus.LedgerID, seq uint32) time.Time {
	if a.validationHistorian == nil {
		return time.Time{}
	}
	validations := a.filterNegativeUNL(a.validationHistorian.GetTrustedValidations(ledgerID))
	signTime, count := sampleValidatedSignTime(validations, seq)
	if count == 0 || count < a.GetQuorum() {
		return time.Time{}
	}
	return signTime
}

func sampleValidatedSignTime(validations []*consensus.Validation, seq uint32) (time.Time, int) {
	times := make([]time.Time, 0, len(validations))
	for _, validation := range validations {
		if validation != nil && validation.Full && validation.LedgerSeq == seq && !validation.SignTime.IsZero() {
			times = append(times, time.Unix(validation.SignTime.Unix(), 0).UTC())
		}
	}
	if len(times) == 0 {
		return time.Time{}, 0
	}
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	t0 := times[(len(times)-1)/2]
	t1 := times[len(times)/2]
	return t0.Add(t1.Sub(t0) / 2), len(times)
}

func (a *Adaptor) refreshRemoteFee(seq uint32, ledgerID, parentID consensus.LedgerID) {
	if a.ledgerService == nil {
		return
	}

	a.mu.Lock()
	historian := a.validationHistorian
	a.mu.Unlock()

	a.remoteFeeMu.Lock()
	defer a.remoteFeeMu.Unlock()
	if seq <= a.remoteFeeSeq {
		return
	}
	if historian == nil {
		return
	}

	ft := a.ledgerService.FeeTrack()
	if ft == nil {
		return
	}
	base := ft.LoadBase()

	fees := collectValidationFees(historian, ledgerID, base)
	fees = append(fees, collectValidationFees(historian, parentID, base)...)
	fee := base
	if len(fees) > 0 {
		slices.Sort(fees)
		fee = fees[len(fees)/2]
	}
	ft.SetRemoteFee(fee)
	a.remoteFeeSeq = seq
}

func collectValidationFees(historian consensus.ValidationHistorian, ledgerID consensus.LedgerID, base uint32) []uint32 {
	vals := historian.GetTrustedValidations(ledgerID)
	fees := make([]uint32, 0, len(vals))
	for _, v := range vals {
		if v == nil || !v.Full {
			continue
		}
		fee := v.LoadFee
		if !v.HasLoadFee() {
			fee = base
		}
		fees = append(fees, fee)
	}
	return fees
}

func (a *Adaptor) OnModeChange(oldMode, newMode consensus.Mode) {
	a.consensusMode.Store(int32(newMode))
	if newMode == consensus.ModeWrongLedger {
		current := a.GetOperatingMode()
		if current == consensus.OpModeFull || current == consensus.OpModeTracking {
			a.SetOperatingMode(consensus.OpModeConnected)
		}
	}
	a.logger.Info("Consensus mode changed",
		"from", oldMode.String(),
		"to", newMode.String(),
	)

	// The engine pins in wrongLedger without running rounds, so no
	// phase-driven status would go out; tell peers directly that our
	// advertised LCL is stale.
	if newMode == consensus.ModeWrongLedger {
		a.broadcastStatus(message.NodeEventLostSync)
	}
}

// NeedsInitialSync reports whether startup still requires a network ledger.
func (a *Adaptor) NeedsInitialSync() bool {
	return a.ledgerService.NeedsInitialSync()
}

// IsFastLoadProvisional reports whether FastLoad startup still requires trusted
// network confirmation.
func (a *Adaptor) IsFastLoadProvisional() bool {
	return a.ledgerService != nil && a.ledgerService.IsFastLoadProvisional()
}

func (a *Adaptor) OnPhaseChange(oldPhase, newPhase consensus.Phase) {
	a.logger.Debug("Consensus phase changed",
		"from", oldPhase.String(),
		"to", newPhase.String(),
	)

	// Broadcast status change so peers learn our ledger state.
	switch newPhase {
	case consensus.PhaseEstablish:
		a.broadcastStatus(message.NodeEventClosingLedger)
	case consensus.PhaseAccepted:
		a.broadcastStatus(message.NodeEventAcceptedLedger)
	}

	// Notify via the ordered dispatcher for WebSocket subscription
	// broadcasting.
	a.emitConsensusPhase(newPhase.String())
}

// OnLedgerSwitched tells peers we abandoned our previous LCL for ledger.
func (a *Adaptor) OnLedgerSwitched(ledger consensus.Ledger) error {
	if ledger == nil {
		return nil
	}
	var historyFloor uint32
	switched := false
	if wrapped, ok := ledger.(*LedgerWrapper); ok {
		selected := wrapped.Unwrap()
		historyFloor = switchedLedgerHistoryFloor(
			selected,
			a.ledgerService.GetClosedLedger(),
			a.ledgerService.GetValidatedLedger(),
		)
		if err := a.ledgerService.SwitchToPreferredLedger(wrapped.Unwrap()); err != nil {
			return fmt.Errorf("switch canonical closed ledger at sequence %d: %w", ledger.Seq(), err)
		}
		switched = true
	}
	id := ledger.ID()
	parent := ledger.ParentID()
	if switched && a.onLedgerSwitched != nil {
		a.onLedgerSwitched(ledger.Seq(), [32]byte(id), [32]byte(parent), historyFloor)
	}
	a.broadcastSwitchedLedger(ledger.Seq(), id[:], parent[:])
	return nil
}

func switchedLedgerHistoryFloor(selected, closed, validated *ledger.Ledger) uint32 {
	if selected == nil || selected.Sequence() == 0 {
		return 0
	}
	if closed != nil {
		if selected.Hash() == closed.Hash() {
			return selected.Sequence() - 1
		}
		if selected.Sequence() > closed.Sequence() &&
			selected.Sequence()-closed.Sequence() == 1 &&
			selected.ParentHash() == closed.Hash() {
			return selected.Sequence() - 1
		}
	}

	var floor uint32
	for _, candidate := range []*ledger.Ledger{validated, closed} {
		if candidate == nil || candidate.Sequence() >= selected.Sequence() || candidate.Sequence() <= floor {
			continue
		}
		hash, ok, err := selected.HashOfSeq(candidate.Sequence())
		if err == nil && ok && hash == candidate.Hash() {
			floor = candidate.Sequence()
		}
	}
	return floor
}

// networkTime is the current time-adjusted clock as ripple-epoch seconds, for
// the TMStatusChange networktime field.
func (a *Adaptor) networkTime() uint64 {
	return uint64(protocol.RippleSeconds(a.Now()))
}

// broadcastSwitchedLedger sends a SWITCHED_LEDGER status change carrying the
// adopted ledger's identity. No status or validated-range fields: receivers
// inherit the prior status, and the jump says nothing about served history.
func (a *Adaptor) broadcastSwitchedLedger(seq uint32, hash, parentHash []byte) {
	sc := &message.StatusChange{
		NewEvent:           message.NodeEventSwitchedLedger,
		LedgerSeq:          seq,
		LedgerHash:         hash,
		LedgerHashPrevious: parentHash,
		NetworkTime:        a.networkTime(),
	}
	if err := a.sender.BroadcastStatusChange(sc); err != nil {
		a.logger.Warn("failed to broadcast status change", "error", err)
	}
}

// broadcastStatus sends a TMStatusChange message to all peers. While the
// engine is building on the wrong LCL the given event is replaced with
// LOST_SYNC, so peers stop counting our advertised ledger. No newstatus is
// set: peers inherit the status they last recorded for us, and the advertised
// [first, last] is the range we durably serve (0/0 when none).
func (a *Adaptor) broadcastStatus(event message.NodeEvent) {
	l := a.ledgerService.GetClosedLedger()
	if l == nil {
		return
	}

	if consensus.Mode(a.consensusMode.Load()) == consensus.ModeWrongLedger {
		event = message.NodeEventLostSync
	}

	hash := l.Hash()
	parentHash := l.ParentHash()

	var firstSeq, lastSeq uint32
	if first, last, ok := a.ledgerService.AdvertisableLedgerRange(); ok {
		firstSeq, lastSeq = first, last
	}

	sc := &message.StatusChange{
		NewEvent:           event,
		LedgerSeq:          l.Sequence(),
		LedgerHash:         hash[:],
		LedgerHashPrevious: parentHash[:],
		NetworkTime:        a.networkTime(),
		FirstSeq:           &firstSeq,
		LastSeq:            &lastSeq,
	}

	if err := a.sender.BroadcastStatusChange(sc); err != nil {
		a.logger.Warn("failed to broadcast status change", "error", err)
	}
}
