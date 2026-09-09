package invariants

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

func lendingV11Rules() *amendment.Rules {
	return amendment.NewRules([][32]byte{
		amendment.FeatureLendingProtocol,
		amendment.FeatureLendingProtocolV1_1,
	})
}

func loanInvariantMap(flags uint32, paymentRemaining uint32) map[string]any {
	return map[string]any{
		"LedgerEntryType":          "Loan",
		"OwnerNode":                "0",
		"LoanBrokerNode":           "0",
		"LoanBrokerID":             strings.Repeat("1", 64),
		"LoanSequence":             uint32(1),
		"Borrower":                 testPseudoAddr,
		"StartDate":                uint32(1000),
		"PaymentInterval":          uint32(100),
		"NextPaymentDueDate":       uint32(1100),
		"PaymentRemaining":         paymentRemaining,
		"PeriodicPayment":          "1",
		"PrincipalOutstanding":     "1",
		"TotalValueOutstanding":    "1",
		"ManagementFeeOutstanding": "0",
		"LoanScale":                int32(0),
		"Flags":                    flags,
		"PreviousTxnID":            strings.Repeat("0", 64),
		"PreviousTxnLgrSeq":        uint32(1),
	}
}

func loanBrokerInvariantMap(ownerCount uint32, debt string) map[string]any {
	return map[string]any{
		"LedgerEntryType":   "LoanBroker",
		"Sequence":          uint32(1),
		"OwnerNode":         "0",
		"VaultNode":         "0",
		"VaultID":           strings.Repeat("2", 64),
		"Account":           testPseudoAddr,
		"Owner":             testPseudoAddr,
		"LoanSequence":      uint32(1),
		"OwnerCount":        ownerCount,
		"DebtTotal":         debt,
		"CoverAvailable":    "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(1),
	}
}

func deletedBrokerView(t *testing.T) mapView {
	t.Helper()
	var vaultID [32]byte
	for i := range vaultID {
		vaultID[i] = 0x22
	}
	vault := mustEncode(t, map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             uint32(0),
		"Sequence":          uint32(1),
		"OwnerNode":         "0",
		"Owner":             testPseudoAddr,
		"Account":           testPseudoAddr,
		"Asset":             map[string]any{"currency": "XRP"},
		"ShareMPTID":        strings.Repeat("0", 48),
		"WithdrawalPolicy":  uint8(1),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(1),
	})
	account := mustSerializeAccount(t, &state.AccountRoot{Account: testPseudoAddr, Balance: 0})
	view := mapView{data: map[[32]byte][]byte{
		keylet.VaultByID(vaultID).Key:                                vault,
		keylet.Account(mustDecodeTestAccount(t, testPseudoAddr)).Key: account,
	}}
	return view
}

func mustDecodeTestAccount(t *testing.T, address string) [20]byte {
	t.Helper()
	id, err := state.DecodeAccountID(address)
	if err != nil {
		t.Fatalf("DecodeAccountID(%q): %v", address, err)
	}
	return id
}

func TestValidLoan_V11LifecycleFlagGates(t *testing.T) {
	before := mustEncode(t, loanInvariantMap(0, 1))
	after := mustEncode(t, loanInvariantMap(entry.LsfLoanImpaired, 1))
	entryChange := InvariantEntry{EntryType: entry.TypeLoan, Before: before, After: after}

	wrongTx := vvTx{txType: protocol.TxTypePayment}
	if violation := checkValidLoanForTx(wrongTx, TesSUCCESS, []InvariantEntry{entryChange}, nil, lendingV11Rules()); violation == nil {
		t.Fatal("expected impaired-flag change outside LoanManage/LoanPay to fail")
	}
	manageTx := vvTx{txType: protocol.TxTypeLoanManage}
	if violation := checkValidLoanForTx(manageTx, TesSUCCESS, []InvariantEntry{entryChange}, nil, lendingV11Rules()); violation != nil {
		t.Fatalf("LoanManage impaired-flag change rejected: %v", violation)
	}
}

func TestValidLoan_V11DeletionRequiresLoanDelete(t *testing.T) {
	before := mustEncode(t, loanInvariantMap(0, 0))
	deleted := InvariantEntry{EntryType: entry.TypeLoan, Before: before, DeleteFinal: before, IsDelete: true}
	if violation := checkValidLoanForTx(vvTx{txType: protocol.TxTypePayment}, TesSUCCESS, []InvariantEntry{deleted}, nil, lendingV11Rules()); violation == nil {
		t.Fatal("expected Loan deletion by Payment to fail")
	}
	if violation := checkValidLoanForTx(vvTx{txType: protocol.TxTypeLoanDelete}, TesSUCCESS, []InvariantEntry{deleted}, nil, lendingV11Rules()); violation != nil {
		t.Fatalf("LoanDelete rejected: %v", violation)
	}
}

func TestValidLoanBroker_V11DeletionUsesPreimageAuthorization(t *testing.T) {
	before := mustEncode(t, loanBrokerInvariantMap(0, "0"))
	deleted := InvariantEntry{Key: [32]byte{1}, EntryType: entry.TypeLoanBroker, Before: before, DeleteFinal: before, IsDelete: true}
	if violation := checkValidLoanBrokerForTx(vvTx{txType: protocol.TxTypePayment}, []InvariantEntry{deleted}, nil, lendingV11Rules()); violation == nil {
		t.Fatal("expected LoanBroker deletion by Payment to fail")
	}
	if violation := checkValidLoanBrokerForTx(vvTx{txType: protocol.TxTypeLoanBrokerDelete}, []InvariantEntry{deleted}, deletedBrokerView(t), lendingV11Rules()); violation != nil {
		t.Fatalf("LoanBrokerDelete rejected zero-debt preimage: %v", violation)
	}
}

func TestValidLoanBroker_V11DeletionAuthorizationUsesBefore(t *testing.T) {
	before := mustEncode(t, loanBrokerInvariantMap(0, "1"))
	final := mustEncode(t, loanBrokerInvariantMap(0, "0"))
	deleted := InvariantEntry{
		Key:         [32]byte{1},
		EntryType:   entry.TypeLoanBroker,
		Before:      before,
		DeleteFinal: final,
		IsDelete:    true,
	}
	violation := checkValidLoanBrokerForTx(
		vvTx{txType: protocol.TxTypeLoanBrokerDelete},
		[]InvariantEntry{deleted},
		deletedBrokerView(t),
		lendingV11Rules(),
	)
	if violation == nil || !strings.Contains(violation.Message, "non-zero debt total") {
		t.Fatalf("deletion with non-zero preimage debt = %v, want preimage authorization failure", violation)
	}
}

func TestValidLoan_RedemptionScheduleRunsBeforeLendingGate(t *testing.T) {
	var brokerID, vaultID [32]byte
	for i := range brokerID {
		brokerID[i] = 0x11
		vaultID[i] = 0x22
	}
	broker := mustEncode(t, map[string]any{
		"LedgerEntryType": "LoanBroker",
		"VaultID":         strings.ToUpper(hexEncode32(vaultID)),
	})
	vault := mustEncode(t, map[string]any{
		"LedgerEntryType": "Vault",
		"VaultKind":       uint8(vvVaultKindClosedEnded),
		"RedemptionDate":  uint32(1150),
		"Asset":           map[string]any{"currency": "XRP"},
	})
	view := mapView{data: map[[32]byte][]byte{
		keylet.LoanBrokerByID(brokerID).Key: broker,
		keylet.VaultByID(vaultID).Key:       vault,
	}}
	loan := mustEncode(t, loanInvariantMap(0, 1))
	entryChange := InvariantEntry{EntryType: entry.TypeLoan, After: loan}
	for _, tc := range []struct {
		name  string
		rules *amendment.Rules
		bad   bool
	}{
		{name: "lending disabled", rules: amendment.EmptyRules(), bad: true},
		{name: "lending enabled", rules: amendment.NewRules([][32]byte{amendment.FeatureLendingProtocol}), bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			violation := checkValidLoanForTx(vvTx{txType: protocol.TxTypeLoanSet}, TesSUCCESS, []InvariantEntry{entryChange}, view, tc.rules)
			if tc.bad != (violation != nil) || (tc.bad && !strings.Contains(violation.Message, "RedemptionDate")) {
				t.Fatalf("schedule violation = %v, want bad=%v", violation, tc.bad)
			}
		})
	}

	validVault := mustEncode(t, map[string]any{
		"LedgerEntryType": "Vault",
		"VaultKind":       uint8(vvVaultKindClosedEnded),
		"RedemptionDate":  uint32(1200),
		"Asset":           map[string]any{"currency": "XRP"},
	})
	view.data[keylet.VaultByID(vaultID).Key] = validVault
	if violation := checkValidLoanForTx(vvTx{txType: protocol.TxTypeLoanSet}, TesSUCCESS, []InvariantEntry{entryChange}, view, amendment.EmptyRules()); violation != nil {
		t.Fatalf("valid schedule rejected while lending disabled: %v", violation)
	}
}

func hexEncode32(id [32]byte) string {
	return strings.ToUpper(hex.EncodeToString(id[:]))
}
