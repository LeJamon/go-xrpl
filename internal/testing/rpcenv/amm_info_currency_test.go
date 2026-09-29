package rpcenv_test

import (
	"encoding/json"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/rpcenv"
	"github.com/stretchr/testify/require"
)

func TestAMMInfoCanonicalCurrency(t *testing.T) {
	for _, tc := range []struct {
		name            string
		currency        string
		requestCurrency string
	}{
		{
			name:            "hex encoded USD",
			currency:        "USD",
			requestCurrency: "0000000000000000000000005553440000000000",
		},
		{
			name:            "lowercase nonstandard hex",
			currency:        "0158415500000000C1F76FF6ECB0BAC600000000",
			requestCurrency: "0158415500000000c1f76ff6ecb0bac600000000",
		},
		{
			name:            "preserve lowercase currency code",
			currency:        "usd",
			requestCurrency: "0000000000000000000000007573640000000000",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			env.FundAmount(env.GW, uint64(jtx.XRP(10000)))
			env.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
			env.Close()
			env.Trust(env.Alice, env.GW, tc.currency, 1000)
			env.PayIOU(env.GW, env.Alice, tc.currency, 100)
			jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(
				env.Alice, amm.IOUAmount(env.GW, tc.currency, 100), amm.XRPAmount(200),
			).Build()))
			env.Close()
			rpc := rpcenv.Wrap(t, env.TestEnv)
			iouAsset := map[string]any{"currency": tc.requestCurrency, "issuer": env.GW.Address}
			xrpAsset := map[string]any{"currency": "XRP"}
			iouAmount := map[string]any{"currency": tc.currency, "issuer": env.GW.Address, "value": "100"}

			for _, order := range []struct {
				name            string
				asset, asset2   map[string]any
				amount, amount2 any
			}{
				{"IOU first", iouAsset, xrpAsset, iouAmount, "200000000"},
				{"XRP first", xrpAsset, iouAsset, "200000000", iouAmount},
			} {
				t.Run(order.name, func(t *testing.T) {
					result, rpcErr := rpc.RPC("amm_info", map[string]any{
						"asset": order.asset, "asset2": order.asset2,
					})
					require.Nil(t, rpcErr)
					encoded, err := json.Marshal(result)
					require.NoError(t, err)
					var response struct {
						AMM map[string]any `json:"amm"`
					}
					require.NoError(t, json.Unmarshal(encoded, &response))
					require.Equal(t, order.amount, response.AMM["amount"])
					require.Equal(t, order.amount2, response.AMM["amount2"])
				})
			}
		})
	}
}
