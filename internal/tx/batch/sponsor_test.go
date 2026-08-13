package batch

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

func batchWithInnerSponsor(signature *tx.SponsorSignature, sponsorFlags uint32, withBatchSigner bool) *Batch {
	batch := NewBatch(testOuter)
	inner := makeTestPayment()
	innerCommon := inner.GetCommon()
	innerCommon.Sponsor = testSigner1
	innerCommon.SponsorFlags = &sponsorFlags
	innerCommon.SponsorSignature = signature
	batch.AddInnerTransaction(inner)
	batch.AddInnerTransaction(makeTestPayment())
	flags := BatchFlagAllOrNothing
	batch.Common.Flags = &flags
	if withBatchSigner {
		batch.BatchSigners = []BatchSigner{{
			BatchSigner: BatchSignerData{
				Account:           testSigner1,
				SigningPubKey:     "ED01",
				BatchTxnSignature: "AA",
			},
		}}
	}
	return batch
}

func TestBatchInnerSponsorSignatureFields(t *testing.T) {
	testCases := []struct {
		name      string
		signature *tx.SponsorSignature
		wantErr   error
	}{
		{
			name:      "TxnSignature present",
			signature: &tx.SponsorSignature{TxnSignature: "DEADBEEF"},
			wantErr:   ErrBatchInnerHasTxnSignature,
		},
		{
			name: "Signers present",
			signature: &tx.SponsorSignature{Signers: []tx.SignerWrapper{
				{Signer: tx.Signer{Account: testSigner2}},
			}},
			wantErr: ErrBatchInnerHasSigners,
		},
		{
			name: "SigningPubKey present",
			signature: &tx.SponsorSignature{
				SigningPubKey: "ED0000000000000000000000000000000000000000000000000000000000000001",
			},
			wantErr: ErrBatchInnerHasSigningPubKey,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			batch := batchWithInnerSponsor(
				testCase.signature,
				tx.SpfSponsorReserve,
				true,
			)
			if err := batch.Validate(); !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

func TestBatchInnerSponsorRules(t *testing.T) {
	t.Run("empty cosignature requires batch signer", func(t *testing.T) {
		batch := batchWithInnerSponsor(
			&tx.SponsorSignature{},
			tx.SpfSponsorReserve,
			false,
		)
		if err := batch.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if err := batch.PreflightSigValidated(); !errors.Is(err, ErrBatchMissingSigner) {
			t.Fatalf("PreflightSigValidated() error = %v, want %v", err, ErrBatchMissingSigner)
		}

		batch.BatchSigners = []BatchSigner{{
			BatchSigner: BatchSignerData{
				Account:           testSigner1,
				SigningPubKey:     "ED01",
				BatchTxnSignature: "AA",
			},
		}}
		if err := batch.Validate(); err != nil {
			t.Fatalf("empty inner SponsorSignature with BatchSigner rejected: %v", err)
		}
		if err := batch.PreflightSigValidated(); err != nil {
			t.Fatalf("empty inner SponsorSignature with BatchSigner rejected: %v", err)
		}
	})

	t.Run("prefunded reserve needs no batch signer", func(t *testing.T) {
		batch := batchWithInnerSponsor(nil, tx.SpfSponsorReserve, false)
		if err := batch.Validate(); err != nil {
			t.Fatalf("pre-funded reserve sponsor rejected: %v", err)
		}
	})

	t.Run("fee sponsorship is forbidden", func(t *testing.T) {
		batch := batchWithInnerSponsor(nil, tx.SpfSponsorFee, false)
		if err := batch.Validate(); !errors.Is(err, ErrBatchInnerFeeSponsored) {
			t.Fatalf("Validate() error = %v, want %v", err, ErrBatchInnerFeeSponsored)
		}
	})
}

func TestCalculateMinimumFeeIncludesOuterSponsorMultisigners(t *testing.T) {
	config := tx.EngineConfig{BaseFee: 10}

	direct := NewBatch(testOuter)
	direct.AddInnerTransaction(makeTestPayment())
	direct.AddInnerTransaction(makeTestPayment())
	direct.Common.SponsorSignature = &tx.SponsorSignature{
		SigningPubKey: "ED01",
		TxnSignature:  "AA",
	}
	if got := direct.CalculateMinimumFee(nil, config); got != 40 {
		t.Fatalf("direct sponsor minimum fee = %d, want 40", got)
	}

	multisigned := NewBatch(testOuter)
	multisigned.AddInnerTransaction(makeTestPayment())
	multisigned.AddInnerTransaction(makeTestPayment())
	multisigned.Common.SponsorSignature = &tx.SponsorSignature{
		Signers: make([]tx.SignerWrapper, 2),
	}
	if got := multisigned.CalculateMinimumFee(nil, config); got != 60 {
		t.Fatalf("multisigned sponsor minimum fee = %d, want 60", got)
	}
}
