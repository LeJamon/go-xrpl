package invariants

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
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

func mustSerializeMPToken(t *testing.T, token *state.MPTokenData) []byte {
	t.Helper()
	b, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
