package engine

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

type batchMissingReadView struct {
	*mockBaseView
	missingKey keylet.Keylet
	err        error
}

func (v *batchMissingReadView) Read(k keylet.Keylet) ([]byte, error) {
	if k.Key == v.missingKey.Key {
		return nil, v.err
	}
	return v.mockBaseView.Read(k)
}

type batchStorageFailureTx struct {
	*txcore.BaseTx
	missingKey keylet.Keylet
	applied    *bool
}

func (tx *batchStorageFailureTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	*tx.applied = true
	if _, err := ctx.View.Read(tx.missingKey); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

func newStorageCauseBatch(missingKey keylet.Keylet, applied *bool) *batch.Batch {
	outer := batch.NewBatch(recoveryTestAccount)
	outer.Common.Fee = "10"
	outerSequence := uint32(1)
	outer.Common.Sequence = &outerSequence
	flags := batch.BatchFlagAllOrNothing
	outer.Common.Flags = &flags

	first := successfulRecoveryTx{recoveryTx(0, 2)}
	firstFlags := txcore.TfInnerBatchTxn
	first.Common.Flags = &firstFlags
	first.Common.SigningPubKey = ""
	outer.AddInnerTransaction(first)
	second := &batchStorageFailureTx{
		BaseTx:     recoveryTx(0, 3),
		missingKey: missingKey,
		applied:    applied,
	}
	innerFlags := txcore.TfInnerBatchTxn
	second.Common.Flags = &innerFlags
	second.Common.SigningPubKey = ""
	outer.AddInnerTransaction(second)
	return outer
}

func TestApplyBatchInnerTransactionsRollsBackOnTypedStorageCause(t *testing.T) {
	batch.Register()
	missingKey := keylet.Keylet{Key: [32]byte{0x91}}
	missingHash := [32]byte{0x92}
	innerApplied := false
	base := newMockBaseView()
	fundRecoveryAccount(t, base, 1_000_000_000, 2)
	view := &batchMissingReadView{
		mockBaseView: base,
		missingKey:   missingKey,
		err:          &shamap.MissingNodeError{Hash: missingHash},
	}
	accountID, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	accountKey := keylet.Account(accountID)
	before, err := view.Read(accountKey)
	if err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(view, txcore.EngineConfig{
		BaseFee:                   10,
		LedgerSequence:            100,
		Rules:                     amendment.AllSupportedRules(),
		SkipSignatureVerification: true,
	})
	outer := newStorageCauseBatch(missingKey, &innerApplied)
	outerResult := txcore.ApplyResult{
		Result:  ter.TesSUCCESS,
		Applied: true,
		Metadata: &txcore.Metadata{
			AffectedNodes:     []txcore.AffectedNode{},
			TransactionIndex:  0,
			TransactionResult: ter.TesSUCCESS,
		},
	}
	result := engine.ApplyBatchInnerTransactions(context.Background(), outer, outerResult)

	if result.Result != ter.TefEXCEPTION || result.Applied {
		t.Fatalf("batch result = (%s, applied=%t), want tefEXCEPTION and not applied", result.Result, result.Applied)
	}
	if !innerApplied {
		t.Fatal("batch did not execute the faulting inner transaction")
	}
	if !errors.Is(result.Cause, shamap.ErrNodeNotInStore) {
		t.Fatalf("batch cause = %v, want ErrNodeNotInStore", result.Cause)
	}
	var missing *shamap.MissingNodeError
	if !errors.As(result.Cause, &missing) || missing.Hash != missingHash {
		t.Fatalf("batch cause = %v, want missing node %x", result.Cause, missingHash)
	}
	after, err := view.Read(accountKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("batch storage failure committed the successful inner transaction")
	}
}
