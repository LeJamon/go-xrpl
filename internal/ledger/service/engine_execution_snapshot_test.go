package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	ledgerheader "github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/testing/conformance"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

func TestServiceSnapshotExecutionFromDurableParent(t *testing.T) {
	cases, err := conformance.LoadSnapshotCases()
	if err != nil {
		t.Fatal(err)
	}
	compatible := make([]conformance.SnapshotCase, 0, len(cases))
	skipped := make([]string, 0)
	for _, c := range cases {
		if !serviceRulesEqual(c.Parent.Ledger.Rules(), c.Parent.EffectiveRules) || !serviceRulesEqual(c.Closed.Ledger.Rules(), c.Closed.EffectiveRules) {
			skipped = append(skipped, c.Name+": explicit rules do not match Amendments SLE")
			continue
		}
		if c.Fixture.SkipSignatureVerification {
			skipped = append(skipped, c.Name+": fixture requests signature bypass")
			continue
		}
		if c.Fixture.ApplyFlags != 0 {
			skipped = append(skipped, c.Name+": nonzero apply_flags are not exposed by SubmitTransaction")
			continue
		}
		compatible = append(compatible, c)
	}
	t.Logf("service-compatible cases=%d skipped=%d", len(compatible), len(skipped))
	if len(skipped) != 0 {
		t.Logf("skipped cases: %s", strings.Join(skipped, ", "))
	}
	if len(compatible) == 0 {
		t.Fatalf("pinned service corpus has no case whose explicit rules equal the parent Amendments SLE rules")
	}

	for _, c := range compatible {
		t.Run(c.Name, func(t *testing.T) {
			runServiceConsensusSnapshot(t, c)
			switch {
			case c.Fixture.CloseInput.CloseFlags&ledgerheader.LCFNoConsensusTime != 0:
				t.Log("standalone AcceptLedger path skipped: explicit no-consensus close flags are not injectable")
			case serviceHasSubmittedCloseTx(c):
				runServiceStandaloneSnapshot(t, c)
			default:
				t.Log("standalone AcceptLedger path skipped: no applied submitted transaction is in the close set")
			}
		})
	}
}

func runServiceConsensusSnapshot(t *testing.T, c conformance.SnapshotCase) {
	t.Helper()
	ctx := context.Background()
	svc := newServiceFromSnapshotParent(t, c, false)
	parent := svc.GetClosedLedger()
	if err := assertServiceLedger("startup parent", parent, c.Parent); err != nil {
		t.Fatal(err)
	}
	submitServiceSequence(t, svc, c)

	closeAgree := c.Fixture.CloseInput.CloseFlags&ledgerheader.LCFNoConsensusTime == 0
	seq, err := svc.AcceptConsensusResult(ctx, parent, c.CloseSet, nil, protocol.FromRippleTime(c.Fixture.CloseInput.CloseTime), closeAgree)
	if err != nil {
		t.Fatalf("AcceptConsensusResult: %v", err)
	}
	if seq != c.Closed.Header.LedgerIndex {
		t.Fatalf("accepted sequence=%d, want %d", seq, c.Closed.Header.LedgerIndex)
	}
	closed := svc.GetClosedLedger()
	if err := assertServiceLedger("consensus closed", closed, c.Closed); err != nil {
		t.Fatal(err)
	}
	// Consensus acceptance requires validation before persisting history.
	svc.SetValidatedLedger(seq, closed.Hash())
	svc.FlushPersists()
	bySequence, err := svc.GetLedgerBySequence(c.Closed.Header.LedgerIndex)
	if err != nil {
		t.Fatalf("GetLedgerBySequence: %v", err)
	}
	if bySequence.Hash() != c.Closed.Header.Hash {
		t.Fatalf("history ledger hash=%x, want %x", bySequence.Hash(), c.Closed.Header.Hash)
	}
	if err := assertServiceTransactionHistory(svc, c); err != nil {
		t.Fatal(err)
	}
}

func runServiceStandaloneSnapshot(t *testing.T, c conformance.SnapshotCase) {
	t.Helper()
	ctx := context.Background()
	svc := newServiceFromSnapshotParent(t, c, true)
	// Standalone SubmitTransaction deliberately bypasses signatures in the
	// service, so signature evidence comes only from the consensus-mode run.
	submitServiceSequence(t, svc, c)
	seq, err := svc.acceptLedgerAt(ctx, protocol.FromRippleTime(c.Fixture.CloseInput.CloseTime))
	if err != nil {
		t.Fatalf("acceptLedgerAt: %v", err)
	}
	if seq != c.Closed.Header.LedgerIndex {
		t.Fatalf("standalone accepted sequence=%d, want %d", seq, c.Closed.Header.LedgerIndex)
	}
	if err := assertServiceLedger("standalone closed", svc.GetClosedLedger(), c.Closed); err != nil {
		t.Fatal(err)
	}
	if err := assertServiceTransactionHistory(svc, c); err != nil {
		t.Fatal(err)
	}
}

func submitServiceSequence(t *testing.T, svc *Service, c conformance.SnapshotCase) *SubmitResult {
	t.Helper()
	for i, prior := range c.PreSubmit {
		submitServiceTransaction(t, svc, fmt.Sprintf("pre_submit[%d]", i), prior.TxBlob, prior.TxHash, prior.Submit)
	}
	return submitServiceTransaction(t, svc, "submit", c.TxBlob, c.TxHash, c.Fixture.Submit)
}

func submitServiceTransaction(
	t *testing.T,
	svc *Service,
	label string,
	blob []byte,
	hash [32]byte,
	want conformance.SnapshotSubmit,
) *SubmitResult {
	t.Helper()
	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("%s parse: %v", label, err)
	}
	result, err := svc.SubmitTransaction(parsed, blob, false)
	if err != nil {
		t.Fatalf("%s SubmitTransaction: %v", label, err)
	}
	candidates := svc.QueueAllTxs()
	expectedQueueSize := 0
	if want.Queued {
		expectedQueueSize = 1
	}
	if len(candidates) != expectedQueueSize {
		t.Fatalf("%s queue size=%d, want %d for the empty-start queue", label, len(candidates), expectedQueueSize)
	}
	queued := serviceQueueHasTx(candidates, hash)
	outcome := openledger.SubmitOutcome{
		Result:  result.Result,
		Applied: result.Applied,
		Queued:  queued,
		Fee:     result.Fee,
		Message: result.Message,
	}
	if err := conformance.AssertSnapshotSubmit(outcome, want); err != nil {
		t.Fatalf("%s result: %v", label, err)
	}
	if err := assertServiceSubmitState(svc, blob, hash, want, result); err != nil {
		t.Fatalf("%s state: %v", label, err)
	}
	return result
}

func newServiceFromSnapshotParent(t *testing.T, c conformance.SnapshotCase, standalone bool) *Service {
	t.Helper()
	ctx := context.Background()
	db, relational := newStartupTestStorage(t, ctx)
	family := backend.New(db)
	parent := serviceSnapshotParent(t, c)
	parent.SetSHAMapFamily(family)
	writer, err := New(Config{
		Standalone:     true,
		GenesisConfig:  genesis.DefaultConfig(),
		ConfiguredFees: &c.Parent.Fees,
		NodeStore:      db,
		SHAMapFamily:   family,
		RelationalDB:   relational,
	})
	if err != nil {
		t.Fatalf("writer New: %v", err)
	}
	if err := writer.Start(); err != nil {
		t.Fatalf("writer Start: %v", err)
	}
	if err := writer.persistValidatedLedger(ctx, parent, false); err != nil {
		writer.Stop()
		t.Fatalf("persist parent: %v", err)
	}
	writer.FlushPersists()
	writer.Stop()

	hash := c.Parent.Header.Hash
	svc, err := New(Config{
		Standalone: standalone,
		Startup: StartupConfig{
			Mode:   StartupLoad,
			Ledger: strings.ToUpper(hex.EncodeToString(hash[:])),
		},
		GenesisConfig:  genesis.DefaultConfig(),
		NetworkID:      c.Fixture.NetworkID,
		ConfiguredFees: &c.Parent.Fees,
		NodeStore:      db,
		SHAMapFamily:   family,
		RelationalDB:   relational,
		TxQ:            serviceSnapshotTxQ(c.Fixture.TxQConfig),
	})
	if err != nil {
		t.Fatalf("service New: %v", err)
	}
	if err := svc.Start(); err != nil {
		svc.Stop()
		t.Fatalf("service Start: %v", err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

func serviceSnapshotParent(t *testing.T, c conformance.SnapshotCase) *ledger.Ledger {
	t.Helper()
	state := shamap.New(shamap.TypeState)
	var stateErr error
	if err := c.Parent.State.ForEach(func(item *shamap.Item) bool {
		stateErr = state.Put(item.Key(), item.Data())
		return stateErr == nil
	}); err != nil {
		t.Fatalf("copy parent state: %v", err)
	}
	if stateErr != nil {
		t.Fatalf("copy parent state item: %v", stateErr)
	}

	txs := shamap.New(shamap.TypeTransaction)
	var txErr error
	if err := c.Parent.Txs.ForEach(func(item *shamap.Item) bool {
		txErr = txs.PutWithNodeType(item.Key(), item.Data(), shamap.NodeTypeTransactionWithMeta)
		return txErr == nil
	}); err != nil {
		t.Fatalf("copy parent transactions: %v", err)
	}
	if txErr != nil {
		t.Fatalf("copy parent transaction item: %v", txErr)
	}

	parent, err := ledger.NewFromHeader(c.Parent.Header, state, txs, c.Parent.Fees)
	if err != nil {
		t.Fatalf("construct service parent: %v", err)
	}
	return parent
}

func serviceSnapshotTxQ(value conformance.SnapshotTxQConfig) *txq.Config {
	return &txq.Config{
		LedgersInQueue:                 value.LedgersInQueue,
		QueueSizeMin:                   value.QueueSizeMin,
		RetrySequencePercent:           value.RetrySequencePercent,
		MinimumEscalationMultiplier:    value.MinimumEscalationMultiplier,
		MinimumTxnInLedger:             value.MinimumTxnInLedger,
		MinimumTxnInLedgerStandalone:   value.MinimumTxnInLedgerStandalone,
		TargetTxnInLedger:              value.TargetTxnInLedger,
		MaximumTxnInLedger:             value.MaximumTxnInLedger,
		MaximumTxnInLedgerSet:          value.MaximumTxnInLedgerSet,
		NormalConsensusIncreasePercent: value.NormalConsensusIncreasePercent,
		SlowConsensusDecreasePercent:   value.SlowConsensusDecreasePercent,
		MaximumTxnPerAccount:           value.MaximumTxnPerAccount,
		MinimumLastLedgerBuffer:        value.MinimumLastLedgerBuffer,
		Standalone:                     value.Standalone,
	}
}

func assertServiceLedger(label string, got *ledger.Ledger, want conformance.SnapshotLedger) error {
	if err := conformance.AssertSnapshotLedger(got, want); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if !serviceRulesEqual(got.Rules(), want.Ledger.Rules()) {
		return fmt.Errorf("%s amendment rules differ", label)
	}
	return nil
}

func assertServiceSubmitState(
	svc *Service,
	blob []byte,
	hash [32]byte,
	want conformance.SnapshotSubmit,
	result *SubmitResult,
) error {
	if svc.openLedgerView == nil || svc.openLedgerView.Current() == nil {
		return errors.New("submit did not leave an open-ledger view")
	}
	current := svc.openLedgerView.Current()
	if err := conformance.AssertSnapshotPostSubmitState(current, want.PostSubmitSLE); err != nil {
		return fmt.Errorf("post-submit state: %w", err)
	}
	openLeaf, present, err := current.GetTransaction(hash)
	if err != nil {
		return fmt.Errorf("read submitted transaction: %w", err)
	}
	if result.Applied {
		if !present {
			return errors.New("applied submission is absent from open transaction map")
		}
		txBlob, metaBlob, err := tx.SplitTxWithMetaBlob(openLeaf)
		if err != nil {
			return fmt.Errorf("split submitted transaction: %w", err)
		}
		if !bytes.Equal(txBlob, blob) {
			return errors.New("open transaction bytes differ from submitted bytes")
		}
		if result.Metadata == nil {
			return errors.New("applied submission returned nil metadata")
		}
		serialized, err := tx.SerializeMetadata(result.Metadata)
		if err != nil {
			return fmt.Errorf("serialize submit metadata: %w", err)
		}
		if !bytes.Equal(serialized, metaBlob) {
			return errors.New("submit metadata differs from open transaction metadata")
		}
	} else {
		if present {
			return errors.New("non-applied submission entered open transaction map")
		}
		if result.Metadata != nil {
			return errors.New("non-applied submission returned metadata")
		}
	}

	relayBlob, included, deferred, relayOK := svc.TransactionForRelay(hash)
	queueHas := serviceQueueHasTx(svc.QueueAllTxs(), hash)
	switch {
	case result.Applied:
		if !relayOK || !included || deferred || !bytes.Equal(relayBlob, blob) || queueHas {
			return fmt.Errorf("applied relay/queue state inconsistent: relay=%t included=%t deferred=%t queue=%t", relayOK, included, deferred, queueHas)
		}
	case queueHas:
		if !relayOK || included || !deferred || !bytes.Equal(relayBlob, blob) {
			return fmt.Errorf("queued relay/queue state inconsistent: relay=%t included=%t deferred=%t", relayOK, included, deferred)
		}
	default:
		if relayOK {
			return errors.New("rejected submission remained relayable")
		}
	}
	return nil
}

func assertServiceTransactionHistory(svc *Service, c conformance.SnapshotCase) error {
	expected := make(map[[32]byte]struct{})
	var callbackErr error
	walkErr := c.Closed.Txs.ForEach(func(item *shamap.Item) bool {
		hash := item.Key()
		expected[hash] = struct{}{}
		wantTx, wantMeta, err := tx.SplitTxWithMetaBlob(item.Data())
		if err != nil {
			callbackErr = fmt.Errorf("split expected transaction %x: %w", hash, err)
			return false
		}
		result, err := svc.GetTransaction(hash)
		if err != nil {
			callbackErr = fmt.Errorf("get transaction %x: %w", hash, err)
			return false
		}
		gotTx, gotMeta, err := tx.SplitTxWithMetaBlob(result.TxData)
		if err != nil {
			callbackErr = fmt.Errorf("split returned transaction %x: %w", hash, err)
			return false
		}
		if !bytes.Equal(gotTx, wantTx) || !bytes.Equal(gotMeta, wantMeta) {
			callbackErr = fmt.Errorf("transaction %x bytes differ", hash)
			return false
		}
		if result.LedgerIndex != c.Closed.Header.LedgerIndex || result.LedgerHash != c.Closed.Header.Hash {
			callbackErr = fmt.Errorf("transaction %x ledger location differs", hash)
			return false
		}
		return true
	})
	if walkErr != nil {
		return fmt.Errorf("transaction history map: %w", walkErr)
	}
	if callbackErr != nil {
		return fmt.Errorf("transaction history map: %w", callbackErr)
	}

	history, err := svc.GetTransactionHistory(context.Background(), 0)
	if err != nil {
		return fmt.Errorf("GetTransactionHistory: %w", err)
	}
	seen := make(map[[32]byte]struct{}, len(expected))
	for _, item := range history.Transactions {
		if item.LedgerIndex != c.Closed.Header.LedgerIndex {
			continue
		}
		if _, ok := expected[item.Hash]; !ok {
			return fmt.Errorf("transaction history contains unexpected current-ledger hash %x", item.Hash)
		}
		if _, duplicate := seen[item.Hash]; duplicate {
			return fmt.Errorf("transaction history repeats current-ledger hash %x", item.Hash)
		}
		seen[item.Hash] = struct{}{}
		leaf, found, err := c.Closed.Ledger.GetTransaction(item.Hash)
		if err != nil {
			return fmt.Errorf("read expected transaction %x: %w", item.Hash, err)
		}
		if !found {
			return fmt.Errorf("expected transaction %x is absent from closed ledger", item.Hash)
		}
		wantTx, wantMeta, err := tx.SplitTxWithMetaBlob(leaf)
		if err != nil {
			return fmt.Errorf("split expected history transaction %x: %w", item.Hash, err)
		}
		if !bytes.Equal(item.TxBlob, wantTx) || !bytes.Equal(item.Meta, wantMeta) {
			return fmt.Errorf("transaction history %x bytes differ", item.Hash)
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("transaction history current-ledger count=%d, want %d", len(seen), len(expected))
	}
	return nil
}

func serviceQueueHasTx(candidates []*txq.CandidateDetails, hash [32]byte) bool {
	for _, candidate := range candidates {
		if candidate != nil && candidate.TxID == hash {
			return true
		}
	}
	return false
}

func serviceHasSubmittedCloseTx(c conformance.SnapshotCase) bool {
	if serviceCloseSetContains(c.CloseSet, c.TxHash) {
		return true
	}
	for _, prior := range c.PreSubmit {
		if serviceCloseSetContains(c.CloseSet, prior.TxHash) {
			return true
		}
	}
	return false
}

func serviceCloseSetContains(values [][]byte, want [32]byte) bool {
	for _, blob := range values {
		parsed, err := tx.ParseFromBinary(blob)
		if err != nil {
			continue
		}
		hash, err := tx.ComputeTransactionHash(parsed)
		if err == nil && hash == want {
			return true
		}
	}
	return false
}

func serviceRulesEqual(left, right *amendment.Rules) bool {
	if left == nil || right == nil || left.EnabledCount() != right.EnabledCount() {
		return left == nil && right == nil
	}
	rightIDs := make(map[[32]byte]struct{}, right.EnabledCount())
	for _, id := range right.EnabledIDs() {
		rightIDs[id] = struct{}{}
	}
	for _, id := range left.EnabledIDs() {
		if _, ok := rightIDs[id]; !ok {
			return false
		}
	}
	return true
}
