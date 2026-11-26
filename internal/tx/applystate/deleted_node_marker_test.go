package applystate

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// A field absent from the original but set before deletion is recorded by
// rippled as a not-present PreviousFields slot, which serializes to an empty
// PreviousFields object.
func TestBuildDeletedNode_EmptyPreviousFieldsMarker(t *testing.T) {
	orig := &state.LedgerOffer{
		Account:           "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		Sequence:          7,
		TakerPays:         state.NewXRPAmountFromInt(1_000_000),
		TakerGets:         state.NewXRPAmountFromInt(2_000_000),
		PreviousTxnLgrSeq: 3,
	}
	orig.PreviousTxnID[0] = 0xAB
	origBytes, err := state.SerializeLedgerOffer(orig)
	if err != nil {
		t.Fatalf("SerializeLedgerOffer original: %v", err)
	}

	tests := []struct {
		name       string
		expiration uint32
		wantMarker bool
	}{
		{name: "absent field set before deletion", expiration: 100, wantMarker: true},
		{name: "unchanged", expiration: 0, wantMarker: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur := *orig
			cur.Expiration = tt.expiration
			curBytes, err := state.SerializeLedgerOffer(&cur)
			if err != nil {
				t.Fatalf("SerializeLedgerOffer current: %v", err)
			}
			node, err := (&ApplyStateTable{}).buildDeletedNode([32]byte{0xBB}, origBytes, curBytes)
			if err != nil {
				t.Fatalf("buildDeletedNode: %v", err)
			}
			if len(node.PreviousFields) != 0 {
				t.Fatalf("PreviousFields = %v, want empty", node.PreviousFields)
			}
			if node.EmitEmptyPreviousFields != tt.wantMarker {
				t.Fatalf("EmitEmptyPreviousFields = %v, want %v", node.EmitEmptyPreviousFields, tt.wantMarker)
			}
		})
	}
}
