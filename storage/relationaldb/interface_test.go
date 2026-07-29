package relationaldb

import (
	"errors"
	"testing"
)

func TestValidatedLedgerValidate(t *testing.T) {
	ledger := ValidatedLedger{Ledger: LedgerInfo{Hash: Hash{1}, Sequence: 10}}
	transaction := IndexedTransaction{
		Transaction: TransactionInfo{
			Hash:      Hash{2},
			LedgerSeq: 10,
			RawTxn:    []byte{1},
		},
	}
	for _, test := range []struct {
		name  string
		value ValidatedLedger
		valid bool
	}{
		{name: "empty ledger", value: ledger, valid: true},
		{
			name: "transaction",
			value: ValidatedLedger{
				Ledger:       ledger.Ledger,
				Transactions: []IndexedTransaction{transaction},
			},
			valid: true,
		},
		{
			name: "zero ledger hash",
			value: ValidatedLedger{
				Ledger: LedgerInfo{Sequence: 10},
			},
		},
		{
			name: "mismatched sequence",
			value: ValidatedLedger{
				Ledger: ledger.Ledger,
				Transactions: []IndexedTransaction{{
					Transaction: TransactionInfo{Hash: Hash{2}, LedgerSeq: 11, RawTxn: []byte{1}},
				}},
			},
		},
		{
			name: "zero transaction hash",
			value: ValidatedLedger{
				Ledger: ledger.Ledger,
				Transactions: []IndexedTransaction{{
					Transaction: TransactionInfo{LedgerSeq: 10, RawTxn: []byte{1}},
				}},
			},
		},
		{
			name: "empty transaction payload",
			value: ValidatedLedger{
				Ledger: ledger.Ledger,
				Transactions: []IndexedTransaction{{
					Transaction: TransactionInfo{Hash: Hash{2}, LedgerSeq: 10},
				}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.value.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !test.valid && !errors.Is(err, ErrInvalidData) {
				t.Fatalf("Validate() error = %v, want ErrInvalidData", err)
			}
		})
	}
}
