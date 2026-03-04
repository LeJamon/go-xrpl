package offer

// Offer ticket tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testTicketOffer (lines 4863-4980)
//   - testTicketCancelOffer (lines 4983-5093)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_TicketOffer verifies offers can be created using tickets and
// remain in chronological order regardless of sequence/ticket numbers.
// Reference: rippled Offer_test.cpp testTicketOffer (lines 4863-4980)
func TestOffer_TicketOffer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testTicketOffer(t, fs.disabled)
		})
	}
}

func testTicketOffer(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.Trust(alice, USD(1000))
	env.Trust(bob, USD(1000))
	env.Close()

	result := env.Submit(payment.PayIssued(gw, alice, USD(200)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	xrp50 := jtx.XRPTxAmountFromXRP(50)
	usd50 := USD(50)

	// Create four offers from the same account with identical quality
	// so they go in the same order book. Each offer goes in a different
	// ledger so the chronology is clear.
	offerId0 := env.Seq(alice)
	result = env.Submit(OfferCreate(alice, xrp50, usd50).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create two tickets.
	ticketSeq := env.Seq(alice) + 1
	env.CreateTickets(alice, 2)
	env.Close()

	// Create another sequence-based offer.
	offerId1 := env.Seq(alice)
	require.Equal(t, offerId0+4, offerId1)
	result = env.Submit(OfferCreate(alice, xrp50, usd50).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create two ticket based offers in reverse order.
	offerId2 := ticketSeq + 1
	result = env.Submit(OfferCreate(alice, xrp50, usd50).TicketSeq(offerId2).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create the last offer.
	offerId3 := ticketSeq
	result = env.Submit(OfferCreate(alice, xrp50, usd50).TicketSeq(offerId3).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify that all of alice's offers are present.
	{
		offers := SortedOffersOnAccount(env, alice)
		require.Equal(t, 4, len(offers))
		require.Equal(t, offerId0, offers[0].Sequence)
		require.Equal(t, offerId3, offers[1].Sequence)
		require.Equal(t, offerId2, offers[2].Sequence)
		require.Equal(t, offerId1, offers[3].Sequence)
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 200)
		jtx.RequireOwnerCount(t, env, alice, 5) // 1 trust + 4 offers
	}

	// Cross alice's first offer.
	result = env.Submit(OfferCreate(bob, usd50, xrp50).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify that the first offer alice created was consumed.
	{
		offers := SortedOffersOnAccount(env, alice)
		require.Equal(t, 3, len(offers))
		require.Equal(t, offerId3, offers[0].Sequence)
		require.Equal(t, offerId2, offers[1].Sequence)
		require.Equal(t, offerId1, offers[2].Sequence)
	}

	// Cross alice's second offer.
	result = env.Submit(OfferCreate(bob, usd50, xrp50).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify that the second offer alice created was consumed.
	{
		offers := SortedOffersOnAccount(env, alice)
		require.Equal(t, 2, len(offers))
		require.Equal(t, offerId3, offers[0].Sequence)
		require.Equal(t, offerId2, offers[1].Sequence)
	}

	// Cross alice's third offer.
	result = env.Submit(OfferCreate(bob, usd50, xrp50).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify that the third offer alice created was consumed.
	{
		offers := SortedOffersOnAccount(env, alice)
		require.Equal(t, 1, len(offers))
		require.Equal(t, offerId3, offers[0].Sequence)
	}

	// Cross alice's last offer.
	result = env.Submit(OfferCreate(bob, usd50, xrp50).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify all offers consumed.
	{
		offers := SortedOffersOnAccount(env, alice)
		require.Equal(t, 0, len(offers))
	}
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
	jtx.RequireOwnerCount(t, env, alice, 1) // just trust line
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 200)
	jtx.RequireOwnerCount(t, env, bob, 1) // just trust line
}

// TestOffer_TicketCancelOffer verifies offers created with/without tickets
// can be canceled by transactions with/without tickets.
// Reference: rippled Offer_test.cpp testTicketCancelOffer (lines 4983-5093)
func TestOffer_TicketCancelOffer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testTicketCancelOffer(t, fs.disabled)
		})
	}
}

func testTicketCancelOffer(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.Trust(alice, USD(1000))
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireTicketCount(t, env, alice, 0)

	result := env.Submit(payment.PayIssued(gw, alice, USD(200)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	xrp50 := jtx.XRPTxAmountFromXRP(50)
	usd50 := USD(50)

	// Create the first of four offers using a sequence.
	offerSeqId0 := env.Seq(alice)
	result = env.Submit(OfferCreate(alice, xrp50, usd50).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 2)
	jtx.RequireTicketCount(t, env, alice, 0)

	// Create four tickets.
	ticketSeq := env.Seq(alice) + 1
	env.CreateTickets(alice, 4)
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 6) // 1 trust + 1 offer + 4 tickets
	jtx.RequireTicketCount(t, env, alice, 4)

	// Create the second (also sequence-based) offer.
	offerSeqId1 := env.Seq(alice)
	require.Equal(t, offerSeqId0+6, offerSeqId1)
	result = env.Submit(OfferCreate(alice, xrp50, usd50).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create the third (ticket-based) offer.
	offerTixId0 := ticketSeq + 1
	result = env.Submit(OfferCreate(alice, xrp50, usd50).TicketSeq(offerTixId0).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create the last offer.
	offerTixId1 := ticketSeq
	result = env.Submit(OfferCreate(alice, xrp50, usd50).TicketSeq(offerTixId1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify that all of alice's offers are present.
	{
		offers := SortedOffersOnAccount(env, alice)
		require.Equal(t, 4, len(offers))
		require.Equal(t, offerSeqId0, offers[0].Sequence)
		require.Equal(t, offerTixId1, offers[1].Sequence)
		require.Equal(t, offerTixId0, offers[2].Sequence)
		require.Equal(t, offerSeqId1, offers[3].Sequence)
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 200)
		jtx.RequireOwnerCount(t, env, alice, 7) // 1 trust + 4 offers + 2 remaining tickets
	}

	// Use a ticket to cancel an offer created with a sequence.
	result = env.Submit(OfferCancel(alice, offerSeqId0).TicketSeq(ticketSeq + 2).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify that offerSeqId0 was canceled.
	{
		offers := SortedOffersOnAccount(env, alice)
		require.Equal(t, 3, len(offers))
		require.Equal(t, offerTixId1, offers[0].Sequence)
		require.Equal(t, offerTixId0, offers[1].Sequence)
		require.Equal(t, offerSeqId1, offers[2].Sequence)
	}

	// Use a ticket to cancel an offer created with a ticket.
	result = env.Submit(OfferCancel(alice, offerTixId0).TicketSeq(ticketSeq + 3).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify that offerTixId0 was canceled.
	{
		offers := SortedOffersOnAccount(env, alice)
		require.Equal(t, 2, len(offers))
		require.Equal(t, offerTixId1, offers[0].Sequence)
		require.Equal(t, offerSeqId1, offers[1].Sequence)
	}

	// All of alice's tickets should now be used up.
	jtx.RequireOwnerCount(t, env, alice, 3) // 1 trust + 2 offers
	jtx.RequireTicketCount(t, env, alice, 0)

	// Use a sequence to cancel an offer created with a ticket.
	result = env.Submit(OfferCancel(alice, offerTixId1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify that offerTixId1 was canceled.
	{
		offers := SortedOffersOnAccount(env, alice)
		require.Equal(t, 1, len(offers))
		require.Equal(t, offerSeqId1, offers[0].Sequence)
	}

	// Use a sequence to cancel an offer created with a sequence.
	result = env.Submit(OfferCancel(alice, offerSeqId1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify that offerSeqId1 was canceled.
	// All of alice's tickets should now be used up.
	jtx.RequireOwnerCount(t, env, alice, 1) // just trust line
	jtx.RequireTicketCount(t, env, alice, 0)
	RequireOfferCount(t, env, alice, 0)
}
