package nftoken

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestNFTCleanup340AmountPrecedence(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	for _, enabled := range []bool{false, true} {
		for _, currency := range []string{"XRP", "0000000000000000000000005852500000000000", "USD"} {
			for _, value := range []float64{-1, 0, 1} {
				t.Run(fmt.Sprintf("cleanup=%t/%s/%g", enabled, currency, value), func(t *testing.T) {
					rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
					if enabled {
						rules.Enable(amendment.FeatureFixCleanup3_4_0)
					}
					amount := tx.NewIssuedAmountFromFloat64(value, currency, account)
					expected := ter.TesSUCCESS
					if value < 0 {
						expected = ter.TemBAD_AMOUNT
					} else if enabled && currency != "USD" {
						expected = ter.TemBAD_CURRENCY
					} else if value == 0 {
						expected = ter.TemBAD_AMOUNT
					}
					err := tokenOfferCreatePreflight(rules.Build(), account, amount, "", nil, 0, "", true)
					if expected == ter.TesSUCCESS {
						require.NoError(t, err)
					} else {
						result, ok := ter.AsResultError(err)
						require.True(t, ok)
						require.Equal(t, expected, result.Code)
					}
					accept := NewNFTokenAcceptOffer(account)
					accept.Fee = "10"
					accept.NFTokenSellOffer = strings.Repeat("1", 64)
					accept.NFTokenBuyOffer = strings.Repeat("2", 64)
					accept.NFTokenBrokerFee = &amount
					err = accept.Validate()
					if err == nil {
						err = accept.PreflightRules(rules.Build())
					}
					expected = ter.TesSUCCESS
					if value <= 0 {
						expected = ter.TemMALFORMED
					} else if enabled && currency != "USD" {
						expected = ter.TemBAD_CURRENCY
					}
					if expected == ter.TesSUCCESS {
						require.NoError(t, err)
					} else {
						result, ok := ter.AsResultError(err)
						require.True(t, ok)
						require.Equal(t, expected, result.Code)
					}
				})
			}
		}
	}
}
