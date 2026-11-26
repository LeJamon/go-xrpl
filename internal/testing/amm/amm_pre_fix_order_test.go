package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreAmm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// A single-asset withdrawal too small to burn any LP token fails with
// tecAMM_FAILED before fixAMMv1_3 and tecAMM_INVALID_TOKENS after it.
func TestAMMWithdrawSingleAssetDustResultByFixAMMv1_3(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixV1_3 bool
		want    ter.Result
	}{
		{name: "PreFix", want: ter.TecAMM_FAILED},
		{name: "Fix", fixV1_3: true, want: ter.TecAMM_INVALID_TOKENS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := setupGBPEURPoolAliceOnly(t, 10_000, 10_000, 0, tc.fixV1_3)
			gbp := tx.Asset{Currency: "GBP", Issuer: env.GW.Address}
			eur := tx.Asset{Currency: "EUR", Issuer: env.GW.Address}

			result := env.Submit(amm.AMMWithdraw(env.Alice, gbp, eur).
				Amount(ammIssuedAmount(t, "1e-20", gbp)).
				SingleAsset().
				Build())
			amm.ExpectTER(t, result, tc.want.String())
		})
	}
}

// Before fixCleanup3_3_0 and AMMClawback, a tfLPToken deposit checks the pool
// assets in the AMM's canonical order regardless of the transaction's order.
func TestAMMDepositLPTokenChecksAssetsInPoolOrder(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.DisableFeature("fixCleanup3_3_0")
	env.DisableFeature("AMMClawback")

	authGW := jtx.NewAccount("authgw")
	frozenGW := jtx.NewAccount("frozengw")
	for _, acc := range []*jtx.Account{authGW, frozenGW, env.Alice, env.Carol} {
		env.FundAmount(acc, uint64(jtx.XRP(30_000)))
	}
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(authGW).RequireAuth().Build()))
	env.Close()

	authAsset := tx.Asset{Currency: "AAA", Issuer: authGW.Address}
	frozenAsset := tx.Asset{Currency: "FFF", Issuer: frozenGW.Address}
	env.Trust(env.Alice, authGW, "AAA", 100_000)
	env.AuthorizeTrustLine(authGW, env.Alice, "AAA")
	env.Trust(env.Alice, frozenGW, "FFF", 100_000)
	env.Trust(env.Carol, frozenGW, "FFF", 100_000)
	env.Close()
	env.PayIOU(authGW, env.Alice, "AAA", 10_000)
	env.PayIOU(frozenGW, env.Alice, "FFF", 10_000)
	env.PayIOU(frozenGW, env.Carol, "FFF", 10_000)
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.Alice,
		amm.IOUAmount(authGW, "AAA", 1_000),
		amm.IOUAmount(frozenGW, "FFF", 1_000),
	).Build()))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(frozenGW).GlobalFreeze().Build()))
	env.Close()

	ammData := env.ReadAMMData(authAsset, frozenAsset)
	want := ter.TecFROZEN
	if ammData.Asset.Issuer == authGW.Address {
		want = ter.TecNO_LINE
	}
	ammAcc := amm.AMMAccount(t, env, authAsset, frozenAsset)
	lpt := ammIssuedAmount(t, "1", tx.Asset{
		Currency: coreAmm.GenerateAMMLPTCurrencyForAssets(authAsset, frozenAsset),
		Issuer:   ammAcc.Address,
	})

	for _, order := range [][2]tx.Asset{{authAsset, frozenAsset}, {frozenAsset, authAsset}} {
		result := env.Submit(amm.AMMDeposit(env.Carol, order[0], order[1]).
			LPTokenOut(lpt).
			LPToken().
			Build())
		amm.ExpectTER(t, result, want.String())
	}
}
