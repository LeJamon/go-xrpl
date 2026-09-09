package nft_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	nft "github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestNFTCleanup340FakeXRP(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, currency := range []string{"0000000000000000000000005852500000000000"} {
			t.Run(fmt.Sprintf("cleanup=%t/%s", enabled, currency), func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				if enabled {
					env.EnableFeature("fixCleanup3_4_0")
				}
				alice, buyer, broker, gw := jtx.NewAccount("alice"), jtx.NewAccount("buyer"), jtx.NewAccount("broker"), jtx.NewAccount("gw")
				env.Fund(alice, buyer, broker, gw)
				env.Close()
				id := nft.GetNextNFTokenID(env, alice, 0, 8, 0)
				jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build()))
				bad := tx.NewIssuedAmountFromFloat64(1, currency, gw.Address)
				balance, seq := env.Balance(alice), env.Seq(alice)
				res := env.Submit(nft.NFTokenCreateSellOffer(alice, id, bad).Build())
				if enabled {
					require.Equal(t, "temBAD_CURRENCY", res.Code)
					require.Equal(t, balance, env.Balance(alice))
					require.Equal(t, seq, env.Seq(alice))
				} else {
					jtx.RequireTxSuccess(t, res)
				}
				sell := nft.GetOfferIndex(env, alice)
				jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(alice, id, jtx.XRPTxAmount(10_000_000)).Build()))
				buy := nft.GetOfferIndex(env, buyer)
				jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateBuyOffer(buyer, id, jtx.XRPTxAmount(40_000_000), alice).Build()))
				res = env.Submit(nft.NFTokenBrokeredSale(broker, sell, buy).BrokerFee(bad).Build())
				if enabled {
					require.Equal(t, "temBAD_CURRENCY", res.Code)
				} else {
					jtx.RequireTxClaimed(t, res, "tecNFTOKEN_BUY_SELL_MISMATCH")
				}
				jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenBrokeredSale(broker, sell, buy).BrokerFee(jtx.XRPTxAmount(1_000_000)).Build()))
			})
		}
	}
}

func TestNFTCleanup340IssuerGlobalFreeze(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, mintOffer := range []bool{false, true} {
			t.Run(fmt.Sprintf("cleanup=%t/mintOffer=%t", enabled, mintOffer), func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				if enabled {
					env.EnableFeature("fixCleanup3_4_0")
				}
				issuer, buyer := jtx.NewAccount("issuer"), jtx.NewAccount("buyer")
				env.Fund(issuer, buyer)
				env.Close()
				amount := jtx.USD(issuer, 100)
				jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).GlobalFreeze().Build()))
				var res jtx.TxResult
				if mintOffer {
					res = env.Submit(nft.NFTokenMint(issuer, 0).Transferable().Amount(amount).Build())
				} else {
					id := nft.GetNextNFTokenID(env, issuer, 0, 8, 0)
					jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(issuer, 0).Transferable().Build()))
					res = env.Submit(nft.NFTokenCreateSellOffer(issuer, id, amount).Build())
				}
				if enabled {
					jtx.RequireTxSuccess(t, res)
				} else {
					jtx.RequireTxClaimed(t, res, "tecFROZEN")
				}
			})
		}
	}
}
