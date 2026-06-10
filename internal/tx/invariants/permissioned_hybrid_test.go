package invariants

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// makeHybridOfferBlob builds a serialized hybrid Offer SLE, optionally with the
// AdditionalBooks STArray present, for invariant testing.
func makeHybridOfferBlob(t *testing.T, withDomain, withAdditionalBooks bool) []byte {
	t.Helper()

	var bookDir, addlBookDir, domain [32]byte
	for i := range bookDir {
		bookDir[i] = 0x11
		addlBookDir[i] = 0x22
		domain[i] = 0x33
	}

	offer := &state.LedgerOffer{
		Account:       "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		Sequence:      7,
		TakerPays:     state.NewXRPAmountFromInt(10_000_000),
		TakerGets:     state.NewIssuedAmountFromFloat64(10, "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"),
		BookDirectory: bookDir,
		Flags:         lsfHybridInvariant,
	}
	if withDomain {
		offer.DomainID = domain
	}
	if withAdditionalBooks {
		offer.AdditionalBookDirectory = addlBookDir
		offer.AdditionalBookNode = 0
	}

	data, err := state.SerializeLedgerOffer(offer)
	if err != nil {
		t.Fatalf("SerializeLedgerOffer: %v", err)
	}
	return data
}

// TestValidPermissionedDEX_HybridAdditionalBooks pins rippled's badHybrids
// predicate: a hybrid offer is malformed unless it carries both a DomainID and
// a single-entry AdditionalBooks STArray.
func TestValidPermissionedDEX_HybridAdditionalBooks(t *testing.T) {
	tx := stubTx{txType: TypeOfferCreate}

	tests := []struct {
		name          string
		withDomain    bool
		withAddlBooks bool
		wantViolation bool
	}{
		{"well-formed hybrid", true, true, false},
		{"missing AdditionalBooks", true, false, true},
		{"missing DomainID", false, true, true},
		{"missing both", false, false, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blob := makeHybridOfferBlob(t, tc.withDomain, tc.withAddlBooks)
			entries := []InvariantEntry{{EntryType: "Offer", After: blob}}
			v := checkValidPermissionedDEX(tx, TesSUCCESS, entries, nil)
			if tc.wantViolation && v == nil {
				t.Fatalf("expected ValidPermissionedDEX violation, got none")
			}
			if !tc.wantViolation && v != nil {
				t.Fatalf("unexpected violation: %s", v.Message)
			}
		})
	}
}
