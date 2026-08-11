package batch

import (
	"testing"

	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

func TestInnerBaseFeeConfidentialMPT(t *testing.T) {
	for _, transactionType := range []txcore.Type{
		txcore.TypeConfidentialMPTConvert,
		txcore.TypeConfidentialMPTMergeInbox,
		txcore.TypeConfidentialMPTConvertBack,
		txcore.TypeConfidentialMPTSend,
		txcore.TypeConfidentialMPTClawback,
	} {
		t.Run(transactionType.String(), func(t *testing.T) {
			txn := txcore.NewBaseTx(transactionType, "account")
			if got := innerBaseFee(txn, nil, txcore.EngineConfig{BaseFee: 10}); got != 100 {
				t.Fatalf("innerBaseFee() = %d, want 100", got)
			}
		})
	}
}
