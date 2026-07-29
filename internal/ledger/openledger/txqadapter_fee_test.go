package openledger_test

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

type panicBaseFeeTransaction struct {
	tx.BaseTx
}

func (p *panicBaseFeeTransaction) CalculateBaseFee(tx.LedgerView, tx.EngineConfig) uint64 {
	panic("fee calculation failed")
}

func TestTxqAdapterGetBaseFeeDispatchesTransactionType(t *testing.T) {
	adapter := openledger.NewTxqAdapter(nil, openledger.ApplyConfig{BaseFee: 10})

	loanSet := lending.NewLoanSet("rAccount", strings.Repeat("1", 64), "1")
	loanSet.GetCommon().CounterpartySignature = &tx.CounterpartySignature{TxnSignature: "AA"}
	if got := adapter.GetBaseFee(loanSet); got != 20 {
		t.Fatalf("LoanSet base fee = %d, want 20", got)
	}

	multisigned := payment.NewPayment("rAccount", "rDestination", tx.NewXRPAmount(1))
	multisigned.GetCommon().Signers = make([]tx.SignerWrapper, 2)
	if got := adapter.GetBaseFee(multisigned); got != 30 {
		t.Fatalf("multisigned base fee = %d, want 30", got)
	}

	panicking := &panicBaseFeeTransaction{BaseTx: *tx.NewBaseTx(tx.TypePayment, "rAccount")}
	if got := adapter.GetBaseFee(panicking); got != 10 {
		t.Fatalf("panicking calculator fallback = %d, want 10", got)
	}
}

func TestTxqAdapterGetBaseFeeWaivesEligibleSetRegularKey(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	regularKey := jtx.NewAccount("regular-key")
	env.Fund(alice)
	setRegularKeyTx := jtx.NewSetRegularKeyTx(alice, regularKey)
	sequence := env.Seq(alice)
	setRegularKeyTx.GetCommon().Sequence = &sequence
	view := freshView(t, env)

	blob := buildSignedBlob(t, env, setRegularKeyTx, alice)
	setRegularKey, err := tx.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}

	adapter := openledger.NewTxqAdapter(view, openledger.ApplyConfig{
		BaseFee:                   10,
		Rules:                     amendment.AllSupportedRules(),
		SkipSignatureVerification: true,
	})
	baseFee, defaultBaseFee := adapter.GetBaseFees(setRegularKey)
	if baseFee != 0 || defaultBaseFee != 10 {
		t.Fatalf("eligible SetRegularKey fees = (%d, %d), want (0, 10)", baseFee, defaultBaseFee)
	}
	if got := txq.ToFeeLevelWithDefaultBaseFee(10, baseFee, defaultBaseFee); got != 512 {
		t.Fatalf("eligible SetRegularKey fee level = %d, want 512", got)
	}

	inner := jtx.NewSetRegularKeyTx(alice, regularKey)
	innerFlags := tx.TfInnerBatchTxn
	inner.GetCommon().Flags = &innerFlags
	inner.GetCommon().SigningPubKey = ""
	if got := adapter.GetBaseFee(inner); got != 10 {
		t.Fatalf("inner SetRegularKey base fee = %d, want 10", got)
	}
}
