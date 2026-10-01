package invariants

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type pseudoObjectView struct {
	pseudoAccount [20]byte
	exists        bool
}

func (v pseudoObjectView) Read(keylet.Keylet) ([]byte, error) { return nil, nil }
func (v pseudoObjectView) Exists(k keylet.Keylet) (bool, error) {
	return v.exists && k.Key == keylet.Account(v.pseudoAccount).Key, nil
}
func (v pseudoObjectView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v pseudoObjectView) LedgerSeq() uint32 { return 1 }

func pseudoObjectSLE(t *testing.T, objectType string) []byte {
	t.Helper()
	fields := map[string]any{
		"LedgerEntryType": objectType,
		"Account":         testPseudoAddr,
	}
	if objectType == "AMM" {
		return ammSLE(t, testPseudoAddr, "0")
	}
	if objectType == "Vault" {
		fields["Sequence"] = uint32(1)
		fields["OwnerNode"] = "0"
		fields["Owner"] = testPseudoAddr
		fields["Asset"] = map[string]any{"currency": "XRP"}
		fields["ShareMPTID"] = strings.Repeat("0", 48)
		fields["WithdrawalPolicy"] = uint8(1)
		fields["Flags"] = uint32(0)
		fields["PreviousTxnID"] = strings.Repeat("0", 64)
		fields["PreviousTxnLgrSeq"] = uint32(0)
	}
	if objectType == "LoanBroker" {
		fields["Sequence"] = uint32(1)
		fields["OwnerNode"] = "0"
		fields["VaultNode"] = "0"
		fields["VaultID"] = strings.Repeat("0", 64)
		fields["Owner"] = testPseudoAddr
		fields["LoanSequence"] = uint32(1)
		fields["Flags"] = uint32(0)
		fields["PreviousTxnID"] = strings.Repeat("0", 64)
		fields["PreviousTxnLgrSeq"] = uint32(0)
	}
	return mustEncode(t, fields)
}

func TestObjectsHavePseudoAccounts(t *testing.T) {
	rules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_3_0})
	pseudoID, err := decodeTestAccount(testPseudoAddr)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		entryType entry.Type
		json      string
	}{
		{name: "AMM", entryType: entry.TypeAMM, json: "AMM"},
		{name: "Vault", entryType: entry.TypeVault, json: "Vault"},
		{name: "LoanBroker", entryType: entry.TypeLoanBroker, json: "LoanBroker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleted := []InvariantEntry{{
				EntryType: tc.entryType,
				Before:    pseudoObjectSLE(t, tc.json),
				IsDelete:  true,
			}}
			view := pseudoObjectView{pseudoAccount: pseudoID}
			if v := checkObjectsHavePseudoAccounts(deleted, view, rules); v != nil {
				t.Fatalf("deleted object with removed pseudo-account: unexpected violation %v", v)
			}
			view.exists = true
			if v := checkObjectsHavePseudoAccounts(deleted, view, rules); v == nil {
				t.Fatal("deleted object with surviving pseudo-account: expected violation")
			}
			if v := checkObjectsHavePseudoAccounts(deleted, view, amendment.EmptyRules()); v != nil {
				t.Fatalf("cleanup disabled: unexpected violation %v", v)
			}
		})
	}
}

func decodeTestAccount(address string) ([20]byte, error) {
	return state.DecodeAccountID(address)
}
