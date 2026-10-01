package openledger_test

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/stretchr/testify/require"
)

type panicBaseFeeTransaction struct {
	tx.BaseTx
}

func (p *panicBaseFeeTransaction) CalculateBaseFee(tx.ReadOnlyLedgerView, tx.EngineConfig) (uint64, error) {
	panic("fee calculation failed")
}

func TestTxqAdapterGetBaseFeeDispatchesTransactionType(t *testing.T) {
	adapter := openledger.NewTxqAdapter(nil, openledger.ApplyConfig{BaseFee: 10})

	loanSet := lending.NewLoanSet("rAccount", strings.Repeat("1", 64), "1")
	loanSet.GetCommon().CounterpartySignature = &tx.CounterpartySignature{TxnSignature: "AA"}
	if got, err := adapter.GetBaseFee(loanSet); got != 20 || err != nil {
		t.Fatalf("LoanSet base fee = %d, want 20", got)
	}

	multisigned := payment.NewPayment("rAccount", "rDestination", tx.NewXRPAmount(1))
	multisigned.GetCommon().Signers = make([]tx.SignerWrapper, 2)
	if got, err := adapter.GetBaseFee(multisigned); got != 30 || err != nil {
		t.Fatalf("multisigned base fee = %d, want 30", got)
	}

	panicking := &panicBaseFeeTransaction{BaseTx: *tx.NewBaseTx(tx.TypePayment, "rAccount")}
	fee, err := adapter.GetBaseFee(panicking)
	require.Zero(t, fee)
	var result *ter.ResultError
	require.ErrorAs(t, err, &result)
	require.Equal(t, ter.TefEXCEPTION, result.Code)
}

func TestTxqAdapterGetBaseFeeWaivesEligibleSetRegularKey(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	regularKey := jtx.NewAccount("regular-key")
	env.Fund(alice)
	setRegularKeyTx := jtx.NewSetRegularKeyTx(alice, regularKey)
	sequence := env.Seq(alice)
	setRegularKeyTx.GetCommon().Sequence = &sequence
	setRegularKeyTx.GetCommon().Fee = "0"
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
	baseFee, defaultBaseFee, err := adapter.GetBaseFees(setRegularKey)
	require.NoError(t, err)
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
	if got, err := adapter.GetBaseFee(inner); got != 10 || err != nil {
		t.Fatalf("inner SetRegularKey base fee = %d, want 10", got)
	}
}

func TestSubmitQueueEnforcesContextualBaseFee(t *testing.T) {
	for _, test := range []struct {
		name       string
		fee        string
		regularKey bool
		want       ter.Result
	}{
		{name: "zero", fee: "0", want: ter.TelINSUF_FEE_P},
		{name: "one drop", fee: "1", want: ter.TelINSUF_FEE_P},
		{name: "below base", fee: "9", want: ter.TelINSUF_FEE_P},
		{name: "base", fee: "10", want: ter.TesSUCCESS},
		{name: "free regular key", fee: "0", regularKey: true, want: ter.TesSUCCESS},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.SetVerifySignatures(true)
			alice := jtx.NewAccount("alice")
			env.Fund(alice)
			parent := closedParent(t, env)
			rules := amendment.AllSupportedRules()
			view, err := openledger.New(parent, openledger.Config{Rules: rules})
			require.NoError(t, err)
			queue, err := txq.New(txq.DefaultConfig())
			require.NoError(t, err)
			var transaction tx.Transaction = account.NewAccountSet(alice.Address)
			if test.regularKey {
				transaction = jtx.NewSetRegularKeyTx(alice, jtx.NewAccount("regular-key"))
			}
			transaction.GetCommon().SetSequence(env.Seq(alice))
			transaction.GetCommon().Fee = test.fee
			pending, err := openledger.ParsePendingTx(buildSignedBlobOL(t, env, transaction, alice))
			require.NoError(t, err)
			before := view.Current()
			out := view.SubmitDetailed(pending, openledger.ApplyConfig{
				BaseFee: 10, ReserveBase: 200_000_000, ReserveIncrement: 50_000_000,
				LedgerSequence: before.Sequence(), Rules: rules,
			}, queue)
			require.Equal(t, test.want, out.Result)
			require.False(t, out.Queued)
			require.Zero(t, queue.Size())
			if test.want == ter.TesSUCCESS {
				require.True(t, out.Applied)
				require.True(t, ledgerTxExists(t, view.Current(), pending.Hash))
			} else {
				require.False(t, out.Applied)
				require.False(t, out.Changed)
				require.Zero(t, out.Fee)
				require.Same(t, before, view.Current())
			}
		})
	}
}
