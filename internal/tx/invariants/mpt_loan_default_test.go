package invariants

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type loanDefaultMPTTx struct {
	loanID [32]byte
	flags  uint32
}

func (t loanDefaultMPTTx) TxType() TxType         { return txcore.TypeLoanManage }
func (t loanDefaultMPTTx) TxAccount() string      { return addrHolderA }
func (t loanDefaultMPTTx) TxHasField(string) bool { return false }
func (t loanDefaultMPTTx) Flatten() (map[string]any, error) {
	return map[string]any{
		"Flags":  t.flags,
		"LoanID": strings.ToUpper(hex.EncodeToString(t.loanID[:])),
	}, nil
}

func mptDefaultRules(fix340, mptV2 bool) *amendment.Rules {
	features := [][32]byte{amendment.FeatureLendingProtocol}
	if fix340 {
		features = append(features, amendment.FeatureFixCleanup3_4_0)
	}
	if mptV2 {
		features = append(features, amendment.FeatureMPTokensV2)
	}
	return amendment.NewRules(features)
}

func mptDefaultAccount(t *testing.T, address string) [20]byte {
	t.Helper()
	account, err := state.DecodeAccountID(address)
	if err != nil {
		t.Fatalf("decode account %q: %v", address, err)
	}
	return account
}

func mptDefaultID(t *testing.T, sequence byte) [24]byte {
	t.Helper()
	issuer := mptDefaultAccount(t, addrIssuer)
	return keylet.MakeMPTID(uint32(sequence), issuer)
}

func mptDefaultBytes(value byte) (result [32]byte) {
	for i := range result {
		result[i] = value
	}
	return result
}

func mptDefaultFixture(
	t *testing.T,
	assetID [24]byte,
	issuanceFlags uint32,
	sender, receiver [20]byte,
	tokenFlags uint32,
) (loanDefaultMPTTx, mapView, []InvariantEntry) {
	t.Helper()
	owner := mptDefaultAccount(t, addrIssuer)
	broker := mptDefaultAccount(t, addrHolderA)
	vault := mptDefaultAccount(t, addrHolderB)
	loanID := mptDefaultBytes(0x11)
	brokerID := mptDefaultBytes(0x22)
	vaultID := mptDefaultBytes(0x33)
	vaultAssetID := mptDefaultID(t, 50)
	brokerIDHex := strings.ToUpper(hex.EncodeToString(brokerID[:]))
	vaultIDHex := strings.ToUpper(hex.EncodeToString(vaultID[:]))
	vaultAssetIDHex := strings.ToUpper(hex.EncodeToString(vaultAssetID[:]))

	vaultBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             uint32(0),
		"Sequence":          uint32(1),
		"OwnerNode":         "0",
		"Owner":             addrIssuer,
		"Account":           addrHolderB,
		"Asset":             map[string]any{"mpt_issuance_id": vaultAssetIDHex},
		"ShareMPTID":        strings.Repeat("0", 48),
		"WithdrawalPolicy":  uint8(1),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	brokerBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "LoanBroker",
		"Flags":             uint32(0),
		"Owner":             addrIssuer,
		"Account":           addrHolderA,
		"VaultID":           vaultIDHex,
		"CoverAvailable":    "100",
		"Sequence":          uint32(1),
		"OwnerNode":         "0",
		"VaultNode":         "0",
		"LoanSequence":      uint32(1),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	loanBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "Loan",
		"Flags":             uint32(0),
		"OwnerNode":         "0",
		"LoanBrokerNode":    "0",
		"LoanBrokerID":      brokerIDHex,
		"LoanSequence":      uint32(1),
		"Borrower":          addrHolderB,
		"StartDate":         uint32(1),
		"PaymentInterval":   uint32(100),
		"PeriodicPayment":   "1",
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	issuanceBytes, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:            owner,
		Sequence:          50,
		OutstandingAmount: 100,
		Flags:             issuanceFlags,
	})
	if err != nil {
		t.Fatalf("serialize issuance: %v", err)
	}
	brokerAccountBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           addrHolderA,
		"Balance":           "0",
		"Flags":             uint32(0),
		"OwnerCount":        uint32(0),
		"Sequence":          uint32(0),
		"LoanBrokerID":      brokerIDHex,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	vaultAccountBytes := mustEncode(t, map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           addrHolderB,
		"Balance":           "0",
		"Flags":             uint32(0),
		"OwnerCount":        uint32(0),
		"Sequence":          uint32(0),
		"VaultID":           vaultIDHex,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})

	beforeSender, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           sender,
		MPTokenIssuanceID: assetID,
		MPTAmount:         100,
		Flags:             tokenFlags,
	})
	if err != nil {
		t.Fatalf("serialize sender before: %v", err)
	}
	afterSender, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           sender,
		MPTokenIssuanceID: assetID,
		MPTAmount:         90,
		Flags:             tokenFlags,
	})
	if err != nil {
		t.Fatalf("serialize sender after: %v", err)
	}
	beforeReceiver, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           receiver,
		MPTokenIssuanceID: assetID,
		Flags:             tokenFlags,
	})
	if err != nil {
		t.Fatalf("serialize receiver before: %v", err)
	}
	afterReceiver, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           receiver,
		MPTokenIssuanceID: assetID,
		MPTAmount:         10,
		Flags:             tokenFlags,
	})
	if err != nil {
		t.Fatalf("serialize receiver after: %v", err)
	}

	view := mapView{data: map[[32]byte][]byte{
		keylet.LoanByID(loanID).Key:               loanBytes,
		keylet.LoanBrokerByID(brokerID).Key:       brokerBytes,
		keylet.VaultByID(vaultID).Key:             vaultBytes,
		keylet.MPTIssuance(assetID).Key:           issuanceBytes,
		keylet.MPTokenByID(assetID, sender).Key:   afterSender,
		keylet.MPTokenByID(assetID, receiver).Key: afterReceiver,
		keylet.Account(broker).Key:                brokerAccountBytes,
		keylet.Account(vault).Key:                 vaultAccountBytes,
	}}
	entries := []InvariantEntry{
		{
			Key:       keylet.MPTokenByID(assetID, sender).Key,
			EntryType: entry.TypeMPToken,
			Before:    beforeSender,
			After:     afterSender,
		},
		{
			Key:       keylet.MPTokenByID(assetID, receiver).Key,
			EntryType: entry.TypeMPToken,
			Before:    beforeReceiver,
			After:     afterReceiver,
		},
	}
	return loanDefaultMPTTx{loanID: loanID, flags: lending.TfLoanDefault}, view, entries
}

func TestLoanDefaultMPTFreezeGateAndExemption(t *testing.T) {
	assetID := mptDefaultID(t, 50)
	sender := mptDefaultAccount(t, addrHolderA)
	receiver := mptDefaultAccount(t, addrHolderB)

	tests := []struct {
		name       string
		rules      *amendment.Rules
		flags      uint32
		tokenFlags uint32
		want       bool
	}{
		{
			name:  "legacy log only",
			rules: mptDefaultRules(false, false),
			flags: entry.LsfMPTLocked | entry.LsfMPTCanTransfer,
		},
		{
			name:  "MPTokensV2 enforces without cleanup",
			rules: mptDefaultRules(false, true),
			flags: entry.LsfMPTLocked | entry.LsfMPTCanTransfer,
			want:  true,
		},
		{
			name:  "cleanup exempts own broker and vault",
			rules: mptDefaultRules(true, false),
			flags: entry.LsfMPTLocked | entry.LsfMPTCanTransfer,
		},
		{
			name:  "both gates exempt own broker and vault",
			rules: mptDefaultRules(true, true),
			flags: entry.LsfMPTLocked | entry.LsfMPTCanTransfer,
		},
		{
			name:       "individual locks are exempt for own broker and vault",
			rules:      mptDefaultRules(true, false),
			flags:      entry.LsfMPTCanTransfer,
			tokenFlags: entry.LsfMPTLocked,
		},
		{
			name:  "cleanup preserves pseudo authorization",
			rules: mptDefaultRules(true, false),
			flags: entry.LsfMPTCanTransfer | entry.LsfMPTRequireAuth,
		},
		{
			name:  "cleanup does not waive CanTransfer",
			rules: mptDefaultRules(true, false),
			flags: entry.LsfMPTLocked,
			want:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx, view, entries := mptDefaultFixture(t, assetID, test.flags, sender, receiver, test.tokenFlags)
			got := checkLoanDefaultMPTTransfer(tx, TesSUCCESS, entries, view, test.rules) != nil
			if got != test.want {
				t.Fatalf("violation = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCheckInvariantsRoutesLoanDefaultMPTTransfer(t *testing.T) {
	assetID := mptDefaultID(t, 50)
	sender := mptDefaultAccount(t, addrHolderA)
	receiver := mptDefaultAccount(t, addrHolderB)
	tx, view, entries := mptDefaultFixture(
		t,
		assetID,
		entry.LsfMPTLocked|entry.LsfMPTCanTransfer,
		sender,
		receiver,
		0,
	)
	violation := CheckInvariants(tx, TesSUCCESS, 0, 0, entries, view, mptDefaultRules(false, true))
	if violation == nil || violation.Name != "ValidMPTTransfer" {
		t.Fatalf("CheckInvariants violation = %v, want ValidMPTTransfer", violation)
	}
}

func TestLoanDefaultMPTFreezeExemptionScope(t *testing.T) {
	ownID := mptDefaultID(t, 50)
	otherID := mptDefaultID(t, 51)
	broker := mptDefaultAccount(t, addrHolderA)
	vault := mptDefaultAccount(t, addrHolderB)
	otherHolder := mptDefaultAccount(t, addrIssuer)
	rules := mptDefaultRules(true, false)

	tests := []struct {
		name     string
		assetID  [24]byte
		sender   [20]byte
		receiver [20]byte
		want     bool
	}{
		{name: "unrelated issuance", assetID: otherID, sender: broker, receiver: vault, want: true},
		{name: "unrelated holder", assetID: ownID, sender: otherHolder, receiver: vault, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx, view, entries := mptDefaultFixture(
				t,
				test.assetID,
				entry.LsfMPTLocked|entry.LsfMPTCanTransfer,
				test.sender,
				test.receiver,
				0,
			)
			got := checkLoanDefaultMPTTransfer(tx, TesSUCCESS, entries, view, rules) != nil
			if got != test.want {
				t.Fatalf("violation = %v, want %v", got, test.want)
			}
		})
	}
}

func TestLoanDefaultMPTAuthorizationScope(t *testing.T) {
	assetID := mptDefaultID(t, 50)
	var ordinary [20]byte
	for i := range ordinary {
		ordinary[i] = 0x44
	}
	receiver := mptDefaultAccount(t, addrHolderB)
	tx, view, entries := mptDefaultFixture(
		t,
		assetID,
		entry.LsfMPTCanTransfer|entry.LsfMPTRequireAuth,
		ordinary,
		receiver,
		0,
	)
	if got := checkLoanDefaultMPTTransfer(tx, TesSUCCESS, entries, view, mptDefaultRules(true, false)); got == nil {
		t.Fatal("cleanup freeze exemption must not bypass ordinary-holder MPT authorization")
	}
}

func TestLoanDefaultMPTFailedChangeCleanupGate(t *testing.T) {
	assetID := mptDefaultID(t, 50)
	sender := mptDefaultAccount(t, addrHolderA)
	receiver := mptDefaultAccount(t, addrHolderB)
	tx, view, entries := mptDefaultFixture(
		t,
		assetID,
		entry.LsfMPTCanTransfer,
		sender,
		receiver,
		0,
	)

	if got := checkLoanDefaultMPTTransfer(tx, TecINCOMPLETE, entries, view, mptDefaultRules(true, false)); got == nil {
		t.Fatal("cleanup must reject MPT balance changes on a failed default")
	}
	if got := checkLoanDefaultMPTTransfer(tx, TecINCOMPLETE, entries, view, mptDefaultRules(false, true)); got != nil {
		t.Fatalf("MPTokensV2 without cleanup must retain legacy behavior: %v", got)
	}

	deleted := append([]InvariantEntry(nil), entries...)
	deleted[0].After = nil
	deleted[0].IsDelete = true
	if got := checkLoanDefaultMPTTransfer(tx, TecINCOMPLETE, deleted, view, mptDefaultRules(true, false)); got == nil {
		t.Fatal("cleanup must reject MPToken deletion on a failed default")
	}
	if got := checkLoanDefaultMPTTransfer(tx, TesSUCCESS, deleted, view, mptDefaultRules(true, false)); got != nil {
		t.Fatalf("successful MPToken deletion should pass: %v", got)
	}
	if got := checkLoanDefaultMPTTransfer(tx, TecINCOMPLETE, deleted, view, mptDefaultRules(false, true)); got != nil {
		t.Fatalf("MPTokensV2 without cleanup must retain legacy deletion behavior: %v", got)
	}

	unchanged := append([]InvariantEntry(nil), entries...)
	unchanged[0].After = unchanged[0].Before
	unchanged[1].After = unchanged[1].Before
	if got := checkLoanDefaultMPTTransfer(tx, TecINCOMPLETE, unchanged, view, mptDefaultRules(true, false)); got != nil {
		t.Fatalf("unchanged MPToken balances should pass on a failed default: %v", got)
	}
}

func TestLoanDefaultMPTOrphanHandling(t *testing.T) {
	assetID := mptDefaultID(t, 50)
	sender := mptDefaultAccount(t, addrHolderA)
	receiver := mptDefaultAccount(t, addrHolderB)
	rules := mptDefaultRules(true, false)

	newOrphan := func(t *testing.T) (loanDefaultMPTTx, mapView, []InvariantEntry) {
		t.Helper()
		tx, view, entries := mptDefaultFixture(
			t,
			assetID,
			entry.LsfMPTCanTransfer,
			sender,
			receiver,
			0,
		)
		delete(view.data, keylet.MPTIssuance(assetID).Key)
		return tx, view, entries
	}

	t.Run("successful deletion passes", func(t *testing.T) {
		tx, view, entries := newOrphan(t)
		entries = entries[:1]
		entries[0].After = nil
		entries[0].IsDelete = true
		if got := checkLoanDefaultMPTTransfer(tx, TesSUCCESS, entries, view, rules); got != nil {
			t.Fatalf("successful orphan MPToken deletion should pass: %v", got)
		}
	})

	t.Run("unchanged balance passes", func(t *testing.T) {
		tx, view, entries := newOrphan(t)
		entries[0].After = entries[0].Before
		entries[1].After = entries[1].Before
		if got := checkLoanDefaultMPTTransfer(tx, TecINCOMPLETE, entries, view, rules); got != nil {
			t.Fatalf("unchanged orphan MPToken balances should pass: %v", got)
		}
	})

	t.Run("balance change fails", func(t *testing.T) {
		tx, view, entries := newOrphan(t)
		if got := checkLoanDefaultMPTTransfer(tx, TesSUCCESS, entries, view, rules); got == nil {
			t.Fatal("orphan MPToken balance change should fail")
		}
	})

	t.Run("failed deletion fails with cleanup", func(t *testing.T) {
		tx, view, entries := newOrphan(t)
		entries = entries[:1]
		entries[0].After = nil
		entries[0].IsDelete = true
		if got := checkLoanDefaultMPTTransfer(tx, TecINCOMPLETE, entries, view, rules); got == nil {
			t.Fatal("cleanup must reject orphan MPToken deletion on a failed default")
		}
	})
}
