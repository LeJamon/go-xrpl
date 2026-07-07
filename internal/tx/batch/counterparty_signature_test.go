package batch

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

// batchWithInnerCounterparty builds a valid two-inner AllOrNothing batch whose
// first inner transaction carries the given CounterpartySignature, so the
// per-inner signature-field checks can be exercised.
func batchWithInnerCounterparty(cp *tx.CounterpartySignature) *Batch {
	b := NewBatch(testOuter)
	inner := makeTestPayment()
	inner.GetCommon().CounterpartySignature = cp
	b.AddInnerTransaction(inner)
	b.AddInnerTransaction(makeTestPayment())
	flags := BatchFlagAllOrNothing
	b.Common.Flags = &flags
	return b
}

// TestBatchInnerCounterpartySignatureFields mirrors rippled Batch::preflight's
// checkSignatureFields applied to an inner transaction's CounterpartySignature:
// a TxnSignature is temBAD_SIGNATURE, a Signers array is temBAD_SIGNER, and a
// non-empty SigningPubKey is temBAD_REGKEY.
func TestBatchInnerCounterpartySignatureFields(t *testing.T) {
	tests := []struct {
		name    string
		cp      *tx.CounterpartySignature
		wantErr error
	}{
		{
			name:    "TxnSignature present",
			cp:      &tx.CounterpartySignature{TxnSignature: "DEADBEEF"},
			wantErr: ErrBatchInnerHasTxnSignature,
		},
		{
			name: "Signers present",
			cp: &tx.CounterpartySignature{Signers: []tx.SignerWrapper{
				{Signer: tx.Signer{Account: testSigner1}},
			}},
			wantErr: ErrBatchInnerHasSigners,
		},
		{
			name:    "SigningPubKey present",
			cp:      &tx.CounterpartySignature{SigningPubKey: "ED0000000000000000000000000000000000000000000000000000000000000001"},
			wantErr: ErrBatchInnerHasSigningPubKey,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := batchWithInnerCounterparty(tc.cp)
			err := b.Validate()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestBatchInnerCounterpartyEmptyAllowed confirms that an inner transaction may
// carry an empty CounterpartySignature (no signature material) without error —
// only signature fields are rejected.
func TestBatchInnerCounterpartyEmptyAllowed(t *testing.T) {
	b := batchWithInnerCounterparty(&tx.CounterpartySignature{})
	if err := b.Validate(); err != nil {
		t.Fatalf("empty inner CounterpartySignature should be allowed, got %v", err)
	}
}
