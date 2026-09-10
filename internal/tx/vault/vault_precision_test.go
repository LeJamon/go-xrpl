package vault

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func precisionRules(fix340 bool) *amendment.Rules {
	b := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
	if fix340 {
		b.Enable(amendment.FeatureFixCleanup3_4_0)
	}
	return b.Build()
}

func precisionVaultID() (id [32]byte) {
	for i := range id {
		id[i] = 0x55
	}
	return id
}

func precisionVaultIDHex() string {
	id := precisionVaultID()
	return hex.EncodeToString(id[:])
}

func TestVaultDepositApplyMPTUpdatesAssetAndShareRails(t *testing.T) {
	view, deposit, assetID, depositorID := vaultDepositFixture(t, 0, true)
	rules := precisionRules(true)
	view.rules = rules

	var pseudoID [20]byte
	for i := range pseudoID {
		pseudoID[i] = 0x44
	}
	shareID := keylet.MakeMPTID(1, pseudoID)
	assetIssuer := deposit.Amount.Issuer
	assetHex := hex.EncodeToString(assetID[:])

	issuance := &state.MPTokenIssuanceData{
		Issuer:            pseudoID,
		Sequence:          1,
		OutstandingAmount: 1000,
	}
	view.data[keylet.MPTIssuance(shareID).Key] = mustIssuance(t, issuance)
	view.data[keylet.MPTokenByID(shareID, depositorID).Key] = mustToken(t, &state.MPTokenData{
		Account:           depositorID,
		MPTokenIssuanceID: shareID,
	})
	view.data[keylet.MPTokenByID(assetID, depositorID).Key] = mustToken(t, &state.MPTokenData{
		Account:           depositorID,
		MPTokenIssuanceID: assetID,
		MPTAmount:         100,
	})
	view.data[keylet.MPTokenByID(assetID, pseudoID).Key] = mustToken(t, &state.MPTokenData{
		Account:           pseudoID,
		MPTokenIssuanceID: assetID,
		MPTAmount:         1000,
	})

	depositor := state.EncodeAccountIDSafe(depositorID)
	ctx := buildArmsCtx(t, newMPTArmsView(), depositorID, rules)
	ctx.View = view
	deposit.Account = depositor
	deposit.Amount = state.NewMPTAmountWithIssuanceID(100, assetIssuer, assetHex)

	if got := deposit.Apply(ctx); got != ter.TesSUCCESS {
		t.Fatalf("Apply() = %v, want tesSUCCESS", got)
	}

	updated, err := parseVault(view.data[keylet.VaultByID(precisionVaultID()).Key])
	if err != nil {
		t.Fatalf("parse updated vault: %v", err)
	}
	total, err := vaultNumber(updated.AssetsTotal)
	if err != nil {
		t.Fatalf("parse AssetsTotal: %v", err)
	}
	available, err := vaultNumber(updated.AssetsAvailable)
	if err != nil {
		t.Fatalf("parse AssetsAvailable: %v", err)
	}
	wantTotal := state.NewXRPLNumber(1100, 0)
	if !total.Equal(wantTotal) || !available.Equal(wantTotal) {
		t.Fatalf("vault totals = (%s, %s), want (1100, 1100)", updated.AssetsTotal, updated.AssetsAvailable)
	}
	updatedIssuance, err := state.ParseMPTokenIssuance(view.data[keylet.MPTIssuance(shareID).Key])
	if err != nil {
		t.Fatalf("parse share issuance: %v", err)
	}
	if got, want := updatedIssuance.OutstandingAmount, uint64(1100); got != want {
		t.Fatalf("share outstanding = %d, want %d", got, want)
	}
	shares, err := state.ParseMPToken(view.data[keylet.MPTokenByID(shareID, depositorID).Key])
	if err != nil {
		t.Fatalf("parse holder shares: %v", err)
	}
	if got, want := shares.MPTAmount, uint64(100); got != want {
		t.Fatalf("holder shares = %d, want %d", got, want)
	}
	depositorAsset, err := state.ParseMPToken(view.data[keylet.MPTokenByID(assetID, depositorID).Key])
	if err != nil {
		t.Fatalf("parse depositor asset: %v", err)
	}
	vaultAsset, err := state.ParseMPToken(view.data[keylet.MPTokenByID(assetID, pseudoID).Key])
	if err != nil {
		t.Fatalf("parse vault asset: %v", err)
	}
	if depositorAsset.MPTAmount != 0 || vaultAsset.MPTAmount != 1100 {
		t.Fatalf("asset balances = (%d, %d), want (0, 1100)", depositorAsset.MPTAmount, vaultAsset.MPTAmount)
	}
}

func TestVaultDepositApplyIOUClampPreservesMintedShares(t *testing.T) {
	view := newVaultDepositView()
	rules := precisionRules(true)
	view.rules = rules
	var issuerID, depositorID, ownerID, pseudoID [20]byte
	for i := range issuerID {
		issuerID[i] = 0x11
		depositorID[i] = 0x22
		ownerID[i] = 0x33
		pseudoID[i] = 0x44
	}
	issuerAddr := vaultDepositAccount(t, view, issuerID, state.LsfDefaultRipple)
	depositorAddr := vaultDepositAccount(t, view, depositorID, 0)
	pseudoAddr := vaultDepositAccount(t, view, pseudoID, 0)
	line := func(accountAddr string, balance int64, exponent int) []byte {
		data, err := state.SerializeRippleState(&state.RippleState{
			Balance:   state.NewIssuedAmountFromValue(balance, exponent, "USD", issuerAddr),
			LowLimit:  state.NewIssuedAmountFromValue(1_000_000, 0, "USD", issuerAddr),
			HighLimit: state.NewIssuedAmountFromValue(1_000_000, 0, "USD", accountAddr),
		})
		if err != nil {
			t.Fatalf("serialize trust line: %v", err)
		}
		return data
	}
	view.data[keylet.Line(depositorID, issuerID, "USD").Key] = line(depositorAddr, -2, -9)
	view.data[keylet.Line(pseudoID, issuerID, "USD").Key] = line(pseudoAddr, 0, 0)

	shareID := keylet.MakeMPTID(1, pseudoID)
	shareTotal := uint64(2_000_000_000_000_001)
	view.data[keylet.MPTIssuance(shareID).Key] = mustIssuance(t, &state.MPTokenIssuanceData{
		Issuer:            pseudoID,
		Sequence:          1,
		OutstandingAmount: shareTotal,
	})
	view.data[keylet.MPTokenByID(shareID, depositorID).Key] = mustToken(t, &state.MPTokenData{
		Account:           depositorID,
		MPTokenIssuanceID: shareID,
	})
	vaultID := precisionVaultID()
	view.data[keylet.VaultByID(vaultID).Key] = mustVault(t, &vaultData{
		Owner:            ownerID,
		Account:          pseudoID,
		Asset:            tx.Asset{Currency: "USD", Issuer: issuerAddr},
		ShareMPTID:       shareID,
		AssetsTotal:      "1000000",
		AssetsAvailable:  "1000000",
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
	})
	deposit := NewVaultDeposit(
		depositorAddr,
		hex.EncodeToString(vaultID[:]),
		state.NewIssuedAmountFromValue(2, -9, "USD", issuerAddr),
	)
	ctx := buildArmsCtx(t, newMPTArmsView(), depositorID, rules)
	ctx.View = view

	if got := deposit.Apply(ctx); got != ter.TesSUCCESS {
		t.Fatalf("Apply() = %v, want tesSUCCESS", got)
	}
	updated, err := parseVault(view.data[keylet.VaultByID(vaultID).Key])
	if err != nil {
		t.Fatalf("parse updated vault: %v", err)
	}
	total, err := vaultNumber(updated.AssetsTotal)
	if err != nil {
		t.Fatalf("parse AssetsTotal: %v", err)
	}
	beforeTotal := state.NewXRPLNumber(1_000_000, 0)
	delta := total.Sub(beforeTotal)
	wantDelta := state.NewXRPLNumber(1, -9)
	if !delta.Equal(wantDelta) {
		t.Fatalf("AssetsTotal delta = %s, want %s", delta, wantDelta)
	}
	available, err := vaultNumber(updated.AssetsAvailable)
	if err != nil {
		t.Fatalf("parse AssetsAvailable: %v", err)
	}
	if !available.Sub(beforeTotal).Equal(wantDelta) {
		t.Fatalf("AssetsAvailable delta = %s, want %s", available.Sub(beforeTotal), wantDelta)
	}
	issuance, err := state.ParseMPTokenIssuance(view.data[keylet.MPTIssuance(shareID).Key])
	if err != nil {
		t.Fatalf("parse share issuance: %v", err)
	}
	if issuance.OutstandingAmount != shareTotal+4 {
		t.Fatalf("share outstanding = %d, want %d", issuance.OutstandingAmount, shareTotal+4)
	}
	shareValue := beforeTotal.Mul(state.NewXRPLNumber(int64(4), 0)).Div(state.NewXRPLNumber(int64(shareTotal), 0))
	if delta.Cmp(shareValue) > 0 {
		t.Fatalf("assets taken = %s exceeds minted share value %s", delta, shareValue)
	}
	discount := shareValue.Sub(delta)
	if discount.Cmp(state.NewXRPLNumber(1, -9)) >= 0 {
		t.Fatalf("share-value discount = %s, want less than one new-total ULP", discount)
	}
	shares, err := state.ParseMPToken(view.data[keylet.MPTokenByID(shareID, depositorID).Key])
	if err != nil {
		t.Fatalf("parse holder shares: %v", err)
	}
	if shares.MPTAmount != 4 {
		t.Fatalf("holder shares = %d, want 4", shares.MPTAmount)
	}
	depositorHolding, err := actualAssetHolding(view, depositorID, tx.Asset{Currency: "USD", Issuer: issuerAddr}, rules)
	if err != nil {
		t.Fatalf("read depositor holding: %v", err)
	}
	pseudoHolding, err := actualAssetHolding(view, pseudoID, tx.Asset{Currency: "USD", Issuer: issuerAddr}, rules)
	if err != nil {
		t.Fatalf("read vault holding: %v", err)
	}
	if depositorHolding.String() != "0.000000001" || pseudoHolding.String() != "0.000000001" {
		t.Fatalf("IOU holdings = (%s, %s), want (1e-9, 1e-9)", depositorHolding, pseudoHolding)
	}
}

func TestVaultWithdrawPrecisionLossPreservesStateForXRPAndIOU(t *testing.T) {
	cases := []struct {
		name   string
		asset  tx.Asset
		amount tx.Amount
	}{
		{name: "XRP", asset: tx.Asset{Currency: "XRP"}, amount: tx.NewXRPAmount(1)},
		{name: "IOU", asset: tx.Asset{Currency: "USD", Issuer: state.EncodeAccountIDSafe([20]byte{0x21})}, amount: state.NewIssuedAmountFromValue(1, 0, "USD", state.EncodeAccountIDSafe([20]byte{0x21}))},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			view := newMPTArmsView()
			var holderID [20]byte
			holderID[19] = 1
			rules := precisionRules(true)
			ctx := buildArmsCtx(t, view, holderID, rules)
			var vaultID [32]byte
			vaultID[31] = byte(len(test.name))
			shareID := keylet.MakeMPTID(1, holderID)
			view.data[keylet.VaultByID(vaultID).Key] = mustVault(t, &vaultData{
				Owner:            holderID,
				Account:          [20]byte{0x33},
				ShareMPTID:       shareID,
				Asset:            test.asset,
				AssetsTotal:      "3",
				AssetsAvailable:  "3",
				WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
			})
			view.data[keylet.MPTIssuance(shareID).Key] = mustIssuance(t, &state.MPTokenIssuanceData{
				Issuer:            holderID,
				Sequence:          1,
				OutstandingAmount: 2,
			})
			before := append([]byte(nil), view.data[keylet.VaultByID(vaultID).Key]...)
			withdraw := NewVaultWithdraw(ctx.Account.Account, hex.EncodeToString(vaultID[:]), test.amount)
			if got := withdraw.Apply(ctx); got != ter.TecPRECISION_LOSS {
				t.Fatalf("Apply() = %v, want tecPRECISION_LOSS", got)
			}
			if got := view.data[keylet.VaultByID(vaultID).Key]; string(got) != string(before) {
				t.Fatal("precision-loss withdrawal changed vault state")
			}
		})
	}
}

func TestVaultWithdrawMPTRequestedAmountHonorsCleanup340(t *testing.T) {
	for _, fix340 := range []bool{false, true} {
		name := "amendment off"
		if fix340 {
			name = "amendment on"
		}
		t.Run(name, func(t *testing.T) {
			f := newVaultClawbackFixture(t, 2)
			rules := precisionRules(fix340)
			f.ctx.Config.Rules = rules
			f.ctx.AccountID = f.holderID
			f.ctx.Account = &state.AccountRoot{
				Account:    clawbackTestAddress(t, f.holderID),
				Balance:    1_000_000_000,
				Sequence:   1,
				OwnerCount: 1,
			}
			f.view.data[keylet.Account(f.holderID).Key] = mustAccountRoot(t, f.holderID, 0)
			f.view.data[keylet.MPTokenByID(f.assetMPTID, f.holderID).Key] = mustToken(t, &state.MPTokenData{
				Account:           f.holderID,
				MPTokenIssuanceID: f.assetMPTID,
			})
			vd := &vaultData{
				Owner:            f.ownerID,
				Account:          f.pseudoID,
				ShareMPTID:       f.shareMPTID,
				AssetIsMPT:       true,
				AssetMPTID:       f.assetMPTID,
				WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
				AssetsTotal:      "3",
				AssetsAvailable:  "3",
			}
			f.view.data[f.vaultKey.Key] = mustVault(t, vd)
			shareIssuance := clawbackTestIssuance(t, f.view, f.shareMPTID)
			shareIssuance.OutstandingAmount = 2
			f.view.data[keylet.MPTIssuance(f.shareMPTID).Key] = mustIssuance(t, shareIssuance)
			asset := state.NewMPTAmountWithIssuanceID(1, clawbackTestAddress(t, f.assetIssuerID), hex.EncodeToString(f.assetMPTID[:]))
			withdraw := NewVaultWithdraw(f.ctx.Account.Account, precisionVaultIDHex(), asset)
			if got := withdraw.Apply(f.ctx); fix340 && got != ter.TecPRECISION_LOSS {
				t.Fatalf("Apply() = %v, want tecPRECISION_LOSS", got)
			} else if !fix340 && got != ter.TesSUCCESS {
				t.Fatalf("Apply() = %v, want tesSUCCESS", got)
			}
			updated := clawbackTestIssuance(t, f.view, f.shareMPTID)
			shares := clawbackTestToken(t, f.view, f.shareMPTID, f.holderID)
			assetToken := clawbackTestToken(t, f.view, f.assetMPTID, f.pseudoID)
			if fix340 {
				if updated.OutstandingAmount != 2 || shares.MPTAmount != 2 || assetToken.MPTAmount != 100 {
					t.Fatalf("precision-loss state = (%d, %d, %d), want (2, 2, 100)", updated.OutstandingAmount, shares.MPTAmount, assetToken.MPTAmount)
				}
				return
			}
			if updated.OutstandingAmount != 1 || shares.MPTAmount != 1 || assetToken.MPTAmount != 98 {
				t.Fatalf("legacy state = (%d, %d, %d), want (1, 1, 98)", updated.OutstandingAmount, shares.MPTAmount, assetToken.MPTAmount)
			}
		})
	}
}

func TestVaultClawbackMPTRequestedAmountHonorsCleanup340(t *testing.T) {
	for _, fix340 := range []bool{false, true} {
		name := "amendment off"
		if fix340 {
			name = "amendment on"
		}
		t.Run(name, func(t *testing.T) {
			f := newVaultClawbackFixture(t, 2)
			f.ctx.Config.Rules = precisionRules(fix340)
			vd := &vaultData{
				Owner:            f.ownerID,
				Account:          f.pseudoID,
				ShareMPTID:       f.shareMPTID,
				AssetIsMPT:       true,
				AssetMPTID:       f.assetMPTID,
				WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
				AssetsTotal:      "3",
				AssetsAvailable:  "3",
			}
			f.view.data[f.vaultKey.Key] = mustVault(t, vd)
			shareIssuance := clawbackTestIssuance(t, f.view, f.shareMPTID)
			shareIssuance.OutstandingAmount = 2
			f.view.data[keylet.MPTIssuance(f.shareMPTID).Key] = mustIssuance(t, shareIssuance)
			amount := state.NewMPTAmountWithIssuanceID(
				1,
				clawbackTestAddress(t, f.assetIssuerID),
				hex.EncodeToString(f.assetMPTID[:]),
			)
			f.txn.Amount = &amount
			beforeVault := append([]byte(nil), f.view.data[f.vaultKey.Key]...)
			beforeAssetIssuance := clawbackTestIssuance(t, f.view, f.assetMPTID)
			if got := f.txn.Apply(f.ctx); fix340 && got != ter.TecPRECISION_LOSS {
				t.Fatalf("Apply() = %v, want tecPRECISION_LOSS", got)
			} else if !fix340 && got != ter.TesSUCCESS {
				t.Fatalf("Apply() = %v, want tesSUCCESS", got)
			}
			if fix340 {
				if got := f.view.data[f.vaultKey.Key]; string(got) != string(beforeVault) {
					t.Fatal("precision-loss clawback changed vault state")
				}
				assetIssuance := clawbackTestIssuance(t, f.view, f.assetMPTID)
				if assetIssuance.OutstandingAmount != beforeAssetIssuance.OutstandingAmount {
					t.Fatal("precision-loss clawback changed asset issuance")
				}
				return
			}
			updated := clawbackTestIssuance(t, f.view, f.shareMPTID)
			shares := clawbackTestToken(t, f.view, f.shareMPTID, f.holderID)
			assetToken := clawbackTestToken(t, f.view, f.assetMPTID, f.pseudoID)
			assetIssuance := clawbackTestIssuance(t, f.view, f.assetMPTID)
			if updated.OutstandingAmount != 1 || shares.MPTAmount != 1 || assetToken.MPTAmount != 98 || assetIssuance.OutstandingAmount != 98 {
				t.Fatalf("legacy state = (%d, %d, %d, %d), want (1, 1, 98, 98)", updated.OutstandingAmount, shares.MPTAmount, assetToken.MPTAmount, assetIssuance.OutstandingAmount)
			}
		})
	}
}
