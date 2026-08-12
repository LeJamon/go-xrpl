package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreAmm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/stretchr/testify/require"
)

func TestAMMMPTLifecycleAndLPTransfer(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.EnableFeature("MPTokensV2")
	env.DisableFeature("SingleAssetVault")
	env.DisableFeature("LendingProtocol")
	env.Close()
	issuer := jtx.NewAccount("mptIssuer")
	token := mpt.NewMPTTester(t, env.TestEnv, issuer, mpt.MPTInit{
		Holders:    []*jtx.Account{env.Alice, env.Carol},
		XRP:        uint64(jtx.XRP(100_000)),
		XRPHolders: uint64(jtx.XRP(100_000)),
	})
	token.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanTrade | mpt.TfMPTCanTransfer})
	token.Authorize(mpt.AuthorizeOpts{Account: env.Alice})
	token.Authorize(mpt.AuthorizeOpts{Account: env.Carol})
	token.Pay(issuer, env.Alice, 30_000)
	token.Pay(issuer, env.Carol, 30_000)
	env.Close()

	mptAsset := tx.Asset{MPTIssuanceID: token.IssuanceID()}
	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(
		env.Alice,
		amm.XRPAmount(10_000),
		token.MPTAmount(10_000),
	).Build()))
	env.Close()

	ammAcc := amm.AMMAccount(t, env, amm.XRP(), mptAsset)
	token.RequireMPTokenAmount(ammAcc, 10_000)
	require.Equal(t, uint64(jtx.XRP(10_000)), env.Balance(ammAcc))
	ammData := env.ReadAMMData(amm.XRP(), mptAsset)
	require.NotNil(t, ammData)
	require.Equal(t, "10000000", ammData.LPTokenBalance.Value())

	lpCurrency := coreAmm.GenerateAMMLPTCurrencyForAssets(amm.XRP(), mptAsset)
	env.Trust(env.Carol, ammAcc, lpCurrency, 20_000_000)
	env.Close()

	transfer := tx.NewIssuedAmountFromFloat64(100, lpCurrency, ammAcc.Address)
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(env.Alice, env.Carol, transfer).Build()))
	env.Close()

	aliceLP, found := env.LookupIOUBalance(env.Alice, ammAcc, lpCurrency)
	require.True(t, found)
	carolLP, found := env.LookupIOUBalance(env.Carol, ammAcc, lpCurrency)
	require.True(t, found)
	require.Equal(t, "9999900", aliceLP.Value())
	require.Equal(t, "100", carolLP.Value())
	require.Equal(t, "10000000", env.ReadAMMData(amm.XRP(), mptAsset).LPTokenBalance.Value())
}

func TestAMMMPTPaymentPath(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.EnableFeature("MPTokensV2")
	env.DisableFeature("SingleAssetVault")
	env.DisableFeature("LendingProtocol")
	env.Close()
	issuer := jtx.NewAccount("mptIssuer")
	token := mpt.NewMPTTester(t, env.TestEnv, issuer, mpt.MPTInit{
		Holders:    []*jtx.Account{env.Alice, env.Carol},
		XRP:        uint64(jtx.XRP(100_000)),
		XRPHolders: uint64(jtx.XRP(100_000)),
	})
	token.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanTrade | mpt.TfMPTCanTransfer})
	token.Authorize(mpt.AuthorizeOpts{Account: env.Alice})
	token.Authorize(mpt.AuthorizeOpts{Account: env.Carol})
	token.Pay(issuer, env.Alice, 20_000)
	token.Pay(issuer, env.Carol, 20_000)
	env.TestEnv.Fund(env.Bob)
	env.Close()

	mptAsset := tx.Asset{MPTIssuanceID: token.IssuanceID()}
	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(
		env.Alice,
		token.MPTAmount(10_000),
		amm.XRPAmount(10_100),
	).Build()))
	env.Close()

	ammAcc := amm.AMMAccount(t, env, amm.XRP(), mptAsset)
	lpBefore := env.ReadAMMData(amm.XRP(), mptAsset).LPTokenBalance
	bobXRP := env.Balance(env.Bob)
	result := env.Submit(
		payment.Pay(env.Carol, env.Bob, uint64(jtx.XRP(100))).
			SendMax(token.MPTAmount(100)).
			PathsXRP().
			NoDirectRipple().
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	token.RequireMPTokenAmount(env.Carol, 19_900)
	token.RequireMPTokenAmount(ammAcc, 10_100)
	require.Equal(t, bobXRP+uint64(jtx.XRP(100)), env.Balance(env.Bob))
	require.Equal(t, uint64(jtx.XRP(10_000)), env.Balance(ammAcc))
	require.Equal(t, lpBefore, env.ReadAMMData(amm.XRP(), mptAsset).LPTokenBalance)
}

func TestAMMMPTFeatureGate(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.EnableFeature("MPTokensV2")
	env.DisableFeature("SingleAssetVault")
	env.DisableFeature("LendingProtocol")
	env.Close()
	issuer := jtx.NewAccount("mptIssuer")
	token := mpt.NewMPTTester(t, env.TestEnv, issuer, mpt.MPTInit{
		Holders:    []*jtx.Account{env.Alice},
		XRP:        uint64(jtx.XRP(100_000)),
		XRPHolders: uint64(jtx.XRP(100_000)),
	})
	token.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanTrade | mpt.TfMPTCanTransfer})
	token.Authorize(mpt.AuthorizeOpts{Account: env.Alice})
	token.Pay(issuer, env.Alice, 20_000)
	env.Close()

	mptAsset := tx.Asset{MPTIssuanceID: token.IssuanceID()}
	env.DisableFeature("MPTokensV2")
	env.Close()
	result := env.Submit(amm.AMMCreate(
		env.Alice,
		amm.XRPAmount(10_000),
		token.MPTAmount(10_000),
	).Build())
	jtx.RequireTxFail(t, result, jtx.TemDISABLED)
	require.Nil(t, env.ReadAMMData(amm.XRP(), mptAsset))
	token.RequireMPTokenAmount(env.Alice, 20_000)
}
