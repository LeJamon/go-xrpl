package invariants

import (
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

type stubView struct {
	seq uint32
}

func (v stubView) Read(k keylet.Keylet) ([]byte, error) { return nil, nil }
func (v stubView) Exists(k keylet.Keylet) (bool, error) { return false, nil }
func (v stubView) Succ(k [32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v stubView) LedgerSeq() uint32 { return v.seq }

func mustSerializeAccount(t *testing.T, a *state.AccountRoot) []byte {
	t.Helper()
	b, err := state.SerializeAccountRoot(a)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	return b
}

func mustSerializeMPTIssuance(t *testing.T, m *state.MPTokenIssuanceData) []byte {
	t.Helper()
	b, err := state.SerializeMPTokenIssuance(m)
	if err != nil {
		t.Fatalf("SerializeMPTokenIssuance: %v", err)
	}
	return b
}

// TestXRPNotCreated_StrictEquality: a net XRP change more negative than -fee
// (XRP burned beyond what the fee accounts for) must trip XRPNotCreated.
// Reference: rippled InvariantCheck.cpp:161-166.
func TestXRPNotCreated_StrictEquality(t *testing.T) {
	before := mustSerializeAccount(t, &state.AccountRoot{
		Account: "rrrrrrrrrrrrrrrrrrrrrhoLvTp", Balance: 1_000_000, Sequence: 5,
	})
	after := mustSerializeAccount(t, &state.AccountRoot{
		Account: "rrrrrrrrrrrrrrrrrrrrrhoLvTp", Balance: 999_000, Sequence: 6,
	})
	entries := []InvariantEntry{{EntryType: "AccountRoot", Before: before, After: after}}

	if v := checkXRPNotCreated(TesSUCCESS, 10, entries); v == nil {
		t.Fatalf("expected XRPNotCreated violation (net change -1000 doesn't match fee 10)")
	}
	if v := checkXRPNotCreated(TesSUCCESS, 1000, entries); v != nil {
		t.Fatalf("unexpected XRPNotCreated violation when net change == -fee: %v", v)
	}
}

// TestValidNewAccountRoot_PermittedTypes ensures the allow-list now covers
// VaultCreate and the XChain attestation tx types in addition to Payment /
// AMMCreate / Batch.
// Reference: rippled InvariantCheck.cpp:964-967.
func TestValidNewAccountRoot_PermittedTypes(t *testing.T) {
	rules := amendment.AllSupportedRules()
	view := stubView{seq: 100}
	newAcct := mustSerializeAccount(t, &state.AccountRoot{
		Account:  "rrrrrrrrrrrrrrrrrrrrrhoLvTp",
		Balance:  1_000_000,
		Sequence: 100,
	})
	entries := []InvariantEntry{{EntryType: "AccountRoot", Before: nil, After: newAcct}}

	for _, txType := range []string{"Payment", "AMMCreate", "VaultCreate", "XChainAddClaimAttestation", "XChainAddAccountCreateAttest", "Batch"} {
		if v := checkValidNewAccountRoot(txType, TesSUCCESS, entries, view, rules); v != nil {
			t.Errorf("%s: unexpected violation %v", txType, v)
		}
	}
	if v := checkValidNewAccountRoot("OfferCreate", TesSUCCESS, entries, view, rules); v == nil {
		t.Fatalf("OfferCreate: expected violation creating AccountRoot")
	}
}

// TestValidNewAccountRoot_WrongStartingSeq enforces accountSeq == startingSeq
// when featureDeletableAccounts is enabled.
// Reference: rippled InvariantCheck.cpp:981-993.
func TestValidNewAccountRoot_WrongStartingSeq(t *testing.T) {
	rules := amendment.AllSupportedRules()
	view := stubView{seq: 100}
	bad := mustSerializeAccount(t, &state.AccountRoot{
		Account:  "rrrrrrrrrrrrrrrrrrrrrhoLvTp",
		Balance:  1_000_000,
		Sequence: 1, // should be view.seq() = 100
	})
	entries := []InvariantEntry{{EntryType: "AccountRoot", Before: nil, After: bad}}
	if v := checkValidNewAccountRoot("Payment", TesSUCCESS, entries, view, rules); v == nil {
		t.Fatal("expected violation for wrong starting sequence")
	}
}

// TestAccountRootsNotDeleted_VaultDelete: a successful VaultDelete must be
// allowed to delete exactly one AccountRoot.
// Reference: rippled InvariantCheck.cpp:382-385.
func TestAccountRootsNotDeleted_VaultDelete(t *testing.T) {
	before := mustSerializeAccount(t, &state.AccountRoot{
		Account: "rrrrrrrrrrrrrrrrrrrrrhoLvTp", Balance: 0, Sequence: 0,
	})
	entries := []InvariantEntry{{EntryType: "AccountRoot", Before: before, After: nil, IsDelete: true}}
	if v := checkAccountRootsNotDeleted("VaultDelete", TesSUCCESS, entries); v != nil {
		t.Fatalf("VaultDelete: unexpected violation %v", v)
	}
}

// TestNoZeroEscrow_MPTokenIssuanceBounds enforces the MPTokenIssuance
// OutstandingAmount / LockedAmount bounds added by issue #499.
// Reference: rippled InvariantCheck.cpp:319-327.
func TestNoZeroEscrow_MPTokenIssuanceBounds(t *testing.T) {
	locked := uint64(50)
	good := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Sequence: 1, OutstandingAmount: 100, LockedAmount: &locked,
	})
	if v := checkNoZeroEscrow([]InvariantEntry{{EntryType: "MPTokenIssuance", After: good}}); v != nil {
		t.Fatalf("balanced issuance: unexpected violation %v", v)
	}
	overLocked := uint64(150)
	bad := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Sequence: 1, OutstandingAmount: 100, LockedAmount: &overLocked,
	})
	if v := checkNoZeroEscrow([]InvariantEntry{{EntryType: "MPTokenIssuance", After: bad}}); v == nil {
		t.Fatal("expected violation: LockedAmount > OutstandingAmount")
	}
}
