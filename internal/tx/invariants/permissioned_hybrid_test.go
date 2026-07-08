package invariants

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// cleanup313Rules returns a Rules with only fixCleanup3_1_3 enabled, for
// exercising the post-amendment hybrid-offer and permissioned-domain invariants.
func cleanup313Rules() *amendment.Rules {
	return amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_1_3})
}

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

// makeRawHybridOfferBlob hand-builds a hybrid Offer SLE so that the degenerate
// shapes rippled's serializer never emits — a present all-zero DomainID and a
// present empty AdditionalBooks array — can be exercised. The standard
// serializer omits both when zero/absent, so they must be assembled directly.
func makeRawHybridOfferBlob(domainID *[32]byte, additionalBooks *[]([32]byte)) []byte {
	var blob []byte

	// LedgerEntryType UInt16 (nth=1) = Offer (0x006f).
	blob = append(blob, 0x11, 0x00, 0x6f)
	// Flags UInt32 (nth=2) = lsfHybrid.
	flags := lsfHybridInvariant
	blob = append(blob, 0x22)
	blob = append(blob, byte(flags>>24), byte(flags>>16), byte(flags>>8), byte(flags))
	// Sequence UInt32 (nth=4) — keeps the blob comfortably past the 20-byte floor.
	blob = append(blob, 0x24, 0x00, 0x00, 0x00, 0x07)

	if domainID != nil {
		// DomainID Hash256 (nth=34, extended field code) — present, value as given.
		blob = append(blob, 0x50, 0x22)
		blob = append(blob, domainID[:]...)
	}

	if additionalBooks != nil {
		// AdditionalBooks STArray (nth=13). Each entry is a Book inner object
		// (nth=36) carrying a BookDirectory Hash256 (nth=16).
		blob = append(blob, 0xFD) // type 15, nth 13
		for _, dir := range *additionalBooks {
			blob = append(blob, 0xE0, 0x24) // Book object (nth=36)
			blob = append(blob, 0x50, 0x10) // BookDirectory Hash256 (nth=16)
			blob = append(blob, dir[:]...)
			blob = append(blob, 0xE1) // object end
		}
		blob = append(blob, 0xF1) // array end
	}

	return blob
}

// TestValidPermissionedDEX_HybridDegenerateShapes pins that the badHybrids
// predicate keys on field PRESENCE, not value — mirroring rippled's
// isFieldPresent semantics (InvariantCheck.cpp:1658-1663). A present all-zero
// DomainID and a present empty AdditionalBooks array both satisfy presence and
// must not trip the invariant; only an absent field or an array of size > 1 does.
func TestValidPermissionedDEX_HybridDegenerateShapes(t *testing.T) {
	tx := stubTx{txType: TypeOfferCreate}

	var zero [32]byte
	var nonZero [32]byte
	for i := range nonZero {
		nonZero[i] = 0x22
	}

	oneBook := []([32]byte){nonZero}
	emptyBooks := []([32]byte){}
	twoBooks := []([32]byte){nonZero, nonZero}

	// wantOld is the pre-fixCleanup3_1_3 expectation (size > 1 or absent field
	// fails); wantNew is the post-amendment expectation (size != 1 fails, so a
	// present empty array now also fails).
	tests := []struct {
		name     string
		domainID *[32]byte
		books    *[]([32]byte)
		wantOld  bool
		wantNew  bool
	}{
		{"present zero DomainID passes presence", &zero, &oneBook, false, false},
		{"present empty AdditionalBooks", &nonZero, &emptyBooks, false, true},
		{"present zero DomainID + empty array", &zero, &emptyBooks, false, true},
		{"absent DomainID fails", nil, &oneBook, true, true},
		{"absent AdditionalBooks fails", &nonZero, nil, true, true},
		{"AdditionalBooks size > 1 fails", &nonZero, &twoBooks, true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blob := makeRawHybridOfferBlob(tc.domainID, tc.books)
			entries := []InvariantEntry{{EntryType: "Offer", After: blob}}

			if v := checkValidPermissionedDEX(tx, TesSUCCESS, entries, nil, nil); (v != nil) != tc.wantOld {
				t.Fatalf("pre-amendment: got violation=%v, want %v (%v)", v != nil, tc.wantOld, v)
			}
			if v := checkValidPermissionedDEX(tx, TesSUCCESS, entries, nil, cleanup313Rules()); (v != nil) != tc.wantNew {
				t.Fatalf("post-amendment: got violation=%v, want %v (%v)", v != nil, tc.wantNew, v)
			}
		})
	}
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
			// The serializer emits at most one AdditionalBooks entry, so a
			// well-formed hybrid holds exactly one book: both eras agree.
			for _, rules := range []*amendment.Rules{nil, cleanup313Rules()} {
				v := checkValidPermissionedDEX(tx, TesSUCCESS, entries, nil, rules)
				if tc.wantViolation && v == nil {
					t.Fatalf("expected ValidPermissionedDEX violation, got none")
				}
				if !tc.wantViolation && v != nil {
					t.Fatalf("unexpected violation: %s", v.Message)
				}
			}
		})
	}
}
