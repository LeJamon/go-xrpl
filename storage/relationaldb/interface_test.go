package relationaldb

import (
	"errors"
	"testing"
)

func TestValidatedLedgerValidate(t *testing.T) {
	emptyLedger := LedgerInfo{Hash: Hash{1}, Sequence: 10, AccountHash: Hash{3}}
	transactionLedger := emptyLedger
	transactionLedger.TransactionHash = Hash{4}
	transaction := IndexedTransaction{
		Transaction: TransactionInfo{
			Hash:      Hash{2},
			LedgerSeq: 10,
			RawTxn:    []byte{1},
			TxnMeta:   []byte{2},
		},
		Accounts: []AccountID{{1}},
	}
	for _, test := range []struct {
		name  string
		value ValidatedLedger
		valid bool
	}{
		{name: "empty ledger", value: ValidatedLedger{Ledger: emptyLedger}, valid: true},
		{
			name: "transaction",
			value: ValidatedLedger{
				Ledger:       transactionLedger,
				Transactions: []IndexedTransaction{transaction},
			},
			valid: true,
		},
		{
			name: "zero ledger hash",
			value: ValidatedLedger{
				Ledger: LedgerInfo{Sequence: 10, AccountHash: Hash{3}},
			},
		},
		{
			name:  "zero account hash",
			value: ValidatedLedger{Ledger: LedgerInfo{Hash: Hash{1}, Sequence: 10}},
		},
		{
			name:  "empty ledger with transaction hash",
			value: ValidatedLedger{Ledger: transactionLedger},
		},
		{
			name: "transaction with zero root",
			value: ValidatedLedger{
				Ledger:       emptyLedger,
				Transactions: []IndexedTransaction{transaction},
			},
		},
		{
			name: "mismatched sequence",
			value: ValidatedLedger{
				Ledger: transactionLedger,
				Transactions: []IndexedTransaction{{
					Transaction: TransactionInfo{Hash: Hash{2}, LedgerSeq: 11, RawTxn: []byte{1}, TxnMeta: []byte{2}},
				}},
			},
		},
		{
			name: "zero transaction hash",
			value: ValidatedLedger{
				Ledger: transactionLedger,
				Transactions: []IndexedTransaction{{
					Transaction: TransactionInfo{LedgerSeq: 10, RawTxn: []byte{1}, TxnMeta: []byte{2}},
				}},
			},
		},
		{
			name: "empty transaction payload",
			value: ValidatedLedger{
				Ledger: transactionLedger,
				Transactions: []IndexedTransaction{{
					Transaction: TransactionInfo{Hash: Hash{2}, LedgerSeq: 10, TxnMeta: []byte{2}},
				}},
			},
		},
		{
			name: "empty transaction metadata",
			value: ValidatedLedger{
				Ledger: transactionLedger,
				Transactions: []IndexedTransaction{{
					Transaction: TransactionInfo{Hash: Hash{2}, LedgerSeq: 10, RawTxn: []byte{1}},
				}},
			},
		},
		{
			name: "duplicate transaction hash",
			value: ValidatedLedger{
				Ledger:       transactionLedger,
				Transactions: []IndexedTransaction{transaction, transaction},
			},
		},
		{
			name: "duplicate transaction index",
			value: ValidatedLedger{
				Ledger: transactionLedger,
				Transactions: []IndexedTransaction{
					transaction,
					{
						Transaction: TransactionInfo{
							Hash:      Hash{3},
							LedgerSeq: 10,
							RawTxn:    []byte{1},
							TxnMeta:   []byte{2},
						},
					},
				},
			},
		},
		{
			name: "transaction index out of range",
			value: ValidatedLedger{
				Ledger: transactionLedger,
				Transactions: []IndexedTransaction{{
					Transaction: TransactionInfo{
						Hash:      Hash{2},
						LedgerSeq: 10,
						TxnSeq:    1,
						RawTxn:    []byte{1},
						TxnMeta:   []byte{2},
					},
				}},
			},
		},
		{
			name: "zero account",
			value: ValidatedLedger{
				Ledger: transactionLedger,
				Transactions: []IndexedTransaction{{
					Transaction: transaction.Transaction,
					Accounts:    []AccountID{{}},
				}},
			},
		},
		{
			name: "duplicate account",
			value: ValidatedLedger{
				Ledger: transactionLedger,
				Transactions: []IndexedTransaction{{
					Transaction: transaction.Transaction,
					Accounts:    []AccountID{{1}, {1}},
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
