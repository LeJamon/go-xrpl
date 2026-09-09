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

func TestVaultCleanupClampGrid(t *testing.T) {
	for _, tt := range []struct {
		name, total, delta, want string
		integral                 bool
		result                   ter.Result
	}{
		{"credit", "1000000", "2.5000000005", "2.5", false, ter.TesSUCCESS},
		{"debit", "1000000", "-2.50000000055", "2.5000000005", false, ter.TesSUCCESS},
		{"credit dust", "1000000", "0.0000000001", "0", false, ter.TecPRECISION_LOSS},
		{"debit dust", "1000000", "-0.00000000001", "0", false, ter.TecPRECISION_LOSS},
		{"cross decade", "999999.9999999999", "0.0000000009", "0.0000000001", false, ter.TesSUCCESS},
		{"XRP", "1000000", "-7", "7", true, ter.TesSUCCESS},
		{"MPT", "1000000", "7", "7", true, ter.TesSUCCESS},
	} {
		t.Run(tt.name, func(t *testing.T) {
			total, err := vaultNumber(tt.total)
			if err != nil {
				t.Fatal(err)
			}
			delta, err := vaultNumber(tt.delta)
			if err != nil {
				t.Fatal(err)
			}
			want, err := vaultNumber(tt.want)
			if err != nil {
				t.Fatal(err)
			}
			got, result := clampToAssetsTotalScale(total, delta, tt.integral)
			if result != tt.result || got.Cmp(want) != 0 {
				t.Fatalf("got %s/%v, want %s/%v", got.String(), result, want.String(), tt.result)
			}
		})
	}
}

func TestVaultCleanupExistingIOUHoldingPrecedesFreeze(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, exists := range []bool{false, true} {
			view := newMPTArmsView()
			issuer, holder := [20]byte{1}, [20]byte{2}
			builder := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
			if cleanup {
				builder.Enable(amendment.FeatureFixCleanup3_4_0)
			}
			ctx := buildArmsCtx(t, view, holder, builder.Build())
			issuerAddress := state.EncodeAccountIDSafe(issuer)
			raw, err := state.SerializeAccountRoot(&state.AccountRoot{Account: issuerAddress, Flags: state.LsfGlobalFreeze})
			if err != nil {
				t.Fatal(err)
			}
			view.data[keylet.Account(issuer).Key] = raw
			if exists {
				view.data[keylet.Line(issuer, holder, "USD").Key] = []byte{1}
			}
			want := ter.TecFROZEN
			if exists && cleanup {
				want = ter.TecDUPLICATE
			}
			delta, result := addEmptyHolding(ctx, holder, tx.Asset{Currency: "USD", Issuer: issuerAddress}, 0)
			if delta != 0 || result != want {
				t.Fatalf("cleanup=%v exists=%v: %d/%v, want 0/%v", cleanup, exists, delta, result, want)
			}
		}
	}
}

func TestVaultCleanupRejectsPseudoClawbackHolder(t *testing.T) {
	f := newVaultClawbackFixture(t, 100)
	f.ctx.Config.Rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureFixCleanup3_4_0).Build()
	raw, err := state.SerializeAccountRoot(&state.AccountRoot{Account: state.EncodeAccountIDSafe(f.holderID), VaultID: f.vaultKey.Key})
	if err != nil {
		t.Fatal(err)
	}
	f.view.data[keylet.Account(f.holderID).Key] = raw
	if result := f.txn.Preclaim(f.view, f.ctx.Config); result != ter.TecPSEUDO_ACCOUNT {
		t.Fatalf("got %v", result)
	}
}

func TestVaultCleanupDepositKeepsShareCount(t *testing.T) {
	total, _ := vaultNumber("1000.123456789012")
	supply := state.NewXRPLNumber(1000000000, 0)
	for _, amount := range []int64{1, 7, 1000, 10000000} {
		requested := state.NewXRPLNumber(amount, 0)
		minted, assetValue, shares, result := vaultDepositExchange(total, supply, requested, 6, false)
		if result != ter.TesSUCCESS || shares == 0 {
			t.Fatalf("exchange %d: %v", amount, result)
		}
		debit, result := clampToAssetsTotalScale(total, assetValue, false)
		if result != ter.TesSUCCESS {
			t.Fatalf("clamp %d: %v", amount, result)
		}
		shareValue := total.Mul(minted).Div(supply)
		ulp := state.NewXRPLNumber(1, total.Add(debit).AssetExponent(false, state.RoundToNearest))
		if debit.Cmp(requested) > 0 || debit.Cmp(shareValue) > 0 || shareValue.Sub(debit).Cmp(ulp) >= 0 {
			t.Fatalf("amount %d: shares=%d debit=%s shareValue=%s ulp=%s", amount, shares, debit.String(), shareValue.String(), ulp.String())
		}
	}
}

func TestVaultCleanupClawbackTruncatesRequestedAssets(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		f := newVaultClawbackFixture(t, 2)
		if cleanup {
			f.ctx.Config.Rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureFixCleanup3_4_0).Build()
		}
		vd, err := readVault(f.view, f.vaultKey)
		if err != nil {
			t.Fatal(err)
		}
		vd.AssetsTotal, vd.AssetsAvailable = "3", "3"
		issuance := clawbackTestIssuance(t, f.view, f.shareMPTID)
		issuance.OutstandingAmount = 2
		amount := state.NewMPTAmountWithIssuanceID(1, clawbackTestAddress(t, f.assetIssuerID), hex.EncodeToString(f.assetMPTID[:]))
		f.txn.Amount = &amount
		_, _, recovered, shares, result := f.txn.clawbackAmounts(f.ctx, vd, issuance, f.assetIssuerID, f.holderID)
		if cleanup {
			if result != ter.TecPRECISION_LOSS || shares != 0 {
				t.Fatalf("cleanup: %v shares=%d", result, shares)
			}
		} else if result != ter.TesSUCCESS || shares != 1 || recovered.ToInt64WithMode(state.RoundTowardsZero) != 2 {
			t.Fatalf("legacy: %v shares=%d recovered=%s", result, shares, recovered.String())
		}
	}
}

func TestVaultCleanupSoleHolderClawbackPreservesReceivables(t *testing.T) {
	f := newVaultClawbackFixture(t, 100)
	f.ctx.Config.Rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureFixCleanup3_4_0).Build()
	vd, err := readVault(f.view, f.vaultKey)
	if err != nil {
		t.Fatal(err)
	}
	vd.AssetsTotal, vd.AssetsAvailable, vd.LossUnrealized = "100", "50", "50"
	issuance := clawbackTestIssuance(t, f.view, f.shareMPTID)
	_, _, recovered, shares, result := f.txn.clawbackAmounts(f.ctx, vd, issuance, f.assetIssuerID, f.holderID)
	if result != ter.TesSUCCESS || shares != 50 || recovered.ToInt64WithMode(state.RoundTowardsZero) != 50 {
		t.Fatalf("got %v shares=%d recovered=%s", result, shares, recovered.String())
	}
}
