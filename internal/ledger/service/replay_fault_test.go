package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func replayFaultService(t *testing.T, path string) *Service {
	t.Helper()
	svc, err := New(Config{Standalone: true, GenesisConfig: genesis.DefaultConfig(), ReplayFaultPath: path})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	return svc
}

func replayFaultTarget(t *testing.T, parent *ledger.Ledger, divergent bool) *ledger.Ledger {
	t.Helper()
	closeTime := parent.CloseTime().Add(10 * time.Second)
	target, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, target.Close(closeTime, 0))
	if !divergent {
		return target
	}
	h := target.Header()
	h.AccountHash[0] ^= 1
	h.Hash = header.CalculateHash(h)
	state, err := parent.StateMapSnapshot()
	require.NoError(t, err)
	target, err = ledger.NewFromHeader(h, state, shamap.New(shamap.TypeTransaction), parent.Fees())
	require.NoError(t, err)
	return target
}

func TestReplayFaultPreservesFrontierAndRestartGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	svc := replayFaultService(t, path)
	parent := svc.GetClosedLedger()
	validated := svc.GetValidatedLedger()
	target := replayFaultTarget(t, parent, true)
	replay, err := inbound.NewStoredLedgerReplay(parent, target, nil)
	require.NoError(t, err)
	_, err = svc.ApplyReplay(context.Background(), replay, svc.EngineConfigForReplay(parent), true)
	require.Error(t, err)
	require.True(t, svc.ReplayBlocked())
	require.Same(t, parent, svc.GetClosedLedger())
	require.Same(t, validated, svc.GetValidatedLedger())
	require.ErrorIs(t, svc.SwitchToPreferredLedger(target), replayfault.ErrBlocked)
	state, err := parent.StateMapSnapshot()
	require.NoError(t, err)
	h := parent.Header()
	require.ErrorIs(t, svc.StoreLedgerWithState(context.Background(), &h, state, nil), replayfault.ErrBlocked)
	calls := 0
	require.ErrorIs(t, svc.WithValidatorDuty(func() error { calls++; return nil }), replayfault.ErrBlocked)
	require.Zero(t, calls)
	require.Eventually(t, func() bool {
		f := svc.replayFaults.Snapshot()
		return f != nil && f.Class == replayfault.ExecutionDisagreement
	}, 5*time.Second, time.Millisecond)
	f := svc.replayFaults.Snapshot()
	require.Error(t, svc.RevalidateReplayFault(context.Background(), f.ID))
	svc.Stop()
	restarted := replayFaultService(t, path)
	require.True(t, restarted.ReplayBlocked())
	require.Equal(t, f.ID, restarted.replayFaults.Snapshot().ID)
	require.Error(t, restarted.RevalidateReplayFault(context.Background(), f.ID))
	require.NoError(t, os.Remove(restarted.replayParentPath(f.ID)))
	restarted.SetReplayParentAcquirer(func(_ uint32, hash [32]byte) error {
		require.Equal(t, f.ParentHash, hash)
		require.True(t, restarted.ReplayRecoveryParent(hash))
		return nil
	})
	require.Error(t, restarted.RevalidateReplayFault(context.Background(), f.ID))
	require.Equal(t, replayfault.ExecutionDisagreement, restarted.replayFaults.Snapshot().Class)
	require.Equal(t, 1, restarted.replayFaults.Snapshot().AcquisitionAttempts)
	require.True(t, restarted.ReplayBlocked())
}

func TestReplayFaultExplicitRevalidationUsesSavedTransition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	svc := replayFaultService(t, path)
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, false)
	evidence := replayEvidence{Parent: parent.Header(), Target: target.Header(), Fees: parent.Fees(), Authenticated: true, ParentSnapshot: true}
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, svc.replayFaults.Record(replayfault.Fault{Class: replayfault.ExecutionDisagreement, ParentHash: parent.Hash(), TargetHash: target.Hash(), Sequence: target.Sequence(), Message: "previous engine disagreed", Evidence: raw}))
	fault := svc.replayFaults.Snapshot()
	_, err = svc.captureReplayParent(context.Background(), fault.ID, parent)
	require.NoError(t, err)
	svc.Stop()
	restarted := replayFaultService(t, path)
	require.Error(t, restarted.RevalidateReplayFault(context.Background(), "wrong-id"))
	require.True(t, restarted.ReplayBlocked())
	require.NoError(t, restarted.RevalidateReplayFault(context.Background(), fault.ID))
	require.False(t, restarted.ReplayBlocked())
	require.NoError(t, restarted.WithValidatorDuty(func() error { return nil }))
	reopened, err := replayfault.Open(path)
	require.NoError(t, err)
	require.False(t, reopened.Blocked())
}

func TestReplayFaultRejectsTamperedParentEvidence(t *testing.T) {
	svc := replayFaultService(t, filepath.Join(t.TempDir(), "fault.json"))
	parent := svc.GetClosedLedger()
	fault := replayfault.Fault{ID: "fixture", ParentHash: parent.Hash()}
	_, err := svc.captureReplayParent(context.Background(), fault.ID, parent)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(svc.replayParentPath(fault.ID), nil, 0600))
	_, err = svc.loadReplayParent(context.Background(), fault, replayEvidence{Parent: parent.Header(), ParentSnapshot: true})
	require.ErrorIs(t, err, errReplayParentCorrupt)
	require.NoError(t, os.WriteFile(svc.replayParentPath(fault.ID), []byte("{broken"), 0600))
	_, err = svc.loadReplayParent(context.Background(), fault, replayEvidence{Parent: parent.Header(), ParentSnapshot: true})
	require.Equal(t, replayfault.CorruptState, replayStateFailureClass(err))
}

func TestReplayFaultAcquisitionBudgetPersists(t *testing.T) {
	svc := replayFaultService(t, filepath.Join(t.TempDir(), "fault.json"))
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, false)
	evidence := replayEvidence{Parent: parent.Header(), Target: target.Header(), Authenticated: true}
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, svc.replayFaults.Record(replayfault.Fault{Class: replayfault.MissingState, ParentHash: parent.Hash(), TargetHash: target.Hash(), Sequence: target.Sequence(), Evidence: raw}))
	calls := 0
	svc.SetReplayParentAcquirer(func(seq uint32, hash [32]byte) error {
		require.Equal(t, parent.Sequence(), seq)
		require.Equal(t, parent.Hash(), hash)
		require.True(t, svc.ReplayRecoveryParent(hash))
		calls++
		return errors.New("transient network failure")
	})
	for i := 0; i < 5; i++ {
		fault := svc.replayFaults.Snapshot()
		require.NoError(t, json.Unmarshal(fault.Evidence, &evidence))
		require.Error(t, svc.requestReplayParentRepair(*fault, evidence))
	}
	require.Equal(t, 3, calls)
	fault := svc.replayFaults.Snapshot()
	require.NoError(t, json.Unmarshal(fault.Evidence, &evidence))
	require.Equal(t, 3, svc.replayFaults.Snapshot().AcquisitionAttempts)
	require.True(t, svc.ReplayBlocked())
}

func TestReplayFaultCannotClearUnauthenticatedTransition(t *testing.T) {
	svc := replayFaultService(t, "")
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, false)
	raw, err := json.Marshal(replayEvidence{Parent: parent.Header(), Target: target.Header()})
	require.NoError(t, err)
	require.NoError(t, svc.replayFaults.Record(replayfault.Fault{Class: replayfault.Unclassified, ParentHash: parent.Hash(), TargetHash: target.Hash(), Sequence: target.Sequence(), Evidence: raw}))
	require.ErrorContains(t, svc.RevalidateReplayFault(context.Background(), svc.replayFaults.Snapshot().ID), "authentication")
	require.True(t, svc.ReplayBlocked())
}

func TestReplayFaultMissingParentRequiresVerifiedRepairAndExplicitResume(t *testing.T) {
	svc := replayFaultService(t, filepath.Join(t.TempDir(), "fault.json"))
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, false)
	svc.SetReplayParentAcquirer(func(uint32, [32]byte) error { return nil })
	svc.RecordReplayPreparationFailure(context.Background(), target.Header(), shamap.New(shamap.TypeTransaction), nil, true, shamap.ErrNodeNotInStore)
	fault := svc.replayFaults.Snapshot()
	require.Equal(t, replayfault.MissingState, fault.Class)
	require.True(t, svc.ReplayBlocked())
	h := parent.Header()
	require.Error(t, svc.StoreLedgerWithState(context.Background(), &h, shamap.New(shamap.TypeState), nil))
	state, err := parent.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := parent.TxMapSnapshot()
	require.NoError(t, err)
	require.NoError(t, svc.StoreLedgerWithState(context.Background(), &h, state, txMap))
	require.True(t, svc.ReplayBlocked())
	require.Same(t, parent, svc.GetClosedLedger())
	require.NoError(t, svc.RevalidateReplayFault(context.Background(), fault.ID))
	require.False(t, svc.ReplayBlocked())
}

func TestReplayFaultCorruptParentIsNotExecutionDisagreement(t *testing.T) {
	svc := replayFaultService(t, filepath.Join(t.TempDir(), "fault.json"))
	parent := svc.GetClosedLedger()
	broken, err := ledger.NewFromHeader(parent.Header(), shamap.New(shamap.TypeState), shamap.New(shamap.TypeTransaction), parent.Fees())
	require.NoError(t, err)
	_, err = svc.captureReplayParent(context.Background(), "corrupt", broken)
	require.ErrorIs(t, err, errReplayParentCorrupt)
	require.Equal(t, replayfault.CorruptState, replayStateFailureClass(err))
	require.Equal(t, replayfault.MissingState, replayStateFailureClass(shamap.ErrNodeNotInStore))
	require.Equal(t, replayfault.Unclassified, replayStateFailureClass(context.Canceled))
}

func TestReplayFaultRestoresNonemptyParentTransactionsAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	svc := replayFaultService(t, path)
	original := svc.GetClosedLedger()
	state, err := original.StateMapSnapshot()
	require.NoError(t, err)
	txMap := shamap.New(shamap.TypeTransaction)
	key := [32]byte{0x42}
	require.NoError(t, txMap.PutWithNodeType(key, []byte("parent transaction fixture"), shamap.NodeTypeTransactionWithMeta))
	h := original.Header()
	h.TxHash, err = txMap.Hash()
	require.NoError(t, err)
	h.Hash = header.CalculateHash(h)
	parent, err := ledger.NewFromHeader(h, state, txMap, original.Fees())
	require.NoError(t, err)
	target := replayFaultTarget(t, parent, false)
	evidence := replayEvidence{Parent: h, Target: target.Header(), Fees: parent.Fees(), Authenticated: true, ParentSnapshot: true}
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, svc.replayFaults.Record(replayfault.Fault{Class: replayfault.CorruptState, ParentHash: parent.Hash(), TargetHash: target.Hash(), Sequence: target.Sequence(), Evidence: raw}))
	fault := svc.replayFaults.Snapshot()
	_, err = svc.captureReplayParent(context.Background(), fault.ID, parent)
	require.NoError(t, err)
	svc.Stop()
	restarted := replayFaultService(t, path)
	require.NoError(t, restarted.RevalidateReplayFault(context.Background(), fault.ID))
	repaired, err := restarted.GetLedgerByHash(parent.Hash())
	require.NoError(t, err)
	actual, err := repaired.TxMapHash()
	require.NoError(t, err)
	require.Equal(t, h.TxHash, actual)
	require.False(t, restarted.ReplayBlocked())
}

type replayUnavailableFamily struct {
	shamap.Family
	data []byte
}

func (f replayUnavailableFamily) Fetch(context.Context, [32]byte) ([]byte, error) { return f.data, nil }

func TestReplayFaultClassifiesIncompleteTransactionInputs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  []byte
		class replayfault.Class
	}{
		{"missing", nil, replayfault.MissingState}, {"corrupt", []byte("invalid stored node"), replayfault.CorruptState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := replayFaultService(t, "")
			parent := svc.GetClosedLedger()
			target := replayFaultTarget(t, parent, false)
			full := shamap.New(shamap.TypeTransaction)
			require.NoError(t, full.PutWithNodeType([32]byte{0x42}, []byte("transaction bytes fixture"), shamap.NodeTypeTransactionWithMeta))
			h := target.Header()
			var err error
			h.TxHash, err = full.Hash()
			require.NoError(t, err)
			h.Hash = header.CalculateHash(h)
			root, err := full.SerializeRoot()
			require.NoError(t, err)
			incomplete := shamap.New(shamap.TypeTransaction)
			require.NoError(t, incomplete.AddRootNode(h.TxHash, root))
			incomplete.SetFamily(replayUnavailableFamily{data: tc.data})
			calls := 0
			svc.SetReplayParentAcquirer(func(seq uint32, hash [32]byte) error {
				calls++
				require.Equal(t, h.LedgerIndex, seq)
				require.Equal(t, h.Hash, hash)
				require.True(t, svc.ReplayRecoveryParent(hash))
				return nil
			})
			svc.RecordReplayPreparationFailure(context.Background(), h, incomplete, parent, true, errors.New("transaction input unavailable"))
			require.Equal(t, tc.class, svc.replayFaults.Snapshot().Class)
			require.Equal(t, 1, calls)
			require.True(t, svc.ReplayBlocked())
		})
	}
}

func TestReplayFaultPersistsReproducedMetadataDisagreement(t *testing.T) {
	svc := replayFaultService(t, filepath.Join(t.TempDir(), "fault.json"))
	parent := svc.GetClosedLedger()
	blob, txHash := startupPaymentBlob(t, "replay-fault-destination", 1)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	_, err = svc.SubmitTransaction(parsed, blob, false)
	require.NoError(t, err)
	_, err = svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	targetMap, err := closed.TxMapSnapshot()
	require.NoError(t, err)
	targetMap, err = targetMap.SnapshotMutable()
	require.NoError(t, err)
	item, found, err := targetMap.Get(txHash)
	require.NoError(t, err)
	require.True(t, found)
	txBytes, metaBytes, err := tx.SplitTxWithMetaBlob(item.Data())
	require.NoError(t, err)
	meta, err := binarycodec.Decode(hex.EncodeToString(metaBytes))
	require.NoError(t, err)
	if meta["TransactionResult"] == "tesSUCCESS" {
		meta["TransactionResult"] = "tecNO_AUTH"
	} else {
		meta["TransactionResult"] = "tesSUCCESS"
	}
	encoded, err := binarycodec.Encode(meta)
	require.NoError(t, err)
	peerMeta, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	txVL, err := tx.EncodeWithVL(txBytes)
	require.NoError(t, err)
	metaVL, err := tx.EncodeWithVL(peerMeta)
	require.NoError(t, err)
	require.NoError(t, targetMap.PutWithNodeType(txHash, append(txVL, metaVL...), shamap.NodeTypeTransactionWithMeta))
	h := closed.Header()
	h.TxHash, err = targetMap.Hash()
	require.NoError(t, err)
	h.Hash = header.CalculateHash(h)
	state, err := parent.StateMapSnapshot()
	require.NoError(t, err)
	target, err := ledger.NewFromHeader(h, state, targetMap, parent.Fees())
	require.NoError(t, err)
	replay, err := inbound.NewStoredLedgerReplay(parent, target, nil)
	require.NoError(t, err)
	_, err = svc.ApplyReplay(context.Background(), replay, svc.EngineConfigForReplay(parent), true)
	require.ErrorIs(t, err, inbound.ErrReplayMetadataDiverged)
	require.Eventually(t, func() bool {
		f := svc.replayFaults.Snapshot()
		return f != nil && f.Class == replayfault.ExecutionDisagreement
	}, 5*time.Second, time.Millisecond)
	fault := svc.replayFaults.Snapshot()
	var evidence replayEvidence
	require.NoError(t, json.Unmarshal(fault.Evidence, &evidence))
	require.Len(t, evidence.Transactions, 1)
	require.Equal(t, blob, evidence.Transactions[0].TxBytes)
	var detail inbound.ReplayFailure
	require.NoError(t, json.Unmarshal(evidence.Detail, &detail))
	require.Equal(t, txHash, detail.TxHash)
	require.Equal(t, peerMeta, detail.ExpectedMetadata)
	require.NotEqual(t, detail.ExpectedMetadata, detail.ActualMetadata)
	require.Same(t, closed, svc.GetClosedLedger())
	require.Error(t, svc.RevalidateReplayFault(context.Background(), fault.ID))
	require.True(t, svc.ReplayBlocked())
}
