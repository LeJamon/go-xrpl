package nftoken

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

func TestNFTNegativeOffersWithoutRetiredFix(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	rules := amendment.NewRules([][32]byte{amendment.FeatureNonFungibleTokensV1})
	require.False(t, rules.Enabled(amendment.FeatureFixNFTokenNegOffer))
	for _, sell := range []bool{false, true} {
		for _, amount := range []tx.Amount{tx.NewXRPAmount(1).Negate(), tx.NewIssuedAmountFromFloat64(-1, "USD", account)} {
			t.Run(fmt.Sprintf("sell=%t/native=%t", sell, amount.IsNative()), func(t *testing.T) {
				zero := uint32(0)
				err := tokenOfferCreatePreflight(rules, account, amount, account, &zero, 0, "", sell)
				result, ok := ter.AsResultError(err)
				require.True(t, ok)
				require.Equal(t, ter.TemBAD_AMOUNT, result.Code)
			})
		}
	}
}

func TestNFTAcceptNegativeStoredOfferWithoutRetiredFix(t *testing.T) {
	rules := amendment.NewRules([][32]byte{amendment.FeatureNonFungibleTokensV1})
	owner, acceptor := [20]byte{1}, [20]byte{2}
	for _, sell := range []bool{false, true} {
		for _, iou := range []bool{false, true} {
			t.Run(fmt.Sprintf("sell=%t/iou=%t", sell, iou), func(t *testing.T) {
				view := newMockView()
				var amount any = "-1"
				if iou {
					amount = map[string]any{"currency": "USD", "issuer": state.EncodeAccountIDSafe(owner), "value": "-1"}
				}
				flags := uint32(0)
				if sell {
					flags = 1
				}
				raw, err := state.SerializeNFTokenOffer(owner, [32]byte{3}, amount, flags, 0, 0, "", nil)
				require.NoError(t, err)
				parsed, err := state.ParseNFTokenOffer(raw)
				require.NoError(t, err)
				require.True(t, parsed.Negative)
				key := [32]byte{4}
				view.store[key] = raw
				offer := NewNFTokenAcceptOffer(state.EncodeAccountIDSafe(acceptor))
				if sell {
					offer.NFTokenSellOffer = hex.EncodeToString(key[:])
				} else {
					offer.NFTokenBuyOffer = hex.EncodeToString(key[:])
				}
				ctx := &tx.ApplyContext{View: view, AccountID: acceptor, Config: tx.EngineConfig{Rules: rules}, Log: xrpllog.Discard()}
				require.Equal(t, ter.TemBAD_OFFER, offer.Apply(ctx))
				require.Equal(t, raw, view.store[key])
			})
		}
	}
}
