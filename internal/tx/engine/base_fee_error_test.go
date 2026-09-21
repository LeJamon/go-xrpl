package engine

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type controlledBaseFeeTx struct {
	*txcore.BaseTx

	panicAlways bool
	panicOnCall int
	calls       int
	preclaim    ter.Result
}

func (t *controlledBaseFeeTx) CalculateBaseFee(_ txcore.LedgerView, config txcore.EngineConfig) (uint64, error) {
	t.calls++
	if t.panicAlways || t.calls == t.panicOnCall {
		panic("controlled base-fee failure")
	}
	return config.BaseFee, nil
}

func (t *controlledBaseFeeTx) Apply(*txcore.ApplyContext) ter.Result {
	return ter.TesSUCCESS
}

func (t *controlledBaseFeeTx) Preclaim(txcore.LedgerView, txcore.EngineConfig) ter.Result {
	return t.preclaim
}

func newControlledBaseFeeTx(fee uint32, seq uint32) *controlledBaseFeeTx {
	return &controlledBaseFeeTx{BaseTx: recoveryTx(fee, seq), preclaim: ter.TesSUCCESS}
}

func TestApply_BaseFeePanicInPreclaimReturnsTefException(t *testing.T) {
	view := newRecordingBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	tx := newControlledBaseFeeTx(10, 1)
	tx.panicAlways = true

	result := recoveryEngine(view, txcore.TapNONE).Apply(tx)

	assertBaseFeeFailure(t, result, ter.TefEXCEPTION, view, accountKey, 1_000_000, 1)
	if tx.calls != 1 {
		t.Fatalf("base-fee calculator calls = %d, want 1", tx.calls)
	}
}

func TestApply_BaseFeePanicOnApplyReturnsTefInternalWithoutMutation(t *testing.T) {
	view := newRecordingBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	tx := newControlledBaseFeeTx(10, 1)
	tx.panicOnCall = 2

	result := recoveryEngine(view, txcore.TapNONE).Apply(tx)

	assertBaseFeeFailure(t, result, ter.TefINTERNAL, view, accountKey, 1_000_000, 1)
	if tx.calls != 2 {
		t.Fatalf("base-fee calculator calls = %d, want 2", tx.calls)
	}
}

func TestApply_BaseFeeSuccessRetainsFeeSequenceAndMetadata(t *testing.T) {
	view := newRecordingBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	tx := newControlledBaseFeeTx(10, 1)

	result := recoveryEngine(view, txcore.TapNONE).Apply(tx)

	if result.Result != ter.TesSUCCESS || !result.Applied {
		t.Fatalf("result/applied = %s/%v, want tesSUCCESS/true", result.Result, result.Applied)
	}
	if result.Fee != 10 || result.Metadata == nil {
		t.Fatalf("fee/metadata = %d/%#v, want 10/non-nil", result.Fee, result.Metadata)
	}
	if view.destroyed != drops.XRPAmount(10) {
		t.Fatalf("destroyed drops = %d, want 10", view.destroyed)
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != 999_990 || account.Sequence != 2 {
		t.Fatalf("payer balance/sequence = %d/%d, want 999990/2", account.Balance, account.Sequence)
	}
	if tx.calls != 2 {
		t.Fatalf("base-fee calculator calls = %d, want 2", tx.calls)
	}
}

func TestApply_PreclaimTecBaseFeePanicOnApplyReturnsTefInternalWithoutMutation(t *testing.T) {
	view := newRecordingBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	tx := newControlledBaseFeeTx(10, 1)
	tx.preclaim = ter.TecUNFUNDED_PAYMENT
	tx.panicOnCall = 2

	result := recoveryEngine(view, txcore.TapNONE).Apply(tx)

	assertBaseFeeFailure(t, result, ter.TefINTERNAL, view, accountKey, 1_000_000, 1)
	if tx.calls != 2 {
		t.Fatalf("base-fee calculator calls = %d, want 2", tx.calls)
	}
}

func TestApply_PreclaimTecRetrySkipsApplyBaseFeeRecomputation(t *testing.T) {
	view := newRecordingBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	tx := newControlledBaseFeeTx(10, 1)
	tx.preclaim = ter.TecUNFUNDED_PAYMENT
	tx.panicOnCall = 2

	result := recoveryEngine(view, txcore.TapRETRY).Apply(tx)

	if result.Result != ter.TecUNFUNDED_PAYMENT || result.Applied {
		t.Fatalf("result/applied = %s/%v, want tecUNFUNDED_PAYMENT/false", result.Result, result.Applied)
	}
	if result.Fee != 0 || result.Metadata != nil {
		t.Fatalf("fee/metadata = %d/%#v, want 0/nil", result.Fee, result.Metadata)
	}
	if view.destroyed != 0 {
		t.Fatalf("destroyed drops = %d, want 0", view.destroyed)
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != 1_000_000 || account.Sequence != 1 {
		t.Fatalf("payer balance/sequence = %d/%d, want 1000000/1", account.Balance, account.Sequence)
	}
	if tx.calls != 1 {
		t.Fatalf("base-fee calculator calls = %d, want 1", tx.calls)
	}
}

func TestApply_PreclaimTecSuccessfulBaseFeeRetainsFeeClaim(t *testing.T) {
	view := newRecordingBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	tx := newControlledBaseFeeTx(10, 1)
	tx.preclaim = ter.TecUNFUNDED_PAYMENT

	result := recoveryEngine(view, txcore.TapNONE).Apply(tx)

	if result.Result != ter.TecUNFUNDED_PAYMENT || !result.Applied {
		t.Fatalf("result/applied = %s/%v, want tecUNFUNDED_PAYMENT/true", result.Result, result.Applied)
	}
	if result.Fee != 10 || result.Metadata == nil {
		t.Fatalf("fee/metadata = %d/%#v, want 10/non-nil", result.Fee, result.Metadata)
	}
	if view.destroyed != drops.XRPAmount(10) {
		t.Fatalf("destroyed drops = %d, want 10", view.destroyed)
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != 999_990 || account.Sequence != 2 {
		t.Fatalf("payer balance/sequence = %d/%d, want 999990/2", account.Balance, account.Sequence)
	}
	if tx.calls != 2 {
		t.Fatalf("base-fee calculator calls = %d, want 2", tx.calls)
	}
}

func TestApplyInnerTransaction_BaseFeePanicReturnsTefInternalWithoutMutation(t *testing.T) {
	view := newRecordingBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	tx := newControlledBaseFeeTx(0, 1)
	flags := txcore.TfInnerBatchTxn
	tx.Flags = &flags
	tx.panicAlways = true

	result := recoveryEngine(view, txcore.TapNONE).ApplyInnerTransaction(
		context.Background(), view, tx, [32]byte{1}, 0)

	assertBaseFeeFailure(t, result, ter.TefINTERNAL, view, accountKey, 1_000_000, 1)
	if tx.calls != 1 {
		t.Fatalf("base-fee calculator calls = %d, want 1", tx.calls)
	}
}

func assertBaseFeeFailure(
	t *testing.T,
	result txcore.ApplyResult,
	want ter.Result,
	view *recordingBaseView,
	accountKey keylet.Keylet,
	wantBalance uint64,
	wantSequence uint32,
) {
	t.Helper()
	if result.Result != want || result.Applied {
		t.Fatalf("result/applied = %s/%v, want %s/false", result.Result, result.Applied, want)
	}
	if result.Fee != 0 || result.Metadata != nil {
		t.Fatalf("fee/metadata = %d/%#v, want 0/nil", result.Fee, result.Metadata)
	}
	if view.destroyed != 0 {
		t.Fatalf("destroyed drops = %d, want 0", view.destroyed)
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != wantBalance || account.Sequence != wantSequence {
		t.Fatalf("payer balance/sequence = %d/%d, want %d/%d", account.Balance, account.Sequence, wantBalance, wantSequence)
	}
}

var _ txcore.CustomBaseFeeCalculator = (*controlledBaseFeeTx)(nil)
var _ txcore.Appliable = (*controlledBaseFeeTx)(nil)
var _ txcore.Preclaimer = (*controlledBaseFeeTx)(nil)
