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
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// roView serves crafted SLE bytes by keylet; mutating methods are no-ops.
type roView struct {
	data  map[[32]byte][]byte
	rules *amendment.Rules
}

func (v roView) Read(k keylet.Keylet) ([]byte, error)       { return v.data[k.Key], nil }
func (v roView) Exists(k keylet.Keylet) (bool, error)       { _, ok := v.data[k.Key]; return ok, nil }
func (v roView) Insert(k keylet.Keylet, data []byte) error  { return nil }
func (v roView) Update(k keylet.Keylet, data []byte) error  { return nil }
func (v roView) Erase(k keylet.Keylet) error                { return nil }
func (v roView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (v roView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	return nil
}
func (v roView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v roView) TxExists(txID [32]byte) (bool, error) { return false, nil }
func (v roView) Rules() *amendment.Rules              { return v.rules }
func (v roView) LedgerSeq() uint32                    { return 0 }

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

func mustAccountRoot(t *testing.T, id [20]byte, flags uint32) []byte {
	t.Helper()
	address, err := state.EncodeAccountID(id)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	raw, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  address,
		Balance:  1_000_000_000,
		Sequence: 1,
		Flags:    flags,
	})
	if err != nil {
		t.Fatalf("serialize account: %v", err)
	}
	return raw
}

func mustVaultAccountRoot(t *testing.T, id [20]byte, vaultID [32]byte) []byte {
	t.Helper()
	address, err := state.EncodeAccountID(id)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	raw, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  address,
		Sequence: 0,
		VaultID:  vaultID,
	})
	if err != nil {
		t.Fatalf("serialize vault account: %v", err)
	}
	return raw
}

func mustVault(t *testing.T, vd *vaultData) []byte {
	t.Helper()
	raw, err := serializeVault(vd)
	if err != nil {
		t.Fatalf("serialize vault: %v", err)
	}
	return raw
}

func mustIssuance(t *testing.T, issuance *state.MPTokenIssuanceData) []byte {
	t.Helper()
	raw, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		t.Fatalf("serialize issuance: %v", err)
	}
	return raw
}

func mustToken(t *testing.T, token *state.MPTokenData) []byte {
	t.Helper()
	raw, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatalf("serialize token: %v", err)
	}
	return raw
}

func TestVaultWithdraw_CanTransferPrecedesPolicy(t *testing.T) {
	issuerID := fill20(0x11)
	submitterID := fill20(0x22)
	vaultAccountID := fill20(0x33)
	issuerAddr, _ := state.EncodeAccountID(issuerID)
	submitterAddr, _ := state.EncodeAccountID(submitterID)

	var vaultID [32]byte
	vaultID[31] = 1
	vaultIDHex := strings.ToUpper(hex.EncodeToString(vaultID[:]))
	assetID := keylet.MakeMPTID(7, issuerID)
	shareID := keylet.MakeMPTID(1, vaultAccountID)
	assetHex := strings.ToUpper(hex.EncodeToString(assetID[:]))

	baseData := map[[32]byte][]byte{
		keylet.VaultByID(vaultID).Key: mustVault(t, &vaultData{
			WithdrawalPolicy: 99,
			Owner:            submitterID,
			Account:          vaultAccountID,
			AssetIsMPT:       true,
			AssetMPTID:       assetID,
			ShareMPTID:       shareID,
			AssetsTotal:      "100",
			AssetsAvailable:  "100",
		}),
		keylet.MPTIssuance(assetID).Key: mustIssuance(t, &state.MPTokenIssuanceData{
			Issuer:   issuerID,
			Sequence: 7,
		}),
		keylet.MPTIssuance(shareID).Key: mustIssuance(t, &state.MPTokenIssuanceData{
			Issuer:            vaultAccountID,
			Sequence:          1,
			OutstandingAmount: 100,
		}),
		keylet.Account(issuerID).Key:    mustAccountRoot(t, issuerID, 0),
		keylet.Account(submitterID).Key: mustAccountRoot(t, submitterID, 0),
	}
	withdraw := NewVaultWithdraw(
		submitterAddr,
		vaultIDHex,
		state.NewMPTAmountWithIssuanceID(1, issuerAddr, assetHex),
	)

	t.Run("pre-fix transfer denial wins", func(t *testing.T) {
		rules := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
		view := roView{data: baseData, rules: rules}
		if got := withdraw.Preclaim(view, tx.EngineConfig{Rules: rules}); got != ter.TecNO_AUTH {
			t.Fatalf("got %v, want tecNO_AUTH", got)
		}
	})

	t.Run("fix waives MPT CanTransfer then policy wins", func(t *testing.T) {
		rules := amendment.NewRules([][32]byte{
			amendment.FeatureSingleAssetVault,
			amendment.FeatureFixCleanup3_2_0,
		})
		view := roView{data: baseData, rules: rules}
		if got := withdraw.Preclaim(view, tx.EngineConfig{Rules: rules}); got != ter.TefINTERNAL {
			t.Fatalf("got %v, want tefINTERNAL", got)
		}
	})
}

func TestVaultWithdraw_IOUNoRipplePrecedesPolicy(t *testing.T) {
	issuerID := fill20(0x41)
	submitterID := fill20(0x42)
	vaultAccountID := fill20(0x43)
	issuerAddr, _ := state.EncodeAccountID(issuerID)
	submitterAddr, _ := state.EncodeAccountID(submitterID)
	var vaultID [32]byte
	vaultID[31] = 2
	shareID := keylet.MakeMPTID(1, vaultAccountID)
	rules := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	view := roView{rules: rules, data: map[[32]byte][]byte{
		keylet.VaultByID(vaultID).Key: mustVault(t, &vaultData{
			WithdrawalPolicy: 99,
			Owner:            submitterID,
			Account:          vaultAccountID,
			Asset:            tx.Asset{Currency: "USD", Issuer: issuerAddr},
			ShareMPTID:       shareID,
			AssetsTotal:      "100",
			AssetsAvailable:  "100",
		}),
		keylet.Account(issuerID).Key:    mustAccountRoot(t, issuerID, 0),
		keylet.Account(submitterID).Key: mustAccountRoot(t, submitterID, 0),
	}}
	withdraw := NewVaultWithdraw(
		submitterAddr,
		strings.ToUpper(hex.EncodeToString(vaultID[:])),
		state.NewIssuedAmountFromValue(1, 0, "USD", issuerAddr),
	)
	if got := withdraw.Preclaim(view, tx.EngineConfig{Rules: rules}); got != ter.TerNO_RIPPLE {
		t.Fatalf("got %v, want terNO_RIPPLE", got)
	}
}

func TestVaultWithdraw_DestinationAuthorization(t *testing.T) {
	issuerID := fill20(0x51)
	submitterID := fill20(0x52)
	destinationID := fill20(0x53)
	vaultAccountID := fill20(0x54)
	issuerAddr, _ := state.EncodeAccountID(issuerID)
	submitterAddr, _ := state.EncodeAccountID(submitterID)
	destinationAddr, _ := state.EncodeAccountID(destinationID)
	shareID := keylet.MakeMPTID(1, vaultAccountID)
	rules := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})

	t.Run("IOU destination needs trust line", func(t *testing.T) {
		var vaultID [32]byte
		vaultID[31] = 3
		view := roView{rules: rules, data: map[[32]byte][]byte{
			keylet.VaultByID(vaultID).Key: mustVault(t, &vaultData{
				WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
				Owner:            submitterID,
				Account:          vaultAccountID,
				Asset:            tx.Asset{Currency: "USD", Issuer: issuerAddr},
				ShareMPTID:       shareID,
				AssetsTotal:      "100",
				AssetsAvailable:  "100",
			}),
			keylet.MPTIssuance(shareID).Key: mustIssuance(t, &state.MPTokenIssuanceData{
				Issuer:            vaultAccountID,
				Sequence:          1,
				OutstandingAmount: 100,
			}),
			keylet.Account(issuerID).Key:      mustAccountRoot(t, issuerID, state.LsfDefaultRipple),
			keylet.Account(submitterID).Key:   mustAccountRoot(t, submitterID, 0),
			keylet.Account(destinationID).Key: mustAccountRoot(t, destinationID, 0),
		}}
		shareHex := strings.ToUpper(hex.EncodeToString(shareID[:]))
		withdraw := NewVaultWithdraw(
			submitterAddr,
			strings.ToUpper(hex.EncodeToString(vaultID[:])),
			state.NewMPTAmountWithIssuanceID(1, issuerAddr, shareHex),
		)
		withdraw.Destination = destinationAddr
		if got := withdraw.Preclaim(view, tx.EngineConfig{Rules: rules}); got != ter.TecNO_LINE {
			t.Fatalf("got %v, want tecNO_LINE", got)
		}
	})

	t.Run("MPT destination needs holding", func(t *testing.T) {
		var vaultID [32]byte
		vaultID[31] = 4
		assetID := keylet.MakeMPTID(9, issuerID)
		view := roView{rules: rules, data: map[[32]byte][]byte{
			keylet.VaultByID(vaultID).Key: mustVault(t, &vaultData{
				WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
				Owner:            submitterID,
				Account:          vaultAccountID,
				AssetIsMPT:       true,
				AssetMPTID:       assetID,
				ShareMPTID:       shareID,
				AssetsTotal:      "100",
				AssetsAvailable:  "100",
			}),
			keylet.MPTIssuance(assetID).Key: mustIssuance(t, &state.MPTokenIssuanceData{
				Issuer:   issuerID,
				Sequence: 9,
				Flags:    entry.LsfMPTCanTransfer,
			}),
			keylet.MPTIssuance(shareID).Key: mustIssuance(t, &state.MPTokenIssuanceData{
				Issuer:            vaultAccountID,
				Sequence:          1,
				OutstandingAmount: 100,
			}),
			keylet.Account(issuerID).Key:      mustAccountRoot(t, issuerID, 0),
			keylet.Account(submitterID).Key:   mustAccountRoot(t, submitterID, 0),
			keylet.Account(destinationID).Key: mustAccountRoot(t, destinationID, 0),
		}}
		assetHex := strings.ToUpper(hex.EncodeToString(assetID[:]))
		withdraw := NewVaultWithdraw(
			submitterAddr,
			strings.ToUpper(hex.EncodeToString(vaultID[:])),
			state.NewMPTAmountWithIssuanceID(1, issuerAddr, assetHex),
		)
		withdraw.Destination = destinationAddr
		if got := withdraw.Preclaim(view, tx.EngineConfig{Rules: rules}); got != ter.TecNO_AUTH {
			t.Fatalf("got %v, want tecNO_AUTH", got)
		}
	})
}

func TestVaultWithdraw_DepositorShareLock(t *testing.T) {
	submitterID := fill20(0x61)
	vaultAccountID := fill20(0x62)
	submitterAddr, _ := state.EncodeAccountID(submitterID)
	shareID := keylet.MakeMPTID(1, vaultAccountID)
	var vaultID [32]byte
	vaultID[31] = 5
	rules := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	base := map[[32]byte][]byte{
		keylet.VaultByID(vaultID).Key: mustVault(t, &vaultData{
			WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
			Owner:            submitterID,
			Account:          vaultAccountID,
			Asset:            tx.Asset{Currency: "XRP"},
			ShareMPTID:       shareID,
			AssetsTotal:      "100",
			AssetsAvailable:  "100",
		}),
		keylet.Account(submitterID).Key: mustAccountRoot(t, submitterID, 0),
	}
	withdraw := NewVaultWithdraw(
		submitterAddr,
		strings.ToUpper(hex.EncodeToString(vaultID[:])),
		tx.NewXRPAmount(1),
	)

	tests := []struct {
		name          string
		issuanceFlags uint32
		tokenFlags    uint32
	}{
		{name: "issuance locked", issuanceFlags: entry.LsfMPTLocked},
		{name: "holder locked", tokenFlags: entry.LsfMPTLocked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := make(map[[32]byte][]byte, len(base)+2)
			for key, raw := range base {
				data[key] = raw
			}
			data[keylet.MPTIssuance(shareID).Key] = mustIssuance(t, &state.MPTokenIssuanceData{
				Issuer:            vaultAccountID,
				Sequence:          1,
				OutstandingAmount: 100,
				Flags:             test.issuanceFlags,
			})
			data[keylet.MPTokenByID(shareID, submitterID).Key] = mustToken(t, &state.MPTokenData{
				Account:           submitterID,
				MPTokenIssuanceID: shareID,
				MPTAmount:         100,
				Flags:             test.tokenFlags,
			})
			view := roView{data: data, rules: rules}
			if got := withdraw.Preclaim(view, tx.EngineConfig{Rules: rules}); got != ter.TecLOCKED {
				t.Fatalf("got %v, want tecLOCKED", got)
			}
		})
	}
}

func TestVaultWithdraw_ShareInheritsVaultHoldingFreeze(t *testing.T) {
	issuerID := fill20(0x68)
	submitterID := fill20(0x69)
	vaultAccountID := fill20(0x67)
	issuerAddr, _ := state.EncodeAccountID(issuerID)
	submitterAddr, _ := state.EncodeAccountID(submitterID)
	vaultAccountAddr, _ := state.EncodeAccountID(vaultAccountID)
	shareID := keylet.MakeMPTID(1, vaultAccountID)
	var vaultID [32]byte
	vaultID[31] = 7

	frozenLine, err := state.SerializeRippleState(&state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(100, 0, "USD", issuerAddr),
		LowLimit:  state.NewIssuedAmountFromValue(0, -100, "USD", vaultAccountAddr),
		HighLimit: state.NewIssuedAmountFromValue(0, -100, "USD", issuerAddr),
		Flags:     state.LsfHighFreeze,
	})
	if err != nil {
		t.Fatalf("serialize frozen line: %v", err)
	}
	rules := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	view := roView{rules: rules, data: map[[32]byte][]byte{
		keylet.VaultByID(vaultID).Key: mustVault(t, &vaultData{
			WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
			Owner:            submitterID,
			Account:          vaultAccountID,
			Asset:            tx.Asset{Currency: "USD", Issuer: issuerAddr},
			ShareMPTID:       shareID,
			AssetsTotal:      "100",
			AssetsAvailable:  "100",
		}),
		keylet.MPTIssuance(shareID).Key: mustIssuance(t, &state.MPTokenIssuanceData{
			Issuer:            vaultAccountID,
			Sequence:          1,
			OutstandingAmount: 100,
		}),
		keylet.MPTokenByID(shareID, submitterID).Key: mustToken(t, &state.MPTokenData{
			Account:           submitterID,
			MPTokenIssuanceID: shareID,
			MPTAmount:         100,
		}),
		keylet.Account(issuerID).Key:                     mustAccountRoot(t, issuerID, state.LsfDefaultRipple),
		keylet.Account(submitterID).Key:                  mustAccountRoot(t, submitterID, 0),
		keylet.Account(vaultAccountID).Key:               mustVaultAccountRoot(t, vaultAccountID, vaultID),
		keylet.Line(vaultAccountID, issuerID, "USD").Key: frozenLine,
	}}
	withdraw := NewVaultWithdraw(
		submitterAddr,
		strings.ToUpper(hex.EncodeToString(vaultID[:])),
		state.NewIssuedAmountFromValue(1, 0, "USD", issuerAddr),
	)
	if got := withdraw.Preclaim(view, tx.EngineConfig{Rules: rules}); got != ter.TecLOCKED {
		t.Fatalf("got %v, want tecLOCKED", got)
	}
}

func TestVaultWithdraw_AssetWithdrawalRoundsSharesToNearest(t *testing.T) {
	assetsTotal, _ := vaultNumber("6666.5")
	shareTotal := state.NewXRPLNumber(5_000_000_000, 0)
	assets := state.NewXRPLNumber(1000, 0)
	shares, payout := assetWithdrawalAmounts(
		assetsTotal,
		state.NewXRPLNumber(0, 0),
		shareTotal,
		assets,
		false,
	)
	if got := shares.ToInt64WithMode(state.RoundTowardsZero); got != 750_018_750 {
		t.Fatalf("shares = %d, want 750018750", got)
	}
	wantPayout := assetsTotal.Mul(state.NewXRPLNumber(750_018_750, 0)).Div(shareTotal)
	if payout.Cmp(wantPayout) != 0 {
		t.Fatalf("payout = %s, want %s", payout.String(), wantPayout.String())
	}
}

func TestVaultWithdraw_SoleShareholderWaivesLossInPreclaimLimit(t *testing.T) {
	issuerID := fill20(0x71)
	submitterID := fill20(0x72)
	destinationID := fill20(0x73)
	vaultAccountID := fill20(0x74)
	issuerAddr, _ := state.EncodeAccountID(issuerID)
	submitterAddr, _ := state.EncodeAccountID(submitterID)
	destinationAddr, _ := state.EncodeAccountID(destinationID)
	shareID := keylet.MakeMPTID(1, vaultAccountID)
	shareHex := strings.ToUpper(hex.EncodeToString(shareID[:]))
	var vaultID [32]byte
	vaultID[31] = 6

	lineRaw, err := state.SerializeRippleState(&state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(0, -100, "USD", issuerAddr),
		LowLimit:  state.NewIssuedAmountFromValue(0, -100, "USD", issuerAddr),
		HighLimit: state.NewIssuedAmountFromValue(15, 0, "USD", destinationAddr),
	})
	if err != nil {
		t.Fatalf("serialize trust line: %v", err)
	}
	base := map[[32]byte][]byte{
		keylet.VaultByID(vaultID).Key: mustVault(t, &vaultData{
			WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
			Owner:            submitterID,
			Account:          vaultAccountID,
			Asset:            tx.Asset{Currency: "USD", Issuer: issuerAddr},
			ShareMPTID:       shareID,
			AssetsTotal:      "100",
			AssetsAvailable:  "100",
			LossUnrealized:   "50",
		}),
		keylet.MPTIssuance(shareID).Key: mustIssuance(t, &state.MPTokenIssuanceData{
			Issuer:            vaultAccountID,
			Sequence:          1,
			OutstandingAmount: 100,
		}),
		keylet.MPTokenByID(shareID, submitterID).Key: mustToken(t, &state.MPTokenData{
			Account:           submitterID,
			MPTokenIssuanceID: shareID,
			MPTAmount:         100,
		}),
		keylet.Account(issuerID).Key:                    mustAccountRoot(t, issuerID, state.LsfDefaultRipple),
		keylet.Account(submitterID).Key:                 mustAccountRoot(t, submitterID, 0),
		keylet.Account(destinationID).Key:               mustAccountRoot(t, destinationID, 0),
		keylet.Line(destinationID, issuerID, "USD").Key: lineRaw,
	}
	withdraw := NewVaultWithdraw(
		submitterAddr,
		strings.ToUpper(hex.EncodeToString(vaultID[:])),
		state.NewMPTAmountWithIssuanceID(20, issuerAddr, shareHex),
	)
	withdraw.Destination = destinationAddr

	withoutFix320 := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureFixCleanup3_1_3,
	})
	if got := withdraw.Preclaim(
		roView{data: base, rules: withoutFix320},
		tx.EngineConfig{Rules: withoutFix320},
	); got != ter.TesSUCCESS {
		t.Fatalf("pre-fix got %v, want tesSUCCESS", got)
	}

	withFix320 := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureFixCleanup3_1_3,
		amendment.FeatureFixCleanup3_2_0,
	})
	if got := withdraw.Preclaim(
		roView{data: base, rules: withFix320},
		tx.EngineConfig{Rules: withFix320},
	); got != ter.TecNO_LINE {
		t.Fatalf("fixCleanup3_2_0 got %v, want tecNO_LINE", got)
	}
}
