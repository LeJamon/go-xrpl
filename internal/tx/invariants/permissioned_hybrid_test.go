package invariants

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
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
// shapes the state adapter omits — a present all-zero DomainID and a present
// empty AdditionalBooks array — can be exercised while retaining every field
// required by the Offer ledger-entry template.
func makeRawHybridOfferBlob(t *testing.T, domainID *[32]byte, additionalBooks *[]([32]byte)) []byte {
	t.Helper()

	zeroHash := strings.Repeat("0", 64)
	offer := &entry.Offer{}
	offer.SetAccount("rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	offer.SetSequence(7)
	offer.SetTakerPays(map[string]any{
		"value":    "0",
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	offer.SetTakerGets("0")
	offer.SetBookDirectory(zeroHash)
	offer.SetBookNode("0")
	offer.SetOwnerNode("0")
	offer.SetPreviousTxnID(zeroHash)
	offer.SetPreviousTxnLgrSeq(0)
	offer.SetFlags(lsfHybridInvariant)
	if domainID != nil {
		offer.SetDomainID(strings.ToUpper(hex.EncodeToString(domainID[:])))
	}
	if additionalBooks != nil {
		books := make([]any, 0, len(*additionalBooks))
		for _, dir := range *additionalBooks {
			books = append(books, map[string]any{
				"Book": map[string]any{
					"BookDirectory": strings.ToUpper(hex.EncodeToString(dir[:])),
					"BookNode":      "0",
				},
			})
		}
		offer.SetAdditionalBooks(books)
	}

	blob, err := offer.Encode()
	if err != nil {
		t.Fatalf("encode raw hybrid Offer: %v", err)
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
			blob := makeRawHybridOfferBlob(t, tc.domainID, tc.books)
			entries := []InvariantEntry{{EntryType: entry.TypeOffer, After: blob}}

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
			entries := []InvariantEntry{{EntryType: entry.TypeOffer, After: blob}}
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
