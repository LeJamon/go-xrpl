package invariants

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

// vvTx is a stub transaction exposing a type, account, and flattened fields for
// the ValidVault checker.
type vvTx struct {
	txType TxType
	acct   string
	flat   map[string]any
}

func (x vvTx) TxType() TxType                   { return x.txType }
func (x vvTx) TxAccount() string                { return x.acct }
func (x vvTx) TxHasField(string) bool           { return false }
func (x vvTx) Flatten() (map[string]any, error) { return x.flat, nil }

func vvTestIDs() (owner, pseudo [20]byte, ownerAddr, pseudoAddr string, shareMPTID [24]byte) {
	for i := range owner {
		owner[i] = 0x11
		pseudo[i] = 0x22
	}
	ownerAddr, _ = state.EncodeAccountID(owner)
	pseudoAddr, _ = state.EncodeAccountID(pseudo)
	// The share issuance is issued by the pseudo-account at sequence 1.
	shareMPTID = keylet.MakeMPTID(1, pseudo)
	return
}

// vvCraftVault encodes an XRP Vault ledger entry with the given NUMBER totals
// (empty string ⇒ omit / soeDEFAULT zero).
func vvCraftVault(t *testing.T, ownerAddr, pseudoAddr string, shareMPTID [24]byte, total, available, maximum, loss string) []byte {
	t.Helper()
	m := map[string]any{
		"LedgerEntryType":  "Vault",
		"Flags":            0,
		"Sequence":         1,
		"OwnerNode":        "0",
		"Owner":            ownerAddr,
		"Account":          pseudoAddr,
		"Asset":            map[string]any{"currency": "XRP"},
		"ShareMPTID":       strings.ToUpper(hex.EncodeToString(shareMPTID[:])),
		"WithdrawalPolicy": 1,
	}
	if total != "" {
		m["AssetsTotal"] = total
	}
	if available != "" {
		m["AssetsAvailable"] = available
	}
	if maximum != "" {
		m["AssetsMaximum"] = maximum
	}
	if loss != "" {
		m["LossUnrealized"] = loss
	}
	return mustEncode(t, m)
}

func vvCraftIssuance(t *testing.T, issuer [20]byte, seq uint32, outstanding uint64) []byte {
	t.Helper()
	return mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer:            issuer,
		Sequence:          seq,
		OutstandingAmount: outstanding,
		Flags:             0,
		OwnerNode:         0,
	})
}

func savRules() *amendment.Rules {
	return amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault, amendment.FeatureFixCleanup3_1_3})
}

// TestValidVault_StructuralViolations crafts vault-modifying transactions that
// break a specific structural invariant and asserts ValidVault reports it, while
// the well-formed baseline passes.
func TestValidVault_StructuralViolations(t *testing.T) {
	_, pseudo, ownerAddr, pseudoAddr, shareMPTID := vvTestIDs()
	// The share issuance carries 1000 shares, consistent with a vault holding
	// 1000 assets (a zero-share vault must hold no assets).
	issuance := vvCraftIssuance(t, pseudo, 1, 1000)
	deletedIssuance := vvCraftIssuance(t, pseudo, 1, 0)

	// A modify entry for the share issuance, so updatedShares is found in the
	// after-set without a view read.
	issuanceEntry := InvariantEntry{EntryType: "MPTokenIssuance", Before: issuance, After: issuance}

	setTx := vvTx{txType: protocol.TxTypeVaultSet, acct: ownerAddr}

	cases := []struct {
		name    string
		tx      Transaction
		entries []InvariantEntry
		wantMsg string // "" ⇒ expect no violation
	}{
		{
			name: "valid set no-op",
			tx:   setTx,
			entries: []InvariantEntry{
				{EntryType: "Vault",
					Before: vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", ""),
					After:  vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", "")},
				issuanceEntry,
			},
			wantMsg: "",
		},
		{
			name: "assets available exceeds assets outstanding",
			tx:   setTx,
			entries: []InvariantEntry{
				{EntryType: "Vault",
					Before: vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", ""),
					After:  vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "2000", "", "")},
				issuanceEntry,
			},
			wantMsg: "assets available must not be greater than assets outstanding",
		},
		{
			name: "loss unrealized changed by non-loan transaction",
			tx:   setTx,
			entries: []InvariantEntry{
				{EntryType: "Vault",
					Before: vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "800", "", ""),
					After:  vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "800", "", "100")},
				issuanceEntry,
			},
			wantMsg: "vault transaction must not change loss unrealized",
		},
		{
			name: "immutable data (pseudo-account) changed",
			tx:   setTx,
			entries: []InvariantEntry{
				{EntryType: "Vault",
					Before: vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", ""),
					After:  vvCraftVault(t, ownerAddr, ownerAddr, shareMPTID, "1000", "1000", "", "")},
				issuanceEntry,
			},
			wantMsg: "violation of vault immutable data",
		},
		{
			name: "wrong transaction type touches a vault",
			tx:   vvTx{txType: protocol.TxTypePayment, acct: ownerAddr},
			entries: []InvariantEntry{
				{EntryType: "Vault",
					Before: vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", ""),
					After:  vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", "")},
			},
			wantMsg: "vault updated by a wrong transaction type",
		},
		{
			name: "deleted vault still holds assets",
			tx:   vvTx{txType: protocol.TxTypeVaultDelete, acct: ownerAddr},
			entries: []InvariantEntry{
				{EntryType: "Vault", IsDelete: true,
					Before: vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", "")},
				{EntryType: "MPTokenIssuance", IsDelete: true, Before: deletedIssuance},
			},
			wantMsg: "deleted vault must have no assets outstanding",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := checkValidVault(tc.tx, TesSUCCESS, 0, tc.entries, stubView{}, savRules())
			got := ""
			if v != nil {
				got = v.Message
			}
			if got != tc.wantMsg {
				t.Fatalf("checkValidVault = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

// TestValidVault_EnforceGate asserts a detected violation only fails the
// transaction when SingleAssetVault is enabled (rippled's `return !enforce`).
func TestValidVault_EnforceGate(t *testing.T) {
	_, pseudo, ownerAddr, pseudoAddr, shareMPTID := vvTestIDs()
	issuance := vvCraftIssuance(t, pseudo, 1, 0)

	entries := []InvariantEntry{
		{EntryType: "Vault",
			Before: vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", ""),
			After:  vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "2000", "", "")},
		{EntryType: "MPTokenIssuance", Before: issuance, After: issuance},
	}
	setTx := vvTx{txType: protocol.TxTypeVaultSet, acct: ownerAddr}

	// SAV on: violation fails the transaction.
	if v := checkValidVault(setTx, TesSUCCESS, 0, entries, stubView{}, savRules()); v == nil {
		t.Fatal("expected a violation with SingleAssetVault enabled")
	}
	// SAV off: the same broken state is not enforced.
	off := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_1_3})
	if v := checkValidVault(setTx, TesSUCCESS, 0, entries, stubView{}, off); v != nil {
		t.Fatalf("expected no enforcement with SingleAssetVault disabled, got %q", v.Message)
	}
}

// TestVaultMinScale_AmendmentGate pins the fixCleanup3_2_0 computeVaultMinScale
// site: post-amendment the reconciliation rounds deltas at the posterior
// AssetsTotal scale; pre-amendment at the coarsest delta scale. For an IOU these
// diverge; for the integral (XRP/MPT) assets ValidVault actually reconciles they
// both collapse to zero, which is why the gate is a structural no-op in practice.
func TestVaultMinScale_AmendmentGate(t *testing.T) {
	fixOn := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault, amendment.FeatureFixCleanup3_2_0})
	fixOff := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})

	iou := vvAsset{currency: "USD", issuer: [20]byte{1}}
	afterIOU := vvVault{asset: iou, assetsTotal: state.NewXRPLNumber(1000000, 0)}
	iouDelta := iou.makeDelta(state.NewXRPLNumber(0, 0), state.NewXRPLNumber(25, -1))

	on := (&vvChecker{rules: fixOn}).vaultMinScale(iou, afterIOU, iouDelta)
	off := (&vvChecker{rules: fixOff}).vaultMinScale(iou, afterIOU, iouDelta)
	if on == off {
		t.Fatalf("IOU min-scale must differ by amendment: on=%d off=%d", on, off)
	}
	if want := iou.scaleOf(afterIOU.assetsTotal); on != want {
		t.Fatalf("post-amendment IOU min-scale = %d, want AssetsTotal scale %d", on, want)
	}

	xrp := vvAsset{isXRP: true}
	afterXRP := vvVault{asset: xrp, assetsTotal: state.NewXRPLNumber(100, 0)}
	xrpDelta := xrp.makeDelta(state.NewXRPLNumber(0, 0), state.NewXRPLNumber(100, 0))
	if on, off := (&vvChecker{rules: fixOn}).vaultMinScale(xrp, afterXRP, xrpDelta),
		(&vvChecker{rules: fixOff}).vaultMinScale(xrp, afterXRP, xrpDelta); on != 0 || off != 0 {
		t.Fatalf("integral min-scale must be 0 in both eras: on=%d off=%d", on, off)
	}
}
