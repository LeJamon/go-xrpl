package nft_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
)

// TestNFTokenAcceptOffer_IOUTransferFeeRounding pins the issuer's cut of an IOU
// sale to rippled's Number multiply, rounded to nearest, in every accept mode.
func TestNFTokenAcceptOffer_IOUTransferFeeRounding(t *testing.T) {
	vectors := []struct {
		name         string
		mantissa     int64
		exponent     int
		fee          uint16
		cutMantissa  int64
		cutExponent  int
		sellMantissa int64
		sellExponent int
	}{
		{
			name:     "round_down",
			mantissa: 1_000_000_000_000_001, exponent: -15, fee: 1001,
			cutMantissa: 1_001_000_000_000_001, cutExponent: -17,
			sellMantissa: 9_899_900_000_000_010, sellExponent: -16,
		},
		{
			name:     "round_up",
			mantissa: 1_234_567_890_123_457, exponent: -13, fee: 333,
			cutMantissa: 4_111_111_074_111_112, cutExponent: -16,
			sellMantissa: 1_230_456_779_049_346, sellExponent: -13,
		},
	}
	modes := []string{"sell", "buy", "brokered"}

	for _, v := range vectors {
		for _, mode := range modes {
			t.Run(v.name+"/"+mode, func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				gw := jtx.NewAccount("gateway")
				minter := jtx.NewAccount("minter")
				seller := jtx.NewAccount("seller")
				buyer := jtx.NewAccount("buyer")
				broker := jtx.NewAccount("broker")
				env.Fund(gw, minter, seller, buyer, broker)
				env.Close()

				for _, acc := range []*jtx.Account{minter, seller, buyer} {
					jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(acc, gw.IOU("USD", 10_000)).Build()))
				}
				jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, buyer, gw.IOU("USD", 1_000)).Build()))
				env.Close()

				nftID := nft.GetNextNFTokenID(env, minter, 0, nftoken.NFTokenFlagTransferable, v.fee)
				jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(minter, 0).Transferable().TransferFee(v.fee).Build()))
				env.Close()

				giveaway := nft.GetOfferIndex(env, minter)
				jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(minter, nftID, tx.NewXRPAmount(0)).Destination(seller).Build()))
				env.Close()
				jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenAcceptSellOffer(seller, giveaway).Build()))
				env.Close()

				price := state.NewIssuedAmountFromValue(v.mantissa, v.exponent, "USD", gw.Address)
				var accept tx.Transaction
				switch mode {
				case "sell":
					offer := nft.GetOfferIndex(env, seller)
					jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(seller, nftID, price).Build()))
					accept = nft.NFTokenAcceptSellOffer(buyer, offer).Build()
				case "buy":
					offer := nft.GetOfferIndex(env, buyer)
					jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateBuyOffer(buyer, nftID, price, seller).Build()))
					accept = nft.NFTokenAcceptBuyOffer(seller, offer).Build()
				case "brokered":
					sellOffer := nft.GetOfferIndex(env, seller)
					jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(seller, nftID, price).Build()))
					buyOffer := nft.GetOfferIndex(env, buyer)
					jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateBuyOffer(buyer, nftID, price, seller).Build()))
					accept = nft.NFTokenBrokeredSale(broker, sellOffer, buyOffer).Build()
				}
				env.Close()
				jtx.RequireTxSuccess(t, env.Submit(accept))
				env.Close()

				requireIOU := func(name string, acc *jtx.Account, mantissa int64, exponent int) {
					t.Helper()
					got := env.IOUBalance(acc, gw, "USD")
					if got.Mantissa() != mantissa || got.Exponent() != exponent {
						t.Fatalf("%s USD = %de%d, want %de%d", name, got.Mantissa(), got.Exponent(), mantissa, exponent)
					}
				}
				requireIOU("minter", minter, v.cutMantissa, v.cutExponent)
				requireIOU("seller", seller, v.sellMantissa, v.sellExponent)
			})
		}
	}
}
