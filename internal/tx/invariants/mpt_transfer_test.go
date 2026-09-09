package invariants

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

type mptTransferView struct {
	data map[[32]byte][]byte
}

func (v *mptTransferView) Read(k keylet.Keylet) ([]byte, error) { return v.data[k.Key], nil }
func (v *mptTransferView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.data[k.Key]
	return ok, nil
}
func (v *mptTransferView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v *mptTransferView) LedgerSeq() uint32 { return 1 }

func TestValidMPTTransferUsesPreTransactionPseudoClassification(t *testing.T) {
	issuer := [20]byte{1}
	pseudo := [20]byte{2}
	holder := [20]byte{3}
	id := keylet.MakeMPTID(1, issuer)
	view := &mptTransferView{data: make(map[[32]byte][]byte)}

	issuance, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:   issuer,
		Sequence: 1,
		Flags:    entry.LsfMPTRequireAuth | entry.LsfMPTCanTransfer,
	})
	if err != nil {
		t.Fatal(err)
	}
	view.data[keylet.MPTIssuance(id).Key] = issuance
	account, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:      state.EncodeAccountIDSafe(pseudo),
		LoanBrokerID: [32]byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	view.data[keylet.Account(holder).Key] = mustSerializeAccount(t, &state.AccountRoot{Account: state.EncodeAccountIDSafe(holder)})

	sourceBefore := mustSerializeMPToken(t, &state.MPTokenData{
		Account:           pseudo,
		MPTokenIssuanceID: id,
		MPTAmount:         5,
	})
	sourceAfter := mustSerializeMPToken(t, &state.MPTokenData{
		Account:           pseudo,
		MPTokenIssuanceID: id,
	})
	destinationBefore := mustSerializeMPToken(t, &state.MPTokenData{
		Account:           holder,
		MPTokenIssuanceID: id,
		Flags:             entry.LsfMPTAuthorized,
	})
	destinationAfter := mustSerializeMPToken(t, &state.MPTokenData{
		Account:           holder,
		MPTokenIssuanceID: id,
		MPTAmount:         5,
		Flags:             entry.LsfMPTAuthorized,
	})
	view.data[keylet.MPTokenByID(id, holder).Key] = destinationAfter
	pseudoTokenKey := keylet.MPTokenByID(id, pseudo)
	entries := []InvariantEntry{
		{EntryType: entry.TypeAccountRoot, Before: account, IsDelete: true},
		{Key: pseudoTokenKey.Key, EntryType: entry.TypeMPToken, Before: sourceBefore, DeleteFinal: sourceAfter, IsDelete: true},
		{EntryType: entry.TypeMPToken, Before: destinationBefore, After: destinationAfter},
	}
	rules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypeLoanBrokerDelete}, TesSUCCESS, entries, view, rules); violation != nil {
		t.Fatalf("pseudo-account transfer should pass: %v", violation)
	}
	regular, err := state.SerializeAccountRoot(&state.AccountRoot{Account: state.EncodeAccountIDSafe(pseudo)})
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Before = regular
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypeLoanBrokerDelete}, TesSUCCESS, entries, view, rules); violation == nil {
		t.Fatal("unauthorized regular-account transfer should fail the invariant")
	}
}

type flattenedMPTTx struct {
	stubTx
	fields map[string]any
}

func (t flattenedMPTTx) Flatten() (map[string]any, error) { return t.fields, nil }

func mptTransferRules(ids ...[32]byte) *amendment.Rules {
	return amendment.NewRules(ids)
}

func mptTransferFixture(t *testing.T, issuanceFlags, sourceFlags, destinationFlags uint32) (*mptTransferView, [24]byte, [20]byte, [20]byte, []InvariantEntry) {
	t.Helper()
	issuer := [20]byte{11}
	source := [20]byte{12}
	destination := [20]byte{13}
	id := keylet.MakeMPTID(7, issuer)
	view := &mptTransferView{data: make(map[[32]byte][]byte)}
	issuance := mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer:   issuer,
		Sequence: 7,
		Flags:    issuanceFlags,
	})
	view.data[keylet.MPTIssuance(id).Key] = issuance
	beforeSource := mustSerializeMPToken(t, &state.MPTokenData{Account: source, MPTokenIssuanceID: id, MPTAmount: 10, Flags: sourceFlags})
	afterSource := mustSerializeMPToken(t, &state.MPTokenData{Account: source, MPTokenIssuanceID: id, MPTAmount: 5, Flags: sourceFlags})
	beforeDestination := mustSerializeMPToken(t, &state.MPTokenData{Account: destination, MPTokenIssuanceID: id, Flags: destinationFlags})
	afterDestination := mustSerializeMPToken(t, &state.MPTokenData{Account: destination, MPTokenIssuanceID: id, MPTAmount: 5, Flags: destinationFlags})
	view.data[keylet.MPTokenByID(id, source).Key] = afterSource
	view.data[keylet.MPTokenByID(id, destination).Key] = afterDestination
	return view, id, source, destination, []InvariantEntry{
		{EntryType: entry.TypeMPToken, Before: beforeSource, After: afterSource},
		{EntryType: entry.TypeMPToken, Before: beforeDestination, After: afterDestination},
	}
}

func TestValidMPTTransferRejectsFrozenOrUnauthorizedHolders(t *testing.T) {
	cases := []struct {
		name             string
		issuanceFlags    uint32
		sourceFlags      uint32
		destinationFlags uint32
	}{
		{name: "global lock", issuanceFlags: entry.LsfMPTCanTransfer | entry.LsfMPTLocked},
		{name: "individual lock", issuanceFlags: entry.LsfMPTCanTransfer, sourceFlags: entry.LsfMPTLocked},
		{name: "missing authorization", issuanceFlags: entry.LsfMPTCanTransfer | entry.LsfMPTRequireAuth},
	}
	rules := mptTransferRules(amendment.FeatureMPTokensV2)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view, _, _, _, entries := mptTransferFixture(t, tc.issuanceFlags, tc.sourceFlags, tc.destinationFlags)
			if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypePayment}, TesSUCCESS, entries, view, rules); violation == nil {
				t.Fatal("expected frozen or unauthorized transfer to fail")
			}
		})
	}

	view, id, source, _, entries := mptTransferFixture(t, entry.LsfMPTCanTransfer, 0, 0)
	delete(view.data, keylet.MPTokenByID(id, source).Key)
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypePayment}, TesSUCCESS, entries, view, rules); violation == nil {
		t.Fatal("transfer from a holder without an MPToken should fail the invariant")
	}

	view, _, _, _, entries = mptTransferFixture(t, entry.LsfMPTCanTransfer|entry.LsfMPTLocked, entry.LsfMPTLocked, 0)
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypeAMMClawback}, TesSUCCESS, entries, view, rules); violation != nil {
		t.Fatalf("AMMClawback should override MPT freeze checks: %v", violation)
	}
}

func TestValidMPTTransferRecoveryPathsWaiveCanTransfer(t *testing.T) {
	cases := []struct {
		name    string
		txType  protocol.TxType
		rules   *amendment.Rules
		wantErr bool
	}{
		{name: "ordinary payment", txType: protocol.TxTypePayment, rules: mptTransferRules(amendment.FeatureMPTokensV2), wantErr: true},
		{name: "AMM withdraw", txType: protocol.TxTypeAMMWithdraw, rules: mptTransferRules(amendment.FeatureMPTokensV2)},
		{name: "vault withdraw cleanup", txType: protocol.TxTypeVaultWithdraw, rules: mptTransferRules(amendment.FeatureMPTokensV2, amendment.FeatureFixCleanup3_2_0)},
		{name: "cover withdraw cleanup", txType: protocol.TxTypeLoanBrokerCoverWithdraw, rules: mptTransferRules(amendment.FeatureMPTokensV2, amendment.FeatureFixCleanup3_2_0)},
		{name: "loan pay cleanup", txType: protocol.TxTypeLoanPay, rules: mptTransferRules(amendment.FeatureMPTokensV2, amendment.FeatureFixCleanup3_2_0)},
		{name: "loan pay before cleanup", txType: protocol.TxTypeLoanPay, rules: mptTransferRules(amendment.FeatureMPTokensV2), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view, _, _, _, entries := mptTransferFixture(t, 0, 0, 0)
			violation := checkValidMPTTransfer(stubTx{txType: tc.txType}, TesSUCCESS, entries, view, tc.rules)
			if (violation != nil) != tc.wantErr {
				t.Fatalf("violation = %v, wantErr=%t", violation, tc.wantErr)
			}
		})
	}
}

func TestValidMPTTransferRejectsDEXWithoutCanTrade(t *testing.T) {
	view, id, _, _, entries := mptTransferFixture(t, entry.LsfMPTCanTransfer, 0, 0)
	rules := mptTransferRules(amendment.FeatureMPTokensV2)
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypeOfferCreate}, TesSUCCESS, entries, view, rules); violation == nil {
		t.Fatal("expected OfferCreate transfer to require MPT CanTrade")
	}

	view, _, _, _, entries = mptTransferFixture(t, entry.LsfMPTCanTransfer, 0, 0)
	payment := flattenedMPTTx{
		stubTx: stubTx{txType: protocol.TxTypePayment},
		fields: map[string]any{
			"Amount":  map[string]any{"value": "5", "mpt_issuance_id": hex.EncodeToString(id[:])},
			"SendMax": map[string]any{"value": "6", "currency": "USD", "issuer": state.EncodeAccountIDSafe([20]byte{21})},
		},
	}
	if violation := checkValidMPTTransfer(payment, TesSUCCESS, entries, view, rules); violation == nil {
		t.Fatal("expected cross-currency Payment to require MPT CanTrade")
	}
}

func TestValidMPTTransferRejectsOrphanAndFailedChanges(t *testing.T) {
	rules := mptTransferRules(amendment.FeatureFixCleanup3_4_0)
	view, _, _, _, entries := mptTransferFixture(t, entry.LsfMPTCanTransfer, 0, 0)
	delete(view.data, keylet.MPTIssuance(keylet.MakeMPTID(7, [20]byte{11})).Key)
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypePayment}, TesSUCCESS, entries, view, rules); violation == nil {
		t.Fatal("expected orphaned MPT balance change to fail")
	}

	zeroID := keylet.MakeMPTID(8, [20]byte{11})
	zeroAfter := mustSerializeMPToken(t, &state.MPTokenData{Account: [20]byte{14}, MPTokenIssuanceID: zeroID})
	zeroView := &mptTransferView{data: make(map[[32]byte][]byte)}
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypePayment}, TesSUCCESS, []InvariantEntry{{EntryType: entry.TypeMPToken, After: zeroAfter}}, zeroView, rules); violation != nil {
		t.Fatalf("zero-balance orphan cleanup should pass: %v", violation)
	}

	view, id, source, _, entries := mptTransferFixture(t, entry.LsfMPTCanTransfer, 0, entry.LsfMPTAuthorized)
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypePayment}, TecINCOMPLETE, entries, view, rules); violation == nil {
		t.Fatal("expected failed transaction balance change to fail")
	}

	deleted := mustSerializeMPToken(t, &state.MPTokenData{Account: source, MPTokenIssuanceID: id, MPTAmount: 0, Flags: entry.LsfMPTAuthorized})
	deletedEntry := InvariantEntry{
		Key:         keylet.MPTokenByID(id, source).Key,
		EntryType:   entry.TypeMPToken,
		Before:      deleted,
		DeleteFinal: deleted,
		IsDelete:    true,
	}
	delete(view.data, keylet.MPTIssuance(id).Key)
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypePayment}, TecINCOMPLETE, []InvariantEntry{deletedEntry}, view, rules); violation == nil {
		t.Fatal("expected failed transaction MPToken deletion to fail")
	}
}

func TestValidMPTTransferUsesParentCloseTimeForDomainCredentials(t *testing.T) {
	issuer := [20]byte{21}
	source := [20]byte{22}
	destination := [20]byte{23}
	credentialIssuer := [20]byte{24}
	id := keylet.MakeMPTID(9, issuer)
	domainID := [32]byte{25}
	domainHex := strings.ToUpper(hex.EncodeToString(domainID[:]))
	credentialType := []byte("KYC")
	view := &mptTransferView{data: make(map[[32]byte][]byte)}

	view.data[keylet.MPTIssuance(id).Key] = mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{
		Issuer:   issuer,
		Sequence: 9,
		DomainID: &domainHex,
		Flags:    entry.LsfMPTRequireAuth | entry.LsfMPTCanTransfer,
	})
	domainRaw, err := state.SerializePermissionedDomain(&state.PermissionedDomainData{
		Owner: issuer,
		AcceptedCredentials: []state.PermissionedDomainCredential{{
			Issuer:         credentialIssuer,
			CredentialType: credentialType,
		}},
	}, state.EncodeAccountIDSafe(issuer))
	if err != nil {
		t.Fatal(err)
	}
	view.data[keylet.PermissionedDomainByID(domainID).Key] = domainRaw

	for _, holder := range [][20]byte{source, destination} {
		credentialHex, err := binarycodec.Encode(map[string]any{
			"LedgerEntryType":   "Credential",
			"Subject":           state.EncodeAccountIDSafe(holder),
			"Issuer":            state.EncodeAccountIDSafe(credentialIssuer),
			"CredentialType":    hex.EncodeToString(credentialType),
			"Expiration":        uint32(100),
			"Flags":             entry.LsfAccepted,
			"IssuerNode":        "0",
			"SubjectNode":       "0",
			"PreviousTxnID":     strings.Repeat("0", 64),
			"PreviousTxnLgrSeq": uint32(0),
		})
		if err != nil {
			t.Fatal(err)
		}
		credentialRaw, err := hex.DecodeString(credentialHex)
		if err != nil {
			t.Fatal(err)
		}
		view.data[keylet.Credential(holder, credentialIssuer, credentialType).Key] = credentialRaw
	}

	beforeSource := mustSerializeMPToken(t, &state.MPTokenData{Account: source, MPTokenIssuanceID: id, MPTAmount: 10})
	afterSource := mustSerializeMPToken(t, &state.MPTokenData{Account: source, MPTokenIssuanceID: id, MPTAmount: 5})
	beforeDestination := mustSerializeMPToken(t, &state.MPTokenData{Account: destination, MPTokenIssuanceID: id})
	afterDestination := mustSerializeMPToken(t, &state.MPTokenData{Account: destination, MPTokenIssuanceID: id, MPTAmount: 5})
	view.data[keylet.MPTokenByID(id, source).Key] = afterSource
	view.data[keylet.MPTokenByID(id, destination).Key] = afterDestination
	entries := []InvariantEntry{
		{EntryType: entry.TypeMPToken, Before: beforeSource, After: afterSource},
		{EntryType: entry.TypeMPToken, Before: beforeDestination, After: afterDestination},
	}
	rules := mptTransferRules(amendment.FeatureMPTokensV2)
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypePayment}, TesSUCCESS, entries, WithParentCloseTime(view, 100), rules); violation != nil {
		t.Fatalf("domain credential should be valid at its expiration: %v", violation)
	}
	if violation := checkValidMPTTransfer(stubTx{txType: protocol.TxTypePayment}, TesSUCCESS, entries, WithParentCloseTime(view, 101), rules); violation == nil {
		t.Fatal("expired domain credential should reject the transfer")
	}
}

func mustSerializeMPToken(t *testing.T, token *state.MPTokenData) []byte {
	t.Helper()
	b, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
