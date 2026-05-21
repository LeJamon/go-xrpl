// Package rcl implements the Ripple Consensus Ledger algorithm.
// This is the default consensus algorithm used by the XRP Ledger.
package rcl

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/protocol"
)

// Engine implements the RCL consensus algorithm.
type Engine struct {
	mu sync.RWMutex

	// Configuration
	timing     consensus.Timing
	thresholds consensus.Thresholds

	// Dependencies
	adaptor  consensus.Adaptor
	eventBus *consensus.EventBus

	// Current state
	mode  consensus.Mode
	phase consensus.Phase
	// modeAtomic mirrors mode for lock-free reads on the RPC hot path
	// (server_info → serverStateFunc → IsProposing). The lock-free
	// read also breaks an ABBA deadlock between OnValidation
	// (e.mu.Lock → fullyValidatedCallback → ledgerService.s.mu.Lock)
	// and GetServerInfo (s.mu.RLock → serverStateFunc →
	// e.mu.RLock). Writes happen inside setMode, which is always
	// called with e.mu held. Issue #381 follow-up.
	modeAtomic atomic.Int32
	// lastCloseAtomic mirrors (prevProposers, prevRoundTime) for
	// lock-free reads from GetLastCloseInfo. Same RPC-hot-path
	// rationale as modeAtomic — server_info handler calls into this
	// via the LastCloseInfo callback (cli/server.go). Writes happen
	// from acceptLedger under e.mu via storeLastCloseLocked.
	lastCloseAtomic atomic.Pointer[lastCloseInfo]
	state           *consensus.RoundState
	prevLedger      consensus.Ledger

	// Proposal tracking
	proposals map[consensus.NodeID]*consensus.Proposal
	ourTxSet  consensus.TxSet
	converged bool

	// deadNodes tracks validators that have bowed out of the current
	// consensus round by sending a proposal with Position == seqLeave
	// (0xFFFFFFFF). Matches rippled's deadNodes_ set (Consensus.h:632).
	// Any further proposal from a dead node is dropped until the next
	// round clears the set (startRoundLocked), mirroring rippled's
	// Consensus.h:722.
	deadNodes map[consensus.NodeID]struct{}

	// Validation tracking
	validations map[consensus.NodeID]*consensus.Validation

	// validationTracker accumulates trusted validations across ledgers
	// and fires the fully-validated callback when quorum is reached.
	// This is what drives server_info.validated_ledger forward —
	// mirrors rippled's LedgerMaster::checkAccept quorum gate.
	validationTracker *ValidationTracker

	// Dispute tracking
	//
	// disputeTracker owns the per-tx DisputedTx entries and the
	// per-peer vote map, matching rippled's Result::disputes. It is
	// written by createDisputesAgainst / OnProposal / OnTxSet /
	// UpdateOurPositions and read during checkConvergence.
	disputeTracker *DisputeTracker

	// acquiredTxSets caches peer tx sets we have in memory, keyed
	// by TxSetID. Populated by our own BuildTxSet output and by
	// OnTxSet. Matches rippled's acquired_ (Consensus.h:606) — the
	// dispute wiring reads this to learn which txs a peer's
	// position actually contains.
	acquiredTxSets map[consensus.TxSetID]consensus.TxSet

	// comparesTxSets dedupes createDisputes. Matches rippled's
	// Result::compares (Consensus.h:1829) — once we have diffed
	// against a given peer tx set, the set is recorded here so
	// subsequent repeats are cheap no-ops.
	comparesTxSets map[consensus.TxSetID]struct{}

	// parms holds the avalanche-threshold parameters used by
	// DisputedTx::updateVote (per-tx re-voting). Mirrors rippled's
	// ConsensusParms.
	parms consensus.ConsensusParms

	// peerUnchangedCounter counts consecutive phaseEstablish ticks
	// during which NO peer flipped a dispute vote. Matches rippled's
	// peerUnchangedCounter_ (Consensus.h) — used by stall detection
	// on disputes.
	peerUnchangedCounter int

	// establishCounter counts phaseEstablish ticks since closeLedger,
	// mirroring rippled's establishCounter_ (Consensus.h). Currently
	// surfaced only as the per-dispute AvalancheCounter floor; kept
	// here for parity and so future stall-expiration logic can gate
	// ResultExpired on "minimum rounds at each avalanche level".
	establishCounter int

	// Heartbeat ticker — single global timer matching rippled's ledgerGRANULARITY.
	heartbeat *time.Ticker

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Close time consensus
	haveCloseTimeConsensus  bool
	closeTimeAvalancheState avalancheState
	prevRoundTime           time.Duration
	roundStartTime          time.Time

	// Proposal buffering for cross-round playback.
	// Matches rippled's recentPeerPositions_ (Consensus.h:629).
	recentProposals map[consensus.NodeID][]*consensus.Proposal

	// Number of trusted proposers in the previous round.
	// Used by shouldCloseLedger() for peer pressure calculation.
	prevProposers int

	// wrongLedgerID tracks the ledger we're trying to acquire
	// while in ModeWrongLedger. Prevents spamming handleWrongLedger.
	wrongLedgerID consensus.LedgerID

	// lastSignTime is the monotonic floor for emitted validation
	// SignTime. If the adaptor clock regresses (NTP step, leap-second
	// correction, VM pause/resume), sendValidation bumps SignTime to
	// lastSignTime + 1s so peers never see a non-monotonic sequence of
	// validations from the same node. Matches rippled's
	// RCLConsensus::Adaptor::lastValidationTime_ (RCLConsensus.cpp:825-828).
	// Protected by e.mu (same lock as sendValidation's other state).
	lastSignTime time.Time

	// Highest seq this node has broadcast a validation for. Mirrors
	// rippled's SeqEnforcer (Validations.h:625-665) /
	// Validations::canValidateSeq (Validations.h:830) — without this
	// guard, BBD flags us for "Conflicting validation for N" (#401).
	// Protected by e.mu.
	ourLastValidatedSeq uint32

	// Time the floor was last bumped. After validationSetExpires of
	// silence, the floor resets to 0 (Validations.h:118-128) so a
	// restarted/partitioned validator can resume below its old floor.
	ourLastValidatedTime time.Time

	// Stats
	roundCount     uint64
	consensusCount uint64

	// archive, when non-nil, persists stale validations dropped by the
	// tracker. Wired via SetArchive — optional, the engine functions
	// identically when nil. Stored via an atomic pointer so the
	// fully-validated callback can read it lock-free, even when
	// validationTracker.Add is invoked outside e.mu (e.g. from tests
	// or future callers that bypass OnValidation).
	archive atomic.Pointer[archiveBox]

	// inMemoryLedgers is the tracker's in-memory retention window: after
	// a ledger becomes fully validated at seq S, validations for ledgers
	// below (S - inMemoryLedgers) are dropped and streamed into the
	// archive via OnStale. Zero disables auto-expiry. Atomic for the
	// same reason as archive.
	inMemoryLedgers atomic.Uint32

	// ledgerAncestry is staged by startup wiring and applied to the
	// tracker in Start. Nil keeps flat-count semantics.
	ledgerAncestry LedgerAncestryProvider

	// pendingBroadcasts queues proposal/validation broadcasts produced
	// while e.mu is held so they can be flushed after the lock is
	// released. The overlay write path takes its own per-peer locks
	// and bounded send queues; holding e.mu across BroadcastProposal /
	// BroadcastValidation blocks OnProposal/OnValidation ingress on
	// e.mu.RLock and can stall consensus across the trusted set when
	// a single peer's send queue is slow. Mutated only while e.mu is
	// held; drained by takePendingBroadcastsLocked after Unlock.
	pendingBroadcasts []func()

	// missedHeartbeats counts the number of heartbeat ticks the run
	// loop observed as dropped (gap between consecutive ticks > 2× the
	// configured interval). time.Ticker silently coalesces ticks when
	// the consumer can't keep up; this counter surfaces that pressure
	// so stalls don't hide. Read via MissedHeartbeats().
	missedHeartbeats atomic.Uint64

	// deferBroadcasts is incremented on entry to timerEntry / StartRound
	// (the entry points that drive proposal/validation emission under
	// e.mu) and decremented on exit. When zero, the enqueue helpers fall
	// back to a synchronous send so direct callers — primarily unit
	// tests that drive sendValidation / closeLedger without going
	// through timerEntry — still observe the broadcast immediately.
	// Mutated only while e.mu is held.
	deferBroadcasts int

	// previousTrustedSet is the trusted-validator set as of the previous
	// startRoundLocked invocation. The engine diffs it against the
	// adaptor's current trusted set each round to derive the `added`
	// delta passed to OnUNLChange — equivalent to rippled's
	// TrustChanges.added (NetworkOPs.cpp:2081) but computed from
	// snapshots since goXRPL's writable surface is the explicit
	// adaptor.SetTrustedValidators call rather than a per-round
	// updateTrusted return value. Seeded from the adaptor's current
	// UNL on the first invocation with a parent ledger (see
	// previousTrustedSeeded). Mutated only while e.mu is held.
	previousTrustedSet map[consensus.NodeID]struct{}

	// previousTrustedSeeded latches true after the first call that
	// observes a non-nil prevLedger. While false, the next call seeds
	// previousTrustedSet from the adaptor's startup UNL and skips
	// OnUNLChange — mirroring rippled where ValidatorList::updateTrusted
	// diffs against the publisher-list-loaded trustedMasterKeys_ on its
	// very first call, so the startup UNL is NOT reported as `added`.
	// Mutated only while e.mu is held.
	previousTrustedSeeded bool
}

// ValidationArchive is the subset of the archive API the consensus engine
// consumes. Defined here so the rcl package does not depend on the
// concrete archive type — test doubles can satisfy it with two methods.
type ValidationArchive interface {
	OnStale(*consensus.Validation)
	NoteFullyValidated(seq uint32)
	Close(ctx context.Context) error
}

// archiveBox wraps a ValidationArchive interface so it can be carried
// by atomic.Pointer. atomic.Value would also work but panics on a nil
// store and on type changes; atomic.Pointer is simpler.
type archiveBox struct{ a ValidationArchive }

// loadArchive returns the currently-installed archive (or nil).
func (e *Engine) loadArchive() ValidationArchive {
	if box := e.archive.Load(); box != nil {
		return box.a
	}
	return nil
}

// enqueueProposalBroadcastLocked stages a proposal to be broadcast after
// e.mu is released. See pendingBroadcasts on Engine for rationale.
// Caller must hold e.mu. When no deferred-broadcast scope is active
// (deferBroadcasts == 0, e.g. tests driving sendValidation directly),
// the send is performed synchronously so observers don't have to flush
// manually.
func (e *Engine) enqueueProposalBroadcastLocked(p *consensus.Proposal) {
	if p == nil {
		return
	}
	if e.deferBroadcasts == 0 {
		_ = e.adaptor.BroadcastProposal(p)
		return
	}
	e.pendingBroadcasts = append(e.pendingBroadcasts, func() {
		_ = e.adaptor.BroadcastProposal(p)
	})
}

// enqueueValidationBroadcastLocked stages a validation to be broadcast
// after e.mu is released. Caller must hold e.mu.
func (e *Engine) enqueueValidationBroadcastLocked(v *consensus.Validation) {
	if v == nil {
		return
	}
	if e.deferBroadcasts == 0 {
		_ = e.adaptor.BroadcastValidation(v)
		return
	}
	e.pendingBroadcasts = append(e.pendingBroadcasts, func() {
		_ = e.adaptor.BroadcastValidation(v)
	})
}

// takePendingBroadcastsLocked drains the queued broadcast closures.
// Caller must hold e.mu. The returned slice should be passed to
// flushBroadcasts after the lock is released.
func (e *Engine) takePendingBroadcastsLocked() []func() {
	if len(e.pendingBroadcasts) == 0 {
		return nil
	}
	out := e.pendingBroadcasts
	e.pendingBroadcasts = nil
	return out
}

// flushBroadcasts runs each queued broadcast. MUST be called with e.mu
// released.
func flushBroadcasts(pending []func()) {
	for _, fn := range pending {
		fn()
	}
}

// avalancheState tracks the close time voting threshold escalation.
// Matches rippled's avalanche cutoffs in ConsensusParms.h.
type avalancheState int

const (
	avalancheInit  avalancheState = iota // 50% threshold
	avalancheMid                         // 65% threshold
	avalancheLate                        // 70% threshold
	avalancheStuck                       // 95% threshold
)

// SeqEnforcer reset window — ValidationParms::validationSET_EXPIRES
// (Validations.h:79).
const validationSetExpires = 10 * time.Minute

// Config holds RCL engine configuration.
type Config struct {
	Timing     consensus.Timing
	Thresholds consensus.Thresholds
}

// DefaultConfig returns the default RCL configuration.
func DefaultConfig() Config {
	return Config{
		Timing:     consensus.DefaultTiming(),
		Thresholds: consensus.DefaultThresholds(),
	}
}

// NewEngine creates a new RCL consensus engine.
func NewEngine(adaptor consensus.Adaptor, config Config) *Engine {
	e := &Engine{
		timing:          config.Timing,
		thresholds:      config.Thresholds,
		adaptor:         adaptor,
		eventBus:        consensus.NewEventBus(100),
		mode:            consensus.ModeObserving,
		phase:           consensus.PhaseAccepted,
		proposals:       make(map[consensus.NodeID]*consensus.Proposal),
		validations:     make(map[consensus.NodeID]*consensus.Validation),
		disputeTracker:  NewDisputeTracker(),
		acquiredTxSets:  make(map[consensus.TxSetID]consensus.TxSet),
		comparesTxSets:  make(map[consensus.TxSetID]struct{}),
		parms:           consensus.DefaultConsensusParms(),
		recentProposals: make(map[consensus.NodeID][]*consensus.Proposal),
		deadNodes:       make(map[consensus.NodeID]struct{}),
	}
	e.modeAtomic.Store(int32(e.mode))
	return e
}

// SetArchive wires an on-disk validation archive into the engine. May
// be called before or after Start. Pass nil to detach — the tracker's
// onStale callback is cleared so the just-detached archive can be
// Close()d without risking a use-after-close channel send. Safe to call
// concurrently with Stop but not with Start.
func (e *Engine) SetArchive(a ValidationArchive) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if a == nil {
		e.archive.Store(nil)
	} else {
		e.archive.Store(&archiveBox{a: a})
	}
	if e.validationTracker == nil {
		return
	}
	if a == nil {
		e.validationTracker.SetOnStale(nil)
		return
	}
	e.validationTracker.SetOnStale(a.OnStale)
}

// SetInMemoryLedgers configures how many fully-validated ledgers of
// validation history the tracker holds in memory. Every time a ledger
// becomes fully validated at seq S, validations for ledgers below
// (S - n) are evicted (and streamed into the archive via OnStale).
// Zero disables auto-eviction.
func (e *Engine) SetInMemoryLedgers(n uint32) {
	e.inMemoryLedgers.Store(n)
}

// SetLedgerAncestryProvider installs the trie's ancestry provider.
// Safe before or after Start; nil reverts to flat-count support.
func (e *Engine) SetLedgerAncestryProvider(p LedgerAncestryProvider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ledgerAncestry = p
	if e.validationTracker != nil {
		e.validationTracker.SetLedgerAncestryProvider(p)
	}
}

// Start begins the consensus engine.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.ctx, e.cancel = context.WithCancel(ctx)
	e.eventBus.Start()

	// Get initial ledger state
	ledger, err := e.adaptor.GetLastClosedLedger()
	if err != nil {
		return fmt.Errorf("failed to get last closed ledger: %w", err)
	}
	e.prevLedger = ledger

	// Wire the validation tracker: trusted set + quorum come from the adaptor,
	// and its callback drives the adaptor's fully-validated hook which in turn
	// flips the ledger service's validated_ledger pointer.
	e.validationTracker = NewValidationTracker(e.adaptor.GetQuorum(), 5*time.Minute)
	e.validationTracker.SetTrusted(e.adaptor.GetTrustedValidators())
	if wired, ok := e.adaptor.(consensus.WireableAdaptor); ok {
		wired.SetValidationHistorian(e.validationTracker)
	}
	if e.ledgerAncestry != nil {
		e.validationTracker.SetLedgerAncestryProvider(e.ledgerAncestry)
	}
	// Use the adaptor's network-adjusted clock for freshness checks.
	// Rippled's Validations::isCurrent uses app_.timeKeeper().closeTime()
	// — matching here avoids rejecting our own just-signed validation
	// by the accumulated close-time offset on a skewed node.
	e.validationTracker.SetNow(e.adaptor.Now)
	if arc := e.loadArchive(); arc != nil {
		e.validationTracker.SetOnStale(arc.OnStale)
	}
	tracker := e.validationTracker
	e.validationTracker.SetFullyValidatedCallback(func(ledgerID consensus.LedgerID, seq uint32) {
		// Contract notes (issue #381 follow-up):
		//   - The production callers of validationTracker.Add
		//     (Engine.OnValidation, Engine.sendValidation) invoke it
		//     with e.mu.Lock held. Tests can also drive Add directly
		//     without that lock. The callback must therefore work in
		//     both modes — meaning it MUST NOT take e.mu (Go's
		//     RWMutex is non-recursive: a defensive RLock under the
		//     held write lock self-deadlocks).
		//   - e.archive and e.inMemoryLedgers are read via atomics so
		//     a concurrent SetArchive / SetInMemoryLedgers cannot
		//     race the read here.
		e.adaptor.OnLedgerFullyValidated(ledgerID, seq)

		arc := e.loadArchive()
		inMem := e.inMemoryLedgers.Load()

		if arc != nil {
			arc.NoteFullyValidated(seq)
		}
		// Drive the in-memory retention window. ExpireOld fires the
		// onStale callback for each evicted validation, so the archive
		// captures it before the tracker drops it. ExpireOld takes
		// vt.mu but does NOT touch e.mu, so calling it from inside
		// a held e.mu write lock does not deadlock.
		if inMem > 0 && seq > inMem {
			tracker.ExpireOld(seq - inMem)
		}
	})

	// Start the main loop
	e.wg.Add(1)
	go e.run()

	return nil
}

// Stop gracefully shuts down the consensus engine. If an archive is
// wired, its writer goroutine is drained and committed before Stop
// returns so no stale validations are lost across shutdown — modulo
// SaveBatch failures (which the writer logs and re-queues; see
// Archive.run for the retry policy).
func (e *Engine) Stop() error {
	e.cancel()
	e.wg.Wait()
	e.eventBus.Stop()

	if arc := e.loadArchive(); arc != nil {
		// Bounded close — a stuck archive must not hang shutdown.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = arc.Close(ctx)
		cancel()
	}
	return nil
}

// StartRound begins a new consensus round.
func (e *Engine) StartRound(round consensus.RoundID, proposing bool) error {
	e.mu.Lock()
	e.deferBroadcasts++
	err := e.startRoundLocked(round, proposing, false)
	e.deferBroadcasts--
	pending := e.takePendingBroadcastsLocked()
	e.mu.Unlock()
	flushBroadcasts(pending)
	return err
}

// startRoundLocked is the lock-free inner implementation of StartRound.
// Caller must hold e.mu.
//
// recovering indicates this round is entered immediately after
// handleWrongLedger or OnLedger adoption — rippled calls this the
// "switchedLedger" mode. In that mode the node acts like an observer
// for one round (no proposal, no validation emission) even if it's a
// full-configured validator. Mirrors rippled's Consensus.h:1107 which
// forces ConsensusMode::switchedLedger after a successful LCL switch,
// and Consensus.h:1457 which only emits a proposal when mode equals
// proposing. The suppression is intentional: a node that just
// swapped its prior-ledger pointer hasn't yet built a coherent view
// of the new round's tx-set, and emitting a stale proposal/validation
// would poison the network's convergence.
func (e *Engine) startRoundLocked(round consensus.RoundID, proposing, recovering bool) error {
	// Placed before the mode switch so it runs in every mode (proposing /
	// observing / switchedLedger), matching rippled's preStartRound at
	// RCLConsensus.cpp:1041-1043 which fires regardless of validator state.
	e.driveNegativeUNLNewValidatorsLocked()

	// Determine mode. After a wrongLedger recovery we enter switchedLedger
	// for exactly one round — not proposing, not validating — even though
	// we'd otherwise be ModeProposing. The NEXT startRoundLocked call
	// (via auto-advance in acceptLedger) gets the normal treatment.
	switch {
	case recovering && e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull:
		e.setMode(consensus.ModeSwitchedLedger)
	case proposing && e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull:
		e.setMode(consensus.ModeProposing)
	default:
		e.setMode(consensus.ModeObserving)
	}

	// Initialize round state.
	// StartTime must be wall-clock (time.Now) because shouldCloseLedger
	// reads it via time.Since at engine.go:873 — same class of bug as the
	// roundStartTime fix below, and the same rationale applies. PhaseStart
	// stays on adaptor.Now because its consumer (checkConvergence) reads
	// it via adaptor.Now().Sub(), which keeps the offset-adjusted pair
	// balanced.
	e.state = &consensus.RoundState{
		Round:          round,
		Mode:           e.mode,
		Phase:          consensus.PhaseOpen,
		Proposals:      make(map[consensus.NodeID]*consensus.Proposal),
		Disputed:       make(map[consensus.TxID]*consensus.DisputedTx),
		CloseTimes:     consensus.CloseTimes{Peers: make(map[time.Time]int)},
		StartTime:      time.Now(),
		PhaseStart:     e.adaptor.Now(),
		HaveCorrectLCL: true,
	}

	// Reset tracking maps
	e.proposals = make(map[consensus.NodeID]*consensus.Proposal)
	e.disputeTracker = NewDisputeTracker()
	e.acquiredTxSets = make(map[consensus.TxSetID]consensus.TxSet)
	e.comparesTxSets = make(map[consensus.TxSetID]struct{})
	e.peerUnchangedCounter = 0
	e.establishCounter = 0
	// deadNodes is scoped to a single consensus round — a validator that
	// bowed out of the prior round is free to rejoin in the new one.
	// Matches rippled's Consensus.h:722 (startRoundInternal clears
	// deadNodes_ alongside currPeerPositions_).
	e.deadNodes = make(map[consensus.NodeID]struct{})
	e.converged = false
	e.ourTxSet = nil
	e.haveCloseTimeConsensus = false
	e.closeTimeAvalancheState = avalancheInit
	// Internal duration metric — use the wall clock. Do NOT use
	// adaptor.Now() here: adaptor.Now returns time.Now().Add(closeOffset),
	// where closeOffset drifts as AdjustCloseTime pulls us toward the
	// network's average close time. The consumers of roundStartTime
	// measure elapsed wall time via time.Since (prevRoundTime,
	// phaseEstablish timeout, convergePercent weighting). Mixing
	// offset-adjusted captures with wall-clock-subtracted reads
	// produces -closeOffset as the measured duration — exactly the
	// negative-converge-time artifact visible in server_info.last_close.
	e.roundStartTime = time.Now()

	// Set phase
	e.setPhase(consensus.PhaseOpen)

	// Emit event
	e.eventBus.Publish(&consensus.RoundStartedEvent{
		Round:     round,
		Mode:      e.mode,
		Timestamp: e.adaptor.Now(),
	})

	// Replay buffered proposals matching this round's prevLedger.
	// Matches rippled's playbackProposals() (Consensus.h:1151).
	if e.prevLedger != nil && len(e.recentProposals) > 0 {
		prevID := e.prevLedger.ID()
		replayed := 0
		for nodeID, positions := range e.recentProposals {
			for _, p := range positions {
				if p.PreviousLedger == prevID {
					trusted := e.adaptor.IsTrusted(nodeID)
					existing, exists := e.proposals[nodeID]
					if !exists || p.Position > existing.Position {
						e.proposals[nodeID] = p
					}
					if p.Position == 0 && trusted {
						e.state.CloseTimes.Peers[p.CloseTime]++
					}
					if trusted {
						replayed++
					}
				}
			}
		}

		// Peer pressure: if more than half of previous proposers have
		// already closed, consider closing immediately — but still go
		// through shouldCloseLedger() to enforce timing constraints.
		// Matches rippled's startRoundInternal() (Consensus.h:732-738)
		// which calls timerEntry() → phaseOpen() → shouldCloseLedger().
		if replayed > e.prevProposers/2 {
			if e.shouldCloseLedger() {
				e.closeLedger()
				// Don't call checkConvergence() here — the establish
				// timer will evaluate it after fresh proposals arrive
				// with correct close times. Accepting immediately with
				// only replayed close times causes hash mismatches.
			}
		}
	}

	e.roundCount++
	return nil
}

// driveNegativeUNLNewValidatorsLocked snapshots the current trusted set,
// computes the additions relative to the previous round's snapshot, and
// invokes adaptor.OnUNLChange when the NegativeUNL amendment is enabled
// on the parent ledger and the delta is non-empty. Updates the snapshot
// in-place so the next round sees the new baseline.
//
// The seq passed to OnUNLChange is derived directly from the parent
// ledger (`prevLedger.Seq() + 1`), matching rippled at
// RCLConsensus.cpp:1043 (`nUnlVote_.newValidators(prevLgr.seq() + 1, ...)`).
// Critically, the voting-path purge in GenerateNegativeUNLPseudoTx also
// keys off `prevSeq + 1` (negative_unl_vote.go:74); reading the seq
// from the same source on both sides keeps the grace-period window
// internally consistent even if a caller ever drives StartRound with a
// round.Seq that drifts from prevLedger.Seq()+1 (recovery / wrong-LCL
// paths).
//
// Mirrors rippled's NetworkOPs.cpp:2081-2102 → RCLConsensus.cpp:1041-1043
// pairing: NetworkOPs computes TrustChanges.added per round via
// updateTrusted, then passes it through startRound → preStartRound →
// nUnlVote_.newValidators. goXRPL inverts the seam — the engine polls
// the adaptor each round — but the observable behavior is the same:
// any mutation that lands through adaptor.SetTrustedValidators is
// picked up on the next round and its `added` set drives OnUNLChange.
// previousTrustedSet is seeded once on the first call (from the
// adaptor's current UNL) so the first round does NOT misreport the
// entire startup-loaded UNL as `added`; this matches rippled where
// ValidatorList::trustedMasterKeys_ is already populated by applyLists
// before the first updateTrusted call. Caller must hold e.mu.
func (e *Engine) driveNegativeUNLNewValidatorsLocked() {
	if e.prevLedger == nil {
		return
	}
	if !e.adaptor.IsFeatureEnabledOnLedger(e.prevLedger, "NegativeUNL") {
		return
	}
	current := e.adaptor.GetTrustedValidators()

	// Seed once: on the very first invocation with a parent ledger the
	// prior set is the startup-loaded UNL, not the empty set. Treating
	// the startup UNL as `added` would hand every already-mature
	// validator a fresh NewValidatorDisableSkip-ledger grace period
	// after a restart — silent but observable divergence from rippled.
	if !e.previousTrustedSeeded {
		e.previousTrustedSet = make(map[consensus.NodeID]struct{}, len(current))
		for _, n := range current {
			e.previousTrustedSet[n] = struct{}{}
		}
		e.previousTrustedSeeded = true
		return
	}

	var added []consensus.NodeID
	for _, n := range current {
		if _, seen := e.previousTrustedSet[n]; !seen {
			added = append(added, n)
		}
	}
	if len(added) > 0 {
		e.adaptor.OnUNLChange(e.prevLedger.Seq()+1, added)
	}
	next := make(map[consensus.NodeID]struct{}, len(current))
	for _, n := range current {
		next[n] = struct{}{}
	}
	e.previousTrustedSet = next
}

// OnProposal handles an incoming proposal from a peer. originPeer is
// the overlay peer that delivered the message (0 for self-originated).
// Passed through to RelayProposal so we can exclude the originator from
// the gossip forward.
func (e *Engine) OnProposal(proposal *consensus.Proposal, originPeer uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Verify signature first (before buffering).
	if err := e.adaptor.VerifyProposal(proposal); err != nil {
		return fmt.Errorf("invalid proposal signature: %w", err)
	}

	// Always buffer proposals for future playback, even between rounds.
	// Matches rippled's recentPeerPositions_ (Consensus.h:754): cap at
	// 10 positions per node. The earlier 5-entry cap drifted from
	// rippled's value and would truncate a trusted validator's
	// cross-round trail under sustained gossip.
	positions := e.recentProposals[proposal.NodeID]
	if len(positions) >= 10 {
		positions = positions[1:] // drop oldest
	}
	e.recentProposals[proposal.NodeID] = append(positions, proposal)

	// During accepted phase (between rounds), only buffer — don't process.
	// Matches rippled Consensus.h:769-770.
	if e.phase == consensus.PhaseAccepted {
		return nil
	}

	// Reject proposals referencing a different previous ledger.
	// Matches rippled Consensus.h:776-781.
	if e.prevLedger != nil && proposal.PreviousLedger != e.prevLedger.ID() {
		return nil
	}

	// Ignore proposals from nodes already marked dead this round. This
	// guard must come before the bow-out arm below: rippled's
	// Consensus.h:785-789 drops the message outright before it ever
	// reaches the position-update code, so a node that's already dead
	// cannot keep re-inserting itself by repeatedly sending seqLeave.
	if _, dead := e.deadNodes[proposal.NodeID]; dead {
		return nil
	}

	// isBowOut: a validator bowing out of consensus sets ProposeSeq to
	// seqLeave (0xFFFFFFFF) on its final position so peers know to stop
	// counting it for the rest of the round. Mirrors rippled's
	// ConsensusProposal.h:68,154-156 and the handling in
	// Consensus.h:804-817: erase the current position, record the node
	// as dead, and un-vote its contribution from every active dispute.
	// Without this gate the final seqLeave position would persist in
	// e.proposals and keep "voting" forever, skewing convergence and
	// tie-break logic.
	const seqLeave = uint32(0xFFFFFFFF)
	if proposal.Position == seqLeave {
		delete(e.proposals, proposal.NodeID)
		e.deadNodes[proposal.NodeID] = struct{}{}
		// Strip this peer's contribution from every active dispute
		// so its (now-final) vote stops counting toward convergence.
		// Matches rippled Consensus.h:807-811.
		if e.disputeTracker != nil {
			e.disputeTracker.UnVote(proposal.NodeID)
		}
		return nil
	}

	// Check if from trusted validator
	trusted := e.adaptor.IsTrusted(proposal.NodeID)

	// Store proposal
	existing, exists := e.proposals[proposal.NodeID]
	if !exists || proposal.Position > existing.Position {
		e.proposals[proposal.NodeID] = proposal
	}

	// Record close time only from initial proposals (Position == 0),
	// matching rippled's rawCloseTimes_.peers tracking (Consensus.h:825-830).
	if proposal.Position == 0 && trusted {
		e.state.CloseTimes.Peers[proposal.CloseTime]++
	}

	// Emit event
	e.eventBus.Publish(&consensus.ProposalReceivedEvent{
		Proposal:  proposal,
		Trusted:   trusted,
		Timestamp: e.adaptor.Now(),
	})

	// Relay to other peers, excluding the originating peer. Untrusted
	// proposals are not relayed to limit gossip amplification of spam —
	// matches rippled's relay-only-trusted heuristic.
	if trusted {
		e.adaptor.RelayProposal(proposal, originPeer)
	}

	if trusted {
		var ourTxSet consensus.TxSetID
		ourTxLen := -1
		if e.ourTxSet != nil {
			ourTxSet = e.ourTxSet.ID()
			ourTxLen = e.ourTxSet.Size()
		}
		_, peerCacheHit := e.acquiredTxSets[proposal.TxSet]
		if !peerCacheHit {
			if cached, _ := e.adaptor.GetTxSet(proposal.TxSet); cached != nil {
				peerCacheHit = true
			}
		}
		slog.Info("proposal received",
			"t", "consensus",
			"event", "propose-recv",
			"seq", proposal.Round.Seq,
			"peer", originPeer,
			"node", fmt.Sprintf("%x", proposal.NodeID[:6]),
			"pos_seq", proposal.Position,
			"peer_txset", fmt.Sprintf("%x", proposal.TxSet[:8]),
			"our_txset", fmt.Sprintf("%x", ourTxSet[:8]),
			"our_tx_count", ourTxLen,
			"peer_txset_cache_hit", peerCacheHit,
			"diff", proposal.TxSet != ourTxSet,
		)
	}

	// Check if we need the transaction set. If the adaptor already
	// has it, cache it locally for dispute wiring — rippled's
	// gotTxSet(Consensus.h:843-844) fires eagerly in the same
	// scenario.
	if peerSet, err := e.adaptor.GetTxSet(proposal.TxSet); err == nil && peerSet != nil {
		if _, already := e.acquiredTxSets[proposal.TxSet]; !already {
			e.acquiredTxSets[proposal.TxSet] = peerSet
		}
	} else {
		e.adaptor.RequestTxSet(proposal.TxSet)
	}

	// If we already hold the peer's tx set (either from our own
	// closeLedger, a prior OnTxSet, or the GetTxSet above), run the
	// create/update-disputes loop for this position. Matches rippled's
	// peerProposal path at Consensus.h:836-852: if the proposal's
	// position is in acquired_, updateDisputes(nodeID, txSet);
	// otherwise acquireTxSet is fired and the update happens later in
	// gotTxSet. Self-originated proposals are gated out because we
	// already seeded them in closeLedger.
	if e.ourTxSet != nil && proposal.TxSet != e.ourTxSet.ID() {
		if peerSet, ok := e.acquiredTxSets[proposal.TxSet]; ok {
			e.createDisputesAgainst(peerSet)
			if e.disputeTracker.UpdateDisputes(proposal.NodeID, peerSet) {
				e.peerUnchangedCounter = 0
			}
		}
	}

	// If in establish phase, check for convergence
	if e.phase == consensus.PhaseEstablish {
		e.checkConvergence()
	}

	return nil
}

// OnValidation handles an incoming validation from a peer. originPeer
// is the overlay peer that delivered the message (0 for self-originated).
// Passed through to RelayValidation so we can exclude the originator
// from the gossip forward.
func (e *Engine) OnValidation(validation *consensus.Validation, originPeer uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Verify signature
	if err := e.adaptor.VerifyValidation(validation); err != nil {
		return fmt.Errorf("invalid validation signature: %w", err)
	}

	// Check if from trusted validator
	trusted := e.adaptor.IsTrusted(validation.NodeID)

	// Store validation. Also cap to trusted-only to bound memory under
	// adversarial validator spam — an untrusted key can send us
	// arbitrary validations and the map would grow unbounded.
	if trusted {
		e.validations[validation.NodeID] = validation
	}

	// Feed into the tracker — this is the gate that advances
	// server_info.validated_ledger once quorum of trusted validations
	// accumulates for a given ledger. Trust-gate here as well: the
	// tracker filters by trusted at quorum-count time, but without
	// this gate a byNode entry gets created for every untrusted
	// validator the network gossips, wasting memory on keys that
	// can never contribute to quorum. Rippled's LedgerMaster.cpp:886
	// filters on both Full and trusted before Add.
	if trusted && e.validationTracker != nil {
		e.validationTracker.Add(validation)
	}

	// Emit event
	e.eventBus.Publish(&consensus.ValidationReceivedEvent{
		Validation: validation,
		Trusted:    trusted,
		Timestamp:  e.adaptor.Now(),
	})

	// Relay trusted validations to other peers, excluding the origin.
	// Untrusted validations are dropped from the gossip forward for the
	// same spam-amplification reason as OnProposal. Mirrors rippled's
	// OverlayImpl::relay behavior for TMValidation.
	if trusted {
		e.adaptor.RelayValidation(validation, originPeer)
	}

	return nil
}

// OnTxSet handles receiving a transaction set we requested.
func (e *Engine) OnTxSet(id consensus.TxSetID, txs [][]byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Build and store the transaction set
	txSet, err := e.adaptor.BuildTxSet(txs)
	if err != nil {
		return fmt.Errorf("failed to build tx set: %w", err)
	}

	// Verify the ID matches
	if txSet.ID() != id {
		return fmt.Errorf("tx set ID mismatch: expected %x, got %x", id, txSet.ID())
	}

	// Cache for dispute wiring. Matches rippled's gotTxSet arm at
	// Consensus.h:906 (acquired_.emplace). Late-arriving tx sets
	// retroactively populate any dispute whose disputed tx appears
	// in the new set for some peer.
	if _, already := e.acquiredTxSets[id]; !already {
		e.acquiredTxSets[id] = txSet
		if e.ourTxSet != nil && id != e.ourTxSet.ID() {
			e.createDisputesAgainst(txSet)
			for nodeID, p := range e.proposals {
				if p.TxSet == id {
					if e.disputeTracker.UpdateDisputes(nodeID, txSet) {
						e.peerUnchangedCounter = 0
					}
				}
			}
		}
	}

	// If in establish phase, check for convergence
	if e.phase == consensus.PhaseEstablish {
		e.checkConvergence()
	}

	return nil
}

// createDisputesAgainst diffs a peer's tx set against our current
// proposed tx set and creates a DisputedTx entry for every tx found
// in only one side of the symmetric difference. For each new dispute
// it back-fills per-peer votes from acquired peer positions so the
// count starts out correct.
//
// Matches rippled's createDisputes (Consensus.h:1821-1888). Caller
// must hold e.mu.
func (e *Engine) createDisputesAgainst(peerTxSet consensus.TxSet) {
	if e.ourTxSet == nil || peerTxSet == nil {
		return
	}
	id := peerTxSet.ID()
	if _, seen := e.comparesTxSets[id]; seen {
		return
	}
	e.comparesTxSets[id] = struct{}{}

	if id == e.ourTxSet.ID() {
		return
	}

	ourIDs := e.ourTxSet.TxIDs()
	peerIDs := peerTxSet.TxIDs()

	ours := make(map[consensus.TxID]struct{}, len(ourIDs))
	for _, txID := range ourIDs {
		ours[txID] = struct{}{}
	}
	peers := make(map[consensus.TxID]struct{}, len(peerIDs))
	for _, txID := range peerIDs {
		peers[txID] = struct{}{}
	}

	// txs only in our set: seed ourVote=true and peer-vote=false.
	ourBlobs := e.ourTxSet.Txs()
	for idx, txID := range ourIDs {
		if _, also := peers[txID]; also {
			continue
		}
		if e.disputeTracker.Has(txID) {
			continue
		}
		var blob []byte
		if idx < len(ourBlobs) {
			blob = ourBlobs[idx]
		}
		dispute := e.disputeTracker.CreateDispute(txID, blob, true)
		e.seedDisputeVotes(dispute.TxID)
	}

	// txs only in peer's set: seed ourVote=false.
	peerBlobs := peerTxSet.Txs()
	for idx, txID := range peerIDs {
		if _, also := ours[txID]; also {
			continue
		}
		if e.disputeTracker.Has(txID) {
			continue
		}
		var blob []byte
		if idx < len(peerBlobs) {
			blob = peerBlobs[idx]
		}
		dispute := e.disputeTracker.CreateDispute(txID, blob, false)
		e.seedDisputeVotes(dispute.TxID)
	}
}

// seedDisputeVotes walks every known peer proposal with an acquired
// tx set and records that peer's vote on the new dispute. Runs once
// when a dispute is created (rippled Consensus.h:1874-1881).
// Caller must hold e.mu.
func (e *Engine) seedDisputeVotes(txID consensus.TxID) {
	for nodeID, p := range e.proposals {
		peerSet, ok := e.acquiredTxSets[p.TxSet]
		if !ok {
			continue
		}
		if e.disputeTracker.SetVote(txID, nodeID, peerSet.Contains(txID)) {
			e.peerUnchangedCounter = 0
		}
	}
}

// OnLedger handles receiving a ledger we were missing.
func (e *Engine) OnLedger(id consensus.LedgerID, ledger []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// If we were on wrong ledger, check if this helps
	if e.mode == consensus.ModeWrongLedger {
		l, err := e.adaptor.GetLedger(id)
		if err == nil && l != nil {
			lID := l.ID()
			slog.Info("Acquired missing ledger, restarting round",
				"seq", l.Seq(), "hash", fmt.Sprintf("%x", lID[:8]))
			e.prevLedger = l
			e.state.HaveCorrectLCL = true
			nextRound := consensus.RoundID{
				Seq:        l.Seq() + 1,
				ParentHash: l.ID(),
			}
			// Re-enter consensus with recovering=true. A trusted validator
			// in OpModeFull would normally be ModeProposing; recovering=true
			// drops it to ModeSwitchedLedger for one round. closeLedger
			// and acceptLedger both gate on mode==ModeProposing, so we
			// suppress emission exactly the way rippled does after a
			// wrongLedger recovery (Consensus.h:1107,1457). On the next
			// round (via acceptLedger auto-advance) the engine promotes
			// back to ModeProposing normally.
			proposing := e.adaptor.IsValidator() &&
				e.adaptor.GetOperatingMode() == consensus.OpModeFull
			e.startRoundLocked(nextRound, proposing, true)
		}
	}

	return nil
}

// parentValidations returns the trusted validations the engine has
// recorded for the given ledger ID — passed to
// Adaptor.GenerateFlagLedgerPseudoTxs so the producer can tally
// fee/amendment votes. Callers pass prevLedger.ParentID() (the flag
// ledger's parent) so the lookup matches rippled's
// RCLConsensus.cpp:359-360 getTrustedForLedger(prevLedger->parentHash,
// prevLedger->seq() - 1). Returns nil when the tracker hasn't been
// wired (test fixtures, early startup).
func (e *Engine) parentValidations(id consensus.LedgerID) []*consensus.Validation {
	if e.validationTracker == nil {
		return nil
	}
	return e.validationTracker.GetTrustedValidations(id)
}

// State returns the current consensus state.
func (e *Engine) State() *consensus.RoundState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}

// Mode returns the current consensus mode. Reads the atomic mirror
// so the call is lock-free — see modeAtomic on Engine for the
// rationale (RPC hot path + ABBA deadlock with ledger service mu).
func (e *Engine) Mode() consensus.Mode {
	return consensus.Mode(e.modeAtomic.Load())
}

// Phase returns the current consensus phase.
func (e *Engine) Phase() consensus.Phase {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.phase
}

// IsProposing returns true if we're actively proposing. Lock-free
// read of the atomic mode mirror; called from the RPC server_info
// hot path while ledger.service.s.mu is held — see modeAtomic.
func (e *Engine) IsProposing() bool {
	return consensus.Mode(e.modeAtomic.Load()) == consensus.ModeProposing
}

// Timing returns the consensus timing parameters.
func (e *Engine) Timing() consensus.Timing {
	return e.timing
}

// lastCloseInfo packs the two values returned by GetLastCloseInfo
// into a single allocation so atomic.Pointer can publish them
// together without tearing.
type lastCloseInfo struct {
	Proposers int
	RoundTime time.Duration
}

// GetLastCloseInfo returns the proposer count and convergence time for
// server_info.last_close. Mirrors rippled's NetworkOPs.cpp:2819 — once
// any consensus round has been accepted, we return the strict
// prevProposers_ snapshot from that round (matching rippled exactly).
//
// Bootstrap fallback: a tracker that has never reached acceptLedger
// (e.g. cold start, never promoted to OpModeFull) would otherwise be
// stuck reporting 0 even when trusted peers are actively proposing.
// In that case only, fall back to a freshness-bounded count of recent
// trusted proposers. Issue #421.
func (e *Engine) GetLastCloseInfo() (proposers int, convergeTime time.Duration) {
	if info := e.lastCloseAtomic.Load(); info != nil {
		proposers = info.Proposers
		convergeTime = info.RoundTime
	}
	if proposers > 0 {
		return proposers, convergeTime
	}
	return e.recentTrustedProposerCount(), convergeTime
}

// recentTrustedProposerCount counts distinct trusted nodes whose most
// recent buffered proposal is inside the freshness window. Sourced from
// recentProposals (Consensus.h:626 recentPeerPositions_) so the count
// survives wrongLedger-driven round restarts that empty e.proposals.
// Acquires e.mu.RLock(); cost bounded by the per-node cap of 10.
func (e *Engine) recentTrustedProposerCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.recentProposals) == 0 {
		return 0
	}
	freshness := e.timing.ProposeFreshness
	now := e.adaptor.Now()
	count := 0
	for nodeID, positions := range e.recentProposals {
		if !e.adaptor.IsTrusted(nodeID) {
			continue
		}
		// OnProposal appends under e.mu so slice order is arrival
		// order; iterate newest-first to short-circuit on the first
		// fresh entry.
		for i := len(positions) - 1; i >= 0; i-- {
			if now.Sub(positions[i].Timestamp) > freshness {
				continue
			}
			count++
			break
		}
	}
	return count
}

// storeLastCloseLocked publishes the round-completion stats to the
// atomic mirror. Caller must hold e.mu (writes to e.prevProposers /
// e.prevRoundTime are also expected to happen under that lock so the
// fields and the atomic stay consistent).
func (e *Engine) storeLastCloseLocked() {
	e.lastCloseAtomic.Store(&lastCloseInfo{
		Proposers: e.prevProposers,
		RoundTime: e.prevRoundTime,
	})
}

// Subscribe adds an event subscriber.
func (e *Engine) Subscribe(sub consensus.EventSubscriber) {
	e.eventBus.Subscribe(sub)
}

// Events returns the event channel for direct consumption.
func (e *Engine) Events() <-chan consensus.Event {
	return e.eventBus.Events()
}

// run is the main consensus loop driven by a single global heartbeat,
// matching rippled's processHeartbeatTimer → timerEntry pattern.
//
// time.Ticker silently coalesces ticks when the consumer can't keep up
// — under a stalled timerEntry (e.g. the bootstrap-deadlock class) the
// channel buffer drops ticks without surfacing the back-pressure. We
// observe missed ticks by comparing elapsed wall time between
// invocations against the configured interval and logging when the gap
// exceeds 2× expected. This is a strictly observational signal — the
// next tick still runs unconditionally so a transient stall recovers.
func (e *Engine) run() {
	defer e.wg.Done()

	interval := time.Second
	if e.timing.LedgerMinClose < interval {
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

// MissedHeartbeats returns the total number of heartbeat ticks the
// consensus loop has detected as dropped since process start. Exposed
// for tests and operator tooling that need to assert progress.
func (e *Engine) MissedHeartbeats() uint64 {
	return e.missedHeartbeats.Load()
}

// timerEntry is the single heartbeat dispatch, matching rippled's
// Consensus::timerEntry() (Consensus.h:859-888). Called every
// ledgerGRANULARITY (1s) and dispatches based on current phase.
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

	// Phase work runs in every non-disconnected mode, mirroring rippled
	// NetworkOPs::setHeartbeatTimer which calls mConsensus.timerEntry(...)
	// once numPeers >= minPeerCount (NetworkOPs.cpp:1103) — regardless of
	// CONNECTED / SYNCING / TRACKING / FULL. The proposing/validating gate
	// is per-round: startRoundLocked degrades non-Full rounds to
	// ModeObserving (engine.go:419), and closeLedger / sendValidation gate
	// emission on e.mode == ModeProposing. Without observer-mode advancement
	// a fresh genesis bootstrap deadlocks at OpModeConnected — no round
	// closes, so OnConsensusReached's auto-promote never fires.
	if e.adaptor.GetOperatingMode() == consensus.OpModeDisconnected {
		return
	}

	// checkLedger must run in every non-disconnected mode — it's the
	// Syncing/Tracking → Full recovery path; gating on Full would wedge
	// us permanently once a wrongLedger demotion fires.
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
		// After starting a new round, immediately evaluate the new phase
		// in the same heartbeat tick. Matches rippled's startRoundInternal
		// which calls timerEntry() when peer pressure is detected.
		if e.phase == consensus.PhaseOpen {
			e.phaseOpen()
		}
	}
}

// checkAndStartRoundInner checks if we should start a new round.
// This serves as a fallback in case the auto-advance in acceptLedger
// didn't trigger (e.g., first round after startup).
// Caller must hold e.mu.
func (e *Engine) checkAndStartRoundInner() {
	// Only start if in accepted phase and not on wrong ledger
	if e.phase != consensus.PhaseAccepted {
		return
	}
	if e.mode == consensus.ModeWrongLedger {
		return
	}

	// Get current ledger
	ledger, err := e.adaptor.GetLastClosedLedger()
	if err != nil {
		return
	}

	// Check if we have buffered proposals for this ledger.
	// If so, start immediately (peer pressure will close the open phase right away).
	// Otherwise, wait for the idle interval as before.
	ledgerID := ledger.ID()
	hasBufferedProposals := false
	for _, positions := range e.recentProposals {
		for _, p := range positions {
			if p.PreviousLedger == ledgerID {
				hasBufferedProposals = true
				break
			}
		}
		if hasBufferedProposals {
			break
		}
	}

	if !hasBufferedProposals {
		timeSinceClose := e.adaptor.Now().Sub(ledger.CloseTime())
		if timeSinceClose < e.timing.LedgerIdleInterval {
			return
		}
	}

	// Determine if we should propose
	proposing := e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull

	// Update prevLedger to the current LCL — it may have been changed
	// by an InboundLedger adoption since the last round.
	e.prevLedger = ledger

	// Start the round. Not a recovery — this is the normal idle-timeout
	// kick after acceptance; startRoundLocked picks ModeProposing for a
	// trusted validator in OpModeFull.
	round := consensus.RoundID{
		Seq:        ledger.Seq() + 1,
		ParentHash: ledger.ID(),
	}
	e.startRoundLocked(round, proposing, false)
}

// checkLedger verifies we are on the correct ledger by comparing our
// prevLedger against what the network prefers (from proposal counting).
// If we're on the wrong chain, calls handleWrongLedger to switch.
// Matches rippled's checkLedger() (Consensus.h:1118-1147).
func (e *Engine) checkLedger() {
	if e.prevLedger == nil {
		return
	}
	ourID := e.prevLedger.ID()
	netLgr := e.getNetworkLedger()
	if netLgr != ourID {
		// If the network proposals reference our parent, we just completed
		// the round they're still working on — we're ahead, not wrong.
		// Wait for the network to catch up rather than switching back.
		if netLgr == e.prevLedger.ParentID() {
			return
		}

		// Switch preference: pick whichever ledger has MORE trusted
		// validation support, not strictly fully-validated. Rippled
		// uses vals.getPreferred() (RCLConsensus.cpp:301) which walks a
		// LedgerTrie and returns the ledger with the most validation
		// support on its ancestor chain; our approximation compares
		// the flat trusted-count at each exact hash.
		//
		// The OLD behavior — "only switch if netLgr is fully validated"
		// — could strand a catch-up node on the wrong branch. Example:
		// 2-of-3 trusted validators back the peer branch, but neither
		// has crossed quorum yet because our OWN validation for the
		// same seq is on the other branch. The new rule lets us switch
		// as soon as the PEER branch has MORE support than ours —
		// including the case where we have zero support for ours
		// (which is the common case when we're on a stale branch to
		// begin with).
		//
		// Safety gate: require at least ONE trusted validation on the
		// peer branch. Otherwise we'd flip on nothing but proposals,
		// reintroducing the proposals-only thrash the old gate was
		// installed to prevent.
		if e.validationTracker != nil {
			netSupport := e.validationTracker.GetTrustedSupport(netLgr)
			ourSupport := e.validationTracker.GetTrustedSupport(ourID)
			if netSupport == 0 || netSupport <= ourSupport {
				return
			}
		}

		// Already targeting this ledger — don't spam
		if e.mode == consensus.ModeWrongLedger && e.wrongLedgerID == netLgr {
			return
		}
		slog.Warn("Consensus view changed",
			"phase", e.phase,
			"mode", e.mode,
			"our", fmt.Sprintf("%x", ourID[:8]),
			"net", fmt.Sprintf("%x", netLgr[:8]),
		)
		e.handleWrongLedger(netLgr)
	}
}

// getNetworkLedger determines what ledger the network is working on
// by counting recent proposals from trusted validators.
// Returns the most popular prevLedger ID if a majority of trusted
// proposers agree on a different ledger than ours.
// Simplified substitute for rippled's getPrevLedger() + LedgerTrie.
func (e *Engine) getNetworkLedger() consensus.LedgerID {
	if e.prevLedger == nil {
		return consensus.LedgerID{}
	}
	ourID := e.prevLedger.ID()
	freshness := e.timing.ProposeFreshness
	now := e.adaptor.Now()

	// For each trusted node, take the most recent fresh proposal
	type vote struct {
		prevLedger consensus.LedgerID
	}
	votes := make(map[consensus.NodeID]vote)
	for nodeID, positions := range e.recentProposals {
		if !e.adaptor.IsTrusted(nodeID) {
			continue
		}
		// Slice order is arrival order (OnProposal appends under e.mu);
		// iterate newest-first and take the first fresh entry.
		for i := len(positions) - 1; i >= 0; i-- {
			if now.Sub(positions[i].Timestamp) > freshness {
				continue
			}
			votes[nodeID] = vote{prevLedger: positions[i].PreviousLedger}
			break
		}
	}

	// Include our own position as a vote too. checkConvergence already
	// counts self when tallying tx-set agreement; for consistency, the
	// network-ledger preferred-prevLedger vote should work the same way.
	// Without this, a 3-validator UNL's majority threshold (>len/2) is
	// computed over peers only — two peers disagreeing with us will
	// flip our LCL even though a fair vote would include our own
	// position and produce a 2-2 tie (no switch).
	if e.state != nil && e.state.OurPosition != nil {
		pos := e.state.OurPosition
		if now.Sub(pos.Timestamp) <= freshness {
			if key, err := e.adaptor.GetValidatorKey(); err == nil {
				votes[key] = vote{prevLedger: pos.PreviousLedger}
			}
		}
	}

	// Build the set of hashes already voted for via trusted proposals.
	// Peer-LCL votes for those SAME hashes are redundant and — worse —
	// would double-count a validator that happens to also be connected
	// as a peer (its proposal vote + its peerLCL synthetic vote). We
	// skip them below to match rippled's LedgerTrie which folds votes
	// per ledger, not per signaling channel.
	proposalHashes := make(map[consensus.LedgerID]struct{}, len(votes))
	for _, v := range votes {
		proposalHashes[v.prevLedger] = struct{}{}
	}

	// Fold in peer-reported LCLs from statusChange. A peer that has
	// advanced its LCL but hasn't yet gossipped a proposal to us still
	// contributes a signal about where the network is. We key these on
	// a synthetic NodeID derived from the hash so a single peer's
	// reported LCL counts as one vote regardless of its actual
	// validator pubkey (which we don't know from the status message).
	// The vote set remains deduped by NodeID; and we drop peer-LCL
	// votes whose hash ALREADY has a trusted-proposer vote so a
	// trusted validator connected as a peer isn't counted twice.
	//
	// Gate: ONLY count peer-LCL votes for hashes that have at least
	// one trusted validation already recorded. Without this gate,
	// peers gossiping their non-validated local-build LCLs (the
	// transient "I just built my own L34 in wrongLedger" hashes that
	// every node in a stuck 5-node soak emits) can win the
	// majority-vote tally and push checkLedger into handleWrongLedger
	// for a hash no one can acquire — entrenching the wrongLedger
	// trap rather than escaping it. Trusted-validation backing means
	// the peer-LCL signal aligns with the chain ledgers actually being
	// finalized, not with transient local builds. Observed as the
	// root cause of the iter27 soak stall at L34 (all 5 nodes at L33
	// validated, then goxrpls latched onto rippleds' local L34
	// gossip and stayed in wrongLedger forever).
	for i, h := range e.adaptor.PeerReportedLedgers() {
		if _, already := proposalHashes[h]; already {
			continue
		}
		if e.validationTracker != nil && e.validationTracker.GetTrustedSupport(h) == 0 {
			continue
		}
		var synthKey consensus.NodeID
		// Real validator pubkeys are compressed secp256k1 (0x02/0x03
		// prefix) or ed25519-tagged (0xED). 0xFF is unused by XRPL
		// public-key encoding so synthetic entries can't collide
		// with a real validator key.
		synthKey[0] = 0xFF
		synthKey[1] = byte(i >> 8)
		synthKey[2] = byte(i)
		// Fill the rest with the ledger hash so different reported
		// LCLs from the same ordinal slot stay distinguishable.
		copy(synthKey[3:], h[:30])
		votes[synthKey] = vote{prevLedger: h}
	}

	if len(votes) == 0 {
		return ourID
	}

	// Count votes per prevLedger
	counts := make(map[consensus.LedgerID]int)
	for _, v := range votes {
		counts[v.prevLedger]++
	}

	// Find the most popular
	var bestID consensus.LedgerID
	bestCount := 0
	for id, count := range counts {
		if count > bestCount {
			bestID = id
			bestCount = count
		}
	}

	// Only switch if majority of voters agree AND it's different from ours
	if bestID != ourID && bestCount > len(votes)/2 {
		return bestID
	}
	return ourID
}

// handleWrongLedger switches the engine to the network's preferred ledger.
// Matches rippled's handleWrongLedger() (Consensus.h:1062-1113).
func (e *Engine) handleWrongLedger(netLedgerID consensus.LedgerID) {
	// Step 1: Stop proposing (like rippled's leaveConsensus)
	if e.mode == consensus.ModeProposing {
		e.setMode(consensus.ModeObserving)
	}

	// Step 2: Clear consensus state and replay for new ledger
	// (only if this is a new target ledger)
	if e.prevLedger == nil || netLedgerID != e.prevLedger.ID() {
		e.proposals = make(map[consensus.NodeID]*consensus.Proposal)
		e.disputeTracker = NewDisputeTracker()
		e.acquiredTxSets = make(map[consensus.TxSetID]consensus.TxSet)
		e.comparesTxSets = make(map[consensus.TxSetID]struct{})
		e.peerUnchangedCounter = 0
		e.establishCounter = 0
		e.converged = false
		e.haveCloseTimeConsensus = false
		if e.state != nil {
			e.state.CloseTimes.Peers = make(map[time.Time]int)
		}

		// Replay proposals matching the new ledger
		for nodeID, positions := range e.recentProposals {
			for _, p := range positions {
				if p.PreviousLedger == netLedgerID {
					trusted := e.adaptor.IsTrusted(nodeID)
					existing, exists := e.proposals[nodeID]
					if !exists || p.Position > existing.Position {
						e.proposals[nodeID] = p
					}
					if p.Position == 0 && trusted && e.state != nil {
						e.state.CloseTimes.Peers[p.CloseTime]++
					}
				}
			}
		}
	}

	// Step 3: Try to acquire the correct ledger.
	// First try by hash, then check if the adaptor's LCL has already been
	// updated (e.g., by inbound ledger adoption in the router).
	newLedger, err := e.adaptor.GetLedger(netLedgerID)
	if err != nil || newLedger == nil {
		if lcl, lclErr := e.adaptor.GetLastClosedLedger(); lclErr == nil && lcl != nil && lcl.ID() == netLedgerID {
			newLedger = lcl
			err = nil
		}
	}
	if err == nil && newLedger != nil {
		// Found — restart the round with the correct ledger AND flag
		// recovering=true so the engine enters ModeSwitchedLedger for
		// exactly one round. That mirrors rippled (Consensus.h:1107): a
		// node that just swapped its prior-ledger pointer suppresses its
		// own proposal and validation for the current round to avoid
		// poisoning convergence with stale-view gossip. On the NEXT
		// round (via acceptLedger auto-advance) a trusted validator is
		// promoted back to ModeProposing normally — so we still get
		// full participation, just not on the recovery round itself.
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
		// Not found — enter wrong ledger mode and request from peers
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
		e.adaptor.RequestLedger(netLedgerID)
	}
}

// setMode changes the consensus mode. Caller must hold e.mu.
func (e *Engine) setMode(newMode consensus.Mode) {
	if e.mode == newMode {
		return
	}

	oldMode := e.mode
	e.mode = newMode
	// Mirror to the atomic so IsProposing / Mode can read without
	// taking e.mu.RLock(). The store is paired with the Lock-held
	// write to e.mode above, so a lock-free reader observes either
	// the old or new value (no torn read for an int32) — sufficient
	// for the server_info "are we proposing?" snapshot semantics.
	e.modeAtomic.Store(int32(newMode))

	e.eventBus.Publish(&consensus.ModeChangedEvent{
		OldMode:   oldMode,
		NewMode:   newMode,
		Timestamp: e.adaptor.Now(),
	})

	e.adaptor.OnModeChange(oldMode, newMode)
}

// setPhase changes the consensus phase.
func (e *Engine) setPhase(newPhase consensus.Phase) {
	if e.phase == newPhase {
		return
	}

	oldPhase := e.phase
	oldPhaseDuration := time.Duration(0)
	if e.state != nil && !e.state.PhaseStart.IsZero() {
		oldPhaseDuration = e.adaptor.Now().Sub(e.state.PhaseStart)
	}
	slog.Info("phase transition",
		"t", "consensus",
		"event", "phase-transition",
		"from", oldPhase.String(),
		"to", newPhase.String(),
		"from_duration_ms", oldPhaseDuration.Milliseconds(),
		"mode", e.mode.String(),
	)

	e.phase = newPhase
	if e.state != nil {
		e.state.Phase = newPhase
		e.state.PhaseStart = e.adaptor.Now()
	}

	e.eventBus.Publish(&consensus.PhaseChangedEvent{
		Round:     e.state.Round,
		OldPhase:  oldPhase,
		NewPhase:  newPhase,
		Timestamp: e.adaptor.Now(),
	})

	e.adaptor.OnPhaseChange(oldPhase, newPhase)
}

// shouldCloseLedger checks whether the ledger should be closed now.
// Matches rippled's shouldCloseLedger() (Consensus.cpp:27-103).
func (e *Engine) shouldCloseLedger() bool {
	if e.prevLedger == nil {
		return false
	}
	openTime := time.Since(e.state.StartTime)
	timeSincePrevClose := e.adaptor.Now().Sub(e.prevLedger.CloseTime())

	// Sanity check: if timeSincePrevClose or prevRoundTime are unreasonable,
	// just close (matches rippled lines 52-64).
	if e.prevRoundTime < 0 || e.prevRoundTime > 10*time.Minute ||
		timeSincePrevClose > 10*time.Minute {
		return true
	}

	// Count how many trusted peers have already closed (sent proposals)
	proposersClosed := 0
	for nodeID := range e.proposals {
		if e.adaptor.IsTrusted(nodeID) {
			proposersClosed++
		}
	}

	// Count trusted validators that have validated our previous ledger.
	// Reads the PERSISTENT validation tracker (not the round-scoped
	// e.validations, which is reset at round start and so always zero
	// at the beginning of a round before any current-round validations
	// arrive). Matches rippled's adaptor_.proposersValidated() at
	// RCLConsensus.cpp:281 which reads the persistent Validations
	// store. Fixes the pre-R5.9 behavior where early-close peer
	// pressure from validations was invisible until mid-round.
	proposersValidated := 0
	if e.prevLedger != nil && e.validationTracker != nil {
		proposersValidated = e.validationTracker.ProposersValidated(e.prevLedger.ID())
	}

	// Peer pressure: if more than half of previous round's proposers
	// have already closed or validated, close immediately (matches rippled lines 67-73).
	closed := proposersClosed + proposersValidated
	if closed > e.prevProposers/2 {
		slog.Info("shouldClose peer-pressure",
			"t", "consensus",
			"event", "should-close-pressure",
			"prev_proposers", e.prevProposers,
			"closed", proposersClosed,
			"validated", proposersValidated,
			"open_ms", openTime.Milliseconds(),
		)
		return true
	}
	// Rate-limit miss trace: first tick + one re-emit at ~1s
	// when the LedgerMinClose path takes over.
	if openTime < 100*time.Millisecond || (openTime > 1000*time.Millisecond && openTime < 1100*time.Millisecond) {
		slog.Info("shouldClose peer-pressure miss",
			"t", "consensus",
			"event", "should-close-miss",
			"prev_proposers", e.prevProposers,
			"closed", proposersClosed,
			"validated", proposersValidated,
			"open_ms", openTime.Milliseconds(),
		)
	}

	// No transactions: only close at the idle interval.
	// Matches rippled lines 75-80.
	anyTransactions := len(e.adaptor.GetPendingTxs()) > 0
	if !anyTransactions {
		return timeSincePrevClose >= e.timing.LedgerIdleInterval
	}

	// Preserve minimum ledger open time (matches rippled lines 83-88).
	if openTime < e.timing.LedgerMinClose {
		return false
	}

	// Don't close faster than half the previous round time,
	// so slower validators can keep up (matches rippled lines 93-98).
	if openTime < e.prevRoundTime/2 {
		return false
	}

	return true
}

// phaseOpen evaluates whether to close the ledger during the open phase.
// Called by timerEntry on each heartbeat. Matches rippled's phaseOpen()
// (Consensus.h:1168-1239).
// Caller must hold e.mu.
func (e *Engine) phaseOpen() {
	if e.shouldCloseLedger() {
		e.eventBus.Publish(&consensus.TimerFiredEvent{
			Timer:     consensus.TimerLedgerClose,
			Round:     e.state.Round,
			Timestamp: e.adaptor.Now(),
		})
		e.closeLedger()
	}
}

// closeLedger transitions from open to establish phase.
// Reference: rippled Consensus.h closeLedger() (~line 1434)
func (e *Engine) closeLedger() {
	// #422: if peer proposers from the prior round + self can't
	// meet quorum, this round almost certainly won't reach consensus.
	// Skipped before the first completed round, where prevProposers
	// carries no signal.
	if e.consensusCount > 0 {
		quorum := e.adaptor.GetQuorum()
		if e.prevProposers+1 < quorum {
			seq := uint32(0)
			if e.prevLedger != nil {
				seq = e.prevLedger.Seq() + 1
			}
			slog.Info("consensus close — peer proposers below quorum (likely stall)",
				"t", "consensus",
				"event", "close-below-quorum",
				"peer_proposers", e.prevProposers,
				"quorum", quorum,
				"unl_size", len(e.adaptor.GetTrustedValidators()),
				"seq", seq,
			)
		}
	}

	// Filter pending txs through the open-ledger gate when proposing,
	// matching app_.openLedger().current()->txs at RCLConsensus.cpp:333-349.
	// In non-proposing modes the position isn't broadcast, so skip the
	// per-round apply cost.
	var txs [][]byte
	if e.mode == consensus.ModeProposing || e.adaptor.IsStandalone() {
		txs = e.adaptor.GetProposableTxs(e.prevLedger)
	} else {
		txs = e.adaptor.GetPendingTxs()
	}

	// Inject flag-ledger / voting-ledger pseudo-txs BEFORE building
	// the tx set, so the resulting tx-set hash matches what rippled
	// computes for the same round (RCLConsensus.cpp:351-381). The
	// gate mirrors rippled's `standalone() || (proposing && !wrongLCL)`
	// at RCLConsensus.cpp:352 — ModeProposing already excludes
	// wrongLedger by construction, and the standalone branch keeps
	// single-node test setups injecting pseudo-txs even when they
	// haven't transitioned to proposing.
	if e.prevLedger != nil && (e.mode == consensus.ModeProposing || e.adaptor.IsStandalone()) {
		prev := e.prevLedger
		switch {
		case consensus.IsFlagLedger(prev.Seq()):
			parentVals := e.parentValidations(prev.ParentID())
			if extra := e.adaptor.GenerateFlagLedgerPseudoTxs(prev, parentVals); len(extra) > 0 {
				txs = append(txs, extra...)
			}
		case consensus.IsVotingLedger(prev.Seq()) && e.adaptor.IsFeatureEnabledOnLedger(prev, "NegativeUNL"):
			if extra := e.adaptor.GenerateNegativeUNLPseudoTx(prev); len(extra) > 0 {
				txs = append(txs, extra...)
			}
		}
	}

	txSet, err := e.adaptor.BuildTxSet(txs)
	if err != nil {
		slog.Error("Failed to build tx set, falling back to empty set",
			"t", "Consensus",
			"round", e.state.Round,
			"pending_txs", len(txs),
			"err", err,
		)

		// Fall back to an empty tx set so consensus can still advance.
		txSet, err = e.adaptor.BuildTxSet(nil)
		if err != nil {
			slog.Error("Failed to build empty tx set, cannot close ledger",
				"t", "Consensus",
				"round", e.state.Round,
				"err", err,
			)
			e.setMode(consensus.ModeObserving)
			return
		}
	}
	e.ourTxSet = txSet
	// Our own tx set is immediately "acquired" — matches rippled's
	// closeLedger at Consensus.h:1449 (acquired_.emplace after
	// adaptor_.onClose). Dispute wiring reads this to recognize
	// proposals that reference our position.
	e.acquiredTxSets[txSet.ID()] = txSet

	// Use raw now — rippled sets rawCloseTimes_.self = now_ (Consensus.h:1441).
	// Rounding only happens later via effCloseTime() at acceptance.
	closeTime := e.adaptor.Now()
	e.state.CloseTimes.Self = closeTime

	// If proposing, create and broadcast our proposal
	if e.mode == consensus.ModeProposing {
		nodeID, err := e.adaptor.GetValidatorKey()
		if err == nil {
			proposal := &consensus.Proposal{
				Round:          e.state.Round,
				NodeID:         nodeID,
				Position:       0,
				TxSet:          txSet.ID(),
				CloseTime:      closeTime,
				PreviousLedger: e.prevLedger.ID(),
				Timestamp:      e.adaptor.Now(),
			}

			if err := e.adaptor.SignProposal(proposal); err == nil {
				e.state.OurPosition = proposal
				e.enqueueProposalBroadcastLocked(proposal)
				txSetID := txSet.ID()
				prevID := e.prevLedger.ID()
				slog.Info("our initial position",
					"t", "consensus-build",
					"event", "our-position",
					"round_seq", e.state.Round.Seq,
					"prev", fmt.Sprintf("%x", prevID[:8]),
					"tx_set", fmt.Sprintf("%x", txSetID[:8]),
					"tx_count", len(txs),
					"close_time", closeTime.UTC().Format(time.RFC3339Nano),
					"mode", e.mode.String(),
				)
			}
		}
	}

	// Seed disputes against every peer position whose tx set we hold,
	// and fire acquisition for the rest. Matches Consensus.h:1461-1467
	// plus the implicit acquisition in rippled's playbackProposals via
	// gotTxSet — required because OnProposal isn't re-fired for
	// replayed (buffered) proposals.
	requested := make(map[consensus.TxSetID]struct{})
	for _, p := range e.proposals {
		if peerSet, ok := e.acquiredTxSets[p.TxSet]; ok {
			e.createDisputesAgainst(peerSet)
			continue
		}
		if e.ourTxSet != nil && p.TxSet == e.ourTxSet.ID() {
			continue
		}
		// Try adaptor cache; otherwise dedupe-by-id and request.
		if peerSet, err := e.adaptor.GetTxSet(p.TxSet); err == nil && peerSet != nil {
			e.acquiredTxSets[p.TxSet] = peerSet
			e.createDisputesAgainst(peerSet)
			continue
		}
		if _, already := requested[p.TxSet]; already {
			continue
		}
		requested[p.TxSet] = struct{}{}
		e.adaptor.RequestTxSet(p.TxSet)
	}

	// Move to establish phase
	e.setPhase(consensus.PhaseEstablish)
}

// phaseEstablish re-evaluates convergence during the establish phase.
// Called by timerEntry on each heartbeat. Matches rippled's phaseEstablish()
// (Consensus.h:1366-1430).
// Caller must hold e.mu.
func (e *Engine) phaseEstablish() {
	roundTime := time.Since(e.roundStartTime)

	// Pause the round if our prev LCL has run past the validated chain
	// and a quorum-blocking share of trusted validators is lagging or
	// offline. Mirrors rippled's Consensus<T>::shouldPause check at
	// Consensus.h:1403 — placed BEFORE the abandon/timeout/MovedOn/
	// convergence accept paths so a pause prevents LCL advance, but the
	// pause is bounded by ledgerMAX_CONSENSUS inside shouldPause itself
	// (Consensus.h:1271) so a stuck round still eventually abandons via
	// the hard ceiling below. Without this gate the engine close-then-
	// rollback-cycles every heartbeat against an unreachable quorum and
	// drifts the local closed_ledger arbitrarily far past the network's
	// validated tip (#451).
	if e.shouldPause(roundTime) {
		return
	}

	// Absolute hard ceiling: abandon the round once we exceed the
	// ledgerABANDON_CONSENSUS clamp. Rippled treats this state as
	// ConsensusState::Expired (Consensus.cpp:253-263) and responds by
	// calling leaveConsensus() (Consensus.h:1760-1785): bow out of
	// proposing, then fall through to accept — do NOT restart the round
	// with an empty set. We mirror that here: setMode(Observing) if we
	// were proposing, then accept with ResultAbandoned so higher layers
	// can distinguish a hard abandon from the soft LedgerMaxConsensus
	// force-accept below.
	if e.timing.LedgerAbandonConsensus > 0 && e.abandonDeadlineExceeded(roundTime) {
		slog.Warn("consensus taken too long, abandoning round",
			"t", "Consensus",
			"round", e.state.Round,
			"round_time", roundTime,
			"prev_round_time", e.prevRoundTime,
			"max_consensus", e.timing.LedgerMaxConsensus,
			"abandon_consensus", e.timing.LedgerAbandonConsensus,
		)
		e.eventBus.Publish(&consensus.TimerFiredEvent{
			Timer:     consensus.TimerRoundTimeout,
			Round:     e.state.Round,
			Timestamp: e.adaptor.Now(),
		})
		// Rippled's leaveConsensus: stop proposing if we were.
		if e.mode == consensus.ModeProposing {
			e.setMode(consensus.ModeObserving)
		}
		e.acceptLedger(consensus.ResultAbandoned)
		return
	}

	// (No soft-timeout-to-accept here on purpose.) Rippled's
	// phaseEstablish has exactly three terminal states — Yes / MovedOn
	// / Expired (Consensus.cpp:176-263) — and Expired only fires at the
	// clamp(prevRoundTime * factor, ledgerMAX_CONSENSUS, ledger
	// ABANDON_CONSENSUS) deadline, which is the hard-abandon path
	// above. There is no equivalent "soft" force-accept at
	// ledgerMAX_CONSENSUS; rippled stays in establish as long as
	// neither convergence nor the MovedOn-by-peer-finished threshold
	// has fired. The earlier goxrpl soft-timeout at LedgerMaxConsensus
	// was a goxrpl-only behavior that, combined with the old broad
	// consensusFail rule, advanced the LCL on a never-emitted ledger
	// every 15s and drifted closed_seq arbitrarily far past the
	// validated tip in mixed UNL soaks (#451).

	// Run convergence before MovedOn — rippled's checkConsensus picks
	// Yes over MovedOn so a near-simultaneous-finish round still ends
	// in Success rather than chronic 1-round lag.
	e.establishCounter++
	e.peerUnchangedCounter++

	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil {
		e.updatePosition()
	}
	e.updateCloseTimePosition()
	e.checkConvergence()
	if e.phase != consensus.PhaseEstablish {
		// checkConvergence accepted — round is over.
		return
	}

	// MovedOn detection — checkConsensus at Consensus.cpp:239-246:
	// 80% of prev proposers have validated a ledger past our prev.
	// Denominator is current-round proposer count, not prevProposers
	// (Consensus.h:1740-1751 passes agree+disagree); peers stop
	// proposing for our round as they advance.
	if e.prevLedger != nil && e.validationTracker != nil &&
		roundTime > e.timing.LedgerMinConsensus {
		finished := e.validationTracker.ProposersFinished(e.prevLedger)
		currentProposers := len(e.proposals)

		var fired bool
		if currentProposers == 0 {
			// checkConsensusReached(_, 0, ...) at Consensus.cpp:129-140.
			fired = roundTime > e.timing.LedgerMaxConsensus
		} else {
			fired = finished*100 >= currentProposers*e.thresholds.MinConsensusPct
		}

		if fired {
			slog.Info("consensus moved on, accepting",
				"t", "consensus",
				"event", "moved-on",
				"seq", e.state.Round.Seq,
				"finished", finished,
				"current_proposers", currentProposers,
				"prev_proposers", e.prevProposers,
				"round_time_ms", roundTime.Milliseconds(),
			)
			e.acceptLedger(consensus.ResultMovedOn)
			return
		}
	}
}

// shouldPause mirrors rippled's Consensus<T>::shouldPause at
// Consensus.h:1241-1362. It returns true when the establish phase
// should suspend progress for one heartbeat — because our previous
// LCL has run past the fully-validated tip and a quorum-blocking
// share of trusted validators is lagging or offline. A paused round
// returns from phaseEstablish without calling acceptLedger, so the
// local closed_ledger does NOT advance further away from validated
// state on this tick. The pause naturally clears once the round
// exceeds LedgerMaxConsensus (the abandon path below then takes over)
// or peers catch up, whichever comes first.
//
// Without this gate the engine close-then-rollback-cycles every
// heartbeat when peers can't form quorum, drifting the local LCL
// arbitrarily far past the validated tip and presenting a
// permanently-diverging view to the network (#451).
//
// Caller must hold e.mu.
func (e *Engine) shouldPause(roundTime time.Duration) bool {
	if e.prevLedger == nil {
		return false
	}
	// Rippled's early-out: not a validator, no validation history,
	// nothing ahead, or the round already passed the hard timeout
	// (Consensus.h:1269-1276). Skipping when we have no prior
	// validation lets bootstrap rounds run normally — pause is only a
	// guard against ONGOING drift, not a startup gate.
	if !e.adaptor.IsValidator() {
		return false
	}
	if e.ourLastValidatedSeq == 0 {
		return false
	}
	if e.timing.LedgerMaxConsensus > 0 && roundTime > e.timing.LedgerMaxConsensus {
		return false
	}

	prevSeq := e.prevLedger.Seq()
	validatedSeq := e.validatedSeqLocked()
	if validatedSeq >= prevSeq {
		return false
	}
	ahead := prevSeq - validatedSeq
	if ahead == 0 {
		return false
	}

	trusted := e.adaptor.GetTrustedValidators()
	totalValidators := len(trusted)
	if totalValidators == 0 {
		return false
	}
	quorum := e.adaptor.GetQuorum()
	if quorum == 0 {
		return false
	}

	laggards, offline := e.countLaggardsAndOfflineLocked(prevSeq, trusted)
	if laggards == 0 {
		return false
	}

	// Phase-progressive threshold (Consensus.h:1314-1349). Each
	// additional ledger we are ahead cycles us through 5 phases of
	// increasing strictness; at phase 0 even a single laggard pauses
	// us, at maxPausePhase we pause unconditionally.
	const maxPausePhase = 4
	phase := int(ahead-1) % (maxPausePhase + 1)

	switch phase {
	case 0:
		// Pause when laggards+offline can't be tolerated by quorum slack
		// (Consensus.h:1321-1325).
		if laggards+offline > totalValidators-quorum {
			return logPauseLocked(e, ahead, laggards, offline, totalValidators, quorum, phase)
		}
	case maxPausePhase:
		// No tolerance — strictest phase (Consensus.h:1326-1329).
		return logPauseLocked(e, ahead, laggards, offline, totalValidators, quorum, phase)
	default:
		// Intermediate phases (Consensus.h:1330-1349): require the
		// non-laggard ratio to clear quorum + linear share of slack.
		nonLaggards := float64(totalValidators - laggards - offline)
		quorumRatio := float64(quorum) / float64(totalValidators)
		allowedDissent := 1.0 - quorumRatio
		phaseFactor := float64(phase) / float64(maxPausePhase)
		if nonLaggards/float64(totalValidators) < quorumRatio+(allowedDissent*phaseFactor) {
			return logPauseLocked(e, ahead, laggards, offline, totalValidators, quorum, phase)
		}
	}
	return false
}

// validatedSeqLocked returns the seq of the most recently fully-validated
// ledger (zero if none). Reads the adaptor's validated-hash + ledger
// pair so we don't have to take a separate snapshot of validated state
// inside the engine. Caller must hold e.mu (the adaptor lookups are
// independent of engine state but stay under the lock for consistency
// with the rest of shouldPause).
func (e *Engine) validatedSeqLocked() uint32 {
	vh := e.adaptor.GetValidatedLedgerHash()
	if vh == (consensus.LedgerID{}) {
		return 0
	}
	vl, err := e.adaptor.GetLedger(vh)
	if err != nil || vl == nil {
		return 0
	}
	return vl.Seq()
}

// isBuildCompatibleWithValidatedLocked mirrors rippled's
// LedgerMaster::isCompatible (LedgerMaster.cpp:135-160) which calls
// areCompatible(validatedLedger, built, ...) at View.cpp:797-857.
// Returns true when the locally built ledger has the validated tip on
// its ancestry chain, or when we lack enough state to disprove
// compatibility (rippled treats hashOfSeq returning nullopt as no
// incompatibility detected — same semantic here).
//
// The three branches mirror areCompatible's three-way fork:
//  1. validatedSeq <  builtSeq: walk built back to validatedSeq via
//     ParentID and compare to the validated hash.
//  2. validatedSeq == builtSeq: hash equality at the same seq.
//  3. validatedSeq >  builtSeq: walk validatedLedger back to builtSeq
//     and compare to the built hash.
//
// Missing intermediate ancestors → return true (compatible). This
// matches rippled's "hashOfSeq returned nullopt → no rejection" rule
// at View.cpp:812 / 828.
//
// Caller must hold e.mu.
func (e *Engine) isBuildCompatibleWithValidatedLocked(built consensus.Ledger) bool {
	if built == nil {
		return true
	}
	vh := e.adaptor.GetValidatedLedgerHash()
	if vh == (consensus.LedgerID{}) {
		return true
	}
	vl, err := e.adaptor.GetLedger(vh)
	if err != nil || vl == nil {
		return true
	}
	validatedSeq := vl.Seq()
	builtSeq := built.Seq()

	if validatedSeq == builtSeq {
		return built.ID() == vh
	}

	if validatedSeq < builtSeq {
		current := built
		// Walk back to validatedSeq via parent pointers. The first hop
		// drops to the parent of `built` — which is our prevLedger and
		// is always known — so the walk is well-defined for the common
		// case where validatedSeq == builtSeq-1 (the #451 fact pattern).
		// Deeper walks rely on the adaptor having those ancestors;
		// missing ancestors fall through to compatible per rippled.
		for current != nil && current.Seq() > validatedSeq {
			parent, err := e.adaptor.GetLedger(current.ParentID())
			if err != nil || parent == nil {
				return true
			}
			current = parent
		}
		if current == nil || current.Seq() != validatedSeq {
			return true
		}
		return current.ID() == vh
	}

	// validatedSeq > builtSeq: walk validated back to builtSeq.
	current := vl
	for current != nil && current.Seq() > builtSeq {
		parent, err := e.adaptor.GetLedger(current.ParentID())
		if err != nil || parent == nil {
			return true
		}
		current = parent
	}
	if current == nil || current.Seq() != builtSeq {
		return true
	}
	return current.ID() == built.ID()
}

// validationLaggardFreshness mirrors rippled's
// parms_.validationFRESHNESS = 20s (Validations.h:89). A peer whose
// latest tracked validation is older than this window is considered
// offline for laggard accounting, not a laggard — even if its seq
// would otherwise place it behind us. The shorter window (vs the
// 3m/5m isCurrent windows used elsewhere) reflects laggards' tighter
// "did this peer send a validation in the most recent ledger interval"
// semantic at Validations.h:1136-1140.
const validationLaggardFreshness = 20 * time.Second

// countLaggardsAndOfflineLocked partitions trusted validators (other
// than ourselves) by their latest fresh tracked validation:
//   - laggards: validator whose latest fresh validation is at
//     seq < prevSeq — they have NOT advanced past our prev, so a
//     quorum that includes them can't form on our seq.
//   - offline: validator with no tracked validation, or with only a
//     stale one (outside the laggard-freshness window).
//
// A validator whose latest fresh validation is at seq >= prevSeq is
// current and contributes to neither count. Mirrors rippled's
// RCLValidations::laggards (Validations.h:1128-1147): only fresh
// validations are iterated; a fresh entry at seq < prev increments
// the laggards counter, all other keys (no entry, stale entry) stay
// in trustedKeys and are counted as offline at Consensus.h:1255.
//
// Caller must hold e.mu.
func (e *Engine) countLaggardsAndOfflineLocked(prevSeq uint32, trusted []consensus.NodeID) (laggards, offline int) {
	if e.validationTracker == nil {
		return 0, 0
	}
	self, _ := e.adaptor.GetValidatorKey()
	now := e.adaptor.Now()
	for _, k := range trusted {
		if k == self {
			continue
		}
		v := e.validationTracker.GetLatestValidation(k)
		if v == nil {
			offline++
			continue
		}
		seen := v.SeenTime
		if seen.IsZero() {
			seen = v.SignTime
		}
		if !seen.IsZero() && now.Sub(seen) > validationLaggardFreshness {
			offline++
			continue
		}
		if v.LedgerSeq < prevSeq {
			laggards++
		}
	}
	return laggards, offline
}

// logPauseLocked emits the consensus-pause telemetry line and returns
// true so the caller can `return logPauseLocked(...)` to combine the
// trace with the return value. Matches rippled's pausing log at
// Consensus.h:1353 in payload shape (validators/laggards/offline/quorum).
func logPauseLocked(e *Engine, ahead uint32, laggards, offline, totalValidators, quorum int, phase int) bool {
	seq := uint32(0)
	if e.prevLedger != nil {
		seq = e.prevLedger.Seq()
	}
	slog.Info("consensus pause — ahead of validated, peers lagging",
		"t", "consensus",
		"event", "consensus-pause",
		"working_seq", seq,
		"ahead", ahead,
		"validators", totalValidators,
		"laggards", laggards,
		"offline", offline,
		"quorum", quorum,
		"phase", phase,
	)
	return true
}

// abandonDeadlineExceeded reports whether the current round has run
// past the ledgerABANDON_CONSENSUS clamp. The effective hard deadline
// is std::clamp(prevRoundTime * factor, LedgerMaxConsensus,
// LedgerAbandonConsensus) — see Consensus.cpp:253-258.
// Caller must hold e.mu.
func (e *Engine) abandonDeadlineExceeded(roundTime time.Duration) bool {
	lo := e.timing.LedgerMaxConsensus
	hi := e.timing.LedgerAbandonConsensus
	if hi <= 0 {
		return false
	}
	// Rippled's clamp(maxAgreeTime, lo, hi): factor×previous, clamped
	// to [lo, hi]. Factor 0 (not configured) disables the scaling and
	// falls back to the absolute ceiling.
	var deadline time.Duration
	if e.timing.LedgerAbandonConsensusFactor > 0 && e.prevRoundTime > 0 {
		deadline = e.prevRoundTime * time.Duration(e.timing.LedgerAbandonConsensusFactor)
	} else {
		deadline = hi
	}
	if lo > 0 && deadline < lo {
		deadline = lo
	}
	if deadline > hi {
		deadline = hi
	}
	return roundTime > deadline
}

// checkConvergence drives the accept gate. Matches rippled's
// phaseEstablish → haveConsensus flow (Consensus.h:1400-1422):
// once we've spent ledgerMIN_CONSENSUS in establish and enough peers
// match our position, we accept. The popularity-of-whole-tx-set vote
// that previously lived here was strictly coarser than per-tx
// re-voting and would strand a node whose position differed from
// every peer in the small-set symmetric-difference case (issue #266).
// Per-tx migration now happens in updatePosition, driven by the
// dispute tracker.
func (e *Engine) checkConvergence() {
	if e.phase != consensus.PhaseEstablish {
		return
	}

	// In wrongLedger mode the node has no result_/position and rippled
	// never reaches haveConsensus/onAccept for that round (Consensus.h
	// haveConsensus asserts result_ != null, and result_ is only set by
	// closeLedger which is gated on mode != wrongLedger via the
	// shouldClose path).
	//
	// Mirror that here: don't accept in wrongLedger. Otherwise the
	// observer fallback in countAgreement triggers accept on peer-peer
	// agreement, our local prev_ledger walks past the validated tip on
	// every empty wrongLedger build, the next round's checkLedger sees
	// our local hash ≠ network's, re-enters wrongLedger, repeat. In a
	// 5-node soak with quorum=4 this strands the network permanently —
	// the wrongLedger node's full-validation contribution is lost so
	// the remaining 3 trusted validators can't form the 4th vote.
	// Observed as iter27 (L34) and iter28 (L38) stalls.
	if e.mode == consensus.ModeWrongLedger {
		return
	}

	// Minimum time in establish phase before accepting consensus.
	// Matches rippled's checkConsensus(): currentAgreeTime <= ledgerMIN_CONSENSUS.
	if e.adaptor.Now().Sub(e.state.PhaseStart) <= e.timing.LedgerMinConsensus {
		return
	}

	agree, disagree := e.countAgreement()
	total := agree + disagree
	if total == 0 {
		return
	}

	// EarlyConvergencePct is a goXRPL-local gate for flagging a round
	// as "converged" for observability (e.g., server_info). Acceptance
	// uses MinConsensusPct (rippled's minCONSENSUS_PCT=80).
	if agree*100 >= total*e.thresholds.EarlyConvergencePct {
		e.converged = true
		e.state.Converged = true
	}

	if agree*100 < total*e.thresholds.MinConsensusPct {
		return
	}

	// Close-time consensus is required before accepting — match
	// rippled Consensus.h:1406-1411.
	if !e.haveCloseTimeConsensus {
		e.updateCloseTimePosition()
		if !e.haveCloseTimeConsensus {
			return
		}
	}

	e.acceptLedger(consensus.ResultSuccess)
}

// countAgreement returns the number of participating proposers whose
// current position matches ours (agree) and the number whose
// position differs (disagree). When we are proposing, we count
// ourselves as an agreeing participant, matching rippled's
// haveConsensus where currPeerPositions_ excludes self and the
// threshold denominator adds +1 for the proposer. (Our e.proposals
// map likewise excludes self.)
//
// Matches rippled's haveConsensus tally (Consensus.h:1688-1707).
// Caller must hold e.mu.
func (e *Engine) countAgreement() (agree, disagree int) {
	var ourTxSet consensus.TxSetID
	haveOurs := false
	if e.state != nil && e.state.OurPosition != nil {
		ourTxSet = e.state.OurPosition.TxSet
		haveOurs = true
	} else if e.ourTxSet != nil {
		ourTxSet = e.ourTxSet.ID()
		haveOurs = true
	}
	if !haveOurs {
		// Observer without a position: count peer-peer agreement on
		// the most popular tx set. This preserves the pre-E2 behavior
		// for non-proposing nodes that still need a convergence
		// signal for acceptLedger.
		counts := make(map[consensus.TxSetID]int)
		for nodeID, p := range e.proposals {
			if e.adaptor.IsTrusted(nodeID) {
				counts[p.TxSet]++
			}
		}
		var best int
		for _, c := range counts {
			if c > best {
				best = c
			}
		}
		agree = best
		for _, c := range counts {
			if c != best {
				disagree += c
			}
		}
		return agree, disagree
	}

	for nodeID, p := range e.proposals {
		if !e.adaptor.IsTrusted(nodeID) {
			continue
		}
		if p.TxSet == ourTxSet {
			agree++
		} else {
			disagree++
		}
	}
	if e.mode == consensus.ModeProposing {
		agree++
	}
	return agree, disagree
}

// updatePosition runs the per-tx dispute re-vote and, if any
// dispute flipped our vote, rebuilds our tx set from the inclusion
// decisions and rebroadcasts the new position.
//
// Matches rippled's updateOurPositions TX arm (Consensus.h:1492-1678):
// stale-proposal pruning with unVote, disputeTracker.UpdateOurVote,
// rebuild ourTxSet via ± the flipped disputes, sign/propose, and
// ripple the new position through updateDisputes for peers matching.
//
// Caller must hold e.mu.
func (e *Engine) updatePosition() {
	if e.state == nil {
		return
	}

	// Prune stale peer proposals. A peer that stops proposing within
	// a round loses its votes on every dispute so it can't coast.
	// Matches rippled Consensus.h:1509-1528.
	cutoff := e.adaptor.Now().Add(-e.timing.ProposeFreshness)
	for nodeID, p := range e.proposals {
		if p.Timestamp.IsZero() {
			continue
		}
		if p.Timestamp.Before(cutoff) {
			delete(e.proposals, nodeID)
			if e.disputeTracker != nil {
				e.disputeTracker.UnVote(nodeID)
			}
		}
	}

	if e.disputeTracker == nil || e.ourTxSet == nil {
		return
	}

	// Re-vote each dispute given the current converge percent. Only
	// proposing nodes can shift their own position; observers still
	// run the state-machine bookkeeping so avalanche levels are
	// consistent across the round, but we gate flips on proposing.
	proposing := e.mode == consensus.ModeProposing
	disputeCount := e.disputeTracker.Count()
	changed := e.disputeTracker.UpdateOurVote(e.convergePercent(), proposing, e.parms)

	if disputeCount > 0 || proposing {
		var ourSetID consensus.TxSetID
		ourSetSize := -1
		if e.ourTxSet != nil {
			ourSetID = e.ourTxSet.ID()
			ourSetSize = e.ourTxSet.Size()
		}
		slog.Info("update position",
			"t", "consensus",
			"event", "update-position",
			"seq", e.state.Round.Seq,
			"mode", e.mode.String(),
			"converge_pct", e.convergePercent(),
			"disputes", disputeCount,
			"flipped", len(changed),
			"our_txset", fmt.Sprintf("%x", ourSetID[:8]),
			"our_tx_count", ourSetSize,
			"acquired_txsets", len(e.acquiredTxSets),
			"peer_proposals", len(e.proposals),
		)
	}

	if !proposing || len(changed) == 0 {
		return
	}

	// Rebuild our proposed tx set from the dispute decisions. We
	// start from the current ourTxSet blob list + txID index, then
	// for each changed dispute: if the new vote is yes, add the tx
	// blob (from the dispute); otherwise drop it.
	currentBlobs := e.ourTxSet.Txs()
	currentIDs := e.ourTxSet.TxIDs()
	idSet := make(map[consensus.TxID]int, len(currentIDs))
	for idx, id := range currentIDs {
		idSet[id] = idx
	}

	newBlobs := make([][]byte, 0, len(currentBlobs)+len(changed))
	keep := make(map[consensus.TxID]bool, len(currentIDs))
	for _, id := range currentIDs {
		keep[id] = true
	}
	for _, txID := range changed {
		dispute := e.disputeTracker.GetDispute(txID)
		if dispute == nil {
			continue
		}
		if dispute.OurVote {
			if !keep[txID] {
				keep[txID] = true
			}
		} else {
			keep[txID] = false
		}
	}
	// Preserve original order for txs we keep that were already in
	// ours, then append newly-voted-in disputes.
	for idx, id := range currentIDs {
		if keep[id] {
			newBlobs = append(newBlobs, currentBlobs[idx])
		}
	}
	for _, txID := range changed {
		if _, already := idSet[txID]; already {
			continue
		}
		if !keep[txID] {
			continue
		}
		dispute := e.disputeTracker.GetDispute(txID)
		if dispute == nil || dispute.Tx == nil {
			continue
		}
		newBlobs = append(newBlobs, dispute.Tx)
	}

	newTxSet, err := e.adaptor.BuildTxSet(newBlobs)
	if err != nil || newTxSet == nil {
		slog.Warn("updatePosition: failed to rebuild tx set after dispute re-vote",
			"err", err,
		)
		return
	}

	// No-op if rebuilding produced the same set (all flips cancelled
	// each other out, or BuildTxSet deduped).
	if newTxSet.ID() == e.ourTxSet.ID() {
		return
	}

	e.ourTxSet = newTxSet
	e.acquiredTxSets[newTxSet.ID()] = newTxSet
	// Broadcasting a new position requires BOTH the current OurPosition
	// (for the Position sequence bump) and a prevLedger (for the
	// PreviousLedger field). A unit-test harness that seeds the engine
	// without calling Start() has prevLedger == nil — we still want
	// ourTxSet to update so the per-tx re-vote is observable, we just
	// can't emit a proposal in that scenario.
	if e.state.OurPosition != nil && e.prevLedger != nil {
		nodeID, _ := e.adaptor.GetValidatorKey()
		proposal := &consensus.Proposal{
			Round:          e.state.Round,
			NodeID:         nodeID,
			Position:       e.state.OurPosition.Position + 1,
			TxSet:          newTxSet.ID(),
			CloseTime:      e.state.OurPosition.CloseTime,
			PreviousLedger: e.prevLedger.ID(),
			Timestamp:      e.adaptor.Now(),
		}
		if err := e.adaptor.SignProposal(proposal); err == nil {
			e.state.OurPosition = proposal
			e.enqueueProposalBroadcastLocked(proposal)
		}
	}

	// Refresh per-peer votes for peers whose position matches the
	// new set — rippled's Consensus.h:1665-1670 path after
	// result_->position change.
	for nodeID, p := range e.proposals {
		if p.TxSet != newTxSet.ID() {
			continue
		}
		if e.disputeTracker.UpdateDisputes(nodeID, newTxSet) {
			e.peerUnchangedCounter = 0
		}
	}
}

// acceptLedger finalizes consensus and accepts the new ledger. Mirrors
// rippled's doAccept at RCLConsensus.cpp:464-602: runs even in WrongLedger
// mode, only the validation-emission branch (:587-594) is mode-gated via
// `validating_ = ledgerMaster_.isCompatible(...)`. The validated-precedence
// guard protects ledgerHistory[seq] from being overwritten by a
// Frankenstein entry once the real validated ledger arrives.
func (e *Engine) acceptLedger(result consensus.Result) {
	if e.phase != consensus.PhaseEstablish {
		return
	}

	// Mirror RCLConsensus.cpp:481-496 fork: when close-time consensus
	// is reached use determineCloseTime + effCloseTime; otherwise fall
	// back deterministically to parentCloseTime + 1s. Without the
	// deterministic fallback, determineCloseTime resolves to local
	// clock and diverges across nodes — the root cause of issue #401.
	priorClose := e.prevLedger.CloseTime()
	resolution := e.adaptor.CloseTimeResolution()
	var rawCloseTime, closeTime time.Time
	var ctBranch string
	if e.haveCloseTimeConsensus {
		rawCloseTime = e.determineCloseTime()
		closeTime = effCloseTime(rawCloseTime, resolution, priorClose)
		ctBranch = "consensus"
	} else {
		closeTime = priorClose.Add(time.Second)
		rawCloseTime = closeTime
		ctBranch = "fallback"
	}

	var ourPosCT int64
	var ourPosSeq uint32
	if e.state != nil && e.state.OurPosition != nil {
		ourPosCT = e.state.OurPosition.CloseTime.Unix() - protocol.RippleEpochUnix
		ourPosSeq = e.state.OurPosition.Position
	}
	slog.Info("close-time decision",
		"t", "consensus",
		"event", "accept-ct",
		"seq", e.prevLedger.Seq()+1,
		"mode", e.mode.String(),
		"have_ct_consensus", e.haveCloseTimeConsensus,
		"ct_branch", ctBranch,
		"raw_ct_xrpl", rawCloseTime.Unix()-protocol.RippleEpochUnix,
		"eff_ct_xrpl", closeTime.Unix()-protocol.RippleEpochUnix,
		"prior_ct_xrpl", priorClose.Unix()-protocol.RippleEpochUnix,
		"our_pos_ct_xrpl", ourPosCT,
		"our_pos_seq", ourPosSeq,
		"self_ct_xrpl", e.state.CloseTimes.Self.Unix()-protocol.RippleEpochUnix,
		"resolution_s", int(resolution.Seconds()),
		"peer_ct_count", len(e.state.CloseTimes.Peers),
		"proposer_count", len(e.proposals),
	)

	// Get the agreed transaction set
	var txSet consensus.TxSet
	if e.ourTxSet != nil {
		txSet = e.ourTxSet
	} else {
		// Find most popular among trusted
		txSetCounts := make(map[consensus.TxSetID]int)
		for nodeID, proposal := range e.proposals {
			if e.adaptor.IsTrusted(nodeID) {
				txSetCounts[proposal.TxSet]++
			}
		}

		var bestID consensus.TxSetID
		bestCount := 0
		for id, count := range txSetCounts {
			if count > bestCount {
				bestID = id
				bestCount = count
			}
		}

		var err error
		txSet, err = e.adaptor.GetTxSet(bestID)
		if err != nil {
			return
		}
	}

	// Build the new ledger
	newLedger, err := e.adaptor.BuildLedger(e.prevLedger, txSet, closeTime, e.haveCloseTimeConsensus)
	if err != nil {
		return
	}

	parentID := e.prevLedger.ID()
	parentClose := e.prevLedger.CloseTime()
	newID := newLedger.ID()
	txSetID := txSet.ID()
	slog.Info("ledger built",
		"t", "consensus",
		"event", "ledger-built",
		"seq", newLedger.Seq(),
		"hash", fmt.Sprintf("%x", newID[:8]),
		"parent_seq", e.prevLedger.Seq(),
		"parent_hash", fmt.Sprintf("%x", parentID[:8]),
		"parent_ct_xrpl", parentClose.Unix()-protocol.RippleEpochUnix,
		"close_time_xrpl", closeTime.Unix()-protocol.RippleEpochUnix,
		"close_time_correct", e.haveCloseTimeConsensus,
		"resolution_s", int(resolution.Seconds()),
		"tx_set", fmt.Sprintf("%x", txSetID[:8]),
		"tx_count", txSet.Size(),
		"result", result.String(),
		"mode", e.mode.String(),
	)

	// Validate and store
	if err := e.adaptor.ValidateLedger(newLedger); err != nil {
		return
	}

	if err := e.adaptor.StoreLedger(newLedger); err != nil {
		return
	}

	// Emit consensus reached event
	e.eventBus.Publish(&consensus.ConsensusReachedEvent{
		Round:     e.state.Round,
		TxSet:     txSet.ID(),
		CloseTime: closeTime,
		Proposers: len(e.proposals),
		Result:    result,
		// StartTime is wall-clock (see startRoundLocked); use time.Since
		// to keep the pair balanced rather than mixing offset-adjusted
		// adaptor.Now() against it.
		Duration:  time.Since(e.state.StartTime),
		Timestamp: e.adaptor.Now(),
	})

	// Mirror rippled's emission gate at RCLConsensus.cpp:591-594:
	//   if (validating_ && !consensusFail && canValidateSeq(seq)) validate(...)
	//
	// validating_: we are configured as a validator.
	// consensusFail: ConsensusState::MovedOn ONLY (RCLConsensus.cpp:479).
	//   Rippled's Expired state (the hard timeout) does NOT set
	//   consensusFail — it still emits, and peers form quorum on the
	//   timeout-built ledger because every validator times out around
	//   the same wall-clock instant. The earlier goxrpl rule
	//   `result != ResultSuccess` lumped Timeout and Abandoned in with
	//   MovedOn, which silently bowed us out of every timed-out round.
	//   In a 3-rippled + 2-goxrpl soak that flips quorum from "reachable
	//   after timeout" to "permanently unreachable" — the failure mode
	//   in #451.
	// canValidateSeq: prevents a second validation for a seq we already
	//   validated; without it a divergent close + reacquire races two
	//   validations and BBD flags us as Conflicting (#401).
	//
	// Mode is intentionally NOT part of this gate. Rippled emits a
	// validation here regardless of ConsensusMode — wrongLedger,
	// switchedLedger, observing and proposing all reach this branch
	// (RCLConsensus.cpp:478 only uses mode to derive haveCorrectLCL for
	// notify/censorship paths, not the emission predicate). The Full
	// flag inside sendValidation (set from `mode == ModeProposing`) is
	// what controls whether peers count the validation toward quorum:
	// partials emitted in non-proposing modes act as a liveness signal
	// so rippled's validator-presence detector doesn't mark us
	// `offline` while we recover, but they don't influence quorum until
	// we're back in ModeProposing. Suppressing emission entirely while
	// in wrongLedger (the prior behavior) made the node invisible to
	// peers under mode thrash and caused permanent quorum stalls — #451.
	// ResultFail is a goxrpl-local "we know we failed" sentinel with no
	// rippled analogue; it semantically maps to the same suppress class
	// as MovedOn (don't validate a round we explicitly failed).
	consensusFail := result == consensus.ResultMovedOn || result == consensus.ResultFail
	isValidator := e.adaptor.IsValidator()
	canValidate := e.peekCanValidateSeqLocked(newLedger.Seq())
	// isCompatible mirrors rippled's `validating_ = ledgerMaster_.isCompatible(*built, ...)`
	// at RCLConsensus.cpp:587-589, which calls areCompatible(validatedLedger, built)
	// at View.cpp:797-857. Suppresses emission when the locally built ledger does
	// not have the validated tip on its ancestry — i.e. when our build is genuinely
	// on a side chain rather than just ahead of validated on the same chain. The
	// earlier wrongLedger-mode gate was a coarse proxy that also blocked the
	// just-ahead-but-compatible case (#451); dropping it without this compatibility
	// check would let side-chain builds emit and re-introduce the #401 Frankenstein-
	// hash class.
	compatible := e.isBuildCompatibleWithValidatedLocked(newLedger)
	willEmit := isValidator && !consensusFail && canValidate && compatible

	newLedgerID := newLedger.ID()
	hashShort := fmt.Sprintf("%x", newLedgerID[:8])
	slog.Info("validation gate",
		"t", "consensus",
		"event", "validate-gate",
		"seq", newLedger.Seq(),
		"hash", hashShort,
		"result", result.String(),
		"is_validator", isValidator,
		"consensus_fail", consensusFail,
		"wrong_lcl", e.mode == consensus.ModeWrongLedger,
		"compatible", compatible,
		"can_validate_seq", canValidate,
		"our_last_validated_seq", e.ourLastValidatedSeq,
		"mode", e.mode.String(),
		"decision", emitDecision(willEmit, isValidator, consensusFail, canValidate, compatible),
	)

	if willEmit {
		e.sendValidation(newLedger)
	}

	// Collect validations
	var validations []*consensus.Validation
	for _, v := range e.validations {
		if v.LedgerID == newLedger.ID() {
			validations = append(validations, v)
		}
	}

	// Notify adaptor
	e.adaptor.OnConsensusReached(newLedger, validations)

	// Emit ledger accepted event
	e.eventBus.Publish(&consensus.LedgerAcceptedEvent{
		LedgerID:    newLedger.ID(),
		LedgerSeq:   newLedger.Seq(),
		TxCount:     txSet.Size(),
		CloseTime:   closeTime,
		Validations: len(validations),
		Timestamp:   e.adaptor.Now(),
	})

	// Adjust our clock toward the network's close time average.
	// Matches rippled's adjustCloseTime() in RCLConsensus.cpp:694-732.
	if e.mode == consensus.ModeProposing || e.mode == consensus.ModeObserving {
		e.adaptor.AdjustCloseTime(e.state.CloseTimes)
	}

	// Refresh the ValidationTracker's trusted set on every accept.
	// Amendments and negative-UNL updates can mutate the UNL across
	// ledger boundaries; re-pulling both from the adaptor keeps the
	// tracker in sync without requiring callers to invalidate by hand.
	// Also advance the minSeq floor so far-stale validations get
	// rejected at the Add() gate rather than being filtered out in
	// checkFullValidation every pass.
	if e.validationTracker != nil {
		e.validationTracker.SetTrusted(e.adaptor.GetTrustedValidators())
		e.validationTracker.SetQuorum(e.adaptor.GetQuorum())
		// Pull the negative-UNL from the just-accepted ledger so
		// validations from temporarily-disabled validators are excluded
		// from quorum. Rippled's checkAccept consults the same SLE per
		// ledger. Without this call, SetNegativeUNL is unreachable from
		// production code and the negUNL filter is dead.
		e.validationTracker.SetNegativeUNL(e.adaptor.GetNegativeUNL())
		if newLedger.Seq() > 128 {
			// Keep a small history window so late validations for the
			// just-accepted ledger still count.
			e.validationTracker.SetMinSeq(newLedger.Seq() - 128)
		}
	}

	// Track round time for convergePercent calculation
	e.prevRoundTime = time.Since(e.roundStartTime)

	// Track trusted proposer count for peer pressure in next round
	trustedCount := 0
	for nodeID := range e.proposals {
		if e.adaptor.IsTrusted(nodeID) {
			trustedCount++
		}
	}
	e.prevProposers = trustedCount
	// Publish to the lock-free mirror read by GetLastCloseInfo on
	// the RPC hot path.
	e.storeLastCloseLocked()

	// Update state for next round
	e.prevLedger = newLedger
	e.validations = make(map[consensus.NodeID]*consensus.Validation)
	e.consensusCount++

	// Move to accepted phase
	e.setPhase(consensus.PhaseAccepted)

	// Only auto-advance to the next round if we're in Full mode.
	// If not Full, the router will keep re-adopting until caught up,
	// then transition to Full, at which point checkAndStartRound kicks in.
	if e.adaptor.GetOperatingMode() == consensus.OpModeFull {
		// Round-boundary preferred-LCL jump: if the validation tracker
		// reports a different preferred LCL and we have it locally,
		// retarget prev so the next round avoids a handleWrongLedger
		// detour. Falls through to handleWrongLedger (acquire) when
		// we don't have it cached — matches NetworkOPsImp::checkLastClosedLedger.
		nextPrev := newLedger
		if e.validationTracker != nil {
			candidateID, candidateSeq, ok := e.validationTracker.GetPreferred(newLedger.Seq())
			if !ok {
				candidateID, candidateSeq, ok = e.validationTracker.PreferredFromValidations(newLedger.Seq())
			}
			localID := newLedger.ID()
			if ok && candidateID != localID && candidateSeq >= newLedger.Seq() {
				if cached, err := e.adaptor.GetLedger(candidateID); err == nil && cached != nil {
					localBytes := localID
					slog.Info("preferred LCL differs; jumping prev to cached ledger",
						"t", "consensus",
						"event", "preferred-lcl-jump-cached",
						"local_seq", newLedger.Seq(),
						"local_hash", fmt.Sprintf("%x", localBytes[:8]),
						"preferred_seq", candidateSeq,
						"preferred_hash", fmt.Sprintf("%x", candidateID[:8]),
					)
					nextPrev = cached
					e.prevLedger = cached
				} else {
					localBytes := localID
					slog.Info("preferred LCL differs; routing through handleWrongLedger (acquire)",
						"t", "consensus",
						"event", "preferred-lcl-jump-acquire",
						"local_seq", newLedger.Seq(),
						"local_hash", fmt.Sprintf("%x", localBytes[:8]),
						"preferred_seq", candidateSeq,
						"preferred_hash", fmt.Sprintf("%x", candidateID[:8]),
					)
					e.handleWrongLedger(candidateID)
					return
				}
			}
		}

		// Auto-advance — startRoundLocked picks ModeProposing for a
		// trusted validator in OpModeFull.
		proposing := e.adaptor.IsValidator()
		nextRound := consensus.RoundID{
			Seq:        nextPrev.Seq() + 1,
			ParentHash: nextPrev.ID(),
		}
		e.startRoundLocked(nextRound, proposing, false)
	}
}

// updateCloseTimePosition counts close time votes from peer proposals,
// applies avalanche thresholds, and updates our proposal's close time
// to match the consensus. Matches rippled's updateOurPositions() close
// time logic (Consensus.h:1507-1634).
func (e *Engine) updateCloseTimePosition() {
	resolution := e.adaptor.CloseTimeResolution()

	// Count close time votes from current trusted proposals, rounding
	// each via roundCloseTime (matching rippled's asCloseTime).
	closeTimeVotes := make(map[time.Time]int)
	participants := 0
	for nodeID, proposal := range e.proposals {
		if e.adaptor.IsTrusted(nodeID) {
			rounded := roundCloseTime(proposal.CloseTime, resolution)
			closeTimeVotes[rounded]++
			participants++
		}
	}

	if participants == 0 {
		e.haveCloseTimeConsensus = true // trivially
		return
	}

	// Add our own vote if proposing
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil {
		ourRounded := roundCloseTime(e.state.OurPosition.CloseTime, resolution)
		closeTimeVotes[ourRounded]++
		participants++
	}

	// Determine threshold from avalanche state
	neededWeight := e.getCloseTimeNeededWeight()
	threshVote := participantsNeeded(participants, neededWeight)
	threshConsensus := participantsNeeded(participants, 75) // avCT_CONSENSUS_PCT
	threshVoteInitial := threshVote

	// Iterate ascending so ties are resolved deterministically — rippled's
	// std::map<NetClock,int> iterates ascending and the "raise bar" loop
	// picks the LAST (largest) candidate on a tie. Go's map iteration is
	// randomized, so without an explicit sort validators diverge on ties.
	sortedTimes := make([]time.Time, 0, len(closeTimeVotes))
	for t := range closeTimeVotes {
		sortedTimes = append(sortedTimes, t)
	}
	sort.Slice(sortedTimes, func(i, j int) bool {
		return sortedTimes[i].Before(sortedTimes[j])
	})

	var consensusCloseTime time.Time
	e.haveCloseTimeConsensus = false
	for _, t := range sortedTimes {
		count := closeTimeVotes[t]
		if count >= threshVote {
			consensusCloseTime = t
			threshVote = count // raise bar to pick the MOST popular
			if count >= threshConsensus {
				e.haveCloseTimeConsensus = true
			}
		}
	}

	votesSummary := summarizeCloseTimeVotes(closeTimeVotes)
	var consensusCT int64
	if !consensusCloseTime.IsZero() {
		consensusCT = consensusCloseTime.Unix() - protocol.RippleEpochUnix
	}
	var ourPosCT int64
	var ourPosSeq uint32
	if e.state.OurPosition != nil {
		ourPosCT = e.state.OurPosition.CloseTime.Unix() - protocol.RippleEpochUnix
		ourPosSeq = e.state.OurPosition.Position
	}
	slog.Info("close-time avalanche",
		"t", "consensus",
		"event", "ct-avalanche",
		"seq", e.state.Round.Seq,
		"mode", e.mode.String(),
		"converge_pct", e.convergePercent(),
		"avalanche_state", closeTimeAvalancheStateName(e.closeTimeAvalancheState),
		"needed_weight", neededWeight,
		"thresh_vote", threshVoteInitial,
		"thresh_consensus", threshConsensus,
		"participants", participants,
		"have_consensus", e.haveCloseTimeConsensus,
		"consensus_ct_xrpl", consensusCT,
		"our_pos_ct_xrpl", ourPosCT,
		"our_pos_seq", ourPosSeq,
		"votes", votesSummary,
	)

	// Update our proposal if close time changed
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil && !consensusCloseTime.IsZero() {
		ourRounded := roundCloseTime(e.state.OurPosition.CloseTime, resolution)
		if consensusCloseTime != ourRounded {
			oldCT := e.state.OurPosition.CloseTime.Unix() - protocol.RippleEpochUnix
			e.state.OurPosition.CloseTime = consensusCloseTime
			e.state.OurPosition.Position++
			e.state.OurPosition.Timestamp = e.adaptor.Now()
			if err := e.adaptor.SignProposal(e.state.OurPosition); err == nil {
				e.enqueueProposalBroadcastLocked(e.state.OurPosition)
			}
			slog.Info("our close-time bumped",
				"t", "consensus",
				"event", "ct-bump",
				"seq", e.state.Round.Seq,
				"old_ct_xrpl", oldCT,
				"new_ct_xrpl", consensusCT,
				"new_pos_seq", e.state.OurPosition.Position,
			)
		}
	}
}

// summarizeCloseTimeVotes renders the vote distribution as "ct=count"
// pairs (XRPL-epoch seconds), capped at 8 entries.
func summarizeCloseTimeVotes(votes map[time.Time]int) string {
	if len(votes) == 0 {
		return "(empty)"
	}
	type kv struct {
		ct    int64
		count int
	}
	all := make([]kv, 0, len(votes))
	for t, c := range votes {
		all = append(all, kv{ct: t.Unix() - protocol.RippleEpochUnix, count: c})
	}
	limit := len(all)
	if limit > 8 {
		limit = 8
	}
	var b strings.Builder
	for i := 0; i < limit; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d=%d", all[i].ct, all[i].count)
	}
	if len(all) > limit {
		fmt.Fprintf(&b, " (+%d more)", len(all)-limit)
	}
	return b.String()
}

func closeTimeAvalancheStateName(s avalancheState) string {
	switch s {
	case avalancheInit:
		return "init"
	case avalancheMid:
		return "mid"
	case avalancheLate:
		return "late"
	case avalancheStuck:
		return "stuck"
	}
	return "unknown"
}

// getCloseTimeNeededWeight returns the minimum vote percentage for close time
// based on the avalanche state machine. Matches rippled's getNeededWeight()
// in ConsensusParms.h:172-199.
func (e *Engine) getCloseTimeNeededWeight() int {
	pct := e.convergePercent()
	switch e.closeTimeAvalancheState {
	case avalancheInit:
		if pct >= 0 {
			e.closeTimeAvalancheState = avalancheMid
		}
		return 50
	case avalancheMid:
		if pct >= 50 {
			e.closeTimeAvalancheState = avalancheLate
		}
		return 65
	case avalancheLate:
		if pct >= 85 {
			e.closeTimeAvalancheState = avalancheStuck
		}
		return 70
	case avalancheStuck:
		return 95
	}
	return 50
}

// convergePercent returns how far through the establish phase we are,
// as a percentage of the previous round time (min 5s).
// Matches rippled's convergePercent_ calculation.
func (e *Engine) convergePercent() int {
	elapsed := time.Since(e.roundStartTime)
	prevRound := e.prevRoundTime
	if prevRound < 5*time.Second {
		prevRound = 5 * time.Second
	}
	return int(elapsed * 100 / prevRound)
}

// participantsNeeded computes the minimum number of participants required
// to meet a given percentage threshold. Matches rippled's participantsNeeded().
func participantsNeeded(participants, percent int) int {
	result := (participants*percent + percent/2) / 100
	if result == 0 {
		return 1
	}
	return result
}

// determineCloseTime returns the consensus close time.
// Uses the close time that was converged on by updateCloseTimePosition().
// If we have a consensus position with a non-zero close time, use it.
// For observers (no position), use the most popular peer close time
// ROUNDED to the current resolution — matching rippled where all nodes
// (proposers and observers) use rounded consensus values.
func (e *Engine) determineCloseTime() time.Time {
	// If we have a position (from updateCloseTimePosition convergence), use its close time.
	// This is already rounded by updateCloseTimePosition().
	if e.state.OurPosition != nil && !e.state.OurPosition.CloseTime.IsZero() {
		return e.state.OurPosition.CloseTime
	}

	resolution := e.adaptor.CloseTimeResolution()

	// For observers: use the most popular peer close time from proposals,
	// but ROUND it to the resolution before returning. CloseTimes.Peers
	// stores raw times; rippled rounds before voting (asCloseTime), so
	// we must round here to match.
	if len(e.state.CloseTimes.Peers) > 0 {
		// Vote on rounded times (matching rippled's updateOurPositions)
		roundedVotes := make(map[time.Time]int)
		for t, count := range e.state.CloseTimes.Peers {
			rounded := roundCloseTime(t, resolution)
			roundedVotes[rounded] += count
		}

		var bestTime time.Time
		bestCount := 0
		for t, count := range roundedVotes {
			if count > bestCount {
				bestTime = t
				bestCount = count
			}
		}
		if bestCount > 0 {
			return bestTime
		}
	}

	return roundCloseTime(e.state.CloseTimes.Self, resolution)
}

// sendValidation creates and broadcasts a validation.
//
// The Full flag on the emitted validation reflects whether we were
// actively PROPOSING this round. Rippled sets vfFullValidation iff
// mode == proposing (RCLConsensus.cpp:849-851); switchedLedger and
// observing emit the same frame with the bit cleared (partial
// validation). Partial validations are accepted by peers but don't
// count toward quorum (LedgerMaster.cpp:886 filters Full=false out of
// the trusted count).
// peekCanValidateSeqLocked is the non-mutating predicate half of
// SeqEnforcer.operator() (Validations.h:118-128). Caller holds e.mu read.
func (e *Engine) peekCanValidateSeqLocked(seq uint32) bool {
	floor := e.ourLastValidatedSeq
	if !e.ourLastValidatedTime.IsZero() &&
		e.adaptor.Now().Sub(e.ourLastValidatedTime) > validationSetExpires {
		floor = 0
	}
	return seq > floor
}

// tryAdvanceValidatedSeqLocked mirrors SeqEnforcer::operator() at
// Validations.h:118-128: idle-reset, reject-or-bump in one atomic call.
// The floor is committed before signing so a sign failure still consumes
// the seq slot. Caller holds e.mu write.
func (e *Engine) tryAdvanceValidatedSeqLocked(seq uint32) bool {
	now := e.adaptor.Now()
	if !e.ourLastValidatedTime.IsZero() &&
		now.Sub(e.ourLastValidatedTime) > validationSetExpires {
		e.ourLastValidatedSeq = 0
	}
	if seq <= e.ourLastValidatedSeq {
		return false
	}
	e.ourLastValidatedSeq = seq
	e.ourLastValidatedTime = now
	return true
}

func (e *Engine) sendValidation(ledger consensus.Ledger) {
	// SeqEnforcer guard + bump (Validations.h:118-128). Defensive
	// in-function check so direct test callers cannot bypass.
	if !e.tryAdvanceValidatedSeqLocked(ledger.Seq()) {
		return
	}

	nodeID, err := e.adaptor.GetValidatorKey()
	if err != nil {
		return
	}

	full := e.mode == consensus.ModeProposing

	// Compute SignTime under a monotonic floor. If the adaptor clock
	// regresses (NTP step, leap-second correction, VM pause/resume) the
	// emitted SignTime could be older than the prior validation from
	// this node, so peers would reject it as stale. Bump to
	// lastSignTime + 1s in that case to preserve monotonicity. Matches
	// rippled RCLConsensus.cpp:825-828. SeenTime mirrors SignTime (as
	// before) so the two remain equal on emission.
	signTime := e.adaptor.Now()
	if !e.lastSignTime.IsZero() && !signTime.After(e.lastSignTime) {
		signTime = e.lastSignTime.Add(1 * time.Second)
	}
	e.lastSignTime = signTime

	validation := &consensus.Validation{
		LedgerID:  ledger.ID(),
		LedgerSeq: ledger.Seq(),
		NodeID:    nodeID,
		SignTime:  signTime,
		SeenTime:  signTime,
		Full:      full,
		// R6b.5b: emit local load_fee (sfLoadFee) — rippled
		// RCLConsensus.cpp:851 always populates this under
		// HardenedValidations. Zero means "no load info",
		// serializer omits the field.
		LoadFee: e.adaptor.GetLoadFee(),
	}

	// B1: sfCookie and sfServerVersion are scoped inside rippled's
	// `if (rules().enabled(featureHardenedValidations))` block at
	// RCLConsensus.cpp:853-867. Before HV is active (pre-2020 on
	// mainnet, any modern testnet/standalone on old rules) peers
	// reject validations that carry these fields because the preimage
	// they compute for signature verification omits them. sfCookie
	// emits on every HV-enabled validation; sfServerVersion emits
	// ONLY on voting ledgers within the same block (cpp:864-866 —
	// "Report our server version every flag ledger").
	if e.adaptor.IsFeatureEnabled("HardenedValidations") {
		cookie := e.adaptor.GetCookie()
		if cookie == 0 {
			slog.Warn("sendValidation: cookie is zero under HardenedValidations — adaptor must generate one at boot; emitting without cookie")
		}
		validation.Cookie = cookie

		if consensus.IsVotingLedger(ledger.Seq()) {
			serverVersion := e.adaptor.GetServerVersion()
			if serverVersion == 0 {
				slog.Warn("sendValidation: serverVersion is zero on voting ledger under HardenedValidations — adaptor must advertise a build tag; emitting without serverVersion")
			}
			validation.ServerVersion = serverVersion
		}
	}

	// Fee vote + amendment vote emission is gated on isVotingLedger.
	// Rippled emits these ONLY on flag ledgers — the validation signed
	// for ledger seq covers the transition to seq+1; on the flag
	// boundary (seq+1)%256 == 0) we attach the vote for the next
	// flag-ledger cycle. Emitting on every ledger inflates bandwidth
	// ~256× and confuses peer aggregators that accept these fields
	// only on the expected boundary. Matches
	// Ledger.cpp:951-953 isVotingLedger + RCLConsensus.cpp:879.
	if consensus.IsVotingLedger(ledger.Seq()) {
		// Fee vote: emit the AMOUNT triple under post-XRPFees rules, the
		// legacy UINT triple otherwise. Rippled's FeeVoteImpl.cpp:120-192
		// is a hard if/else on featureXRPFees; the adaptor's postXRPFees
		// flag mirrors that decision so the two paths never co-emit.
		// Zero values from the adaptor mean "no vote" and the serializer
		// omits the fields.
		if baseFee, reserveBase, reserveIncrement, postXRPFees := e.adaptor.GetFeeVote(); baseFee != 0 || reserveBase != 0 || reserveIncrement != 0 {
			if postXRPFees {
				validation.BaseFeeDrops = baseFee
				validation.ReserveBaseDrops = reserveBase
				validation.ReserveIncrementDrops = reserveIncrement
			} else {
				validation.BaseFee = baseFee
				validation.ReserveBase = uint32(reserveBase)
				validation.ReserveIncrement = uint32(reserveIncrement)
			}
		}

		// Amendment vote — populated alongside fee vote on flag
		// ledgers only. See R5.3. Adaptor returns nil when there is
		// no vote to cast (non-validators, empty stance, all
		// amendments already enabled).
		validation.Amendments = e.adaptor.GetAmendmentVote()
	}

	// Tie the validation to the tx-set we converged on, so peers can
	// tie-break between concurrent same-seq ledgers with different tx
	// sets. Rippled's STValidation always includes this when available;
	// we only have it when we actually produced a proposal this round
	// (observers that didn't propose can legitimately omit it).
	if e.ourTxSet != nil {
		setID := e.ourTxSet.ID()
		copy(validation.ConsensusHash[:], setID[:])
	}

	// Attach the most-recent fully-validated LCL hash we know about.
	// Rippled emits sfValidatedHash ONLY under featureHardenedValidations
	// (RCLConsensus.cpp:853). On mainnet that amendment has been active
	// since 2020 so this is always true; on testnet/standalone a node
	// running against pre-HardenedValidations rules must omit the field
	// or peers on the old rules reject the validation as malformed.
	// GetValidatedLedgerHash returns the zero LedgerID on a node that
	// hasn't yet crossed quorum — in that case we also skip emission.
	if e.adaptor.IsFeatureEnabled("HardenedValidations") {
		if vh := e.adaptor.GetValidatedLedgerHash(); vh != (consensus.LedgerID{}) {
			copy(validation.ValidatedHash[:], vh[:])
		}
	}

	if err := e.adaptor.SignValidation(validation); err != nil {
		slog.Warn("validation sign failed",
			"t", "consensus",
			"event", "validate-sign-fail",
			"seq", ledger.Seq(),
			"error", err,
		)
		return
	}

	ledgerID := ledger.ID()
	slog.Info("validation emitted",
		"t", "consensus",
		"event", "validate-emit",
		"seq", ledger.Seq(),
		"hash", fmt.Sprintf("%x", ledgerID[:8]),
		"full", full,
		"sign_time_xrpl", signTime.Unix()-protocol.RippleEpochUnix,
	)

	e.enqueueValidationBroadcastLocked(validation)

	// Feed our own validation into the tracker. Only Full validations
	// are accepted by the tracker's Full-gate, so a partial
	// switchedLedger emission is automatically excluded from our own
	// quorum view — matching rippled's behavior where switchedLedger
	// partials don't count toward local checkAccept. In a
	// 1-validator standalone setup the Full path crosses the
	// threshold immediately and fires OnLedgerFullyValidated.
	if e.validationTracker != nil {
		e.validationTracker.Add(validation)
	}
}

// roundCloseTime rounds a close time to the nearest multiple of resolution.
// Rounds up if the close time is at the midpoint.
//
// Mirrors rippled's chrono integer math at LedgerTiming.h:131-143 over
// NetClock::time_point (XRPL-epoch seconds). Rippled's input has
// integer-second precision; we match that semantics by truncating
// time.Time's sub-second component before rounding, so two validators
// with skewed nanosecond clocks reduce to the same integer-second
// input and round to the same boundary.
//
// Doing the modulo in XRPL-epoch space (rather than Unix epoch) is
// equivalent at any resolution that divides RippleEpochUnix — which
// covers every resolution rippled currently uses — but matches
// rippled byte-for-byte without depending on that coincidence.
func roundCloseTime(closeTime time.Time, resolution time.Duration) time.Time {
	if closeTime.IsZero() {
		return closeTime
	}
	resSec := int64(resolution.Seconds())
	if resSec <= 0 {
		return closeTime
	}
	xrplSec := closeTime.Unix() - protocol.RippleEpochUnix
	xrplSec += resSec / 2
	xrplSec -= xrplSec % resSec
	return time.Unix(xrplSec+protocol.RippleEpochUnix, 0).UTC()
}

// emitDecision labels which arm of the validation gate fired.
// wrongLedger mode is intentionally NOT a skip reason: rippled emits
// a partial validation in that mode (#451). The wrong_lcl field is
// still logged separately so operators can see the recovery context.
func emitDecision(emit, isValidator, consensusFail, canValidate, compatible bool) string {
	if emit {
		return "emit"
	}
	if !isValidator {
		return "skip:not-validator"
	}
	if consensusFail {
		return "skip:consensus-fail"
	}
	if !canValidate {
		return "skip:already-validated-seq"
	}
	if !compatible {
		return "skip:incompatible-with-validated"
	}
	return "skip:unknown"
}

// effCloseTime calculates the effective ledger close time.
// After rounding to the close time resolution, ensures the result is
// at least 1 second after the prior ledger's close time.
// Reference: rippled LedgerTiming.h effCloseTime()
func effCloseTime(closeTime time.Time, resolution time.Duration, priorCloseTime time.Time) time.Time {
	if closeTime.IsZero() {
		return closeTime
	}
	rounded := roundCloseTime(closeTime, resolution)
	minTime := priorCloseTime.Add(time.Second)
	if rounded.Before(minTime) {
		return minTime
	}
	return rounded
}
