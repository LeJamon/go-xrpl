package lending

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestLoanSetCalculateBaseFee(t *testing.T) {
	for _, tc := range []struct {
		name               string
		outerSigners       int
		counterparty       *tx.CounterpartySignature
		expectedFeeInDrops uint64
	}{
		{"no counterparty signature", 0, nil, 10},
		{"single counterparty signature", 0, &tx.CounterpartySignature{TxnSignature: "AA"}, 20},
		{"counterparty multisignature", 0, &tx.CounterpartySignature{Signers: make([]tx.SignerWrapper, 2)}, 30},
		{"outer multisignature and single counterparty signature", 2, &tx.CounterpartySignature{TxnSignature: "AA"}, 40},
		{"outer and counterparty multisignatures", 2, &tx.CounterpartySignature{Signers: make([]tx.SignerWrapper, 2)}, 50},
		{"counterparty key without signature", 0, &tx.CounterpartySignature{SigningPubKey: "ED"}, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loanSet := NewLoanSet("rAccount", strings.Repeat("1", 64), "1")
			loanSet.GetCommon().Signers = make([]tx.SignerWrapper, tc.outerSigners)
			loanSet.GetCommon().CounterpartySignature = tc.counterparty
			if got := loanSet.CalculateBaseFee(nil, tx.EngineConfig{BaseFee: 10}); got != tc.expectedFeeInDrops {
				t.Fatalf("CalculateBaseFee = %d, want %d", got, tc.expectedFeeInDrops)
			}
		})
	}
}
