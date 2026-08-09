package invariants

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type confidentialMPTView struct {
	stubView
	entries map[keylet.Keylet][]byte
	readErr error
}

func (v confidentialMPTView) Read(k keylet.Keylet) ([]byte, error) {
	if v.readErr != nil {
		return nil, v.readErr
	}
	return v.entries[k], nil
}

func mustSerializeConfidentialToken(t *testing.T, token *state.MPTokenData) []byte {
	t.Helper()
	data, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatalf("SerializeMPToken: %v", err)
	}
	return data
}

func TestValidConfidentialMPTokenConservation(t *testing.T) {
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
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypeConfidentialMPTConvert}, TesSUCCESS, changes, stubView{}, nil); violation != nil {
		t.Fatalf("balanced Convert invariant = %v", violation)
	}

	afterIssuance = mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, OutstandingAmount: 100, ConfidentialOutstandingAmount: 9,
		Flags: entry.LsfMPTCanHoldConfidentialBalance,
	})
	changes[0].After = afterIssuance
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypeConfidentialMPTConvert}, TesSUCCESS, changes, stubView{}, nil); violation == nil {
		t.Fatal("unbalanced Convert passed")
	}
}

func TestValidConfidentialMPTokenStateRules(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(3, issuer)
	issuance := &state.MPTokenIssuanceData{Issuer: issuer, Sequence: 3, OutstandingAmount: 100}
	view := confidentialMPTView{entries: map[keylet.Keylet][]byte{
		keylet.MPTIssuance(id): mustSerializeMPTIssuance(t, issuance),
	}}

	inconsistent := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id, ConfidentialBalanceInbox: []byte{1},
	})
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypePayment}, TesSUCCESS,
		[]InvariantEntry{{EntryType: entry.TypeMPToken, After: inconsistent}}, view, nil); violation == nil {
		t.Fatal("inconsistent confidential fields passed")
	}

	before := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id,
		ConfidentialBalanceInbox: []byte{1}, ConfidentialBalanceSpending: []byte{2},
		IssuerEncryptedBalance: []byte{3}, ConfidentialBalanceVersion: 7,
	})
	after := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id,
		ConfidentialBalanceInbox: []byte{1}, ConfidentialBalanceSpending: []byte{4},
		IssuerEncryptedBalance: []byte{3}, ConfidentialBalanceVersion: 7,
	})
	issuance.Flags = entry.LsfMPTCanHoldConfidentialBalance
	view.entries[keylet.MPTIssuance(id)] = mustSerializeMPTIssuance(t, issuance)
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypeConfidentialMPTMergeInbox}, TesSUCCESS,
		[]InvariantEntry{{EntryType: entry.TypeMPToken, Before: before, After: after}}, view, nil); violation == nil {
		t.Fatal("spending change without version increment passed")
	}
}

func TestValidConfidentialMPTokenDeletionGuard(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(3, issuer)
	token := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id, MPTAmount: 5,
	})
	deleted := []InvariantEntry{{EntryType: entry.TypeMPToken, Before: token, IsDelete: true}}
	issuance := &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, OutstandingAmount: 100,
		Flags: entry.LsfMPTCanHoldConfidentialBalance,
	}
	view := confidentialMPTView{entries: map[keylet.Keylet][]byte{
		keylet.MPTIssuance(id): mustSerializeMPTIssuance(t, issuance),
	}}
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypeMPTokenAuthorize}, TesSUCCESS, deleted, view, nil); violation != nil {
		t.Fatalf("deletion with zero confidential outstanding = %v", violation)
	}

	issuance.ConfidentialOutstandingAmount = 1
	view.entries[keylet.MPTIssuance(id)] = mustSerializeMPTIssuance(t, issuance)
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypeMPTokenAuthorize}, TesSUCCESS, deleted, view, nil); violation == nil {
		t.Fatal("deletion with non-zero confidential outstanding passed")
	}
}

func TestValidConfidentialMPTokenDeletionGuardIsOrderIndependent(t *testing.T) {
	issuer := [20]byte{1}
	id := keylet.MakeMPTID(3, issuer)
	nonempty := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: [20]byte{2}, MPTokenIssuanceID: id, ConfidentialBalanceInbox: []byte{1},
		ConfidentialBalanceSpending: []byte{2}, IssuerEncryptedBalance: []byte{3},
	})
	empty := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: [20]byte{3}, MPTokenIssuanceID: id,
	})
	issuance := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, OutstandingAmount: 100, ConfidentialOutstandingAmount: 1,
		Flags: entry.LsfMPTCanHoldConfidentialBalance,
	})
	view := confidentialMPTView{entries: map[keylet.Keylet][]byte{keylet.MPTIssuance(id): issuance}}
	deleted := func(data []byte) InvariantEntry {
		return InvariantEntry{EntryType: entry.TypeMPToken, Before: data, DeleteFinal: data, IsDelete: true}
	}

	orders := [][]InvariantEntry{{deleted(nonempty), deleted(empty)}, {deleted(empty), deleted(nonempty)}}
	for i, changes := range orders {
		if violation := checkValidConfidentialMPToken(stubTx{txType: TypeMPTokenAuthorize}, TesSUCCESS, changes, view, nil); violation == nil {
			t.Fatalf("deletion order %d passed", i)
		}
	}
}

func TestValidConfidentialMPTokenChecksDeleteFinalImage(t *testing.T) {
	issuer := [20]byte{1}
	id := keylet.MakeMPTID(3, issuer)
	before := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: [20]byte{2}, MPTokenIssuanceID: id,
	})
	final := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: [20]byte{2}, MPTokenIssuanceID: id, ConfidentialBalanceInbox: []byte{1},
	})
	issuance := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, OutstandingAmount: 100,
		Flags: entry.LsfMPTCanHoldConfidentialBalance,
	})
	view := confidentialMPTView{entries: map[keylet.Keylet][]byte{keylet.MPTIssuance(id): issuance}}
	changes := []InvariantEntry{{
		EntryType: entry.TypeMPToken, Before: before, DeleteFinal: final, IsDelete: true,
	}}
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypeMPTokenAuthorize}, TesSUCCESS, changes, view, nil); violation == nil {
		t.Fatal("inconsistent final pre-delete image passed")
	}
}

func TestValidConfidentialMPTokenReadError(t *testing.T) {
	issuer := [20]byte{1}
	id := keylet.MakeMPTID(3, issuer)
	after := mustSerializeConfidentialToken(t, &state.MPTokenData{
		Account: [20]byte{2}, MPTokenIssuanceID: id,
	})
	view := confidentialMPTView{readErr: errors.New("read failed")}
	changes := []InvariantEntry{{EntryType: entry.TypeMPToken, After: after}}
	if violation := checkValidConfidentialMPToken(stubTx{txType: TypePayment}, TesSUCCESS, changes, view, nil); violation == nil {
		t.Fatal("issuance read error passed")
	}
}
