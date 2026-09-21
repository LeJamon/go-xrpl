package handlers_test

import (
	"encoding/json"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/rpcenv"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func decodePathFindResponse(t *testing.T, result any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var response map[string]any
	require.NoError(t, json.Unmarshal(raw, &response))
	delete(response, "ledger_current_index")
	return response
}

func TestRipplePathFindResponsePreservesMPTPathAndAmounts(t *testing.T) {
	env := rpcenv.New(t)
	alice := jtx.NewAccount("mpt-path-response-alice")
	bob := jtx.NewAccount("mpt-path-response-bob")
	issuer := jtx.NewAccount("mpt-path-response-issuer")
	maker := jtx.NewAccount("mpt-path-response-maker")
	env.EnableFeature("MPTokensV1")
	env.EnableFeature("MPTokensV2")
	token := mpt.NewMPTTester(t, env.TestEnv, issuer, mpt.MPTInit{Holders: []*jtx.Account{alice, bob, maker}})
	env.Close()
	token.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanTrade | mpt.TfMPTCanTransfer})
	for _, holder := range []*jtx.Account{alice, bob, maker} {
		token.Authorize(mpt.AuthorizeOpts{Account: holder})
	}
	token.Pay(issuer, maker, 100)
	env.Close()

	result := env.Submit(offer.OfferCreate(maker, tx.NewXRPAmount(100_000_000), token.MPTAmount(100)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	resultRPC, rpcErr := env.RPC("ripple_path_find", map[string]any{
		"source_account":      alice.Address,
		"destination_account": bob.Address,
		"destination_amount":  map[string]any{"mpt_issuance_id": token.IssuanceID(), "value": "10"},
	})
	require.Nil(t, rpcErr)
	response := decodePathFindResponse(t, resultRPC)
	require.Equal(t, map[string]any{
		"alternatives": []any{
			map[string]any{
				"paths_canonical": []any{},
				"paths_computed": []any{
					[]any{map[string]any{
						"issuer":          issuer.Address,
						"mpt_issuance_id": token.IssuanceID(),
						"type":            float64(96),
					}},
				},
				"source_amount": "10000000",
			},
		},
		"destination_account":    bob.Address,
		"destination_amount":     map[string]any{"mpt_issuance_id": token.IssuanceID(), "value": "10"},
		"destination_currencies": []any{token.IssuanceID(), "XRP"},
		"full_reply":             true,
		"source_account":         alice.Address,
		"validated":              false,
	}, response)
}

func TestRipplePathFindResponsePreservesDistinctIOUPaths(t *testing.T) {
	env := rpcenv.New(t)
	alice := jtx.NewAccount("path-response-alice")
	bob := jtx.NewAccount("path-response-bob")
	gw1 := jtx.NewAccount("path-response-gateway-1")
	gw2 := jtx.NewAccount("path-response-gateway-2")
	env.FundAmount(alice, uint64(jtx.XRP(10_000)))
	env.FundAmount(bob, uint64(jtx.XRP(10_000)))
	env.FundAmount(gw1, uint64(jtx.XRP(10_000)))
	env.FundAmount(gw2, uint64(jtx.XRP(10_000)))
	env.Close()

	for _, account := range []*jtx.Account{alice, bob} {
		result := env.Submit(trustset.TrustLine(account, "USD", gw1, "1000").Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(account, "USD", gw2, "1000").Build())
		jtx.RequireTxSuccess(t, result)
	}
	env.Close()

	for _, gateway := range []*jtx.Account{gw1, gw2} {
		result := env.Submit(payment.PayIssued(gateway, alice, tx.NewIssuedAmountFromFloat64(50, "USD", gateway.Address)).Build())
		jtx.RequireTxSuccess(t, result)
	}
	env.Close()

	result, rpcErr := env.RPC("ripple_path_find", map[string]any{
		"source_account":      alice.Address,
		"destination_account": bob.Address,
		"destination_amount":  map[string]any{"currency": "USD", "issuer": bob.Address, "value": "5"},
	})
	require.Nil(t, rpcErr)
	response := decodePathFindResponse(t, result)
	require.Equal(t, map[string]any{
		"alternatives": []any{
			map[string]any{
				"paths_canonical": []any{},
				"paths_computed": []any{
					[]any{map[string]any{"account": gw2.Address, "type": float64(1)}},
					[]any{map[string]any{"account": gw1.Address, "type": float64(1)}},
				},
				"source_amount": map[string]any{
					"currency": "USD",
					"issuer":   alice.Address,
					"value":    "5",
				},
			},
		},
		"destination_account": bob.Address,
		"destination_amount": map[string]any{
			"currency": "USD",
			"issuer":   bob.Address,
			"value":    "5",
		},
		"destination_currencies": []any{"USD", "XRP"},
		"full_reply":             true,
		"source_account":         alice.Address,
		"validated":              false,
	}, response)
}
