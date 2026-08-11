package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestCheckFeeConfidentialMPTMinimum(t *testing.T) {
	account := &state.AccountRoot{Balance: 1_000_000}
	engine := &Engine{config: txcore.EngineConfig{BaseFee: 10, OpenLedger: true}}

	for _, transactionType := range []txcore.Type{
		txcore.TypeConfidentialMPTConvert,
		txcore.TypeConfidentialMPTMergeInbox,
		txcore.TypeConfidentialMPTConvertBack,
		txcore.TypeConfidentialMPTSend,
		txcore.TypeConfidentialMPTClawback,
	} {
		t.Run(transactionType.String(), func(t *testing.T) {
			for _, test := range []struct {
				fee      string
				expected ter.Result
			}{
				{fee: "99", expected: ter.TelINSUF_FEE_P},
				{fee: "100", expected: ter.TesSUCCESS},
			} {
				txn := txcore.NewBaseTx(transactionType, "rTestAccount")
				txn.Fee = test.fee
				if got := engine.checkFee(txn, txn.GetCommon(), account); got != test.expected {
					t.Fatalf("checkFee(%s) = %v, want %v", test.fee, got, test.expected)
				}
			}
		})
	}
}
