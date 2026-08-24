package invariants

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
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

func vvTestAccount(t *testing.T, fill byte) ([20]byte, string) {
	t.Helper()
	var id [20]byte
	for i := range id {
		id[i] = fill
	}
	addr, err := state.EncodeAccountID(id)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	return id, addr
}

func vvCraftVaultAsset(t *testing.T, ownerAddr, pseudoAddr string, asset map[string]any, shareMPTID [24]byte, total, available, maximum, loss string) []byte {
	t.Helper()
	m := map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             0,
		"Sequence":          1,
		"OwnerNode":         "0",
		"Owner":             ownerAddr,
		"Account":           pseudoAddr,
		"Asset":             asset,
		"ShareMPTID":        strings.ToUpper(hex.EncodeToString(shareMPTID[:])),
		"WithdrawalPolicy":  1,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
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

func vvCraftVault(t *testing.T, ownerAddr, pseudoAddr string, shareMPTID [24]byte, total, available, maximum, loss string) []byte {
	t.Helper()
	return vvCraftVaultAsset(
		t, ownerAddr, pseudoAddr, map[string]any{"currency": "XRP"},
		shareMPTID, total, available, maximum, loss)
}

func vvCraftIOUVault(t *testing.T, ownerAddr, pseudoAddr, issuerAddr string, shareMPTID [24]byte, total, available string) []byte {
	t.Helper()
	return vvCraftVaultAsset(
		t, ownerAddr, pseudoAddr,
		map[string]any{"currency": "USD", "issuer": issuerAddr},
		shareMPTID, total, available, "", "")
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

func vvCraftMPToken(t *testing.T, account [20]byte, shareMPTID [24]byte, amount uint64) []byte {
	t.Helper()
	b, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           account,
		MPTokenIssuanceID: shareMPTID,
		MPTAmount:         amount,
	})
	if err != nil {
		t.Fatalf("SerializeMPToken: %v", err)
	}
	return b
}

func vvIssuanceEntry(t *testing.T, pseudo [20]byte, shareMPTID [24]byte, before, after uint64) InvariantEntry {
	t.Helper()
	return InvariantEntry{
		Key:       keylet.MPTIssuance(shareMPTID).Key,
		EntryType: entry.TypeMPTokenIssuance,
		Before:    vvCraftIssuance(t, pseudo, 1, before),
		After:     vvCraftIssuance(t, pseudo, 1, after),
	}
}

func vvMPTokenEntry(t *testing.T, account [20]byte, shareMPTID [24]byte, before, after uint64) InvariantEntry {
	t.Helper()
	return InvariantEntry{
		Key:       keylet.MPTokenByID(shareMPTID, account).Key,
		EntryType: entry.TypeMPToken,
		Before:    vvCraftMPToken(t, account, shareMPTID, before),
		After:     vvCraftMPToken(t, account, shareMPTID, after),
	}
}

func vvIOULineEntry(t *testing.T, account, issuer [20]byte, before, after string) InvariantEntry {
	t.Helper()
	low, high := account, issuer
	if state.CompareAccountIDs(low, high) > 0 {
		low, high = high, low
	}
	lowAddr, err := state.EncodeAccountID(low)
	if err != nil {
		t.Fatalf("EncodeAccountID(low): %v", err)
	}
	highAddr, err := state.EncodeAccountID(high)
	if err != nil {
		t.Fatalf("EncodeAccountID(high): %v", err)
	}
	encode := func(value string) []byte {
		balance, err := state.NewIssuedAmountFromDecimalString(value, "USD", state.AccountOneAddress)
		if err != nil {
			t.Fatalf("NewIssuedAmountFromDecimalString(%q): %v", value, err)
		}
		if account == high {
			balance = balance.Negate()
		}
		lowLimit, err := state.NewIssuedAmountFromDecimalString("100000000000000000", "USD", lowAddr)
		if err != nil {
			t.Fatalf("low limit: %v", err)
		}
		highLimit, err := state.NewIssuedAmountFromDecimalString("100000000000000000", "USD", highAddr)
		if err != nil {
			t.Fatalf("high limit: %v", err)
		}
		b, err := state.SerializeRippleState(&state.RippleState{
			Balance: balance, LowLimit: lowLimit, HighLimit: highLimit,
		})
		if err != nil {
			t.Fatalf("SerializeRippleState: %v", err)
		}
		return b
	}
	return InvariantEntry{
		Key:       keylet.Line(account, issuer, "USD").Key,
		EntryType: entry.TypeRippleState,
		Before:    encode(before),
		After:     encode(after),
	}
}

func savRules() *amendment.Rules {
	return amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault, amendment.FeatureFixCleanup3_1_3})
}

func savFixedRules() *amendment.Rules {
	return amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureFixCleanup3_1_3,
		amendment.FeatureFixCleanup3_2_0,
	})
}

func vvInvariantResult(violation *InvariantViolation) ter.Result {
	if violation != nil {
		return ter.TecINVARIANT_FAILED
	}
	return ter.TesSUCCESS
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
	issuanceEntry := InvariantEntry{EntryType: entry.TypeMPTokenIssuance, Before: issuance, After: issuance}

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
				{EntryType: entry.TypeVault,
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
				{EntryType: entry.TypeVault,
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
				{EntryType: entry.TypeVault,
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
				{EntryType: entry.TypeVault,
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
				{EntryType: entry.TypeVault,
					Before: vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", ""),
					After:  vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", "")},
			},
			wantMsg: "vault updated by a wrong transaction type",
		},
		{
			name: "deleted vault still holds assets",
			tx:   vvTx{txType: protocol.TxTypeVaultDelete, acct: ownerAddr},
			entries: []InvariantEntry{
				{EntryType: entry.TypeVault, IsDelete: true,
					Before: vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", "")},
				{EntryType: entry.TypeMPTokenIssuance, IsDelete: true, Before: deletedIssuance},
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
		{EntryType: entry.TypeVault,
			Before: vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "1000", "", ""),
			After:  vvCraftVault(t, ownerAddr, pseudoAddr, shareMPTID, "1000", "2000", "", "")},
		{EntryType: entry.TypeMPTokenIssuance, Before: issuance, After: issuance},
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

// TestVaultMinScale_AmendmentGate pins the fixCleanup3_2_0 scale selection:
// post-amendment reconciliation uses the posterior AssetsTotal scale while the
// legacy path uses the coarsest modified-value scale.
func TestVaultMinScale_AmendmentGate(t *testing.T) {
	fixOn := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault, amendment.FeatureFixCleanup3_2_0})
	fixOff := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})

	iou := vvAsset{currency: "USD", issuer: [20]byte{1}}
	afterIOU := vvVault{asset: iou, assetsTotal: state.NewXRPLNumber(1000000, 0)}
	iouDelta := iou.makeDelta(state.NewXRPLNumber(0, 0), state.NewXRPLNumber(25, -1))
	if want := iou.scaleOf(state.NewXRPLNumber(25, -1)); iouDelta.scale != want {
		t.Fatalf("zero-to-nonzero IOU delta scale = %d, want %d", iouDelta.scale, want)
	}

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

func TestValidVault_IOUAssetReconciliation(t *testing.T) {
	owner, ownerAddr := vvTestAccount(t, 0x11)
	pseudo, pseudoAddr := vvTestAccount(t, 0x22)
	issuer, issuerAddr := vvTestAccount(t, 0x33)
	shareMPTID := keylet.MakeMPTID(1, pseudo)
	vault := func(total, available string) []byte {
		return vvCraftIOUVault(t, ownerAddr, pseudoAddr, issuerAddr, shareMPTID, total, available)
	}
	issuance := func(before, after uint64) InvariantEntry {
		return vvIssuanceEntry(t, pseudo, shareMPTID, before, after)
	}
	holderShares := func(before, after uint64) InvariantEntry {
		return vvMPTokenEntry(t, owner, shareMPTID, before, after)
	}
	amount := func(value string) map[string]any {
		return map[string]any{"currency": "USD", "issuer": issuerAddr, "value": value}
	}

	tests := []struct {
		name    string
		tx      vvTx
		entries []InvariantEntry
		wantMsg string
	}{
		{
			name: "set rejects canonical vault line movement",
			tx:   vvTx{txType: protocol.TxTypeVaultSet, acct: ownerAddr},
			entries: []InvariantEntry{
				{EntryType: entry.TypeVault, Before: vault("1000", "1000"), After: vault("1000", "1000")},
				issuance(1000, 1000),
				vvIOULineEntry(t, pseudo, issuer, "1000", "1001"),
			},
			wantMsg: "set must not change vault balance",
		},
		{
			name: "deposit reconciles canonical issuer lines",
			tx: vvTx{txType: protocol.TxTypeVaultDeposit, acct: ownerAddr, flat: map[string]any{
				"Amount": amount("5"),
			}},
			entries: []InvariantEntry{
				{EntryType: entry.TypeVault, Before: vault("1000", "1000"), After: vault("1005", "1005")},
				issuance(1000, 1005),
				holderShares(1000, 1005),
				vvIOULineEntry(t, pseudo, issuer, "1000", "1005"),
				vvIOULineEntry(t, owner, issuer, "100", "95"),
			},
		},
		{
			name: "deposit rejects unequal canonical issuer line movements",
			tx: vvTx{txType: protocol.TxTypeVaultDeposit, acct: ownerAddr, flat: map[string]any{
				"Amount": amount("5"),
			}},
			entries: []InvariantEntry{
				{EntryType: entry.TypeVault, Before: vault("1000", "1000"), After: vault("1005", "1005")},
				issuance(1000, 1005),
				holderShares(1000, 1005),
				vvIOULineEntry(t, pseudo, issuer, "1000", "1004"),
				vvIOULineEntry(t, owner, issuer, "100", "95"),
			},
			wantMsg: "deposit must change vault and depositor balance by equal amount",
		},
		{
			name: "withdraw reconciles canonical vault line",
			tx: vvTx{txType: protocol.TxTypeVaultWithdraw, acct: ownerAddr, flat: map[string]any{
				"Destination": issuerAddr,
			}},
			entries: []InvariantEntry{
				{EntryType: entry.TypeVault, Before: vault("1000", "1000"), After: vault("995", "995")},
				issuance(1000, 995),
				holderShares(1000, 995),
				vvIOULineEntry(t, pseudo, issuer, "1000", "995"),
			},
		},
		{
			name: "withdraw rejects vault total mismatch",
			tx: vvTx{txType: protocol.TxTypeVaultWithdraw, acct: ownerAddr, flat: map[string]any{
				"Destination": issuerAddr,
			}},
			entries: []InvariantEntry{
				{EntryType: entry.TypeVault, Before: vault("1000", "1000"), After: vault("995", "995")},
				issuance(1000, 995),
				holderShares(1000, 995),
				vvIOULineEntry(t, pseudo, issuer, "1000", "996"),
			},
			wantMsg: "withdrawal and assets outstanding must add up",
		},
		{
			name: "clawback reconciles canonical vault line",
			tx: vvTx{txType: protocol.TxTypeVaultClawback, acct: issuerAddr, flat: map[string]any{
				"Holder": ownerAddr,
			}},
			entries: []InvariantEntry{
				{EntryType: entry.TypeVault, Before: vault("1000", "1000"), After: vault("995", "995")},
				issuance(1000, 995),
				holderShares(1000, 995),
				vvIOULineEntry(t, pseudo, issuer, "1000", "995"),
			},
		},
		{
			name: "clawback rejects vault total mismatch",
			tx: vvTx{txType: protocol.TxTypeVaultClawback, acct: issuerAddr, flat: map[string]any{
				"Holder": ownerAddr,
			}},
			entries: []InvariantEntry{
				{EntryType: entry.TypeVault, Before: vault("1000", "1000"), After: vault("995", "995")},
				issuance(1000, 995),
				holderShares(1000, 995),
				vvIOULineEntry(t, pseudo, issuer, "1000", "996"),
			},
			wantMsg: "clawback and assets outstanding must add up",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			violation := checkValidVault(tc.tx, TesSUCCESS, 0, tc.entries, stubView{}, savFixedRules())
			got := ""
			if violation != nil {
				got = violation.Message
			}
			if got != tc.wantMsg {
				t.Fatalf("checkValidVault = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

func TestValidVault_IOUScaleBoundaryAmendment(t *testing.T) {
	owner, ownerAddr := vvTestAccount(t, 0x11)
	pseudo, pseudoAddr := vvTestAccount(t, 0x22)
	issuer, issuerAddr := vvTestAccount(t, 0x33)
	shareMPTID := keylet.MakeMPTID(1, pseudo)
	vault := func(total string) []byte {
		return vvCraftIOUVault(t, ownerAddr, pseudoAddr, issuerAddr, shareMPTID, total, total)
	}
	baseEntries := func() []InvariantEntry {
		return []InvariantEntry{
			{
				EntryType: entry.TypeVault,
				Before:    vault("10000000000000000"),
				After:     vault("9999999999999995"),
			},
			vvIssuanceEntry(t, pseudo, shareMPTID, 1000, 995),
			vvMPTokenEntry(t, owner, shareMPTID, 1000, 995),
			vvIOULineEntry(t, pseudo, issuer, "10000000000000000", "9999999999999995"),
		}
	}
	tests := []struct {
		name       string
		tx         vvTx
		rules      *amendment.Rules
		wantResult ter.Result
	}{
		{
			name: "withdraw pre-fix",
			tx: vvTx{txType: protocol.TxTypeVaultWithdraw, acct: ownerAddr, flat: map[string]any{
				"Destination": issuerAddr,
			}},
			rules: savRules(), wantResult: ter.TecINVARIANT_FAILED,
		},
		{
			name: "withdraw post-fix",
			tx: vvTx{txType: protocol.TxTypeVaultWithdraw, acct: ownerAddr, flat: map[string]any{
				"Destination": issuerAddr,
			}},
			rules: savFixedRules(), wantResult: ter.TesSUCCESS,
		},
		{
			name: "clawback pre-fix",
			tx: vvTx{txType: protocol.TxTypeVaultClawback, acct: issuerAddr, flat: map[string]any{
				"Holder": ownerAddr,
			}},
			rules: savRules(), wantResult: ter.TecINVARIANT_FAILED,
		},
		{
			name: "clawback post-fix",
			tx: vvTx{txType: protocol.TxTypeVaultClawback, acct: issuerAddr, flat: map[string]any{
				"Holder": ownerAddr,
			}},
			rules: savFixedRules(), wantResult: ter.TesSUCCESS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			violation := checkValidVault(tc.tx, TesSUCCESS, 0, baseEntries(), stubView{}, tc.rules)
			message := ""
			if violation != nil {
				message = violation.Message
			}
			result := vvInvariantResult(violation)
			if result != tc.wantResult {
				t.Fatalf("result = %s, want %s (violation %q)", result, tc.wantResult, message)
			}
		})
	}
}

func TestValidVault_IOUWithdrawDestinationRounding(t *testing.T) {
	owner, ownerAddr := vvTestAccount(t, 0x11)
	pseudo, pseudoAddr := vvTestAccount(t, 0x22)
	issuer, issuerAddr := vvTestAccount(t, 0x33)
	destination, destinationAddr := vvTestAccount(t, 0x44)
	shareMPTID := keylet.MakeMPTID(1, pseudo)
	vault := func(total string) []byte {
		return vvCraftIOUVault(t, ownerAddr, pseudoAddr, issuerAddr, shareMPTID, total, total)
	}
	entries := func(after string, shares uint64) []InvariantEntry {
		return []InvariantEntry{
			{EntryType: entry.TypeVault, Before: vault("1000"), After: vault(after)},
			vvIssuanceEntry(t, pseudo, shareMPTID, 1000, shares),
			vvMPTokenEntry(t, owner, shareMPTID, 1000, shares),
			vvIOULineEntry(t, pseudo, issuer, "1000", after),
			vvIOULineEntry(t, destination, issuer, "9999999999999995", "10000000000000000"),
		}
	}
	tx := vvTx{txType: protocol.TxTypeVaultWithdraw, acct: ownerAddr, flat: map[string]any{
		"Destination": destinationAddr,
	}}
	tests := []struct {
		name       string
		rules      *amendment.Rules
		entries    []InvariantEntry
		wantResult ter.Result
		wantMsg    string
	}{
		{
			name:       "pre-fix zero-rounded destination",
			rules:      savRules(),
			entries:    entries("994", 994),
			wantResult: ter.TecINVARIANT_FAILED,
			wantMsg:    "withdrawal must increase destination balance",
		},
		{
			name:       "post-fix sub-ULP destruction",
			rules:      savFixedRules(),
			entries:    entries("994", 994),
			wantResult: ter.TesSUCCESS,
		},
		{
			name:       "post-fix exact-ULP destruction",
			rules:      savFixedRules(),
			entries:    entries("985", 985),
			wantResult: ter.TecINVARIANT_FAILED,
			wantMsg:    "withdrawal must change vault and destination balance by equal amount",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			violation := checkValidVault(tx, TesSUCCESS, 0, tc.entries, stubView{}, tc.rules)
			message := ""
			if violation != nil {
				message = violation.Message
			}
			result := vvInvariantResult(violation)
			if result != tc.wantResult {
				t.Fatalf("result = %s, want %s (violation %q)", result, tc.wantResult, message)
			}
			if message != tc.wantMsg {
				t.Fatalf("violation = %q, want %q", message, tc.wantMsg)
			}
		})
	}
}
