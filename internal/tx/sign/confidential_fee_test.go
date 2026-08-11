package sign

import (
	"testing"

	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

func TestCalculateBaseFeeConfidentialMPT(t *testing.T) {
	const ledgerBaseFee uint64 = 10
	config := txcore.EngineConfig{BaseFee: ledgerBaseFee}

	for _, transactionType := range []txcore.Type{
		txcore.TypeConfidentialMPTConvert,
		txcore.TypeConfidentialMPTMergeInbox,
		txcore.TypeConfidentialMPTConvertBack,
		txcore.TypeConfidentialMPTSend,
		txcore.TypeConfidentialMPTClawback,
	} {
		t.Run(transactionType.String(), func(t *testing.T) {
			txn := txcore.NewBaseTx(transactionType, "account")
			if got := CalculateBaseFee(txn, nil, config); got != 100 {
				t.Fatalf("CalculateBaseFee() = %d, want 100", got)
			}
		})
	}

	control := txcore.NewBaseTx(txcore.TypePayment, "account")
	if got := CalculateBaseFee(control, nil, config); got != ledgerBaseFee {
		t.Fatalf("non-confidential CalculateBaseFee() = %d, want %d", got, ledgerBaseFee)
	}
}

func TestCalculateBaseFeeConfidentialMPTSigners(t *testing.T) {
	const ledgerBaseFee uint64 = 10

	tests := []struct {
		name     string
		prepare  func(*txcore.BaseTx)
		expected uint64
	}{
		{name: "single signed", expected: 100},
		{
			name: "transaction multisigned",
			prepare: func(txn *txcore.BaseTx) {
				txn.Signers = make([]txcore.SignerWrapper, 3)
			},
			expected: 130,
		},
		{
			name: "Sponsor single signed",
			prepare: func(txn *txcore.BaseTx) {
				txn.Sponsor = "sponsor"
				txn.SponsorSignature = &txcore.SponsorSignature{SigningPubKey: "key"}
			},
			expected: 100,
		},
		{
			name: "Sponsor multisigned",
			prepare: func(txn *txcore.BaseTx) {
				txn.Sponsor = "sponsor"
				txn.SponsorSignature = &txcore.SponsorSignature{
					Signers: make([]txcore.SignerWrapper, 2),
				}
			},
			expected: 120,
		},
		{
			name: "transaction and Sponsor multisigned",
			prepare: func(txn *txcore.BaseTx) {
				txn.Signers = make([]txcore.SignerWrapper, 3)
				txn.Sponsor = "sponsor"
				txn.SponsorSignature = &txcore.SponsorSignature{
					Signers: make([]txcore.SignerWrapper, 2),
				}
			},
			expected: 150,
		},
		{
			name: "prefunded Sponsor",
			prepare: func(txn *txcore.BaseTx) {
				txn.Sponsor = "sponsor"
			},
			expected: 100,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			txn := txcore.NewBaseTx(txcore.TypeConfidentialMPTSend, "account")
			if test.prepare != nil {
				test.prepare(txn)
			}
			got := CalculateBaseFee(txn, nil, txcore.EngineConfig{BaseFee: ledgerBaseFee})
			if got != test.expected {
				t.Fatalf("CalculateBaseFee() = %d, want %d", got, test.expected)
			}
		})
	}
}
