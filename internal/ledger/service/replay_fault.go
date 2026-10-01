package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
)

type replayEvidence struct {
	Origin                   string              `json:"origin,omitempty"`
	RepairClass              replayfault.Class   `json:"repair_class,omitempty"`
	TransactionMapIncomplete bool                `json:"transaction_map_incomplete,omitempty"`
	TransactionLeaves        []replayStateItem   `json:"transaction_leaves,omitempty"`
	Parent                   header.LedgerHeader `json:"parent"`
	Target                   header.LedgerHeader `json:"target"`
	Fees                     drops.Fees          `json:"fees"`
	Transactions             []inbound.DecodedTx `json:"transactions"`
	Amendments               [][32]byte          `json:"amendments"`
	NetworkID                uint32              `json:"network_id"`
	Authenticated            bool                `json:"authenticated"`
	AcquisitionClass         replayfault.Class   `json:"acquisition_class,omitempty"`
	AcquisitionError         string              `json:"acquisition_error,omitempty"`
	ParentSnapshot           bool                `json:"parent_snapshot"`
	RepairHash               [32]byte            `json:"repair_hash,omitempty"`
	RepairSequence           uint32              `json:"repair_sequence,omitempty"`
	MissingNodeHash          [32]byte            `json:"missing_node_hash,omitempty"`
	MissingTree              string              `json:"missing_tree,omitempty"`
	Detail                   json.RawMessage     `json:"detail,omitempty"`
}

type replayRepairReservation struct {
	// These pointers remain reusable until the durable replay-fault clear has
	// committed. Cleanup compares identity so a newer acquisition is preserved.
	parent *ledger.Ledger
	target *ledger.Ledger
}

const replayFaultOriginStateBaseRecertification = "state_base_recertification"
const replayFaultOriginExecution = "execution"

func replayFaultStateClass(class replayfault.Class) bool {
	return class == replayfault.MissingState || class == replayfault.CorruptState
}

type replaySnapshotItem struct {
	replayStateItem
	Transaction bool `json:"transaction,omitempty"`
}

type replayStateItem struct {
	Key  [32]byte `json:"key"`
	Data []byte   `json:"data"`
}

func (s *Service) ReplayBlocked() bool { return s.replayFaults != nil && s.replayFaults.Blocked() }
func (s *Service) ReplayFaultStatus() replayfault.Status {
	status := s.replayFaults.Status()
	if status.Fault != nil {
		status.Recovery.AcquisitionAttempts = status.Fault.AcquisitionAttempts
		status.Recovery.AcquisitionError = status.Fault.AcquisitionError
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	status.TransitionVerification = "unknown"
	if s.closedLedger != nil {
		status.LastTransitionHash = s.closedLedger.Hash()
		if status.LastTransitionHash == s.replayAcquiredHash {
			status.TransitionVerification = "acquired_state"
		}
		if status.LastTransitionHash == s.replayVerifiedHash {
			status.TransitionVerification = "locally_replayed"
		}
	}
	return status
}
func (s *Service) WithValidatorDuty(fn func() error) error {
	if s.replayFaults == nil {
		return fn()
	}
	return s.replayFaults.WithValidator(fn)
}

// ApplyReplay keeps failures out of the canonical ledger and latches the duty
// gate before returning control to an acquisition fallback.
func (s *Service) ApplyReplay(ctx context.Context, replay *inbound.ReplayDelta, cfg tx.EngineConfig, authenticated bool) (*ledger.Ledger, error) {
	releaseAdmission, err := s.AcquireStateAdmission(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseAdmission()
	return s.applyReplay(ctx, replay, cfg, authenticated, false)
}

func replayEvidenceFor(replay *inbound.ReplayDelta, cfg tx.EngineConfig, authenticated bool) replayEvidence {
	evidence := replayEvidence{Target: replay.TargetHeader(), Transactions: replay.OrderedTxs(), NetworkID: cfg.NetworkID, Authenticated: authenticated}
	if parent := replay.Parent(); parent != nil {
		evidence.Parent = parent.Header()
		evidence.Fees = parent.Fees()
	}
	if cfg.Rules != nil {
		evidence.Amendments = cfg.Rules.EnabledIDs()
	}
	return evidence
}

func (s *Service) applyReplay(ctx context.Context, replay *inbound.ReplayDelta, cfg tx.EngineConfig, authenticated, lockHeld bool) (derived *ledger.Ledger, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if replay == nil {
		return nil, errors.New("replay transition is required")
	}
	evidence := replayEvidenceFor(replay, cfg, authenticated)
	raw, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h := evidence.Target
	id, err := s.replayFaults.BeginReplay(replayfault.Fault{ParentHash: h.ParentHash, TargetHash: h.Hash, Sequence: h.LedgerIndex, Evidence: raw})
	if err != nil {
		return nil, fmt.Errorf("persist replay intent: %w", err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			derived = nil
			err = fmt.Errorf("replay panic: %v", recovered)
			s.recordReplayFailure(ctx, id, replay, cfg, evidence, err, lockHeld)
		}
	}()
	derived, err = replay.ApplyContext(ctx, cfg)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if cancelErr := s.replayFaults.CancelReplay(id); cancelErr != nil {
				return nil, errors.Join(err, fmt.Errorf("clear canceled replay intent: %w", cancelErr))
			}
			return nil, err
		}
		s.recordReplayFailure(ctx, id, replay, cfg, evidence, err, lockHeld)
		return nil, err
	}
	if err := s.replayFaults.CompleteReplay(id); err != nil {
		return nil, fmt.Errorf("commit verified replay: %w", err)
	}
	if !lockHeld {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	s.replayVerifiedHash = derived.Hash()
	return derived, nil
}

func (s *Service) recordReplayFailure(ctx context.Context, id string, replay *inbound.ReplayDelta, cfg tx.EngineConfig, evidence replayEvidence, cause error, lockHeld bool) {
	parent := replay.Parent()
	h := evidence.Target
	replayEvidence := replay.Evidence()
	evidence.Detail, _ = json.Marshal(replayEvidence.Failure)
	class := replayStateFailureClass(cause)
	if replayFaultStateClass(class) {
		evidence.RepairClass = class
		if missing, ok := missingNodeHash(cause); ok {
			evidence.MissingNodeHash = missing
		}
	}
	raw, _ := json.Marshal(evidence)
	if !lockHeld {
		s.mu.Lock()
	}
	message := "replay transition failed"
	if cause != nil {
		message = cause.Error()
	}
	err := s.replayFaults.FailReplay(id, replayfault.Fault{Class: class, ParentHash: h.ParentHash, TargetHash: h.Hash, Sequence: h.LedgerIndex, Message: message, Evidence: raw})
	if !lockHeld {
		s.mu.Unlock()
	}
	if err != nil {
		s.logger.Error("persist replay fault", "error", err)
	}
	fault := s.replayFaults.Snapshot()
	if fault == nil || fault.ID != id {
		return
	}
	if replayFaultStateClass(class) {
		if lockHeld {
			_, requestErr := s.admitReplayRecoveryJob(func(workerCtx context.Context) error {
				if err := workerCtx.Err(); err != nil {
					return err
				}
				return s.requestReplayParentRepair(*fault, evidence)
			})
			if requestErr != nil && !errors.Is(requestErr, errReplayRecoveryWorkerRunning) {
				s.logger.Warn("replay state repair request unavailable", "error", requestErr)
			}
		} else if err := s.requestReplayParentRepair(*fault, evidence); err != nil {
			s.logger.Warn("replay state repair request unavailable", "error", err)
		}
		return
	}
	// Evidence capture and independent reproduction must not hold the router or
	// consensus thread while a large parent state is traversed. It shares the
	// same admitted worker as explicit recovery, so the two operations cannot
	// overlap or outlive the service lifecycle.
	_, err = s.admitReplayRecoveryJob(func(workerCtx context.Context) error {
		captureCtx, cancel := context.WithTimeout(workerCtx, 5*time.Minute)
		defer cancel()
		releaseAdmission, admissionErr := s.AcquireStateAdmission(captureCtx)
		if admissionErr != nil {
			return admissionErr
		}
		defer releaseAdmission()
		s.diagnoseReplayFault(captureCtx, *fault, evidence, parent, replay, cfg, cause)
		return nil
	})
	if err != nil && !errors.Is(err, errReplayRecoveryWorkerRunning) {
		s.logger.Error("start replay evidence capture", "error", err)
	}
}

func (s *Service) diagnoseReplayFault(ctx context.Context, fault replayfault.Fault, evidence replayEvidence, parent *ledger.Ledger, replay *inbound.ReplayDelta, cfg tx.EngineConfig, cause error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if !replayFaultStateClass(fault.Class) {
				fault.Class = replayfault.Unclassified
			}
			fault.Message += fmt.Sprintf("; evidence capture panic: %v", recovered)
			if err := s.replayFaults.Update(fault.ID, fault); err != nil {
				s.logger.Error("persist replay diagnosis panic", "error", err)
			}
		}
	}()

	verified, err := s.captureReplayParent(ctx, fault.ID, parent)
	if err != nil {
		if diagnosed := replayStateFailureClass(err); diagnosed != replayfault.Unclassified {
			fault.Class = diagnosed
		} else if !replayFaultStateClass(fault.Class) {
			fault.Class = replayfault.Unclassified
		}
		fault.Message += "; parent verification: " + err.Error()
	} else {
		evidence.ParentSnapshot = s.config.ReplayFaultPath != ""
		retry, retryErr := replay.Retry(verified)
		if retryErr == nil {
			_, retryErr = retry.Apply(cfg)
		}
		if !replayFaultStateClass(fault.Class) && evidence.Authenticated && retryErr != nil && sameReplayFailure(retryErr, cause) && replayExecutionDisagreement(retryErr) {
			fault.Class = replayfault.ExecutionDisagreement
		}
	}
	fault.Evidence, _ = json.Marshal(evidence)
	if err := s.replayFaults.Update(fault.ID, fault); err != nil {
		s.logger.Error("update replay fault evidence", "error", err)
	}
	if replayFaultStateClass(fault.Class) {
		_ = s.requestReplayParentRepair(fault, evidence)
	}
}

func sameReplayFailure(first, second error) bool {
	var a, b *inbound.ReplayFailure
	if !errors.As(first, &a) || !errors.As(second, &b) {
		return false
	}
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func replayExecutionDisagreement(err error) bool {
	return errors.Is(err, inbound.ErrReplayTxDiverged) || errors.Is(err, inbound.ErrReplayMetadataDiverged) || errors.Is(err, inbound.ErrReplayStateDiverged) || errors.Is(err, inbound.ErrReplayHeaderDiverged)
}

func replayStateFailureClass(err error) replayfault.Class {
	if errors.Is(err, shamap.ErrNodeNotInStore) || errors.Is(err, svcerr.ErrLedgerNotFound) || errors.Is(err, os.ErrNotExist) {
		return replayfault.MissingState
	}
	if errors.Is(err, errReplayParentCorrupt) || errors.Is(err, shamap.ErrInvalidNodeData) {
		return replayfault.CorruptState
	}
	return replayfault.Unclassified
}

var errReplayParentCorrupt = errors.New("replay parent state commitment mismatch")

func (s *Service) replayParentPath(id string) string {
	return s.config.ReplayFaultPath + ".parent-" + id
}

func (s *Service) captureReplayParent(ctx context.Context, id string, parent *ledger.Ledger) (_ *ledger.Ledger, err error) {
	if parent == nil {
		return nil, shamap.ErrNodeNotInStore
	}
	original, err := parent.StateMapSnapshot()
	if err != nil {
		return nil, err
	}
	rebuilt := shamap.New(shamap.TypeState)
	var file *os.File
	var encoder *json.Encoder
	if s.config.ReplayFaultPath != "" {
		file, err = os.CreateTemp(filepath.Dir(s.config.ReplayFaultPath), ".replay-parent-*")
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
		encoder = json.NewEncoder(file)
	}
	var itemErr error
	err = original.ForEachCtx(ctx, func(item *shamap.Item) bool {
		itemErr = rebuilt.Put(item.Key(), item.Data())
		if itemErr == nil && encoder != nil {
			itemErr = encoder.Encode(replayStateItem{item.Key(), item.Data()})
		}
		return itemErr == nil
	})
	if err != nil {
		return nil, err
	}
	if itemErr != nil {
		return nil, itemErr
	}
	h := parent.Header()
	root, err := rebuilt.Hash()
	if err != nil {
		return nil, err
	}
	if root != h.AccountHash || header.CalculateHash(h) != h.Hash {
		return nil, errReplayParentCorrupt
	}
	parentTx, err := parent.TxMapSnapshot()
	if err != nil {
		return nil, err
	}
	rebuiltTx := shamap.New(shamap.TypeTransaction)
	err = parentTx.ForEachCtx(ctx, func(item *shamap.Item) bool {
		itemErr = rebuiltTx.PutWithNodeType(item.Key(), item.Data(), shamap.NodeTypeTransactionWithMeta)
		if itemErr == nil && encoder != nil {
			itemErr = encoder.Encode(replaySnapshotItem{replayStateItem: replayStateItem{Key: item.Key(), Data: item.Data()}, Transaction: true})
		}
		return itemErr == nil
	})
	if err != nil {
		return nil, err
	}
	if itemErr != nil {
		return nil, itemErr
	}
	txRoot, err := rebuiltTx.Hash()
	if err != nil {
		return nil, err
	}
	if txRoot != h.TxHash {
		return nil, errReplayParentCorrupt
	}
	verified, err := ledger.NewFromHeader(h, rebuilt, rebuiltTx, parent.Fees())
	if err != nil {
		return nil, err
	}
	if file != nil {
		if err = file.Sync(); err != nil {
			return nil, err
		}
		if err = file.Close(); err != nil {
			return nil, err
		}
		if err = os.Rename(file.Name(), s.replayParentPath(id)); err != nil {
			return nil, err
		}
		dir, openErr := os.Open(filepath.Dir(s.config.ReplayFaultPath))
		if openErr != nil {
			return nil, openErr
		}
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
	}
	return verified, nil
}

func (s *Service) loadReplayParent(ctx context.Context, fault replayfault.Fault, evidence replayEvidence) (*ledger.Ledger, error) {
	if !evidence.ParentSnapshot || s.config.ReplayFaultPath == "" {
		s.mu.RLock()
		parent := s.replayRepairParent
		s.mu.RUnlock()
		var err error
		if parent == nil || parent.Hash() != fault.ParentHash {
			parent, err = s.GetLedgerByHashContext(ctx, fault.ParentHash)
		}
		if err != nil {
			return nil, err
		}
		return s.captureReplayParent(ctx, fault.ID, parent)
	}
	file, err := os.Open(s.replayParentPath(fault.ID))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	state := shamap.New(shamap.TypeState)
	txMap := shamap.New(shamap.TypeTransaction)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var item replaySnapshotItem
		err := decoder.Decode(&item)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.Join(errReplayParentCorrupt, err)
		}
		if item.Transaction {
			err = txMap.PutWithNodeType(item.Key, item.Data, shamap.NodeTypeTransactionWithMeta)
		} else {
			err = state.Put(item.Key, item.Data)
		}
		if err != nil {
			return nil, err
		}
	}
	root, err := state.Hash()
	if err != nil {
		return nil, err
	}
	if root != evidence.Parent.AccountHash || evidence.Parent.Hash != fault.ParentHash || header.CalculateHash(evidence.Parent) != fault.ParentHash {
		return nil, errReplayParentCorrupt
	}
	txRoot, err := txMap.Hash()
	if err != nil {
		return nil, err
	}
	if txRoot != evidence.Parent.TxHash {
		return nil, errReplayParentCorrupt
	}
	return ledger.NewFromHeader(evidence.Parent, state, txMap, evidence.Fees)
}

func (s *Service) RevalidateReplayFault(ctx context.Context, id string) error {
	s.lifecycleMu.Lock()
	if s.lifecycleState != serviceRunning {
		s.lifecycleMu.Unlock()
		return errServiceNotRunning
	}
	s.consensusWG.Add(1)
	s.lifecycleMu.Unlock()
	defer s.consensusWG.Done()
	if !s.replayRecoveryMu.TryLock() {
		return errReplayRecoveryWorkerRunning
	}
	defer s.replayRecoveryMu.Unlock()
	return s.revalidateReplayFault(ctx, id, nil)
}

func (s *Service) revalidateReplayFault(ctx context.Context, id string, onStarted func()) error {
	if ctx == nil {
		ctx = context.Background()
	}
	releaseAdmission, err := s.AcquireStateAdmission(ctx)
	if err != nil {
		return err
	}
	defer releaseAdmission()
	var reservation replayRepairReservation
	err = s.replayFaults.Revalidate(ctx, id, func(ctx context.Context, fault replayfault.Fault) error {
		if onStarted != nil {
			onStarted()
		}
		var evidence replayEvidence
		if err := json.Unmarshal(fault.Evidence, &evidence); err != nil {
			return err
		}
		if !evidence.Authenticated {
			s.mu.RLock()
			authenticate := s.replayAuthenticate
			s.mu.RUnlock()
			if authenticate == nil || !authenticate(evidence.Target) {
				return errors.New("fault target lacks trusted-validation authentication; cannot resume")
			}
			evidence.Authenticated = true
		}
		if evidence.NetworkID != s.config.NetworkID {
			return errors.New("replay evidence network does not match configuration")
		}
		if evidence.Origin == replayFaultOriginStateBaseRecertification || evidence.Origin == replayFaultOriginExecution {
			var err error
			reservation, err = s.revalidateStateBaseRecertificationFault(ctx, fault, evidence)
			return err
		}
		s.mu.RLock()
		reservation.parent = s.replayRepairParent
		reservation.target = s.replayRepairTarget
		s.mu.RUnlock()
		parent, err := s.loadReplayParent(ctx, fault, evidence)
		if err != nil {
			class := replayStateFailureClass(err)
			if class == replayfault.MissingState || class == replayfault.CorruptState {
				if fault.Class != replayfault.ExecutionDisagreement {
					fault.Class = class
				}
				evidence.RepairClass = class
				evidence.ParentSnapshot = false
				fault.Evidence, _ = json.Marshal(evidence)
				if updateErr := s.replayFaults.Update(fault.ID, fault); updateErr != nil {
					return updateErr
				}
			}
			if class == replayfault.MissingState || class == replayfault.CorruptState {
				if acquireErr := s.requestReplayParentRepair(fault, evidence); acquireErr != nil {
					return fmt.Errorf("verify replay parent: %w; acquisition: %v", err, acquireErr)
				}
			}
			return fmt.Errorf("verify replay parent: %w; repair requested, retry explicitly after acquisition", err)
		}
		evidence.Parent = parent.Header()
		txMap := shamap.New(shamap.TypeTransaction)
		for _, leaf := range evidence.TransactionLeaves {
			if err := txMap.PutWithNodeType(leaf.Key, leaf.Data, shamap.NodeTypeTransactionWithMeta); err != nil {
				return err
			}
		}
		for _, txn := range evidence.Transactions {
			if err := txMap.PutWithNodeType(txn.Hash, txn.LeafBlob, shamap.NodeTypeTransactionWithMeta); err != nil {
				return err
			}
		}
		if evidence.TransactionMapIncomplete {
			s.mu.RLock()
			repaired := s.replayRepairTarget
			s.mu.RUnlock()
			if repaired == nil || repaired.Hash() != fault.TargetHash {
				if acquireErr := s.requestReplayParentRepair(fault, evidence); acquireErr != nil {
					return acquireErr
				}
				return errors.New("target transaction acquisition requested; retry after completion")
			}
			reservation.target = repaired
			txMap, err = repaired.TxMapSnapshot()
			if err != nil {
				return err
			}
		}
		state, err := parent.StateMapSnapshot()
		if err != nil {
			return err
		}
		target, err := ledger.NewFromHeader(evidence.Target, state, txMap, parent.Fees())
		if err != nil {
			return err
		}
		if target.Hash() != fault.TargetHash {
			return errors.New("replay evidence target mismatch")
		}
		replay, err := inbound.NewStoredLedgerReplay(parent, target, nil)
		if err != nil {
			return err
		}
		if _, err := replay.Apply(s.EngineConfigForReplay(parent)); err != nil {
			return fmt.Errorf("failed transition still disagrees: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if fault.Class == replayfault.MissingState || fault.Class == replayfault.CorruptState || evidence.RepairClass == replayfault.MissingState || evidence.RepairClass == replayfault.CorruptState {
			return s.restoreReplayParent(ctx, parent)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.releaseReplayRepair(reservation)
	return nil
}

func (s *Service) revalidateStateBaseRecertificationFault(ctx context.Context, fault replayfault.Fault, evidence replayEvidence) (replayRepairReservation, error) {
	var reservation replayRepairReservation
	if evidence.RepairHash == ([32]byte{}) || evidence.RepairHash != fault.TargetHash || evidence.RepairSequence != fault.Sequence ||
		evidence.Target.Hash != fault.TargetHash || evidence.Target.ParentHash != fault.ParentHash || header.CalculateHash(evidence.Target) != fault.TargetHash {
		return reservation, errors.New("state base repair identity is invalid")
	}
	s.mu.RLock()
	reservation.parent = s.replayRepairParent
	repaired := s.replayRepairTarget
	validated := s.validatedLedger
	closed := s.closedLedger
	s.mu.RUnlock()
	if repaired == nil || repaired.Hash() != evidence.RepairHash {
		if err := s.requestReplayParentRepair(fault, evidence); err != nil {
			return reservation, err
		}
		return reservation, errors.New("state base repair requested; retry explicitly after acquisition")
	}
	reservation.target = repaired
	validatedIdentity := validated != nil && validated.Hash() == evidence.Target.Hash && validated.Sequence() == evidence.Target.LedgerIndex
	if validatedIdentity && !repaired.IsValidated() {
		if err := repaired.SetValidated(); err != nil {
			return reservation, fmt.Errorf("mark exact validated repair: %w", err)
		}
	}
	if evidence.Origin == replayFaultOriginExecution {
		if closed == nil || closed.Hash() != evidence.Target.Hash || closed.Sequence() != evidence.Target.LedgerIndex {
			return reservation, errors.New("closed ledger changed while execution repair was pending")
		}
		if err := s.persistRepairedLedger(ctx, repaired); err != nil {
			return reservation, fmt.Errorf("persist repaired execution state: %w", err)
		}
		if err := s.restoreReplayParentAt(ctx, repaired, evidence.Target.Hash); err != nil {
			return reservation, fmt.Errorf("install repaired execution state: %w", err)
		}
		return reservation, nil
	}
	if !validatedIdentity {
		return reservation, errors.New("validated ledger changed while state base repair was pending")
	}
	if err := s.restoreReplayParent(ctx, repaired); err != nil {
		return reservation, fmt.Errorf("install repaired state base: %w", err)
	}
	if err := s.persistRepairedValidatedTip(ctx, repaired); err != nil {
		return reservation, fmt.Errorf("publish repaired validated tip: %w", err)
	}
	if err := s.recertifyValidatedStateBase(ctx); err != nil {
		return reservation, fmt.Errorf("re-certify repaired state base: %w", err)
	}
	return reservation, nil
}

func (s *Service) SetReplayParentAcquirer(acquire func(uint32, [32]byte) error) {
	s.mu.Lock()
	s.replayAcquireParent = acquire
	s.mu.Unlock()
}

func (s *Service) ReplayRecoveryParent(hash [32]byte) bool {
	if s.replayFaults == nil {
		return false
	}
	fault := s.replayFaults.Snapshot()
	if fault == nil {
		return false
	}
	var evidence replayEvidence
	if json.Unmarshal(fault.Evidence, &evidence) != nil {
		return false
	}
	if fault.Class != replayfault.MissingState && fault.Class != replayfault.CorruptState && evidence.RepairClass != replayfault.MissingState && evidence.RepairClass != replayfault.CorruptState {
		return false
	}
	expected := fault.ParentHash
	if evidence.RepairHash != ([32]byte{}) {
		expected = evidence.RepairHash
	} else if evidence.TransactionMapIncomplete && evidence.Parent.Hash != ([32]byte{}) {
		expected = fault.TargetHash
	}
	return hash == expected && evidence.Authenticated && fault.AcquisitionAttempts > 0 && fault.AcquisitionAttempts <= 3
}

func replayFaultMatchesRepairTarget(fault *replayfault.Fault, hash [32]byte) bool {
	if fault == nil || hash == ([32]byte{}) {
		return false
	}
	if hash == fault.TargetHash {
		return true
	}
	var evidence replayEvidence
	return json.Unmarshal(fault.Evidence, &evidence) == nil &&
		evidence.RepairHash != ([32]byte{}) && hash == evidence.RepairHash
}

func (s *Service) requestReplayParentRepair(fault replayfault.Fault, evidence replayEvidence) error {
	if fault.Sequence == 0 || fault.ParentHash == ([32]byte{}) || evidence.Target.ParentHash != fault.ParentHash || header.CalculateHash(evidence.Target) != fault.TargetHash {
		return errors.New("invalid replay repair identity")
	}
	if !evidence.Authenticated {
		return errors.New("repair target has no trusted-validation authentication")
	}
	if fault.AcquisitionAttempts >= 3 {
		return errors.New("replay parent acquisition budget exhausted")
	}
	s.mu.RLock()
	acquire := s.replayAcquireParent
	s.mu.RUnlock()
	if acquire == nil {
		return errors.New("replay parent acquisition unavailable")
	}
	fault.Evidence, _ = json.Marshal(evidence)
	if err := s.replayFaults.Update(fault.ID, fault); err != nil {
		return err
	}
	if err := s.replayFaults.ReserveAcquisition(fault.ID, 3); err != nil {
		return err
	}
	seq, hash := fault.Sequence-1, fault.ParentHash
	if evidence.RepairHash != ([32]byte{}) {
		seq, hash = evidence.RepairSequence, evidence.RepairHash
	} else if evidence.TransactionMapIncomplete && evidence.Parent.Hash != ([32]byte{}) {
		seq, hash = fault.Sequence, fault.TargetHash
	}
	if err := acquire(seq, hash); err != nil {
		evidence.AcquisitionError = err.Error()
		fault.AcquisitionError = err.Error()
		evidence.AcquisitionClass = replayfault.TransientAcquisition
		fault.Evidence, _ = json.Marshal(evidence)
		_ = s.replayFaults.Update(fault.ID, fault)
		return err
	}
	return nil
}

func (s *Service) recordStateBaseRecertificationFailure(ctx context.Context, h header.LedgerHeader, mapType shamap.Type, cause error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || s.replayFaults == nil {
		return
	}
	class := replayStateFailureClass(cause)
	if !replayFaultStateClass(class) {
		return
	}
	s.mu.RLock()
	current := s.validatedLedger != nil && s.validatedLedger.IsValidated() && s.validatedLedger.Hash() == h.Hash
	authenticate := s.replayAuthenticate
	s.mu.RUnlock()
	if !current || h.Hash == ([32]byte{}) {
		return
	}
	authenticated := authenticate != nil && authenticate(h)
	s.mu.RLock()
	current = s.validatedLedger != nil && s.validatedLedger.IsValidated() && s.validatedLedger.Hash() == h.Hash
	if current {
		s.recordLiveStateVerificationFailure(ctx, h, mapType, cause, authenticated)
	}
	s.mu.RUnlock()
	if !current {
		return
	}
	if !authenticated {
		return
	}
	fault := s.replayFaults.Snapshot()
	if fault == nil {
		return
	}
	var evidence replayEvidence
	if err := json.Unmarshal(fault.Evidence, &evidence); err != nil {
		return
	}
	if err := s.requestReplayParentRepair(*fault, evidence); err != nil {
		s.logger.Warn("state base repair request unavailable", "error", err)
	}
}

func (s *Service) recordExecutionStateFailure(ctx context.Context, parent *ledger.Ledger, cause error) {
	if parent == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || s.replayFaults == nil {
		return
	}
	class := replayStateFailureClass(cause)
	if !replayFaultStateClass(class) {
		return
	}
	h := parent.Header()
	if h.Hash == ([32]byte{}) {
		return
	}
	s.mu.RLock()
	current := s.closedLedger != nil && s.closedLedger.Hash() == h.Hash && s.closedLedger.Sequence() == h.LedgerIndex
	authenticate := s.replayAuthenticate
	s.mu.RUnlock()
	if !current {
		return
	}
	authenticated := authenticate != nil && authenticate(h)
	s.mu.RLock()
	current = s.closedLedger != nil && s.closedLedger.Hash() == h.Hash && s.closedLedger.Sequence() == h.LedgerIndex
	if current {
		s.recordLiveStateVerificationFailureOrigin(ctx, h, shamap.TypeState, cause, authenticated, replayFaultOriginExecution)
	}
	s.mu.RUnlock()
	if !current || !authenticated {
		return
	}
	fault := s.replayFaults.Snapshot()
	if fault == nil {
		return
	}
	var evidence replayEvidence
	if err := json.Unmarshal(fault.Evidence, &evidence); err != nil {
		return
	}
	if err := s.requestReplayParentRepair(*fault, evidence); err != nil {
		s.logger.Warn("execution state repair request unavailable", "error", err)
	}
}

// recordLiveStateVerificationFailure latches a proven missing or corrupt node
// in a live validated ledger. The caller must establish that h is the current
// live validated ledger before calling this method. It intentionally does not
// take Service.mu, so retention and persistence guards can call it safely.
func (s *Service) recordLiveStateVerificationFailure(ctx context.Context, h header.LedgerHeader, mapType shamap.Type, cause error, authenticated bool) {
	s.recordLiveStateVerificationFailureOrigin(ctx, h, mapType, cause, authenticated, replayFaultOriginStateBaseRecertification)
}

func (s *Service) recordLiveStateVerificationFailureOrigin(ctx context.Context, h header.LedgerHeader, mapType shamap.Type, cause error, authenticated bool, origin string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || s.replayFaults == nil || s.ReplayBlocked() || h.Hash == ([32]byte{}) {
		return
	}
	class := replayStateFailureClass(cause)
	if !replayFaultStateClass(class) {
		return
	}
	evidence := replayEvidence{
		Origin:         origin,
		RepairClass:    class,
		Target:         h,
		NetworkID:      s.config.NetworkID,
		Authenticated:  authenticated,
		RepairHash:     h.Hash,
		RepairSequence: h.LedgerIndex,
		MissingTree:    stateBaseMapName(mapType),
	}
	if missing, ok := missingNodeHash(cause); ok {
		evidence.MissingNodeHash = missing
	}
	evidence.Detail, _ = json.Marshal(struct {
		Error string `json:"error"`
	}{Error: cause.Error()})
	raw, err := json.Marshal(evidence)
	if err != nil {
		s.logger.Error("encode state base recertification fault", "error", err)
		return
	}
	s.invalidateFastLoadCheckpointEligibility("durable validated state is incomplete")
	id, err := s.replayFaults.BeginReplay(replayfault.Fault{
		ParentHash: h.ParentHash, TargetHash: h.Hash, Sequence: h.LedgerIndex, Evidence: raw,
	})
	if err != nil {
		s.logger.Error("persist state base recertification intent", "error", err)
		return
	}
	message := cause.Error()
	if err := s.replayFaults.FailReplay(id, replayfault.Fault{
		Class: class, ParentHash: h.ParentHash, TargetHash: h.Hash, Sequence: h.LedgerIndex,
		Message: message, Evidence: raw,
	}); err != nil {
		s.logger.Error("persist state base recertification fault", "error", err)
		return
	}
	if fault := s.replayFaults.Snapshot(); fault != nil {
		if authenticated {
			s.logger.Info("live state verification fault latched", "sequence", h.LedgerIndex, "tree", evidence.MissingTree)
		}
	}
}

func (s *Service) persistRepairedLedger(ctx context.Context, repaired *ledger.Ledger) error {
	if repaired == nil {
		return errors.New("verified replay ledger is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	releaseAdmission, err := s.AcquireStateAdmission(ctx)
	if err != nil {
		return fmt.Errorf("admit repaired state: %w", err)
	}
	defer releaseAdmission()
	if err := s.VerifyDetachedLedger(ctx, repaired); err != nil {
		return fmt.Errorf("verify repaired state: %w", err)
	}
	if repaired.IsValidated() {
		return s.persistValidatedLedger(ctx, repaired, false)
	}
	if s.nodeStore == nil {
		return nil
	}
	return s.persistToNodeStore(ctx, repaired, repaired.Sequence())
}

func (s *Service) persistRepairedValidatedTip(ctx context.Context, repaired *ledger.Ledger) error {
	if s.nodeStore == nil {
		return errors.New("NodeStore is required to publish a repaired validated tip")
	}
	s.canonicalPersistMu.Lock()
	defer s.canonicalPersistMu.Unlock()
	return s.persistValidatedTipLocked(ctx, repaired, true)
}

func missingNodeHash(err error) ([32]byte, bool) {
	var missing *shamap.MissingNodeError
	if !errors.As(err, &missing) || missing == nil {
		return [32]byte{}, false
	}
	return missing.Hash, true
}

func stateBaseMapName(mapType shamap.Type) string {
	if mapType == shamap.TypeTransaction {
		return "transaction"
	}
	return "state"
}

func (s *Service) RecordReplayPreparationFailure(ctx context.Context, h header.LedgerHeader, txMap *shamap.SHAMap, parent *ledger.Ledger, authenticated bool, cause error) {
	if s.ReplayBlocked() {
		return
	}
	evidence := replayEvidence{Target: h, Authenticated: authenticated, NetworkID: s.config.NetworkID}
	class := replayStateFailureClass(cause)
	if cause != nil {
		evidence.Detail, _ = json.Marshal(struct {
			Error string `json:"error"`
		}{Error: cause.Error()})
	}
	if missing, ok := missingNodeHash(cause); ok {
		evidence.MissingNodeHash = missing
	}
	if parent == nil {
		class = replayfault.MissingState
	} else {
		evidence.Parent = parent.Header()
		evidence.Fees = parent.Fees()
	}
	evidence.TransactionMapIncomplete = h.TxHash != ([32]byte{})
	if parent == nil || evidence.TransactionMapIncomplete {
		evidence.RepairClass = replayfault.MissingState
	}
	if replayFaultStateClass(class) {
		evidence.RepairClass = class
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		s.logger.Error("encode replay preparation intent", "error", err)
		return
	}
	id, err := s.replayFaults.BeginReplay(replayfault.Fault{ParentHash: h.ParentHash, TargetHash: h.Hash, Sequence: h.LedgerIndex, Evidence: raw})
	if err != nil {
		s.logger.Error("persist replay preparation intent", "error", err)
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.mu.Lock()
			err := s.replayFaults.FailReplay(id, replayfault.Fault{Class: replayfault.Unclassified, Message: fmt.Sprintf("replay preparation panic: %v", recovered)})
			s.mu.Unlock()
			if err != nil {
				s.logger.Error("persist replay preparation panic", "error", err)
			}
		}
	}()
	if txMap != nil {
		evidence.TransactionMapIncomplete = false
		if err := txMap.ForEachCtx(ctx, func(item *shamap.Item) bool {
			evidence.TransactionLeaves = append(evidence.TransactionLeaves, replayStateItem{item.Key(), item.Data()})
			return true
		}); err != nil {
			if diagnosed := replayStateFailureClass(err); diagnosed != replayfault.Unclassified || !replayFaultStateClass(class) {
				class = diagnosed
			}
			if missing, ok := missingNodeHash(err); ok {
				evidence.MissingNodeHash = missing
			}
			if replayFaultStateClass(class) {
				evidence.RepairClass = class
			}
			evidence.TransactionMapIncomplete = true
		}
	} else if h.TxHash != ([32]byte{}) {
		class = replayfault.MissingState
		evidence.TransactionMapIncomplete = true
	}
	raw, _ = json.Marshal(evidence)
	s.mu.Lock()
	message := "replay preparation failed"
	if cause != nil {
		message = cause.Error()
	}
	err = s.replayFaults.FailReplay(id, replayfault.Fault{Class: class, ParentHash: h.ParentHash, TargetHash: h.Hash, Sequence: h.LedgerIndex, Message: message, Evidence: raw})
	s.mu.Unlock()
	if err != nil {
		s.logger.Error("persist replay preparation fault", "error", err)
		return
	}
	if class == replayfault.MissingState || class == replayfault.CorruptState {
		if fault := s.replayFaults.Snapshot(); fault != nil {
			_ = s.requestReplayParentRepair(*fault, evidence)
		}
	}
}

func (s *Service) SetReplayTargetAuthenticator(authenticate func(header.LedgerHeader) bool) {
	s.mu.Lock()
	s.replayAuthenticate = authenticate
	s.mu.Unlock()
}

func (s *Service) RecordReplayAcquisitionFailure(hash [32]byte, cause error) {
	fault := s.replayFaults.Snapshot()
	if fault == nil || (fault.ParentHash != hash && fault.TargetHash != hash) {
		return
	}
	var evidence replayEvidence
	if json.Unmarshal(fault.Evidence, &evidence) != nil {
		return
	}
	evidence.AcquisitionError = cause.Error()
	fault.AcquisitionError = cause.Error()
	evidence.AcquisitionClass = replayfault.TransientAcquisition
	fault.Evidence, _ = json.Marshal(evidence)
	if err := s.replayFaults.Update(fault.ID, *fault); err != nil {
		s.logger.Error("persist replay acquisition failure", "error", err)
	}
}

func (s *Service) restoreReplayParent(ctx context.Context, verified *ledger.Ledger) error {
	if err := s.persistRepairedLedger(ctx, verified); err != nil {
		return err
	}
	if err := s.restoreReplayParentAt(ctx, verified, [32]byte{}); err != nil {
		return err
	}
	return nil
}

func (s *Service) restoreReplayParentAt(ctx context.Context, verified *ledger.Ledger, expectedClosedHash [32]byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	repaired := verified
	if repaired == nil {
		return errors.New("verified replay ledger is required")
	}
	releaseAdmission, err := s.AcquireStateAdmission(ctx)
	if err != nil {
		return fmt.Errorf("admit repaired state: %w", err)
	}
	defer releaseAdmission()
	if err := s.VerifyDetachedLedger(ctx, repaired); err != nil {
		return fmt.Errorf("verify repaired state: %w", err)
	}
	if err := s.lockOpenLedgerIfRunning(openLedgerPreferredSwitch); err != nil {
		return err
	}
	defer s.openLedgerMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()
	if expectedClosedHash != ([32]byte{}) &&
		(s.closedLedger == nil || s.closedLedger.Hash() != expectedClosedHash) {
		return errors.New("closed ledger changed before repaired execution state could be installed")
	}
	if s.closedLedger != nil && s.closedLedger.Hash() == repaired.Hash() {
		newOpen, err := ledger.NewOpen(repaired, time.Now())
		if err != nil {
			return err
		}
		if err := s.acceptPreferredOpenLedgerLocked(repaired); err != nil {
			return err
		}
		s.closedLedger = repaired
		s.openLedger = newOpen
	}
	if s.validatedLedger != nil && s.validatedLedger.Hash() == repaired.Hash() {
		if !repaired.IsValidated() {
			if err := repaired.SetValidated(); err != nil {
				return err
			}
		}
		s.validatedLedger = repaired
	}
	s.putHistoryLocked(repaired)
	s.cachePersistedLedgerLocked(repaired)
	return nil
}

func (s *Service) releaseReplayRepair(reservation replayRepairReservation) {
	if reservation.parent == nil && reservation.target == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Preserve an already-latched fault. The service lock also serializes this
	// check with publication, while identity checks prevent cleanup from
	// clearing a reservation acquired for a newer fault.
	if s.replayFaults != nil && s.replayFaults.Snapshot() != nil {
		return
	}
	if reservation.parent != nil && s.replayRepairParent == reservation.parent {
		s.replayRepairParent = nil
	}
	if reservation.target != nil && s.replayRepairTarget == reservation.target {
		s.replayRepairTarget = nil
	}
}
