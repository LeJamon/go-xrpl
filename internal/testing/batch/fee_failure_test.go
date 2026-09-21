package batch

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
)

// Payment is a permitted Batch inner; overriding its fee hook isolates inner-fee failure handling.
type panicFeePayment struct {
	*payment.Payment
}

func (*panicFeePayment) CalculateBaseFee(tx.LedgerView, tx.EngineConfig) uint64 {
	panic("controlled inner base-fee failure")
}

func feeFailureEnv(t *testing.T) (*jtx.TestEnv, *jtx.Account, *jtx.Account) {
	t.Helper()
	env := newBatchEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()
	if !env.Rules().Enabled(amendment.FeatureBatchV1_1) {
		t.Fatal("BatchV1_1 must be enabled for the fee-failure fixture")
	}
	return env, alice, bob
}

func batchEngineConfig(env *jtx.TestEnv, flags tx.ApplyFlags) tx.EngineConfig {
	view := env.Ledger()
	return tx.EngineConfig{
		BaseFee:                   env.BaseFee(),
		ReserveBase:               env.ReserveBase(),
		ReserveIncrement:          env.ReserveIncrement(),
		LedgerSequence:            view.Sequence(),
		ParentCloseTime:           protocol.ToRippleTime(view.ParentCloseTime()),
		Rules:                     env.Rules(),
		SkipSignatureVerification: true,
		OpenLedger:                true,
		ApplyFlags:                flags,
	}
}

func feeFailureBatch(env *jtx.TestEnv, owner, destination *jtx.Account) *batchtx.Batch {
	seq := env.Seq(owner)
	payment := MakeInnerPaymentXRP(owner, destination, 1, seq+1)
	failingPayment := MakeInnerPaymentXRP(owner, destination, 1, seq+2)
	return NewBatchBuilder(owner, seq, env.BaseFee(), batchtx.BatchFlagAllOrNothing).
		AddInnerTx(payment).
		AddInnerTx(&panicFeePayment{Payment: failingPayment}).
		MustBuild()
}

func TestBatchInnerBaseFeeFailureApplyControls(t *testing.T) {
	tests := []struct {
		name       string
		flags      tx.ApplyFlags
		wantApply  bool
		wantClaim  bool
		wantMeta   bool
		wantSeqInc uint32
	}{
		{name: "claim fee", wantApply: true, wantClaim: true, wantMeta: true, wantSeqInc: 1},
		{name: "tap retry", flags: tx.TapRETRY},
		{name: "fail hard", flags: tx.TapFAIL_HARD},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, alice, bob := feeFailureEnv(t)
			batch := feeFailureBatch(env, alice, bob)
			if err := batch.Validate(); err != nil {
				t.Fatalf("batch.Validate: %v", err)
			}
			aliceBalance := env.Balance(alice)
			bobBalance := env.Balance(bob)
			aliceSequence := env.Seq(alice)
			wantFee := uint64(0)
			if test.wantClaim {
				wantFee = env.BaseFee()
			}

			result := txengine.NewEngine(env.Ledger(), batchEngineConfig(env, test.flags)).Apply(batch)
			if result.Result != ter.TecINSUFF_FEE {
				t.Fatalf("result = %s (%s), want tecINSUFF_FEE", result.Result, result.Message)
			}
			if result.Applied != test.wantApply {
				t.Fatalf("applied = %v, want %v", result.Applied, test.wantApply)
			}
			if result.Fee != wantFee {
				t.Fatalf("fee = %d, want %d", result.Fee, wantFee)
			}
			if (result.Metadata != nil) != test.wantMeta {
				t.Fatalf("metadata present = %v, want %v", result.Metadata != nil, test.wantMeta)
			}
			if test.wantMeta {
				if result.Metadata.TransactionResult != ter.TecINSUFF_FEE {
					t.Fatalf("metadata result = %s, want tecINSUFF_FEE", result.Metadata.TransactionResult)
				}
				if len(result.Metadata.AffectedNodes) != 1 || result.Metadata.AffectedNodes[0].LedgerEntryType != "AccountRoot" {
					t.Fatalf("fee-only metadata affected nodes = %#v, want one AccountRoot", result.Metadata.AffectedNodes)
				}
			}
			if got := env.Balance(alice); got != aliceBalance-wantFee {
				t.Fatalf("owner balance = %d, want %d", got, aliceBalance-wantFee)
			}
			if got := env.Balance(bob); got != bobBalance {
				t.Fatalf("destination balance = %d, want %d", got, bobBalance)
			}
			if got := env.Seq(alice); got != aliceSequence+test.wantSeqInc {
				t.Fatalf("owner sequence = %d, want %d", got, aliceSequence+test.wantSeqInc)
			}
		})
	}
}
