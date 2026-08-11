package batch

import (
	"testing"

	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

func TestCalculateMinimumFeeConfidentialMPT(t *testing.T) {
	for _, transactionType := range []txcore.Type{
		txcore.TypeConfidentialMPTConvert,
		txcore.TypeConfidentialMPTMergeInbox,
		txcore.TypeConfidentialMPTConvertBack,
		txcore.TypeConfidentialMPTSend,
		txcore.TypeConfidentialMPTClawback,
	} {
		t.Run(transactionType.String(), func(t *testing.T) {
			txn := txcore.NewBaseTx(transactionType, "account")
			batch := NewBatch("account")
			batch.AddInnerTransaction(txn)
			if got := batch.CalculateMinimumFee(nil, txcore.EngineConfig{BaseFee: 10}); got != 120 {
				t.Fatalf("CalculateMinimumFee() = %d, want 120", got)
			}
		})
	}
}
