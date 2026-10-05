package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
)

var ErrRecoveryUnavailable = errors.New("recovery data temporarily unavailable")

// RecoveryTarget is a header selected by the consensus recovery coordinator.
// Evidence is coordinator-owned proof metadata and is never copied into the
// public fault summary.
type RecoveryTarget struct {
	Header   header.LedgerHeader
	Evidence json.RawMessage
}

// PivotRecoveryBackend supplies the authenticated, detached inputs for an
// operator-approved verified-pivot recovery. It must keep its own consensus
// and acquisition locks; none of these callbacks may publish a canonical
// ledger or clear the replay-fault gate.
type PivotRecoveryBackend interface {
	SelectPivot(context.Context, uint32) (RecoveryTarget, error)
	Authenticate(context.Context, RecoveryTarget) error
	Acquire(context.Context, RecoveryTarget, *ledger.Ledger) (*ledger.Ledger, error)
	Next(context.Context, header.LedgerHeader) (RecoveryTarget, error)
	CaughtUp(context.Context, header.LedgerHeader) bool
	Handoff(context.Context, header.LedgerHeader) error
}

// PivotRecoveryProgressReporter is optional backend telemetry used only for
// sanitized operator status. The recovery state machine does not depend on it.
type PivotRecoveryProgressReporter interface {
	PivotRecoveryProgress() (reused, fetched, missing uint64)
}

// PivotRecoveryLagReporter is optional backend telemetry for the sanitized
// operator status. A backend may derive the lag from its authenticated route
// without exposing route proofs to the service API.
type PivotRecoveryLagReporter interface {
	PivotRecoveryLag(uint32) uint32
}

// SetPivotRecoveryBackend installs the consensus recovery coordinator. A
// persisted verified-pivot phase is resumed once the coordinator is wired,
// which normally occurs after ledger service startup.
func (s *Service) SetPivotRecoveryBackend(backend PivotRecoveryBackend) {
	s.mu.Lock()
	s.replayPivotBackend = backend
	s.mu.Unlock()
	if backend == nil || s.replayFaults == nil {
		return
	}
	s.lifecycleMu.Lock()
	running := s.lifecycleState == serviceRunning
	s.lifecycleMu.Unlock()
	if !running {
		return
	}
	status := s.replayFaults.Status()
	if status.Fault == nil || status.Fault.Recovery == nil || status.Recovery.ID != status.Fault.ID || status.Recovery.Phase == replayfault.RecoveryPhaseComplete || status.Recovery.Phase == replayfault.RecoveryPhaseExecutionFailed || status.Recovery.Phase == replayfault.RecoveryPhaseNeedsOperator || status.Recovery.Strategy != replayfault.RecoveryStrategyVerifiedPivot || status.Recovery.InFlight || status.Recovery.LastError != "" {
		return
	}
	go func(id string) {
		if err := s.StartReplayFaultRecoveryStrategy(id, string(replayfault.RecoveryStrategyVerifiedPivot)); err != nil && !errors.Is(err, errReplayRecoveryWorkerRunning) {
			s.logger.Warn("resume verified-pivot replay recovery", "fault_id", id, "error", err)
		}
	}(status.Fault.ID)
}

func (s *Service) pivotRecoveryBackend() PivotRecoveryBackend {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.replayPivotBackend
}

func (s *Service) exactReplayTransitionUsable(ctx context.Context, fault *replayfault.Fault) bool {
	if fault == nil {
		return false
	}
	var evidence replayEvidence
	if json.Unmarshal(fault.Evidence, &evidence) != nil {
		return false
	}
	if !s.authenticateReplayEvidence(evidence) {
		s.mu.RLock()
		authenticate := s.replayAuthenticate
		s.mu.RUnlock()
		if authenticate == nil || !authenticate(evidence.Target) {
			return false
		}
	}
	s.mu.RLock()
	target := s.replayRepairTarget
	s.mu.RUnlock()
	parent, err := s.loadReplayParent(ctx, *fault, evidence)
	if err != nil || parent == nil || parent.Hash() != fault.ParentHash {
		return false
	}
	if evidence.TransactionMapIncomplete && (target == nil || target.Hash() != fault.TargetHash) {
		return false
	}
	return true
}

func replayRecoveryTargetIdentity(target RecoveryTarget) error {
	if target.Header.LedgerIndex == 0 || target.Header.Hash == ([32]byte{}) {
		return errors.New("replay recovery target has no ledger identity")
	}
	if header.CalculateHash(target.Header) != target.Header.Hash {
		return errors.New("replay recovery target header hash mismatch")
	}
	return nil
}

func validateReplayRecoveryNext(previous header.LedgerHeader, next RecoveryTarget) error {
	if err := replayRecoveryTargetIdentity(next); err != nil {
		return err
	}
	if previous.LedgerIndex == ^uint32(0) {
		return errors.New("replay recovery route has no successor after maximum sequence")
	}
	if next.Header.LedgerIndex != previous.LedgerIndex+1 {
		return fmt.Errorf("replay recovery route skipped sequence %d", previous.LedgerIndex+1)
	}
	if next.Header.ParentHash != previous.Hash {
		return fmt.Errorf("replay recovery route parent mismatch at sequence %d", next.Header.LedgerIndex)
	}
	return nil
}

func (s *Service) verifyAndStageRecoveryLedger(ctx context.Context, candidate *ledger.Ledger) error {
	stage, err := s.StageRecoveryLedger(ctx, candidate)
	if err != nil {
		return err
	}
	return stage.Retire()
}

func recordPivotRecoveryProgress(progress *replayfault.RecoveryProgress, backend PivotRecoveryBackend) {
	reporter, ok := backend.(PivotRecoveryProgressReporter)
	if !ok || progress == nil {
		return
	}
	progress.ReusedNodes, progress.FetchedNodes, progress.MissingNodes = reporter.PivotRecoveryProgress()
}

func recordPivotRecoveryLag(progress *replayfault.RecoveryProgress, backend PivotRecoveryBackend, frontier uint32) {
	reporter, ok := backend.(PivotRecoveryLagReporter)
	if !ok || progress == nil {
		return
	}
	progress.ReplayLag = reporter.PivotRecoveryLag(frontier)
}

func encodeRecoveryTarget(target RecoveryTarget) (json.RawMessage, error) {
	data, err := json.Marshal(target)
	if err != nil {
		return nil, fmt.Errorf("encode recovery target proof: %w", err)
	}
	return data, nil
}

func decodeRecoveryTarget(raw json.RawMessage) (RecoveryTarget, error) {
	var target RecoveryTarget
	if len(raw) == 0 {
		return target, errors.New("recovery target proof is missing")
	}
	if err := json.Unmarshal(raw, &target); err != nil {
		return target, fmt.Errorf("decode recovery target proof: %w", err)
	}
	if err := replayRecoveryTargetIdentity(target); err != nil {
		return target, err
	}
	return target, nil
}

const maxPivotRecoveryHeaders = uint32(1 << 16)

// recoverVerifiedPivot executes a staged recovery entirely outside the normal
// consensus replay paths. Every externally observable transition is persisted
// before the corresponding callback, so cancellation or a crash leaves the
// validator blocked and the next explicit attempt can resume from the phase.
func (s *Service) recoverVerifiedPivot(ctx context.Context, id string, started chan<- struct{}) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	progress, err := s.replayFaults.BeginRecovery(id, replayfault.RecoveryStrategyVerifiedPivot)
	if err != nil {
		return err
	}
	// Keep the compare-and-swap token owned by this worker. A stale worker must
	// fail its update rather than adopting a newer operator admission's token.
	expectedGeneration := progress.Generation
	if started != nil {
		close(started)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("verified-pivot recovery panic: %v", recovered)
		}
		if err != nil {
			fail := s.replayFaults.HaltRecovery
			if replayExecutionDisagreement(err) {
				fail = s.replayFaults.FailRecovery
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrRecoveryUnavailable) {
				fail = s.replayFaults.SuspendRecovery
			}
			if failErr := fail(id, expectedGeneration, err); failErr != nil {
				s.logger.Error("persist verified-pivot recovery failure", "fault_id", id, "error", failErr)
			}
		}
	}()

	fault := s.replayFaults.Snapshot()
	if fault == nil || fault.ID != id {
		return replayfault.ErrFaultIDMismatch
	}
	var evidence replayEvidence
	if err := json.Unmarshal(fault.Evidence, &evidence); err != nil {
		return fmt.Errorf("decode replay fault evidence: %w", err)
	}
	if evidence.NetworkID != s.config.NetworkID {
		return errors.New("replay evidence network does not match configuration")
	}
	if evidence.Target.LedgerIndex != fault.Sequence || header.CalculateHash(evidence.Target) != fault.TargetHash || evidence.Target.ParentHash != fault.ParentHash {
		return errors.New("replay evidence target identity is invalid")
	}

	backend := s.pivotRecoveryBackend()
	if backend == nil {
		return errors.New("verified-pivot replay recovery backend unavailable")
	}
	if restore, ok := backend.(interface{ RestoreRecoveryProgress(uint64, uint64, uint64) }); ok {
		restore.RestoreRecoveryProgress(progress.ReusedNodes, progress.FetchedNodes, progress.MissingNodes)
	}
	recordPivotRecoveryProgress(&progress, backend)
	recordPivotRecoveryLag(&progress, backend, progress.FrontierSequence)
	if progress.RetentionSequence == 0 && fault.Sequence > 0 {
		progress.RetentionSequence = fault.Sequence - 1
	}
	releaseRetention, retainErr := s.AcquireRecoveryRetention(ctx, id, progress.RetentionSequence)
	if retainErr != nil {
		return fmt.Errorf("retain verified-pivot recovery history: %w", retainErr)
	}
	defer releaseRetention()

	var pivot RecoveryTarget
	if len(fault.RecoveryPivot) != 0 {
		pivot, err = decodeRecoveryTarget(fault.RecoveryPivot)
		if err != nil {
			return err
		}
	} else {
		pivot, err = backend.SelectPivot(ctx, fault.Sequence)
		if err != nil {
			return fmt.Errorf("select verified pivot: %w", err)
		}
		if err := replayRecoveryTargetIdentity(pivot); err != nil {
			return fmt.Errorf("select verified pivot: %w", err)
		}
		if pivot.Header.LedgerIndex <= fault.Sequence {
			return errors.New("verified pivot must be newer than the fault target")
		}
		rawPivot, marshalErr := encodeRecoveryTarget(pivot)
		if marshalErr != nil {
			return marshalErr
		}
		nextProgress, setErr := s.replayFaults.SetRecoveryPivot(id, expectedGeneration, rawPivot)
		if setErr == nil {
			progress = nextProgress
			expectedGeneration = progress.Generation
		}
		err = setErr
		if err != nil {
			return fmt.Errorf("persist verified pivot proof: %w", err)
		}
	}
	if progress.PivotSequence != 0 && (progress.PivotSequence != pivot.Header.LedgerIndex || progress.PivotHash != pivot.Header.Hash) {
		return errors.New("verified pivot changed while recovery was paused")
	}
	if pivot.Header.LedgerIndex <= fault.Sequence {
		return errors.New("verified pivot must be newer than the fault target")
	}
	progress.PivotSequence = pivot.Header.LedgerIndex
	progress.PivotHash = pivot.Header.Hash
	if progress.Phase == replayfault.RecoveryPhaseSelectingPivot {
		progress.Phase = replayfault.RecoveryPhaseAuthenticating
		nextProgress, updateErr := s.replayFaults.UpdateRecovery(id, expectedGeneration, progress)
		if updateErr == nil {
			progress = nextProgress
			expectedGeneration = progress.Generation
		}
		err = updateErr
		if err != nil {
			return fmt.Errorf("persist verified pivot: %w", err)
		}
	}

	if err := backend.Authenticate(ctx, pivot); err != nil {
		return fmt.Errorf("authenticate verified pivot route: %w", err)
	}
	progress.EvidenceAuthenticated = true
	if progress.Phase == replayfault.RecoveryPhaseAuthenticating {
		progress.Phase = replayfault.RecoveryPhaseAcquiringPivot
	}
	if progress.Phase == replayfault.RecoveryPhaseAcquiringPivot {
		nextProgress, updateErr := s.replayFaults.UpdateRecovery(id, expectedGeneration, progress)
		if updateErr == nil {
			progress = nextProgress
			expectedGeneration = progress.Generation
		}
		err = updateErr
		if err != nil {
			return fmt.Errorf("persist authenticated pivot route: %w", err)
		}
	}

	var pivotLedger *ledger.Ledger
	if progress.FrontierSequence <= pivot.Header.LedgerIndex {
		pivotLedger, err = backend.Acquire(ctx, pivot, nil)
		if err != nil {
			return fmt.Errorf("acquire verified pivot state: %w", err)
		}
		if pivotLedger == nil || pivotLedger.Sequence() != pivot.Header.LedgerIndex || pivotLedger.Hash() != pivot.Header.Hash {
			return errors.New("verified pivot acquisition returned the wrong ledger")
		}
		if err := s.verifyAndStageRecoveryLedger(ctx, pivotLedger); err != nil {
			return fmt.Errorf("verify verified pivot state: %w", err)
		}
		progress.StateVerified = true
		if progress.Phase == replayfault.RecoveryPhaseAcquiringPivot {
			progress.FrontierSequence = pivot.Header.LedgerIndex
			progress.FrontierHash = pivot.Header.Hash
			progress.Phase = replayfault.RecoveryPhaseCatchingUp
		}
		if progress.Phase == replayfault.RecoveryPhaseCatchingUp {
			nextProgress, updateErr := s.replayFaults.UpdateRecovery(id, expectedGeneration, progress)
			if updateErr == nil {
				progress = nextProgress
				expectedGeneration = progress.Generation
			}
			err = updateErr
			if err != nil {
				return fmt.Errorf("persist verified pivot frontier: %w", err)
			}
		}
	}

	current := pivot
	currentLedger := pivotLedger
	frontierSequence := progress.FrontierSequence
	frontierHash := progress.FrontierHash
	if frontierSequence != 0 && frontierSequence < pivot.Header.LedgerIndex {
		return errors.New("recovery frontier precedes verified pivot")
	}
	var savedTarget RecoveryTarget
	var savedFrontier RecoveryTarget
	if len(fault.RecoveryTarget) != 0 {
		savedTarget, err = decodeRecoveryTarget(fault.RecoveryTarget)
		if err != nil {
			return err
		}
		if savedTarget.Header.LedgerIndex > pivot.Header.LedgerIndex &&
			(savedTarget.Header.LedgerIndex != frontierSequence &&
				(frontierSequence == ^uint32(0) || savedTarget.Header.LedgerIndex != frontierSequence+1)) {
			return errors.New("persisted recovery target is not adjacent to the frontier")
		}
	}
	if len(fault.RecoveryFrontier) != 0 {
		savedFrontier, err = decodeRecoveryTarget(fault.RecoveryFrontier)
		if err != nil {
			return err
		}
	}
	processedSuccessor := frontierSequence > pivot.Header.LedgerIndex
	if processedSuccessor {
		if savedFrontier.Header.LedgerIndex != frontierSequence || savedFrontier.Header.Hash != frontierHash {
			return errors.New("durable verified recovery frontier proof is missing")
		}
		if err := backend.Authenticate(ctx, savedFrontier); err != nil {
			return fmt.Errorf("reauthenticate verified recovery frontier: %w", err)
		}
		// Storage authorizes detached reads against the currently active
		// admission. FrontierGeneration is diagnostic only; using the older
		// staging token here would make a safe restart look stale.
		frontierGeneration := progress.Generation
		currentLedger, err = s.LoadReplayRecoveryFrontier(ctx, id, frontierGeneration, savedFrontier.Header)
		if err != nil {
			return fmt.Errorf("load verified recovery frontier: %w", err)
		}
		if currentLedger == nil || currentLedger.Sequence() != savedFrontier.Header.LedgerIndex || currentLedger.Hash() != savedFrontier.Header.Hash || !currentLedger.IsClosed() {
			return errors.New("loaded verified recovery frontier has the wrong identity")
		}
		current = savedFrontier
	}
	for {
		routeReady := progress.Phase == replayfault.RecoveryPhaseReadyToActivate || progress.Phase == replayfault.RecoveryPhaseActivating
		if routeReady && !backend.CaughtUp(ctx, current.Header) {
			progress.Phase = replayfault.RecoveryPhaseCatchingUp
			nextProgress, updateErr := s.replayFaults.UpdateRecovery(id, expectedGeneration, progress)
			if updateErr != nil {
				return fmt.Errorf("resume verified successor catch-up: %w", updateErr)
			}
			progress = nextProgress
			expectedGeneration = progress.Generation
			routeReady = false
		}
		for steps := uint32(0); !routeReady; steps++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if steps >= maxPivotRecoveryHeaders {
				return errors.New("verified-pivot recovery route exceeds header limit")
			}

			var next RecoveryTarget
			if savedTarget.Header.LedgerIndex == current.Header.LedgerIndex+1 {
				next = savedTarget
			}
			if next.Header.LedgerIndex == 0 {
				next, err = backend.Next(ctx, current.Header)
				if err != nil {
					return fmt.Errorf("acquire authenticated successor header: %w", err)
				}
			}
			if err := validateReplayRecoveryNext(current.Header, next); err != nil {
				return err
			}
			rawTarget, marshalErr := encodeRecoveryTarget(next)
			if marshalErr != nil {
				return marshalErr
			}
			nextProgress, setErr := s.replayFaults.SetRecoveryTarget(id, expectedGeneration, rawTarget)
			if setErr == nil {
				progress = nextProgress
				expectedGeneration = progress.Generation
			}
			err = setErr
			if err != nil {
				return fmt.Errorf("persist authenticated successor proof: %w", err)
			}
			savedTarget = next
			if err := backend.Authenticate(ctx, next); err != nil {
				return fmt.Errorf("authenticate successor route: %w", err)
			}
			derived, replayErr := s.executePivotSuccessor(ctx, backend, currentLedger, next)
			if replayErr != nil {
				return replayErr
			}
			current = next
			currentLedger = derived
			processedSuccessor = true
			if progress.SuccessorSequence == 0 {
				progress.SuccessorSequence = current.Header.LedgerIndex
				progress.SuccessorHash = current.Header.Hash
			}
			progress.FrontierSequence = current.Header.LedgerIndex
			progress.FrontierHash = current.Header.Hash
			recordPivotRecoveryProgress(&progress, backend)
			recordPivotRecoveryLag(&progress, backend, progress.FrontierSequence)
			if err := s.StageReplayRecoveryFrontier(ctx, id, expectedGeneration, currentLedger); err != nil {
				return fmt.Errorf("stage executed successor frontier: %w", err)
			}
			progress.FrontierGeneration = expectedGeneration
			frontierProof, marshalErr := encodeRecoveryTarget(current)
			if marshalErr != nil {
				return marshalErr
			}
			nextProgress, updateErr := s.replayFaults.UpdateRecoveryWithFrontier(id, expectedGeneration, progress, frontierProof)
			if updateErr == nil {
				progress = nextProgress
				expectedGeneration = progress.Generation
			}
			err = updateErr
			if err != nil {
				return fmt.Errorf("persist executed successor frontier: %w", err)
			}
			frontierSequence, frontierHash = progress.FrontierSequence, progress.FrontierHash
			if backend.CaughtUp(ctx, current.Header) {
				break
			}
		}

		if !processedSuccessor || current.Header.LedgerIndex <= pivot.Header.LedgerIndex {
			return errors.New("verified-pivot route did not execute a successor")
		}
		progress.FrontierSequence = frontierSequence
		progress.FrontierHash = frontierHash
		if progress.Phase != replayfault.RecoveryPhaseActivating {
			progress.Phase = replayfault.RecoveryPhaseReadyToActivate
			nextProgress, updateErr := s.replayFaults.UpdateRecovery(id, expectedGeneration, progress)
			if updateErr == nil {
				progress = nextProgress
				expectedGeneration = progress.Generation
			}
			err = updateErr
			if err != nil {
				return fmt.Errorf("persist verified successor: %w", err)
			}
		}

		progress.Phase = replayfault.RecoveryPhaseActivating
		nextProgress, updateErr := s.replayFaults.UpdateRecovery(id, expectedGeneration, progress)
		if updateErr == nil {
			progress = nextProgress
			expectedGeneration = progress.Generation
		}
		err = updateErr
		if err != nil {
			return fmt.Errorf("stage verified successor activation: %w", err)
		}
		if err := s.activateReplayRecoverySuccessor(ctx, currentLedger, current, id, expectedGeneration); err != nil {
			return fmt.Errorf("activate verified successor: %w", err)
		}
		if err := backend.Handoff(ctx, current.Header); err != nil {
			if !backend.CaughtUp(ctx, current.Header) {
				progress.Phase = replayfault.RecoveryPhaseCatchingUp
				nextProgress, updateErr := s.replayFaults.UpdateRecovery(id, expectedGeneration, progress)
				if updateErr != nil {
					return fmt.Errorf("resume verified successor after handoff race: %w", updateErr)
				}
				progress = nextProgress
				expectedGeneration = progress.Generation
				continue
			}
			return fmt.Errorf("handoff verified successor: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !backend.CaughtUp(ctx, current.Header) {
			continue
		}
		if err := s.replayFaults.ActivateRecovery(id, expectedGeneration); err != nil {
			return fmt.Errorf("commit verified successor recovery: %w", err)
		}
		return nil
	}
}

func (s *Service) executePivotSuccessor(ctx context.Context, backend PivotRecoveryBackend, parent *ledger.Ledger, target RecoveryTarget) (*ledger.Ledger, error) {
	acquired, err := backend.Acquire(ctx, target, parent)
	if err != nil {
		return nil, fmt.Errorf("acquire successor transactions: %w", err)
	}
	if err := verifyReplayRecoveryInput(parent, acquired, target); err != nil {
		return nil, fmt.Errorf("verify successor transaction input: %w", err)
	}
	replay, err := inbound.NewStoredLedgerReplay(parent, acquired, nil)
	if err != nil {
		return nil, fmt.Errorf("prepare successor replay: %w", err)
	}
	derived, err := replay.ApplyContext(ctx, s.EngineConfigForReplay(parent))
	if err != nil {
		if replayExecutionDisagreement(err) {
			retry, prepareErr := inbound.NewStoredLedgerReplay(parent, acquired, nil)
			if prepareErr != nil {
				return nil, fmt.Errorf("prepare successor reproduction: %w", prepareErr)
			}
			_, retryErr := retry.ApplyContext(ctx, s.EngineConfigForReplay(parent))
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if retryErr == nil || !sameReplayFailure(err, retryErr) {
				return nil, fmt.Errorf("successor replay failure did not reproduce: %v; second result: %v", err, retryErr)
			}
		}
		return nil, fmt.Errorf("execute successor replay: %w", err)
	}
	if derived == nil || derived.Hash() != target.Header.Hash || derived.Sequence() != target.Header.LedgerIndex {
		return nil, errors.New("successor replay commitments disagree with authenticated header")
	}
	return derived, nil
}

func verifyReplayRecoveryInput(parent, acquired *ledger.Ledger, target RecoveryTarget) error {
	if parent == nil {
		return errors.New("successor replay parent is nil")
	}
	if acquired == nil || !acquired.IsClosed() {
		return errors.New("successor transaction acquisition is not closed")
	}
	if acquired.Sequence() != target.Header.LedgerIndex || acquired.Hash() != target.Header.Hash {
		return errors.New("successor transaction acquisition returned the wrong ledger identity")
	}
	if header.CalculateHash(acquired.Header()) != target.Header.Hash {
		return errors.New("successor transaction acquisition header hash is invalid")
	}
	if acquired.ParentHash() != parent.Hash() || acquired.Sequence() != parent.Sequence()+1 {
		return errors.New("successor transaction acquisition is not adjacent to its parent")
	}
	if acquired.Header().TxHash != target.Header.TxHash {
		return errors.New("successor transaction acquisition has the wrong transaction root")
	}
	txRoot, err := acquired.TxMapHash()
	if err != nil {
		return fmt.Errorf("read successor transaction root: %w", err)
	}
	if txRoot != target.Header.TxHash {
		return errors.New("successor transaction root disagrees with authenticated header")
	}
	return nil
}

func (s *Service) activateReplayRecoverySuccessor(ctx context.Context, successor *ledger.Ledger, target RecoveryTarget, id string, generation uint64) error {
	if successor == nil || successor.Sequence() != target.Header.LedgerIndex || successor.Hash() != target.Header.Hash {
		return errors.New("verified successor identity is invalid")
	}
	stage, err := s.StageRecoveryLedger(ctx, successor)
	if err != nil {
		return err
	}
	defer stage.Retire()
	return s.ActivateRecoveryStorage(ctx, stage, RecoveryStorageActivation{FaultID: id, Generation: generation})
}

func (s *Service) ReplayRecoveryDiagnostics() replayfault.Status {
	status := s.ReplayFaultStatus()
	if status.Recovery.InFlight && status.Recovery.Strategy == replayfault.RecoveryStrategyVerifiedPivot {
		backend := s.pivotRecoveryBackend()
		recordPivotRecoveryProgress(&status.Recovery, backend)
		recordPivotRecoveryLag(&status.Recovery, backend, status.Recovery.FrontierSequence)
	}
	return status
}
