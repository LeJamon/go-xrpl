package mpt

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type confidentialPreclaimView struct {
	entries map[[32]byte][]byte
	rules   *amendment.Rules
}

func (v *confidentialPreclaimView) Read(k keylet.Keylet) ([]byte, error) {
	return v.entries[k.Key], nil
}

func (v *confidentialPreclaimView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.entries[k.Key]
	return ok, nil
}

func (v *confidentialPreclaimView) Insert(k keylet.Keylet, data []byte) error {
	v.entries[k.Key] = data
	return nil
}

func (v *confidentialPreclaimView) Update(k keylet.Keylet, data []byte) error {
	v.entries[k.Key] = data
	return nil
}

func (v *confidentialPreclaimView) Erase(k keylet.Keylet) error {
	delete(v.entries, k.Key)
	return nil
}

func (*confidentialPreclaimView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (*confidentialPreclaimView) TxExists([32]byte) (bool, error)            { return false, nil }
func (v *confidentialPreclaimView) Rules() *amendment.Rules                  { return v.rules }
func (*confidentialPreclaimView) LedgerSeq() uint32                          { return 0 }

func (v *confidentialPreclaimView) ForEach(fn func([32]byte, []byte) bool) error {
	for k, data := range v.entries {
		if !fn(k, data) {
			break
		}
	}
	return nil
}

func (*confidentialPreclaimView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}

func mustSerializeConfidentialIssuance(t *testing.T, issuance *state.MPTokenIssuanceData) []byte {
	t.Helper()
	data, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		t.Fatalf("SerializeMPTokenIssuance: %v", err)
	}
	return data
}

func mustSerializeConfidentialHolding(t *testing.T, token *state.MPTokenData) []byte {
	t.Helper()
	data, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatalf("SerializeMPToken: %v", err)
	}
	return data
}

func confidentialMPTIDString(id [24]byte) string {
	return strings.ToUpper(hex.EncodeToString(id[:]))
}

func TestConfidentialMPTFlagsMask(t *testing.T) {
	rules := amendment.AllSupportedRules()
	transactions := []interface {
		GetFlagsMask(*amendment.Rules) uint32
	}{
		&ConfidentialMPTConvert{},
		&ConfidentialMPTMergeInbox{},
		&ConfidentialMPTConvertBack{},
	}
	for _, transaction := range transactions {
		mask := transaction.GetFlagsMask(rules)
		if mask != tx.TfUniversalMask {
			t.Fatalf("GetFlagsMask() = %#x, want %#x", mask, tx.TfUniversalMask)
		}
		if mask&1 == 0 {
			t.Fatal("GetFlagsMask() permits undefined flag 0x1")
		}
	}
}

func TestMPTokenIssuanceSetRejectsTransferFeeOnConfidentialIssuance(t *testing.T) {
	issuer := [20]byte{1}
	id := keylet.MakeMPTID(3, issuer)
	issuanceKey := keylet.MPTIssuance(id)
	view := &confidentialPreclaimView{
		entries: map[[32]byte][]byte{
			issuanceKey.Key: mustSerializeConfidentialIssuance(t, &state.MPTokenIssuanceData{
				Issuer:   issuer,
				Sequence: 3,
				Flags:    entry.LsfMPTCanHoldConfidentialBalance | entry.LsfMPTCanTransfer,
			}),
		},
		rules: amendment.NewRulesBuilder().
			Enable(amendment.FeatureDynamicMPT).
			Enable(amendment.FeatureConfidentialTransfer).
			Build(),
	}
	fee := uint16(1)
	transaction := NewMPTokenIssuanceSet(state.EncodeAccountIDSafe(issuer), confidentialMPTIDString(id))
	transaction.TransferFee = &fee
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TecNO_PERMISSION {
		t.Fatalf("Preclaim() = %v, want %v", got, ter.TecNO_PERMISSION)
	}
}

func TestMPTokenIssuanceSetChecksDomainBeforeEncryptionKeyDuplication(t *testing.T) {
	issuer := [20]byte{1}
	id := keylet.MakeMPTID(3, issuer)
	issuanceKey := keylet.MPTIssuance(id)
	view := &confidentialPreclaimView{
		entries: map[[32]byte][]byte{
			issuanceKey.Key: mustSerializeConfidentialIssuance(t, &state.MPTokenIssuanceData{
				Issuer:              issuer,
				Sequence:            3,
				Flags:               entry.LsfMPTRequireAuth | entry.LsfMPTCanHoldConfidentialBalance,
				IssuerEncryptionKey: []byte{1},
			}),
		},
		rules: amendment.NewRulesBuilder().
			Enable(amendment.FeatureDynamicMPT).
			Enable(amendment.FeatureConfidentialTransfer).
			Build(),
	}
	domain := strings.Repeat("01", 32)
	issuerKey := "02"
	transaction := NewMPTokenIssuanceSet(state.EncodeAccountIDSafe(issuer), confidentialMPTIDString(id))
	transaction.DomainID = &domain
	transaction.hasDomainID = true
	transaction.IssuerEncryptionKey = &issuerKey

	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TecOBJECT_NOT_FOUND {
		t.Fatalf("Preclaim() = %v, want %v", got, ter.TecOBJECT_NOT_FOUND)
	}
}

func TestMPTokenUnauthorizeConfidentialHoldingWithGlobalOutstanding(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(3, issuer)
	issuanceKey := keylet.MPTIssuance(id)
	tokenKey := keylet.MPToken(issuanceKey.Key, holder)
	issuance := &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, Flags: entry.LsfMPTCanHoldConfidentialBalance,
		ConfidentialOutstandingAmount: 1,
	}
	token := &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id,
		ConfidentialBalanceInbox:    []byte{1},
		ConfidentialBalanceSpending: []byte{2},
	}
	view := &confidentialPreclaimView{
		entries: map[[32]byte][]byte{
			issuanceKey.Key: mustSerializeConfidentialIssuance(t, issuance),
			tokenKey.Key:    mustSerializeConfidentialHolding(t, token),
		},
		rules: amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build(),
	}
	transaction := NewMPTokenAuthorize(state.EncodeAccountIDSafe(holder), confidentialMPTIDString(id))
	transaction.Flags = ptrUint32AccountSet(MPTokenAuthorizeFlagUnauthorize)

	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TecHAS_OBLIGATIONS {
		t.Fatalf("Preclaim() = %v, want %v", got, ter.TecHAS_OBLIGATIONS)
	}

	view.rules = amendment.NewRulesBuilder().Build()
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TesSUCCESS {
		t.Fatalf("Preclaim() with ConfidentialTransfer disabled = %v, want %v", got, ter.TesSUCCESS)
	}
}
