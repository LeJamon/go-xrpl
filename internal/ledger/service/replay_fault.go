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
	Detail                   json.RawMessage     `json:"detail,omitempty"`
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
func (s *Service) ApplyReplay(ctx context.Context, replay *inbound.ReplayDelta, cfg tx.EngineConfig, authenticated bool) (derived *ledger.Ledger, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			derived = nil
			err = fmt.Errorf("replay panic: %v", recovered)
			s.recordReplayFailure(ctx, replay, cfg, authenticated, err, false)
		}
	}()
	if s.ReplayBlocked() {
		return nil, replayfault.ErrBlocked
	}
	derived, err = replay.Apply(cfg)
	if err == nil {
		s.mu.Lock()
		s.replayVerifiedHash = derived.Hash()
		s.mu.Unlock()
		return derived, nil
	}
	s.recordReplayFailure(ctx, replay, cfg, authenticated, err, false)
	return nil, err
}

func (s *Service) recordReplayFailure(ctx context.Context, replay *inbound.ReplayDelta, cfg tx.EngineConfig, authenticated bool, cause error, lockHeld bool) {
	parent := replay.Parent()
	h := replay.TargetHeader()
	evidence := replayEvidence{Target: h, Transactions: replay.OrderedTxs(), NetworkID: cfg.NetworkID, Authenticated: authenticated}
	if parent != nil {
		evidence.Parent = parent.Header()
		evidence.Fees = parent.Fees()
	}
	if cfg.Rules != nil {
		evidence.Amendments = cfg.Rules.EnabledIDs()
	}
	evidence.Detail, _ = json.Marshal(replay.Evidence().Failure)
	raw, _ := json.Marshal(evidence)
	if !lockHeld {
		s.mu.Lock()
	}
	existed := s.ReplayBlocked()
	err := s.replayFaults.Record(replayfault.Fault{Class: replayfault.Unclassified, ParentHash: h.ParentHash, TargetHash: h.Hash, Sequence: h.LedgerIndex, Message: cause.Error(), Evidence: raw})
	if !lockHeld {
		s.mu.Unlock()
	}
	if err != nil {
		s.logger.Error("persist replay fault", "error", err)
	}
	if existed {
		return
	}
	fault := s.replayFaults.Snapshot()
	if fault == nil {
		return
	}
	// Evidence capture and independent reproduction must not hold the router or
	// consensus thread while a large parent state is traversed.
	s.lifecycleMu.Lock()
	if s.lifecycleState != serviceRunning {
		s.lifecycleMu.Unlock()
		return
	}
	s.consensusWG.Add(1)
	s.lifecycleMu.Unlock()
	go func() {
		defer s.consensusWG.Done()
		s.replayRecoveryMu.Lock()
		defer s.replayRecoveryMu.Unlock()
		captureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		s.diagnoseReplayFault(captureCtx, *fault, evidence, parent, replay, cfg, cause)
	}()
}

func (s *Service) diagnoseReplayFault(ctx context.Context, fault replayfault.Fault, evidence replayEvidence, parent *ledger.Ledger, replay *inbound.ReplayDelta, cfg tx.EngineConfig, cause error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fault.Class = replayfault.Unclassified
			fault.Message += fmt.Sprintf("; evidence capture panic: %v", recovered)
			if err := s.replayFaults.Update(fault.ID, fault); err != nil {
				s.logger.Error("persist replay diagnosis panic", "error", err)
			}
		}
	}()

	verified, err := s.captureReplayParent(ctx, fault.ID, parent)
	if err != nil {
		fault.Class = replayStateFailureClass(err)
		fault.Message += "; parent verification: " + err.Error()
	} else {
		evidence.ParentSnapshot = s.config.ReplayFaultPath != ""
		retry, retryErr := replay.Retry(verified)
		if retryErr == nil {
			_, retryErr = retry.Apply(cfg)
		}
		if evidence.Authenticated && retryErr != nil && sameReplayFailure(retryErr, cause) && replayExecutionDisagreement(retryErr) {
			fault.Class = replayfault.ExecutionDisagreement
		}
	}
	fault.Evidence, _ = json.Marshal(evidence)
	if err := s.replayFaults.Update(fault.ID, fault); err != nil {
		s.logger.Error("update replay fault evidence", "error", err)
	}
	if fault.Class == replayfault.MissingState || fault.Class == replayfault.CorruptState {
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
		return errors.New("replay evidence capture or recovery already running")
	}
	defer s.replayRecoveryMu.Unlock()
	return s.replayFaults.Revalidate(ctx, id, func(ctx context.Context, fault replayfault.Fault) error {
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
	if evidence.TransactionMapIncomplete && evidence.Parent.Hash != ([32]byte{}) {
		expected = fault.TargetHash
	}
	return hash == expected && evidence.Authenticated && fault.AcquisitionAttempts > 0 && fault.AcquisitionAttempts <= 3
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
	if evidence.TransactionMapIncomplete && evidence.Parent.Hash != ([32]byte{}) {
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

func (s *Service) RecordReplayPreparationFailure(ctx context.Context, h header.LedgerHeader, txMap *shamap.SHAMap, parent *ledger.Ledger, authenticated bool, cause error) {
	if s.ReplayBlocked() {
		return
	}
	evidence := replayEvidence{Target: h, Authenticated: authenticated, NetworkID: s.config.NetworkID}
	class := replayStateFailureClass(cause)
	if parent == nil {
		class = replayfault.MissingState
	} else {
		evidence.Parent = parent.Header()
		evidence.Fees = parent.Fees()
	}
	if txMap != nil {
		if err := txMap.ForEachCtx(ctx, func(item *shamap.Item) bool {
			evidence.TransactionLeaves = append(evidence.TransactionLeaves, replayStateItem{item.Key(), item.Data()})
			return true
		}); err != nil {
			class = replayStateFailureClass(err)
			evidence.TransactionMapIncomplete = true
		}
	} else if h.TxHash != ([32]byte{}) {
		class = replayfault.MissingState
		evidence.TransactionMapIncomplete = true
	}
	raw, _ := json.Marshal(evidence)
	s.mu.Lock()
	if s.ReplayBlocked() {
		s.mu.Unlock()
		return
	}
	err := s.replayFaults.Record(replayfault.Fault{Class: class, ParentHash: h.ParentHash, TargetHash: h.Hash, Sequence: h.LedgerIndex, Message: cause.Error(), Evidence: raw})
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
	repaired := verified
	if err := s.lockOpenLedgerIfRunning(openLedgerPreferredSwitch); err != nil {
		return err
	}
	defer s.openLedgerMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()
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
	s.enqueueNodePersist(repaired)
	s.replayRepairParent = nil
	s.replayRepairTarget = nil
	return nil
}
