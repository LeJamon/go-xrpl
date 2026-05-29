package invariants

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
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

type stubTx struct {
	txType TxType
}

func (t stubTx) TxType() TxType                   { return t.txType }
func (t stubTx) TxAccount() string                { return "" }
func (t stubTx) TxHasField(name string) bool      { return false }
func (t stubTx) Flatten() (map[string]any, error) { return map[string]any{}, nil }

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

	for _, txType := range []string{"Payment", "AMMCreate", "VaultCreate", "XChainAddClaimAttestation", "XChainAddAccountCreateAttestation", "Batch"} {
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

// rulesWithSingleAssetVault returns Rules with featureSingleAssetVault and
// featureDeletableAccounts both enabled. featureSingleAssetVault is
// SupportedNo in the registry so AllSupportedRules() does not include it,
// but the gap-fix invariant logic only kicks in when it's on.
func rulesWithSingleAssetVault() *amendment.Rules {
	return amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureDeletableAccounts,
	})
}

// TestValidNewAccountRoot_PseudoAccountWrongTxType: when featureSingleAssetVault
// is enabled, a pseudo-account (sfAMMID set) created by a tx type other than
// AMMCreate / VaultCreate / Batch must violate.
// Reference: rippled Invariants_test.cpp:965-980, InvariantCheck.cpp:973-979.
func TestValidNewAccountRoot_PseudoAccountWrongTxType(t *testing.T) {
	rules := rulesWithSingleAssetVault()
	view := stubView{seq: 100}
	pseudo := mustSerializeAccount(t, &state.AccountRoot{
		Account:  "rrrrrrrrrrrrrrrrrrrrrhoLvTp",
		Balance:  0,
		Sequence: 0,
		Flags:    LsfDisableMaster | LsfDefaultRipple | LsfDepositAuth,
		AMMID:    [32]byte{1},
	})
	entries := []InvariantEntry{{EntryType: "AccountRoot", Before: nil, After: pseudo}}

	// Payment is in the general allow-list but must be rejected for pseudo accounts.
	if v := checkValidNewAccountRoot("Payment", TesSUCCESS, entries, view, rules); v == nil {
		t.Fatal("Payment: expected violation for pseudo-account created by non-AMM/Vault tx")
	}

	// AMMCreate is the allowed pseudo-creator.
	if v := checkValidNewAccountRoot("AMMCreate", TesSUCCESS, entries, view, rules); v != nil {
		t.Fatalf("AMMCreate: unexpected violation %v", v)
	}
}

// TestValidNewAccountRoot_PseudoAccountWrongFlags: when featureSingleAssetVault
// is enabled, a pseudo-account whose Flags are not exactly the canonical mask
// must violate (here LsfAMM-shaped 0x02000000 added for back-compat).
// Reference: rippled Invariants_test.cpp:999-1031, InvariantCheck.cpp:995-1006.
func TestValidNewAccountRoot_PseudoAccountWrongFlags(t *testing.T) {
	rules := rulesWithSingleAssetVault()
	view := stubView{seq: 100}
	const expected = LsfDisableMaster | LsfDefaultRipple | LsfDepositAuth
	wrongFlags := expected | 0x02000000 // extra unrelated bit
	bad := mustSerializeAccount(t, &state.AccountRoot{
		Account:  "rrrrrrrrrrrrrrrrrrrrrhoLvTp",
		Balance:  0,
		Sequence: 0,
		Flags:    wrongFlags,
		AMMID:    [32]byte{1},
	})
	entries := []InvariantEntry{{EntryType: "AccountRoot", Before: nil, After: bad}}
	if v := checkValidNewAccountRoot("AMMCreate", TesSUCCESS, entries, view, rules); v == nil {
		t.Fatal("expected violation for pseudo-account with wrong flag mask")
	}

	good := mustSerializeAccount(t, &state.AccountRoot{
		Account:  "rrrrrrrrrrrrrrrrrrrrrhoLvTp",
		Balance:  0,
		Sequence: 0,
		Flags:    expected,
		AMMID:    [32]byte{1},
	})
	if v := checkValidNewAccountRoot("AMMCreate", TesSUCCESS, []InvariantEntry{{EntryType: "AccountRoot", Before: nil, After: good}}, view, rules); v != nil {
		t.Fatalf("canonical pseudo-account flags: unexpected violation %v", v)
	}
}

// TestValidNewAccountRoot_CrossGatingPreSingleAssetVault locks in the
// gating contract: when featureSingleAssetVault is disabled, neither the
// pseudo-account flag-mask check nor the wrong-tx-type pseudo guard runs.
// AMMCreate must therefore install Sequence == view.seq() (not 0), matching
// the pre-amendment branch at amm_create.go:253-256 and rippled View.cpp:1120-1123.
func TestValidNewAccountRoot_CrossGatingPreSingleAssetVault(t *testing.T) {
	rules := amendment.NewRules([][32]byte{amendment.FeatureDeletableAccounts})
	view := stubView{seq: 100}

	// A pseudo-shaped account (AMMID set) with non-canonical flags must NOT
	// trip the flag-mask check pre-amendment.
	ammNew := mustSerializeAccount(t, &state.AccountRoot{
		Account:  "rrrrrrrrrrrrrrrrrrrrrhoLvTp",
		Balance:  0,
		Sequence: view.LedgerSeq(),
		Flags:    LsfDisableMaster | LsfDefaultRipple | LsfDepositAuth | 0x02000000,
		AMMID:    [32]byte{1},
	})
	if v := checkValidNewAccountRoot("AMMCreate", TesSUCCESS, []InvariantEntry{{EntryType: "AccountRoot", Before: nil, After: ammNew}}, view, rules); v != nil {
		t.Fatalf("pre-singleAssetVault: unexpected violation %v", v)
	}

	// Conversely, AMMCreate with Sequence == 0 pre-amendment must violate
	// because the starting-seq expectation falls back to view.seq().
	zeroSeq := mustSerializeAccount(t, &state.AccountRoot{
		Account:  "rrrrrrrrrrrrrrrrrrrrrhoLvTp",
		Balance:  0,
		Sequence: 0,
		Flags:    LsfDisableMaster | LsfDefaultRipple | LsfDepositAuth,
		AMMID:    [32]byte{1},
	})
	if v := checkValidNewAccountRoot("AMMCreate", TesSUCCESS, []InvariantEntry{{EntryType: "AccountRoot", Before: nil, After: zeroSeq}}, view, rules); v == nil {
		t.Fatal("pre-singleAssetVault: expected violation when Sequence != view.seq()")
	}
}

// TestNoZeroEscrow_IOUBadCurrency: IOU escrows holding the sentinel "XRP"
// currency code must trip NoZeroEscrow. The codec rejects "XRP" as an IOU
// currency at encode-time (as it should), so we build the canonical escrow
// blob with USD and patch the 3-byte currency code in-place to exercise the
// defense-in-depth invariant against future encoder bugs.
// Reference: rippled InvariantCheck.cpp:286-292.
func TestNoZeroEscrow_IOUBadCurrency(t *testing.T) {
	const issuer = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	const owner = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

	jsonObj := map[string]any{
		"LedgerEntryType": "Escrow",
		"Account":         owner,
		"Destination":     owner,
		"Amount": map[string]any{
			"currency": "USD",
			"issuer":   issuer,
			"value":    "1",
		},
		"OwnerNode": "0",
		"Flags":     uint32(0),
	}
	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	good, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	if v := checkNoZeroEscrow([]InvariantEntry{{EntryType: "Escrow", After: good}}); v != nil {
		t.Fatalf("good IOU escrow: unexpected violation %v", v)
	}

	usdMarker := []byte{'U', 'S', 'D'}
	idx := bytes.Index(good, usdMarker)
	if idx < 0 {
		t.Fatal("USD currency marker not found in encoded escrow")
	}
	bad := make([]byte, len(good))
	copy(bad, good)
	copy(bad[idx:idx+3], []byte{'X', 'R', 'P'})

	if v := checkNoZeroEscrow([]InvariantEntry{{EntryType: "Escrow", After: bad}}); v == nil {
		t.Fatal("expected violation: IOU escrow with bad (XRP) currency")
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

// TestXRPBalanceChecks_ParseFailure: a malformed AccountRoot SLE must trip the
// invariant rather than be silently skipped. The bytes were serialized by
// goXRPL moments earlier, so a parse failure is a round-trip bug that rippled
// would catch as tecINVARIANT_FAILED. Reference: issue #597.
func TestXRPBalanceChecks_ParseFailure(t *testing.T) {
	garbage := []byte{0xde, 0xad, 0xbe, 0xef}
	entries := []InvariantEntry{{EntryType: "AccountRoot", After: garbage}}
	if v := checkXRPBalances(entries); v == nil {
		t.Fatal("expected XRPBalanceChecks violation for unparseable AccountRoot SLE")
	}
}

// TestXRPNotCreated_ParseFailure: a malformed XRP-bearing SLE must trip
// XRPNotCreated instead of defaulting the balance to 0 (which could mask an
// XRP-creation bug). Reference: issue #597.
func TestXRPNotCreated_ParseFailure(t *testing.T) {
	garbage := []byte{0xde, 0xad, 0xbe, 0xef}
	for _, entryType := range []string{"AccountRoot", "Escrow", "PayChannel"} {
		entries := []InvariantEntry{{EntryType: entryType, After: garbage}}
		if v := checkXRPNotCreated(TesSUCCESS, 10, entries); v == nil {
			t.Fatalf("%s: expected XRPNotCreated violation for unparseable SLE", entryType)
		}
	}
}

// TestValidAMM_ParseFailure: an entry identified as an AMM SLE that fails to
// decode must trip ValidAMM unconditionally (rippled's visitEntry catch-all is
// not amendment-gated), rather than leaving a zeroed account ID. Reference:
// issue #597.
func TestValidAMM_ParseFailure(t *testing.T) {
	garbage := []byte{0xde, 0xad, 0xbe, 0xef}
	rules := amendment.AllSupportedRules()
	view := stubView{seq: 100}

	after := []InvariantEntry{{EntryType: "AMM", After: garbage}}
	if v := checkValidAMM(stubTx{txType: TypeAMMDeposit}, TesSUCCESS, after, view, rules); v == nil {
		t.Fatal("expected ValidAMM violation for unparseable AMM SLE (after)")
	}

	before := []InvariantEntry{{EntryType: "AMM", Before: garbage}}
	if v := checkValidAMM(stubTx{txType: TypeAMMDeposit}, TesSUCCESS, before, view, rules); v == nil {
		t.Fatal("expected ValidAMM violation for unparseable AMM SLE (before)")
	}
}
