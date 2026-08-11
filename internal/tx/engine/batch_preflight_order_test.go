package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/check"
	"github.com/LeJamon/go-xrpl/internal/tx/escrow"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestBatchInnerPreflightPrecedesSequenceAndDuplicateChecks(t *testing.T) {
	const (
		account     = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		destination = "rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH"
	)

	makeInner := func(amount int64, sequence *uint32) txcore.Transaction {
		inner := payment.NewPayment(account, destination, txcore.NewXRPAmount(amount))
		inner.Fee = "0"
		inner.SigningPubKey = ""
		inner.Sequence = sequence
		inner.SetFlags(txcore.TfInnerBatchTxn)
		return inner
	}
	makeBatch := func(first, second txcore.Transaction) *batch.Batch {
		outer := batch.NewBatch(account)
		outer.Fee = "40"
		outer.SetSequence(1)
		outer.SigningPubKey = ""
		outer.SetFlags(batch.BatchFlagAllOrNothing)
		outer.AddInnerTransaction(first)
		outer.AddInnerTransaction(second)
		return outer
	}
	seq1 := uint32(1)
	seq2 := uint32(2)
	tests := []struct {
		name  string
		batch *batch.Batch
	}{
		{
			name:  "missing sequence",
			batch: makeBatch(makeInner(0, nil), makeInner(1, &seq2)),
		},
		{
			name:  "duplicate sequence",
			batch: makeBatch(makeInner(1, &seq1), makeInner(0, &seq1)),
		},
	}

	engine := dedupEngine(amendment.AllSupportedRules())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := engine.preflight(test.batch); got != ter.TemINVALID_INNER_BATCH {
				t.Fatalf("preflight = %v, want TemINVALID_INNER_BATCH", got)
			}
		})
	}
}

func TestBatchPreflightInterleavesChecksPerInner(t *testing.T) {
	const (
		account     = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		destination = "rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH"
	)

	makeInner := func(amount int64, sequence *uint32) *payment.Payment {
		inner := payment.NewPayment(account, destination, txcore.NewXRPAmount(amount))
		inner.Fee = "0"
		inner.SigningPubKey = ""
		inner.Sequence = sequence
		inner.SetFlags(txcore.TfInnerBatchTxn)
		return inner
	}
	makeBatch := func(first, second txcore.Transaction) *batch.Batch {
		outer := batch.NewBatch(account)
		outer.Fee = "40"
		outer.SetSequence(1)
		outer.SigningPubKey = ""
		outer.SetFlags(batch.BatchFlagAllOrNothing)
		outer.AddInnerTransaction(first)
		outer.AddInnerTransaction(second)
		return outer
	}

	seq1 := uint32(1)
	seq2 := uint32(2)
	badLaterSignature := makeInner(1, &seq2)
	badLaterSignature.TxnSignature = "00"

	tests := []struct {
		name string
		tx   *batch.Batch
		want ter.Result
	}{
		{
			name: "first inner sequence error precedes later signature error",
			tx:   makeBatch(makeInner(1, nil), badLaterSignature),
			want: ter.TemSEQ_AND_TICKET,
		},
		{
			name: "first inner preflight error precedes later signature error",
			tx:   makeBatch(makeInner(0, &seq1), badLaterSignature),
			want: ter.TemINVALID_INNER_BATCH,
		},
	}

	engine := dedupEngine(amendment.AllSupportedRules())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := engine.preflight(test.tx); got != test.want {
				t.Fatalf("preflight = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBatchInnerRunsSigValidatedPreflight(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	credentialIDsPresent := escrow.NewEscrowFinish(account, account, 1)
	credentialIDsPresent.Fee = "0"
	credentialIDsPresent.SigningPubKey = ""
	credentialIDsPresent.SetSequence(2)
	credentialIDsPresent.SetFlags(txcore.TfInnerBatchTxn)
	credentialIDsPresent.SetPresentFields(map[string]bool{"CredentialIDs": true})

	second := payment.NewPayment(account, account, txcore.NewXRPAmount(1))
	second.Fee = "0"
	second.SigningPubKey = ""
	second.SetSequence(3)
	second.SetFlags(txcore.TfInnerBatchTxn)

	outer := batch.NewBatch(account)
	outer.Fee = "40"
	outer.SetSequence(1)
	outer.SigningPubKey = ""
	outer.SetFlags(batch.BatchFlagAllOrNothing)
	outer.AddInnerTransaction(credentialIDsPresent)
	outer.AddInnerTransaction(second)

	engine := dedupEngine(amendment.AllSupportedRules())
	if got := engine.preflight(outer); got != ter.TemINVALID_INNER_BATCH {
		t.Fatalf("preflight = %v, want TemINVALID_INNER_BATCH", got)
	}
}

func TestPreflightInnerRunsUniversalPreflight(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const destination = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"

	oversized := check.NewCheckCreate(
		account,
		destination,
		txcore.NewXRPAmount(int64(drops.MaxDrops)+1),
	)
	oversized.Fee = "0"
	oversized.SigningPubKey = ""
	oversized.SetSequence(2)
	oversized.SetFlags(txcore.TfInnerBatchTxn)

	engine := dedupEngine(amendment.AllSupportedRules())
	if got := engine.preflightInner(oversized); got != ter.TemBAD_AMOUNT {
		t.Fatalf("preflightInner = %v, want TemBAD_AMOUNT", got)
	}
}
