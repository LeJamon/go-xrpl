package invariants

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

const tfLoanDefault uint32 = 0x00010000

func TestLoanDefaultFreezeExemptionIsScoped(t *testing.T) {
	for _, tc := range []struct {
		name        string
		lineFlags   uint32
		issuerFlags uint32
	}{
		{"individual freeze", state.LsfHighFreeze, 0},
		{"deep freeze", state.LsfHighDeepFreeze, 0},
		{"global freeze", 0, state.LsfGlobalFreeze},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newLoanFreezeFixture(t, "USD", tc.lineFlags, tc.issuerFlags)
			rules := amendment.NewRules([][32]byte{amendment.FeatureDeepFreeze, amendment.FeatureFixCleanup3_4_0})
			defaultTx := vvTx{txType: protocol.TxTypeLoanManage, flat: map[string]any{
				"Flags":  lending.TfLoanDefault,
				"LoanID": fixture.loanID,
			}}
			if violation := checkTransfersNotFrozen(defaultTx, fixture.entries, fixture.view, rules); violation != nil {
				t.Fatalf("default transfer rejected under %s: %v", tc.name, violation)
			}

			legacyRules := amendment.NewRules([][32]byte{amendment.FeatureDeepFreeze})
			if violation := checkTransfersNotFrozen(defaultTx, fixture.entries, fixture.view, legacyRules); violation == nil {
				t.Fatalf("legacy default transfer unexpectedly bypassed %s", tc.name)
			}
			ordinaryTx := vvTx{txType: protocol.TxTypeLoanPay, flat: map[string]any{}}
			if violation := checkTransfersNotFrozen(ordinaryTx, fixture.entries, fixture.view, rules); violation == nil {
				t.Fatalf("ordinary transfer unexpectedly bypassed %s", tc.name)
			}
		})
	}

	t.Run("unrelated asset remains frozen", func(t *testing.T) {
		fixture := newLoanFreezeFixtureWithLine(t, "EUR", "USD", state.LsfHighFreeze, 0)
		rules := amendment.NewRules([][32]byte{amendment.FeatureDeepFreeze, amendment.FeatureFixCleanup3_4_0})
		defaultTx := vvTx{txType: protocol.TxTypeLoanManage, flat: map[string]any{
			"Flags":  lending.TfLoanDefault,
			"LoanID": fixture.loanID,
		}}
		if violation := checkTransfersNotFrozen(defaultTx, fixture.entries, fixture.view, rules); violation == nil {
			t.Fatal("default transfer for an unrelated asset unexpectedly bypassed freeze")
		}
	})
}

type loanFreezeFixture struct {
	view    mapView
	entries []InvariantEntry
	loanID  string
}

func newLoanFreezeFixture(t *testing.T, assetCurrency string, lineFlags, issuerFlags uint32) loanFreezeFixture {
	return newLoanFreezeFixtureWithLine(t, assetCurrency, assetCurrency, lineFlags, issuerFlags)
}

func newLoanFreezeFixtureWithLine(t *testing.T, assetCurrency, lineCurrency string, lineFlags, issuerFlags uint32) loanFreezeFixture {
	t.Helper()
	var issuer, broker, vault [20]byte
	for i := range issuer {
		issuer[i] = 0xf0
		broker[i] = 0x10
		vault[i] = 0x20
	}
	issuerAddr, _ := state.EncodeAccountID(issuer)
	brokerAddr, _ := state.EncodeAccountID(broker)
	vaultAddr, _ := state.EncodeAccountID(vault)
	var loanID, brokerID, vaultID [32]byte
	for i := range loanID {
		loanID[i] = 0x11
		brokerID[i] = 0x22
		vaultID[i] = 0x33
	}
	loanIDHex := strings.ToUpper(hex.EncodeToString(loanID[:]))
	brokerIDHex := strings.ToUpper(hex.EncodeToString(brokerID[:]))
	vaultIDHex := strings.ToUpper(hex.EncodeToString(vaultID[:]))

	loan := mustEncode(t, map[string]any{
		"LedgerEntryType": "Loan", "Flags": uint32(0), "OwnerNode": "0", "LoanBrokerNode": "0",
		"LoanBrokerID": brokerIDHex, "LoanSequence": uint32(1), "Borrower": brokerAddr,
		"StartDate": uint32(1), "PaymentInterval": uint32(100), "PeriodicPayment": "1",
		"PreviousTxnID": strings.Repeat("0", 64), "PreviousTxnLgrSeq": uint32(0),
		"PaymentRemaining": uint32(1), "TotalValueOutstanding": "1", "PrincipalOutstanding": "1",
	})
	brokerEntry := mustEncode(t, map[string]any{
		"LedgerEntryType": "LoanBroker", "Flags": uint32(0), "Sequence": uint32(1),
		"OwnerNode": "0", "VaultNode": "0", "Account": brokerAddr,
		"Owner": issuerAddr, "VaultID": vaultIDHex, "LoanSequence": uint32(1), "CoverAvailable": "1",
		"PreviousTxnID": strings.Repeat("0", 64), "PreviousTxnLgrSeq": uint32(0),
	})
	vaultEntry := mustEncode(t, map[string]any{
		"LedgerEntryType": "Vault", "Flags": uint32(0), "Sequence": uint32(1), "OwnerNode": "0",
		"Account": vaultAddr, "Owner": issuerAddr, "Asset": map[string]any{"currency": assetCurrency, "issuer": issuerAddr},
		"ShareMPTID": strings.Repeat("0", 48), "WithdrawalPolicy": uint32(1),
		"PreviousTxnID": strings.Repeat("0", 64), "PreviousTxnLgrSeq": uint32(0),
	})
	issuerRoot := mustSerializeAccount(t, &state.AccountRoot{Account: issuerAddr, Flags: issuerFlags, Balance: 1_000_000})
	view := mapView{data: map[[32]byte][]byte{
		keylet.LoanByID(loanID).Key:         loan,
		keylet.LoanBrokerByID(brokerID).Key: brokerEntry,
		keylet.VaultByID(vaultID).Key:       vaultEntry,
		keylet.Account(issuer).Key:          issuerRoot,
		keylet.Account(broker).Key:          mustSerializeAccount(t, &state.AccountRoot{Account: brokerAddr, Balance: 1_000_000}),
		keylet.Account(vault).Key:           mustSerializeAccount(t, &state.AccountRoot{Account: vaultAddr, Balance: 1_000_000}),
	}}
	entries := []InvariantEntry{
		{EntryType: entry.TypeAccountRoot, After: issuerRoot},
		loanFreezeLineEntry(t, broker, issuer, lineCurrency, "100", "90", lineFlags),
		loanFreezeLineEntry(t, vault, issuer, lineCurrency, "0", "10", lineFlags),
	}
	return loanFreezeFixture{view: view, entries: entries, loanID: loanIDHex}
}

func loanFreezeLineEntry(t *testing.T, account, issuer [20]byte, currency, before, after string, flags uint32) InvariantEntry {
	t.Helper()
	low, high := account, issuer
	if state.CompareAccountIDs(low, high) > 0 {
		low, high = high, low
	}
	lowAddr, _ := state.EncodeAccountID(low)
	highAddr, _ := state.EncodeAccountID(high)
	encode := func(value string) []byte {
		balance, err := state.NewIssuedAmountFromDecimalString(value, currency, state.AccountOneAddress)
		if err != nil {
			t.Fatalf("balance %s: %v", value, err)
		}
		if account == high {
			balance = balance.Negate()
		}
		lowLimit, err := state.NewIssuedAmountFromDecimalString("1000000", currency, lowAddr)
		if err != nil {
			t.Fatalf("low limit: %v", err)
		}
		highLimit, err := state.NewIssuedAmountFromDecimalString("1000000", currency, highAddr)
		if err != nil {
			t.Fatalf("high limit: %v", err)
		}
		result, err := state.SerializeRippleState(&state.RippleState{Balance: balance, LowLimit: lowLimit, HighLimit: highLimit, Flags: flags})
		if err != nil {
			t.Fatalf("SerializeRippleState: %v", err)
		}
		return result
	}
	return InvariantEntry{Key: keylet.Line(account, issuer, currency).Key, EntryType: entry.TypeRippleState, Before: encode(before), After: encode(after)}
}

func TestLoanDefaultDoesNotExemptUnrelatedAccount(t *testing.T) {
	fixture := newLoanFreezeFixture(t, "USD", state.LsfHighFreeze, 0)
	var issuer, other [20]byte
	for i := range issuer {
		issuer[i] = 0xf0
		other[i] = 0x30
	}
	fixture.entries = append(fixture.entries, loanFreezeLineEntry(t, other, issuer, "USD", "100", "90", state.LsfHighFreeze))
	fixture.view.data[keylet.Account(other).Key] = mustSerializeAccount(t, &state.AccountRoot{Account: state.EncodeAccountIDSafe(other), Balance: 1000000})
	rules := amendment.NewRules([][32]byte{amendment.FeatureDeepFreeze, amendment.FeatureFixCleanup3_4_0})
	transaction := vvTx{txType: protocol.TxTypeLoanManage, flat: map[string]any{"Flags": lending.TfLoanDefault, "LoanID": fixture.loanID}}
	if violation := checkTransfersNotFrozen(transaction, fixture.entries, fixture.view, rules); violation == nil {
		t.Fatal("unrelated frozen holder bypassed freeze invariant")
	}
}

func TestLoanDefaultMPTLockExemptionScope(t *testing.T) {
	for _, name := range []string{"default", "other issuance", "unrelated holder", "transfer disabled", "authorization required", "ordinary payment", "cleanup disabled"} {
		t.Run(name, func(t *testing.T) {
			flags := entry.LsfMPTCanTransfer | entry.LsfMPTLocked
			if name == "transfer disabled" {
				flags &^= entry.LsfMPTCanTransfer
			}
			if name == "authorization required" {
				flags |= entry.LsfMPTRequireAuth
			}
			view, id, source, destination, entries := mptTransferFixture(t, flags, 0, 0)
			chain := newLoanFreezeFixture(t, "USD", 0, 0)
			for key, raw := range chain.view.data {
				fields, err := decodeEntry(raw)
				if err != nil {
					t.Fatal(err)
				}
				switch fields["LedgerEntryType"] {
				case "Loan":
					view.data[key] = raw
				case "LoanBroker":
					account := source
					if name == "unrelated holder" {
						account = [20]byte{99}
					}
					fields["Account"] = state.EncodeAccountIDSafe(account)
					view.data[key] = mustEncode(t, fields)
				case "Vault":
					vaultAsset := id
					if name == "other issuance" {
						vaultAsset[0]++
					}
					fields["Asset"] = map[string]any{"mpt_issuance_id": hex.EncodeToString(vaultAsset[:])}
					fields["Account"] = state.EncodeAccountIDSafe(destination)
					view.data[key] = mustEncode(t, fields)
				}
			}
			sourceAccount := &state.AccountRoot{Account: state.EncodeAccountIDSafe(source), LoanBrokerID: [32]byte{1}}
			if name == "authorization required" {
				sourceAccount.LoanBrokerID = [32]byte{}
			}
			view.data[keylet.Account(source).Key] = mustSerializeAccount(t, sourceAccount)
			view.data[keylet.Account(destination).Key] = mustSerializeAccount(t, &state.AccountRoot{Account: state.EncodeAccountIDSafe(destination), VaultID: [32]byte{2}})
			rules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
			if name == "cleanup disabled" {
				rules = amendment.NewRules([][32]byte{amendment.FeatureMPTokensV2})
			}
			transaction := vvTx{txType: protocol.TxTypeLoanManage, flat: map[string]any{"Flags": tfLoanDefault, "LoanID": chain.loanID}}
			if name == "ordinary payment" {
				transaction.txType = protocol.TxTypeLoanPay
			}
			violation := checkValidMPTTransfer(transaction, TesSUCCESS, entries, view, rules)
			if name == "default" && violation != nil {
				t.Fatalf("default rejected: %v", violation)
			}
			if name != "default" && violation == nil {
				t.Fatalf("%s bypassed MPT invariant", name)
			}
		})
	}
}
