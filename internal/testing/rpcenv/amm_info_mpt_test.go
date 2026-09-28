package rpcenv_test

import (
	"encoding/json"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/testing/rpcenv"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestAMMInfoMPTBalancesOrderingAndFreeze(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.EnableFeature("MPTokensV2")
	require.True(t, env.FeatureEnabled("MPTokensV2"))
	env.FundAmount(env.GW, uint64(jtx.XRP(10000)))
	env.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
	env.Close()
	token := mpttest.NewMPTTesterNoFund(t, env.TestEnv, env.GW)
	token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanLock | mpttest.TfMPTCanTrade | mpttest.TfMPTCanTransfer})
	token.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
	const units int64 = 9007199254740993
	token.Pay(env.GW, env.Alice, units)
	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.Alice, token.MPTAmount(units), tx.NewXRPAmount(200000000)).Build()))
	env.Trust(env.Alice, env.GW, "USD", 1000)
	env.PayIOU(env.GW, env.Alice, "USD", 100)
	iouMPT := mpttest.NewMPTTesterNoFund(t, env.TestEnv, env.GW)
	iouMPT.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTrade | mpttest.TfMPTCanTransfer})
	iouMPT.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
	iouMPT.Pay(env.GW, env.Alice, 200)
	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(
		env.Alice,
		amm.IOUAmount(env.GW, "USD", 100),
		iouMPT.MPTAmount(100),
	).Build()))
	mptPair := mpttest.NewMPTTesterNoFund(t, env.TestEnv, env.GW)
	mptPair.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTrade | mpttest.TfMPTCanTransfer})
	mptPair.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
	mptPair.Pay(env.GW, env.Alice, 100)
	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.Alice, iouMPT.MPTAmount(100), mptPair.MPTAmount(100)).Build()))
	env.Close()
	rpc := rpcenv.Wrap(t, env.TestEnv)

	query := func(params map[string]any) map[string]any {
		t.Helper()
		result, rpcErr := rpc.RPC("amm_info", params)
		require.Nil(t, rpcErr)
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		var response struct {
			AMM map[string]any `json:"amm"`
		}
		require.NoError(t, json.Unmarshal(encoded, &response))
		return response.AMM
	}
	mptAsset := map[string]any{"mpt_issuance_id": token.IssuanceID()}
	xrpAsset := map[string]any{"currency": "XRP"}
	expectedMPT := map[string]any{"mpt_issuance_id": token.IssuanceID(), "value": "9007199254740993"}
	result := query(map[string]any{"asset": mptAsset, "asset2": xrpAsset})
	require.Equal(t, expectedMPT, result["amount"])
	require.Equal(t, "200000000", result["amount2"])
	require.Equal(t, false, result["asset_frozen"])
	require.NotContains(t, result, "asset2_frozen")
	require.Equal(t, float64(0), result["trading_fee"])
	byAccount := query(map[string]any{"amm_account": result["account"]})
	require.Equal(t, result["amount"], byAccount["amount"])
	require.Equal(t, result["amount2"], byAccount["amount2"])

	iouAsset := map[string]any{"currency": "USD", "issuer": env.GW.Address}
	iouAmount := map[string]any{"currency": "USD", "issuer": env.GW.Address, "value": "100"}
	iouMPTAsset := map[string]any{"mpt_issuance_id": iouMPT.IssuanceID()}
	iouMPTAmount := map[string]any{"mpt_issuance_id": iouMPT.IssuanceID(), "value": "100"}
	iouMPTResult := query(map[string]any{"asset": iouMPTAsset, "asset2": iouAsset})
	require.Equal(t, iouMPTAmount, iouMPTResult["amount"])
	require.Equal(t, iouAmount, iouMPTResult["amount2"])
	require.Equal(t, false, iouMPTResult["asset_frozen"])
	require.Equal(t, false, iouMPTResult["asset2_frozen"])
	iouMPTByAccount := query(map[string]any{"amm_account": iouMPTResult["account"]})
	require.Equal(t, iouMPTAmount, iouMPTByAccount["amount"])
	require.Equal(t, iouAmount, iouMPTByAccount["amount2"])
	require.Equal(t, false, iouMPTByAccount["asset_frozen"])
	require.Equal(t, false, iouMPTByAccount["asset2_frozen"])
	iouMPTReverse := query(map[string]any{"asset": iouAsset, "asset2": iouMPTAsset})
	require.Equal(t, iouAmount, iouMPTReverse["amount"])
	require.Equal(t, iouMPTAmount, iouMPTReverse["amount2"])
	require.Equal(t, false, iouMPTReverse["asset_frozen"])
	require.Equal(t, false, iouMPTReverse["asset2_frozen"])

	mptPairAsset := map[string]any{"mpt_issuance_id": mptPair.IssuanceID()}
	mptPairAmount := map[string]any{"mpt_issuance_id": mptPair.IssuanceID(), "value": "100"}
	mptMPTResult := query(map[string]any{"asset": iouMPTAsset, "asset2": mptPairAsset})
	require.Equal(t, iouMPTAmount, mptMPTResult["amount"])
	require.Equal(t, mptPairAmount, mptMPTResult["amount2"])
	require.Equal(t, false, mptMPTResult["asset_frozen"])
	require.Equal(t, false, mptMPTResult["asset2_frozen"])
	mptByAccount := query(map[string]any{"amm_account": mptMPTResult["account"]})
	require.Equal(t, iouMPTAmount, mptByAccount["amount"])
	require.Equal(t, mptPairAmount, mptByAccount["amount2"])
	require.Equal(t, false, mptByAccount["asset_frozen"])
	require.Equal(t, false, mptByAccount["asset2_frozen"])
	mptReverse := query(map[string]any{"asset": mptPairAsset, "asset2": iouMPTAsset})
	require.Equal(t, mptPairAmount, mptReverse["amount"])
	require.Equal(t, iouMPTAmount, mptReverse["amount2"])
	require.Equal(t, false, mptReverse["asset_frozen"])
	require.Equal(t, false, mptReverse["asset2_frozen"])

	reverse := query(map[string]any{"asset": xrpAsset, "asset2": mptAsset})
	require.Equal(t, "200000000", reverse["amount"])
	require.Equal(t, expectedMPT, reverse["amount2"])
	require.Equal(t, false, reverse["asset2_frozen"])
	require.NotContains(t, reverse, "asset_frozen")

	token.Set(mpttest.SetOpts{Account: env.GW, Flags: mpttest.TfMPTLock})
	rpc.Close()
	frozen := query(map[string]any{"asset": mptAsset, "asset2": xrpAsset})
	require.Equal(t, true, frozen["asset_frozen"])
	require.Equal(t, expectedMPT, frozen["amount"])
	token.Set(mpttest.SetOpts{Account: env.GW, Flags: mpttest.TfMPTUnlock})
	rpc.Close()
	require.Equal(t, false, query(map[string]any{"asset": mptAsset, "asset2": xrpAsset})["asset_frozen"])
}
