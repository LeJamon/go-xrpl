package mpt

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type confidentialAuthorizeView struct {
	entries    map[[32]byte][]byte
	rules      *amendment.Rules
	readErrKey *[32]byte
}

func (v *confidentialAuthorizeView) Read(k keylet.Keylet) ([]byte, error) {
	if v.readErrKey != nil && *v.readErrKey == k.Key {
		return nil, errors.New("read failed")
	}
	return v.entries[k.Key], nil
}
func (v *confidentialAuthorizeView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.entries[k.Key]
	return ok, nil
}
func (v *confidentialAuthorizeView) Insert(k keylet.Keylet, data []byte) error {
	v.entries[k.Key] = data
	return nil
}
func (v *confidentialAuthorizeView) Update(k keylet.Keylet, data []byte) error {
	v.entries[k.Key] = data
	return nil
}
func (v *confidentialAuthorizeView) Erase(k keylet.Keylet) error {
	delete(v.entries, k.Key)
	return nil
}
func (*confidentialAuthorizeView) AdjustDropsDestroyed(drops.XRPAmount) error  { return nil }
func (*confidentialAuthorizeView) TxExists([32]byte) (bool, error)             { return false, nil }
func (v *confidentialAuthorizeView) Rules() *amendment.Rules                   { return v.rules }
func (*confidentialAuthorizeView) LedgerSeq() uint32                           { return 1 }
func (v *confidentialAuthorizeView) ForEach(func([32]byte, []byte) bool) error { return nil }
func (*confidentialAuthorizeView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}

func TestMPTokenUnauthorizeConfidentialBalanceGuard(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(3, issuer)
	issuanceKey := keylet.MPTIssuance(id)
	tokenKey := keylet.MPToken(issuanceKey.Key, holder)
	issuanceData, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, OutstandingAmount: 10, ConfidentialOutstandingAmount: 1,
		Flags: entry.LsfMPTCanHoldConfidentialBalance,
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenData, err := state.SerializeMPToken(&state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id, ConfidentialBalanceInbox: []byte{1},
		ConfidentialBalanceSpending: []byte{2}, IssuerEncryptedBalance: []byte{3},
	})
	if err != nil {
		t.Fatal(err)
	}
	view := &confidentialAuthorizeView{
		entries: map[[32]byte][]byte{issuanceKey.Key: issuanceData, tokenKey.Key: tokenData},
		rules:   amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build(),
	}
	transaction := NewMPTokenAuthorize(state.EncodeAccountIDSafe(holder), confidentialHexForAuthorize(id[:]))
	transaction.SetFlags(MPTokenAuthorizeFlagUnauthorize)
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TecHAS_OBLIGATIONS {
		t.Fatalf("Preclaim = %v, want tecHAS_OBLIGATIONS", got)
	}
	view.readErrKey = &issuanceKey.Key
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TefINTERNAL {
		t.Fatalf("issuance read error Preclaim = %v, want tefINTERNAL", got)
	}
	view.readErrKey = nil
	view.rules = amendment.EmptyRules()
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TesSUCCESS {
		t.Fatalf("disabled ConfidentialTransfer Preclaim = %v", got)
	}
}

func confidentialHexForAuthorize(value []byte) string {
	const digits = "0123456789ABCDEF"
	result := make([]byte, len(value)*2)
	for i, b := range value {
		result[2*i] = digits[b>>4]
		result[2*i+1] = digits[b&15]
	}
	return string(result)
}
