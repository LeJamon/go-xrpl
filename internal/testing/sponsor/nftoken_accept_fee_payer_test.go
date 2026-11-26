package sponsor_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// The buy-offer owner's funds are checked before the fee is charged, so a
// buyer who also pays the fee as delegate or fee sponsor needs only the price.
func TestNFTokenAcceptBuyOfferFeePaidByBuyer(t *testing.T) {
	const price = 1_000_000
	for _, payer := range []string{"delegate", "sponsor"} {
		for _, tc := range []struct {
			name      string
			shortfall uint64
			buyerPage bool
			want      ter.Result
		}{
			{name: "BelowPrice", shortfall: 1, buyerPage: true, want: ter.TecINSUFFICIENT_FUNDS},
			{name: "ExactPriceWithPage", buyerPage: true, want: ter.TesSUCCESS},
			{name: "ExactPriceWithoutPage", want: ter.TecINSUFFICIENT_RESERVE},
		} {
			t.Run(payer+"/"+tc.name, func(t *testing.T) {
				env, seller, _, buyer, _ := sponsorEnv(t)
				if payer == "delegate" {
					grantDelegatePermission(t, env, seller, buyer, "NFTokenAcceptOffer")
				}

				tokenIDHex := nft.GetNextNFTokenID(env, seller, 0, nftoken.NFTokenFlagTransferable, 0)
				jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(seller, 0).Transferable().Build()))
				if tc.buyerPage {
					jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(buyer, 0).Build()))
				}
				env.Close()
				offerID := nft.GetOfferIndex(env, buyer)
				offerKey := keylet.NFTokenOffer(buyer.ID, env.Seq(buyer))
				jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateBuyOffer(
					buyer, tokenIDHex, tx.NewXRPAmount(price), seller).Build()))
				env.Close()

				ownerCount := accountState(t, env, buyer).OwnerCount
				reserve := env.ReserveBase() + uint64(ownerCount)*env.ReserveIncrement()
				setAccountBalance(t, env, buyer, reserve+price-tc.shortfall)
				buyerBefore := env.Balance(buyer)
				sellerBefore := env.Balance(seller)

				accept := nft.NFTokenAcceptBuyOffer(seller, offerID).Build()
				common := accept.GetCommon()
				common.Fee = "10"
				var result jtx.TxResult
				if payer == "delegate" {
					common.Delegate = buyer.Address
					result = env.SubmitSignedWith(accept, buyer)
				} else {
					common.Sponsor = buyer.Address
					flags := tx.SpfSponsorFee
					common.SponsorFlags = &flags
					attachSponsorSignature(t, env, accept, seller, buyer)
					result = env.SubmitSigned(accept)
				}
				require.Equal(t, tc.want, result.Result)
				require.Equal(t, uint64(10), result.Fee)
				env.Close()

				if tc.want != ter.TesSUCCESS {
					require.Equal(t, buyerBefore-10, env.Balance(buyer))
					require.Equal(t, sellerBefore, env.Balance(seller))
					require.True(t, env.LedgerEntryExists(offerKey))
					return
				}
				require.Equal(t, buyerBefore-10-price, env.Balance(buyer))
				require.Equal(t, sellerBefore+price, env.Balance(seller))
				require.False(t, env.LedgerEntryExists(offerKey))
				require.Equal(t, ownerCount-1, accountState(t, env, buyer).OwnerCount)
			})
		}
	}
}
