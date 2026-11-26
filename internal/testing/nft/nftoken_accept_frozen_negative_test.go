package nft_test

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// A seller that owes the issuer stays negative after being paid. The
// post-payment funds check reads a frozen line as zero, so the sale succeeds
// once the issuer has frozen that line.
func TestNFTokenAcceptOfferSellerFrozenNegativeLine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		freeze bool
		want   ter.Result
	}{
		{name: "Frozen", freeze: true, want: ter.TesSUCCESS},
		{name: "NotFrozen", freeze: false, want: ter.TecINSUFFICIENT_FUNDS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			gw := jtx.NewAccount("gateway")
			seller := jtx.NewAccount("seller")
			buyer := jtx.NewAccount("buyer")
			env.Fund(gw, seller, buyer)
			env.Close()

			jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(buyer, gw.IOU("USD", 1000)).Build()))
			jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(gw, seller.IOU("USD", 1000)).Build()))
			env.Close()
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, buyer, gw.IOU("USD", 100)).Build()))
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(seller, gw, seller.IOU("USD", 50)).Build()))
			env.Close()
			require.Equal(t, -50.0, env.BalanceIOU(seller, "USD", gw))

			tokenIDHex := nft.GetNextNFTokenID(env, seller, 0, 0, 0)
			jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(seller, 0).Build()))
			env.Close()
			offerID := nft.GetOfferIndex(env, seller)
			jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(seller, tokenIDHex, gw.IOU("USD", 10)).Build()))
			env.Close()

			if tc.freeze {
				env.FreezeTrustLine(gw, seller, "USD")
				env.Close()
			}

			result := env.Submit(nft.NFTokenAcceptSellOffer(buyer, offerID).Build())
			require.Equal(t, tc.want, result.Result)
			env.Close()

			tokenIDBytes, err := hex.DecodeString(tokenIDHex)
			require.NoError(t, err)
			var tokenID [32]byte
			copy(tokenID[:], tokenIDBytes)

			if tc.want != ter.TesSUCCESS {
				require.Equal(t, 100.0, env.BalanceIOU(buyer, "USD", gw))
				require.Equal(t, -50.0, env.BalanceIOU(seller, "USD", gw))
				require.True(t, ownsNFToken(t, env.Ledger(), seller.ID, tokenID))
				return
			}
			require.Equal(t, 90.0, env.BalanceIOU(buyer, "USD", gw))
			require.Equal(t, -40.0, env.BalanceIOU(seller, "USD", gw))
			require.True(t, ownsNFToken(t, env.Ledger(), buyer.ID, tokenID))
		})
	}
}
