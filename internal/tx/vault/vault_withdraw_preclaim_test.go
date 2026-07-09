package vault

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
)

// roView serves crafted SLE bytes by keylet; mutating methods are no-ops.
type roView struct {
	data map[[32]byte][]byte
}

func (v roView) Read(k keylet.Keylet) ([]byte, error)      { return v.data[k.Key], nil }
func (v roView) Exists(k keylet.Keylet) (bool, error)      { _, ok := v.data[k.Key]; return ok, nil }
func (v roView) Insert(k keylet.Keylet, data []byte) error { return nil }
func (v roView) Update(k keylet.Keylet, data []byte) error { return nil }
func (v roView) Erase(k keylet.Keylet) error               { return nil }
func (v roView) AdjustDropsDestroyed(drops.XRPAmount)      {}
func (v roView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	return nil
}
func (v roView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v roView) TxExists(txID [32]byte) bool { return false }
func (v roView) Rules() *amendment.Rules     { return nil }
func (v roView) LedgerSeq() uint32           { return 0 }

func fill20(b byte) [20]byte {
	var id [20]byte
	for i := range id {
		id[i] = b
	}
	return id
}

// TestVaultWithdraw_ShareLimitEnforced asserts the fixCleanup3_1_3 arm: a
// share-denominated withdrawal delivered to a third party whose IOU trust limit
// would be exceeded is rejected (tecNO_LINE) once the amendment converts the
// shares to the equivalent asset amount. Pre-amendment the (MPT) shares skip the
// limit check, so the withdrawal is admitted.
func TestVaultWithdraw_ShareLimitEnforced(t *testing.T) {
	issuerID := fill20(0xAA)
	submitterID := fill20(0xBB)
	dstID := fill20(0xCC)
	pseudoID := fill20(0xDD)
	issuerAddr, _ := state.EncodeAccountID(issuerID)
	submitterAddr, _ := state.EncodeAccountID(submitterID)
	dstAddr, _ := state.EncodeAccountID(dstID)

	var vid [32]byte
	for i := range vid {
		vid[i] = 0xE1
	}
	vidHex := strings.ToUpper(hex.EncodeToString(vid[:]))
	var shareMPTID [24]byte
	for i := range shareMPTID {
		shareMPTID[i] = 0xF2
	}
	shareHex := strings.ToUpper(hex.EncodeToString(shareMPTID[:]))

	vaultBytes, err := serializeVault(&vaultData{
		Sequence:         1,
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
		Owner:            submitterID,
		Account:          pseudoID,
		Asset:            tx.Asset{Currency: "USD", Issuer: issuerAddr},
		ShareMPTID:       shareMPTID,
		AssetsTotal:      "1000",
		AssetsAvailable:  "1000",
	})
	if err != nil {
		t.Fatalf("serializeVault: %v", err)
	}
	issBytes, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:            issuerID,
		OutstandingAmount: 1000,
	})
	if err != nil {
		t.Fatalf("serializeMPTokenIssuance: %v", err)
	}
	dstBytes, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account: dstAddr, Balance: 1_000_000_000, Sequence: 1,
	})
	if err != nil {
		t.Fatalf("serializeAccountRoot(dst): %v", err)
	}
	issAcctBytes, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account: issuerAddr, Balance: 1_000_000_000, Sequence: 1,
	})
	if err != nil {
		t.Fatalf("serializeAccountRoot(issuer): %v", err)
	}
	// dst (0xCC) is the HIGH account vs issuer (0xAA); its trust limit is 10 USD
	// and the balance is zero.
	rsBytes, err := state.SerializeRippleState(&state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(0, -100, "USD", issuerAddr),
		LowLimit:  state.NewIssuedAmountFromValue(0, -100, "USD", issuerAddr),
		HighLimit: state.NewIssuedAmountFromValue(10, 0, "USD", dstAddr),
	})
	if err != nil {
		t.Fatalf("serializeRippleState: %v", err)
	}

	view := roView{data: map[[32]byte][]byte{
		keylet.VaultByID(vid).Key:               vaultBytes,
		keylet.MPTIssuance(shareMPTID).Key:      issBytes,
		keylet.Account(dstID).Key:               dstBytes,
		keylet.Account(issuerID).Key:            issAcctBytes,
		keylet.Line(dstID, issuerID, "USD").Key: rsBytes,
	}}

	// Withdraw 100 shares (worth 100 USD) to the third-party destination.
	newWithdraw := func() *VaultWithdraw {
		wd := NewVaultWithdraw(submitterAddr, vidHex, state.NewMPTAmountWithIssuanceID(100, issuerAddr, shareHex))
		wd.Destination = dstAddr
		return wd
	}

	fixOn := tx.EngineConfig{Rules: amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault, amendment.FeatureFixCleanup3_1_3,
	})}
	fixOff := tx.EngineConfig{Rules: amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
	})}

	if got := newWithdraw().Preclaim(view, fixOn); got != ter.TecNO_LINE {
		t.Errorf("fixCleanup3_1_3 ON: got %v, want tecNO_LINE", got)
	}
	if got := newWithdraw().Preclaim(view, fixOff); got != ter.TesSUCCESS {
		t.Errorf("fixCleanup3_1_3 OFF: got %v, want tesSUCCESS", got)
	}
}
