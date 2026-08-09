package invariants

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type confidentialMPTView struct {
	stubView
	entries map[keylet.Keylet][]byte
}

func (v confidentialMPTView) Read(k keylet.Keylet) ([]byte, error) { return v.entries[k], nil }

func mustSerializeConfidentialToken(t *testing.T, token *state.MPTokenData) []byte {
	t.Helper()
	data, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatalf("SerializeMPToken: %v", err)
	}
	return data
}

func confidentialInvariantRules() *amendment.Rules {
	return amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build()
}

func TestValidConfidentialMPTokenFirstConvertDoesNotRequireVersionChange(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(3, issuer)
	beforeIssuance := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, OutstandingAmount: 100,
		Flags: entry.LsfMPTCanHoldConfidentialBalance,
	})
	afterIssuance := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, OutstandingAmount: 100, ConfidentialOutstandingAmount: 10,
		Flags: entry.LsfMPTCanHoldConfidentialBalance,
	})
	beforeToken := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id, MPTAmount: 100,
	})
	afterToken := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id, MPTAmount: 90,
		ConfidentialBalanceInbox:    []byte{1},
		ConfidentialBalanceSpending: []byte{2},
		IssuerEncryptedBalance:      []byte{3},
		HolderEncryptionKey:         []byte{4},
	})
	changes := []InvariantEntry{
		{EntryType: entry.TypeMPTokenIssuance, Before: beforeIssuance, After: afterIssuance},
		{EntryType: entry.TypeMPToken, Before: beforeToken, After: afterToken},
	}
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypeConfidentialMPTConvert}, TesSUCCESS, changes, stubView{}, confidentialInvariantRules()); violation != nil {
		t.Fatalf("first Convert invariant = %v", violation)
	}
}

func TestValidConfidentialMPTokenDeletionDependsOnOutstandingAmount(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(3, issuer)
	token := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id,
		ConfidentialBalanceInbox:    []byte{1},
		ConfidentialBalanceSpending: []byte{2},
		IssuerEncryptedBalance:      []byte{3},
		HolderEncryptionKey:         []byte{4},
	})
	deleted := []InvariantEntry{{EntryType: entry.TypeMPToken, Before: token, IsDelete: true}}

	issuance := &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, OutstandingAmount: 100,
		Flags: entry.LsfMPTCanHoldConfidentialBalance,
	}
	view := confidentialMPTView{entries: map[keylet.Keylet][]byte{
		keylet.MPTIssuance(id): mustSerializeMPTIssuance(t, issuance),
	}}
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypeMPTokenAuthorize}, TesSUCCESS, deleted, view, confidentialInvariantRules()); violation != nil {
		t.Fatalf("drained deletion invariant = %v", violation)
	}

	issuance.ConfidentialOutstandingAmount = 1
	view.entries[keylet.MPTIssuance(id)] = mustSerializeMPTIssuance(t, issuance)
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypeMPTokenAuthorize}, TesSUCCESS, deleted, view, confidentialInvariantRules()); violation == nil {
		t.Fatal("deletion with non-zero ConfidentialOutstandingAmount passed")
	}
}

func TestValidConfidentialMPTokenRunsBeforeAmendmentSupport(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(3, issuer)
	issuance := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, OutstandingAmount: 100,
	})
	inconsistent := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account:                  holder,
		MPTokenIssuanceID:        id,
		ConfidentialBalanceInbox: []byte{1},
	})
	view := confidentialMPTView{entries: map[keylet.Keylet][]byte{
		keylet.MPTIssuance(id): issuance,
	}}
	changes := []InvariantEntry{{EntryType: entry.TypeMPToken, After: inconsistent}}
	rules := amendment.NewRulesBuilder().Build()
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypePayment}, TesSUCCESS, changes, view, rules); violation == nil {
		t.Fatal("inconsistent confidential fields passed with ConfidentialTransfer disabled")
	}
}
