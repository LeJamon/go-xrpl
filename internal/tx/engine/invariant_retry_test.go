package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

type deliveringTx struct {
	*txcore.BaseTx
}

func (deliveringTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	delivered := txcore.NewXRPAmount(500)
	ctx.Metadata.DeliveredAmount = &delivered
	return ter.TesSUCCESS
}

func failFirstInvariantPass(e *Engine) {
	firstPass := true
	e.SetInvariantViolationHookForTest(func(ter.Result, *applystate.ApplyStateTable) *InvariantViolationValue {
		if firstPass {
			firstPass = false
			return NewInvariantViolation("forced", "forced violation for test")
		}
		return nil
	})
}

// An invariant reset after a committed outcome is applied even on a retry pass:
// the fee-only state is committed and the tx keeps its place in the ledger.
func TestApply_Retry_InvariantFailedApplied(t *testing.T) {
	cases := []struct {
		name string
		tx   txcore.Transaction
	}{
		{"tesSUCCESS", successfulRecoveryTx{recoveryTx(10, 1)}},
		{"tecKILLED", codeTecTx{BaseTx: recoveryTx(10, 1), code: ter.TecKILLED}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := newRecordingBaseView()
			acctKey := fundRecoveryAccount(t, view, 1_000_000, 1)

			e := recoveryEngine(view, txcore.TapRETRY)
			failFirstInvariantPass(e)
			res := e.Apply(tc.tx)

			if res.Result != ter.TecINVARIANT_FAILED {
				t.Fatalf("result = %s, want tecINVARIANT_FAILED", res.Result)
			}
			if !res.Applied {
				t.Fatalf("tecINVARIANT_FAILED from the invariant reset must be applied under TapRETRY")
			}
			if res.Metadata == nil || res.Metadata.TransactionResult != ter.TecINVARIANT_FAILED {
				t.Fatalf("metadata = %+v, want tecINVARIANT_FAILED metadata", res.Metadata)
			}
			if view.destroyed != drops.XRPAmount(10) {
				t.Fatalf("destroyed drops = %d, want 10", view.destroyed)
			}
			acct := readRecoveryAccount(t, view, acctKey)
			if acct.Balance != 999_990 || acct.Sequence != 2 {
				t.Fatalf("payer balance/seq = %d/%d, want 999990/2", acct.Balance, acct.Sequence)
			}
		})
	}
}

// A transactor that itself returns tecINVARIANT_FAILED is a generic tec: on a
// retry pass it is neither applied nor charged.
func TestApply_Retry_TransactorInvariantFailedNotApplied(t *testing.T) {
	view := newRecordingBaseView()
	acctKey := fundRecoveryAccount(t, view, 1_000_000, 1)

	res := recoveryEngine(view, txcore.TapRETRY).Apply(codeTecTx{BaseTx: recoveryTx(10, 1), code: ter.TecINVARIANT_FAILED})

	if res.Result != ter.TecINVARIANT_FAILED {
		t.Fatalf("result = %s, want tecINVARIANT_FAILED", res.Result)
	}
	if res.Applied || res.Metadata != nil {
		t.Fatalf("applied/metadata = %v/%v, want false/nil", res.Applied, res.Metadata)
	}
	if view.destroyed != 0 {
		t.Fatalf("destroyed drops = %d, want 0", view.destroyed)
	}
	acct := readRecoveryAccount(t, view, acctKey)
	if acct.Balance != 1_000_000 || acct.Sequence != 1 {
		t.Fatalf("payer balance/seq = %d/%d, want 1000000/1", acct.Balance, acct.Sequence)
	}
}

func TestApply_InvariantReset_ClearsDeliveredAmount(t *testing.T) {
	view := newRecordingBaseView()
	fundRecoveryAccount(t, view, 1_000_000, 1)

	e := recoveryEngine(view, txcore.TapNONE)
	failFirstInvariantPass(e)
	res := e.Apply(deliveringTx{recoveryTx(10, 1)})

	if res.Result != ter.TecINVARIANT_FAILED || !res.Applied {
		t.Fatalf("result/applied = %s/%v, want tecINVARIANT_FAILED/true", res.Result, res.Applied)
	}
	if res.Metadata.DeliveredAmount != nil {
		t.Fatalf("DeliveredAmount = %+v, want nil after invariant reset", res.Metadata.DeliveredAmount)
	}
}
