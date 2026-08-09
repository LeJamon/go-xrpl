package batch

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mpt"
)

func confidentialBatchInner(transaction tx.Transaction, sequence uint32, delegate string) tx.Transaction {
	common := transaction.GetCommon()
	common.Fee = "0"
	common.SigningPubKey = ""
	common.Sequence = &sequence
	common.Delegate = delegate
	flags := tx.TfInnerBatchTxn
	common.Flags = &flags
	return transaction
}

func TestConfidentialMPTBatchInitiators(t *testing.T) {
	const id = "000000000000000000000001000000000000000000000001"
	for _, transaction := range []tx.Transaction{
		&mpt.ConfidentialMPTMergeInbox{
			BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTMergeInbox, testOuter), MPTokenIssuanceID: id,
		},
		&mpt.ConfidentialMPTConvertBack{
			BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTConvertBack, testOuter), MPTokenIssuanceID: id,
			MPTAmount: 1, HolderEncryptedAmount: strings.Repeat("01", 66),
			IssuerEncryptedAmount: strings.Repeat("02", 66), BlindingFactor: strings.Repeat("03", 32),
			ZKProof: strings.Repeat("04", 816), BalanceCommitment: strings.Repeat("02", 33),
		},
	} {
		batch := NewBatch(testOuter)
		batch.AddInnerTransaction(confidentialBatchInner(transaction, 101, testSigner1))
		batch.AddInnerTransaction(makeTestPayment())
		flags := BatchFlagAllOrNothing
		batch.Flags = &flags
		batch.BatchSigners = []BatchSigner{{BatchSigner: BatchSignerData{
			Account: testSigner1, SigningPubKey: "ABC", BatchTxnSignature: "DEF",
		}}}
		if err := batch.Validate(); err != nil {
			t.Fatalf("delegated %s rejected with Delegate BatchSigner: %v", transaction.TxType(), err)
		}
	}

	convert := &mpt.ConfidentialMPTConvert{
		BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTConvert, testOuter), MPTokenIssuanceID: id,
		MPTAmount: 1, HolderEncryptedAmount: strings.Repeat("01", 66),
		IssuerEncryptedAmount: strings.Repeat("02", 66), BlindingFactor: strings.Repeat("03", 32),
	}
	batch := NewBatch(testOuter)
	batch.AddInnerTransaction(confidentialBatchInner(convert, 102, ""))
	batch.AddInnerTransaction(makeTestPayment())
	flags := BatchFlagAllOrNothing
	batch.Flags = &flags
	if err := batch.Validate(); err != nil {
		t.Fatalf("owner-authored Convert rejected without BatchSigner: %v", err)
	}
}
