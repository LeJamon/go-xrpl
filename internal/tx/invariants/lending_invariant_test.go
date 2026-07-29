package invariants

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// mapView routes keylet reads to crafted SLE bytes for the ValidLoanBroker
// cross-asset balance check.
type mapView struct {
	stubView
	data map[[32]byte][]byte
}

func (v mapView) Read(k keylet.Keylet) ([]byte, error) { return v.data[k.Key], nil }

// TestValidLoanBroker_CoverAvailableBounds crafts an XRP-vault LoanBroker whose
// CoverAvailable is above / equal / below the pseudo-account's XRP balance, and
// asserts the lower bound (>=, unconditional) and upper bound (==, gated on
// fixCleanup3_1_3). Pseudo-accounts carry no XRP reserve, so the balance is the
// full drops amount.
func TestValidLoanBroker_CoverAvailableBounds(t *testing.T) {
	var ownerID, pseudoID [20]byte
	for i := range ownerID {
		ownerID[i] = 0x11
		pseudoID[i] = 0x22
	}
	ownerAddr, err := state.EncodeAccountID(ownerID)
	if err != nil {
		t.Fatalf("encode owner: %v", err)
	}
	pseudoAddr, err := state.EncodeAccountID(pseudoID)
	if err != nil {
		t.Fatalf("encode pseudo: %v", err)
	}

	var vid [32]byte
	for i := range vid {
		vid[i] = 0x33
	}
	vidHex := strings.ToUpper(hex.EncodeToString(vid[:]))

	const balanceDrops = "5000000000" // pseudo-account holds 5000 XRP of cover

	vaultBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             0,
		"Sequence":          1,
		"OwnerNode":         "0",
		"Owner":             ownerAddr,
		"Account":           pseudoAddr,
		"Asset":             map[string]any{"currency": "XRP"},
		"ShareMPTID":        strings.Repeat("0", 48),
		"WithdrawalPolicy":  1,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	accountBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           pseudoAddr,
		"Balance":           balanceDrops,
		"Flags":             0,
		"OwnerCount":        0,
		"Sequence":          0,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	view := mapView{data: map[[32]byte][]byte{
		keylet.VaultByID(vid).Key:    vaultBytes,
		keylet.Account(pseudoID).Key: accountBytes,
	}}

	broker := func(cover string) []InvariantEntry {
		return []InvariantEntry{{
			EntryType: entry.TypeLoanBroker,
			After: mustEncode(t, map[string]any{
				"LedgerEntryType":   "LoanBroker",
				"Flags":             0,
				"Owner":             ownerAddr,
				"Account":           pseudoAddr,
				"VaultID":           vidHex,
				"CoverAvailable":    cover,
				"Sequence":          1,
				"OwnerNode":         "0",
				"VaultNode":         "0",
				"LoanSequence":      uint32(0),
				"PreviousTxnID":     strings.Repeat("0", 64),
				"PreviousTxnLgrSeq": uint32(0),
			}),
		}}
	}

	fixOn := amendment.NewRules([][32]byte{amendment.FeatureLendingProtocol, amendment.FeatureFixCleanup3_1_3})
	fixOff := amendment.NewRules([][32]byte{amendment.FeatureLendingProtocol})

	cases := []struct {
		name       string
		cover      string
		wantFixOn  bool // expect a violation with fixCleanup3_1_3 enabled
		wantFixOff bool // expect a violation with fixCleanup3_1_3 disabled
	}{
		{"exact match", balanceDrops, false, false},
		{"cover below balance (lower bound)", "4000000000", true, true},
		{"cover above balance (upper bound, gated)", "6000000000", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := broker(tc.cover)
			if got := checkValidLoanBroker(entries, view, fixOn) != nil; got != tc.wantFixOn {
				t.Errorf("fixCleanup3_1_3 ON: violation=%v, want %v", got, tc.wantFixOn)
			}
			if got := checkValidLoanBroker(entries, view, fixOff) != nil; got != tc.wantFixOff {
				t.Errorf("fixCleanup3_1_3 OFF: violation=%v, want %v", got, tc.wantFixOff)
			}
		})
	}
}

func TestValidLoanBroker_MPTCoverMatchesPseudoHolding(t *testing.T) {
	var ownerID, pseudoID [20]byte
	for i := range ownerID {
		ownerID[i] = 0x11
		pseudoID[i] = 0x22
	}
	ownerAddr, err := state.EncodeAccountID(ownerID)
	if err != nil {
		t.Fatalf("encode owner: %v", err)
	}
	pseudoAddr, err := state.EncodeAccountID(pseudoID)
	if err != nil {
		t.Fatalf("encode pseudo: %v", err)
	}

	var vaultID, brokerID [32]byte
	for i := range vaultID {
		vaultID[i] = 0x33
		brokerID[i] = 0x44
	}
	mptID := keylet.MakeMPTID(50, ownerID)
	mptIDHex := strings.ToUpper(hex.EncodeToString(mptID[:]))
	vaultIDHex := strings.ToUpper(hex.EncodeToString(vaultID[:]))
	brokerIDHex := strings.ToUpper(hex.EncodeToString(brokerID[:]))
	vaultBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             0,
		"Sequence":          1,
		"OwnerNode":         "0",
		"Owner":             ownerAddr,
		"Account":           pseudoAddr,
		"Asset":             map[string]any{"mpt_issuance_id": mptIDHex},
		"ShareMPTID":        strings.Repeat("0", 48),
		"WithdrawalPolicy":  1,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	accountBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           pseudoAddr,
		"Balance":           "0",
		"Flags":             0,
		"OwnerCount":        0,
		"Sequence":          0,
		"LoanBrokerID":      brokerIDHex,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	tokenBytes, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           pseudoID,
		MPTokenIssuanceID: mptID,
		MPTAmount:         110,
	})
	if err != nil {
		t.Fatalf("serialize MPToken: %v", err)
	}
	brokerBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "LoanBroker",
		"Flags":             0,
		"Owner":             ownerAddr,
		"Account":           pseudoAddr,
		"VaultID":           vaultIDHex,
		"CoverAvailable":    "110",
		"Sequence":          1,
		"OwnerNode":         "0",
		"VaultNode":         "0",
		"LoanSequence":      uint32(0),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	view := mapView{data: map[[32]byte][]byte{
		keylet.VaultByID(vaultID).Key:           vaultBytes,
		keylet.LoanBrokerByID(brokerID).Key:     brokerBytes,
		keylet.Account(pseudoID).Key:            accountBytes,
		keylet.MPTokenByID(mptID, pseudoID).Key: tokenBytes,
	}}
	rules := amendment.NewRules([][32]byte{
		amendment.FeatureLendingProtocol,
		amendment.FeatureFixCleanup3_1_3,
	})
	entries := []InvariantEntry{{Key: brokerID, EntryType: entry.TypeLoanBroker, After: brokerBytes}}

	if violation := checkValidLoanBroker(entries, view, rules); violation != nil {
		t.Fatalf("matching MPT cover rejected: %v", violation)
	}

	modifiedToken := &state.MPTokenData{
		Account:           pseudoID,
		MPTokenIssuanceID: mptID,
		MPTAmount:         120,
	}
	modifiedTokenBytes, err := state.SerializeMPToken(modifiedToken)
	if err != nil {
		t.Fatalf("serialize modified MPToken: %v", err)
	}
	view.data[keylet.MPTokenByID(mptID, pseudoID).Key] = modifiedTokenBytes

	for _, tc := range []struct {
		name  string
		entry InvariantEntry
	}{
		{
			name: "MPToken",
			entry: InvariantEntry{
				Key:       keylet.MPTokenByID(mptID, pseudoID).Key,
				EntryType: entry.TypeMPToken,
				Before:    tokenBytes,
				After:     modifiedTokenBytes,
			},
		},
		{
			name: "AccountRoot",
			entry: InvariantEntry{
				Key:       keylet.Account(pseudoID).Key,
				EntryType: entry.TypeAccountRoot,
				After:     accountBytes,
			},
		},
	} {
		t.Run("discovers broker from "+tc.name, func(t *testing.T) {
			violation := checkValidLoanBroker([]InvariantEntry{tc.entry}, view, rules)
			if violation == nil || !strings.Contains(violation.Message, "less than") {
				t.Fatalf("holding-only change violation = %v, want cover below holding", violation)
			}
		})
	}
}

func TestValidLoanBroker_DiscoversChangedRippleState(t *testing.T) {
	var ownerID, pseudoID, issuerID [20]byte
	for i := range ownerID {
		ownerID[i] = 0x11
		pseudoID[i] = 0x22
		issuerID[i] = 0x55
	}
	ownerAddr, err := state.EncodeAccountID(ownerID)
	if err != nil {
		t.Fatalf("encode owner: %v", err)
	}
	pseudoAddr, err := state.EncodeAccountID(pseudoID)
	if err != nil {
		t.Fatalf("encode pseudo: %v", err)
	}
	issuerAddr, err := state.EncodeAccountID(issuerID)
	if err != nil {
		t.Fatalf("encode issuer: %v", err)
	}

	var vaultID, brokerID [32]byte
	for i := range vaultID {
		vaultID[i] = 0x33
		brokerID[i] = 0x44
	}
	vaultIDHex := strings.ToUpper(hex.EncodeToString(vaultID[:]))

	vaultBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             0,
		"Sequence":          1,
		"OwnerNode":         "0",
		"Owner":             ownerAddr,
		"Account":           pseudoAddr,
		"Asset":             map[string]any{"currency": "USD", "issuer": issuerAddr},
		"ShareMPTID":        strings.Repeat("0", 48),
		"WithdrawalPolicy":  1,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	brokerBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "LoanBroker",
		"Flags":             0,
		"Owner":             ownerAddr,
		"Account":           pseudoAddr,
		"VaultID":           vaultIDHex,
		"CoverAvailable":    "100",
		"Sequence":          1,
		"OwnerNode":         "0",
		"VaultNode":         "0",
		"LoanSequence":      uint32(0),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	pseudoBytes := mustSerializeAccount(t, &state.AccountRoot{
		Account: pseudoAddr, LoanBrokerID: brokerID,
	})
	balance, err := state.NewIssuedAmountFromDecimalString("110", "USD", state.AccountOneAddress)
	if err != nil {
		t.Fatalf("make balance: %v", err)
	}
	lowLimit, err := state.NewIssuedAmountFromDecimalString("0", "USD", pseudoAddr)
	if err != nil {
		t.Fatalf("make low limit: %v", err)
	}
	highLimit, err := state.NewIssuedAmountFromDecimalString("0", "USD", issuerAddr)
	if err != nil {
		t.Fatalf("make high limit: %v", err)
	}
	lineBytes, err := state.SerializeRippleState(&state.RippleState{
		Balance: balance, LowLimit: lowLimit, HighLimit: highLimit,
	})
	if err != nil {
		t.Fatalf("serialize RippleState: %v", err)
	}

	lineKey := keylet.Line(pseudoID, issuerID, "USD")
	view := mapView{data: map[[32]byte][]byte{
		keylet.VaultByID(vaultID).Key:       vaultBytes,
		keylet.LoanBrokerByID(brokerID).Key: brokerBytes,
		keylet.Account(pseudoID).Key:        pseudoBytes,
		lineKey.Key:                         lineBytes,
	}}
	rules := amendment.NewRules([][32]byte{
		amendment.FeatureLendingProtocol,
		amendment.FeatureFixCleanup3_1_3,
	})
	entry := InvariantEntry{Key: lineKey.Key, EntryType: entry.TypeRippleState, After: lineBytes}

	violation := checkValidLoanBroker([]InvariantEntry{entry}, view, rules)
	if violation == nil || !strings.Contains(violation.Message, "less than") {
		t.Fatalf("trust-line-only change violation = %v, want cover below holding", violation)
	}
}

// TestValidLoanBroker_InertWhenLendingDisabled asserts the invariant does not run
// (and cannot false-positive) while LendingProtocol is off.
func TestValidLoanBroker_InertWhenLendingDisabled(t *testing.T) {
	entries := []InvariantEntry{{EntryType: entry.TypeLoanBroker, After: []byte{0x01}}}
	if v := checkValidLoanBroker(entries, stubView{}, amendment.EmptyRules()); v != nil {
		t.Fatalf("expected inert check with LendingProtocol off, got %v", v)
	}
}
