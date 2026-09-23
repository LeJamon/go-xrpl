package invariants

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func mptInvariantRules() *amendment.Rules {
	return amendment.NewRulesBuilder().Enable(amendment.FeatureMPTokensV2).Build()
}

func mptInvariantRulesWithCleanup() *amendment.Rules {
	return amendment.NewRulesBuilder().
		Enable(amendment.FeatureMPTokensV2).
		Enable(amendment.FeatureFixCleanup3_4_0).
		Build()
}

func TestValidMPTBalanceChangesConservesLockedAmount(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(1, issuer)
	lockedBefore, lockedAfter := uint64(10), uint64(15)
	issuanceBefore := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 1, OutstandingAmount: 100,
	})
	issuanceAfter := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 1, OutstandingAmount: 110,
	})
	tokenBefore := mustSerializeMPT(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id, MPTAmount: 90, LockedAmount: &lockedBefore,
	})
	tokenAfter := mustSerializeMPT(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id, MPTAmount: 95, LockedAmount: &lockedAfter,
	})
	entries := []InvariantEntry{
		{EntryType: entry.TypeMPTokenIssuance, Before: issuanceBefore, After: issuanceAfter},
		{EntryType: entry.TypeMPToken, Before: tokenBefore, After: tokenAfter},
	}
	if violation := checkValidMPTBalanceChanges(stubTx{txType: TypePayment}, TesSUCCESS, entries, stubView{}, mptInvariantRules()); violation != nil {
		t.Fatalf("balanced public and locked MPT amounts: %v", violation)
	}

	badIssuance := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 1, OutstandingAmount: 111,
	})
	entries[0].After = badIssuance
	if violation := checkValidMPTBalanceChanges(stubTx{txType: TypePayment}, TesSUCCESS, entries, stubView{}, mptInvariantRules()); violation == nil {
		t.Fatal("unbalanced OutstandingAmount was accepted")
	} else if violation.Message != "invalid OutstandingAmount balance 100 111 10" {
		t.Fatalf("unexpected balance violation: %v", violation)
	}
}

func TestValidMPTBalanceChangesRejectsFailureAndHonorsGate(t *testing.T) {
	issuer := [20]byte{3}
	holder := [20]byte{4}
	id := keylet.MakeMPTID(2, issuer)
	issuanceBefore := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 2, OutstandingAmount: 10,
	})
	issuanceAfter := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 2, OutstandingAmount: 11,
	})
	before := mustSerializeMPT(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id, MPTAmount: 10,
	})
	after := mustSerializeMPT(t, &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id, MPTAmount: 11,
	})
	entries := []InvariantEntry{
		{EntryType: entry.TypeMPTokenIssuance, Before: issuanceBefore, After: issuanceAfter},
		{EntryType: entry.TypeMPToken, Before: before, After: after},
	}
	if violation := checkValidMPTBalanceChanges(stubTx{txType: TypePayment}, Result(1), entries, stubView{}, mptInvariantRulesWithCleanup()); violation == nil {
		t.Fatal("failed transaction changed MPT balance without a violation")
	} else if violation.Message != "OutstandingAmount balance changed on failure" {
		t.Fatalf("unexpected failure violation: %v", violation)
	}
	if violation := checkValidMPTBalanceChanges(stubTx{txType: TypePayment}, Result(1), entries, stubView{}, amendment.EmptyRules()); violation != nil {
		t.Fatalf("pre-amendment failed transaction was enforced: %v", violation)
	}
}

func TestValidMPTTransferRequiresCapabilityBetweenHolders(t *testing.T) {
	issuer := [20]byte{5}
	sender := [20]byte{6}
	receiver := [20]byte{7}
	id := keylet.MakeMPTID(3, issuer)
	issuance := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3,
	})
	senderBefore := mustSerializeMPT(t, &state.MPTokenData{
		Account: sender, MPTokenIssuanceID: id, MPTAmount: 10,
	})
	senderAfter := mustSerializeMPT(t, &state.MPTokenData{
		Account: sender, MPTokenIssuanceID: id, MPTAmount: 5,
	})
	receiverBefore := mustSerializeMPT(t, &state.MPTokenData{
		Account: receiver, MPTokenIssuanceID: id,
	})
	receiverAfter := mustSerializeMPT(t, &state.MPTokenData{
		Account: receiver, MPTokenIssuanceID: id, MPTAmount: 5,
	})
	view := mapView{data: map[[32]byte][]byte{
		keylet.MPTIssuance(id).Key:           issuance,
		keylet.MPTokenByID(id, sender).Key:   senderAfter,
		keylet.MPTokenByID(id, receiver).Key: receiverAfter,
	}}
	entries := []InvariantEntry{
		{EntryType: entry.TypeMPToken, Key: keylet.MPTokenByID(id, sender).Key, Before: senderBefore, After: senderAfter},
		{EntryType: entry.TypeMPToken, Key: keylet.MPTokenByID(id, receiver).Key, Before: receiverBefore, After: receiverAfter},
	}
	rules := mptInvariantRules()
	if violation := checkValidMPTTransfer(stubTx{txType: TypePayment}, TesSUCCESS, entries, view, rules); violation == nil {
		t.Fatal("holder transfer without MPTCanTransfer was accepted")
	} else if violation.Message != "invalid MPToken transfer between holders" {
		t.Fatalf("unexpected transfer violation: %v", violation)
	}
	if violation := checkValidMPTTransfer(stubTx{txType: TypePayment}, Result(1), entries, view, rules); violation == nil {
		t.Fatal("failed holder transfer without MPTCanTransfer was accepted")
	}
	if violation := checkValidMPTTransfer(stubTx{txType: TypeAMMClawback}, Result(1), entries, view, rules); violation != nil {
		t.Fatalf("AMMClawback override-freeze privilege was not honored: %v", violation)
	}
	issuance = mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, Flags: entry.LsfMPTCanTransfer,
	})
	view.data[keylet.MPTIssuance(id).Key] = issuance
	if violation := checkValidMPTTransfer(stubTx{txType: TypePayment}, TesSUCCESS, entries, view, rules); violation != nil {
		t.Fatalf("holder transfer with MPTCanTransfer: %v", violation)
	}
}

func mustSerializeMPT(t *testing.T, token *state.MPTokenData) []byte {
	t.Helper()
	data, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatalf("SerializeMPToken: %v", err)
	}
	return data
}
