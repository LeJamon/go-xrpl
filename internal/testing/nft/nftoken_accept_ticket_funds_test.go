package nft_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
)

// Accepting an XRP sell offer checks the buyer's liquid XRP in preclaim, where
// a ticket the transaction consumes still counts toward the reserve.
func TestNFTokenAcceptSellOfferTicketCountsTowardReserve(t *testing.T) {
	env := jtx.NewTestEnv(t)
	seller := jtx.NewAccount("seller")
	buyer := jtx.NewAccount("buyer")
	env.FundAmount(seller, uint64(jtx.XRP(1000)))
	env.FundAmount(buyer, env.ReserveBase()+env.ReserveIncrement()+1000+env.BaseFee())
	env.Close()

	nftID := nft.GetNextNFTokenID(env, seller, 0, nftoken.NFTokenFlagTransferable, 0)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(seller, 0).Transferable().Build()))
	env.Close()
	offer := nft.GetOfferIndex(env, seller)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(seller, nftID, tx.NewXRPAmount(2000)).Build()))
	env.Close()
	ticket := env.CreateTickets(buyer, 1)
	env.Close()

	// Liquid XRP is 1000 drops while the ticket counts, inc+1000 once it is gone.
	result := env.Submit(jtx.WithTicketSeq(nft.NFTokenAcceptSellOffer(buyer, offer).Build(), ticket))
	jtx.RequireTxClaimed(t, result, "tecINSUFFICIENT_FUNDS")
}
