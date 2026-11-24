package nft_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestNFTokenAcceptSellOfferTicketFundsBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		want ter.Result
	}{
		{name: "BelowPreclaimFunds", mode: "below", want: ter.TecINSUFFICIENT_FUNDS},
		{name: "ExactPreclaimFundsFeeStraddlesReserve", mode: "preclaim", want: ter.TecINSUFFICIENT_RESERVE},
		{name: "ExactFinalReserve", mode: "final", want: ter.TesSUCCESS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newAcceptXRPFixture(t, "direct sell", false)
			ticketSeq := fixture.env.CreateTickets(fixture.buyer, 1)
			fixture.env.Close()
			reserve := fixture.env.ReserveBase() + fixture.env.ReserveIncrement()
			balance := reserve + fixture.price - 1
			switch tc.mode {
			case "preclaim":
				balance = reserve + fixture.price
			case "final":
				balance = reserve + fixture.price + fixture.env.BaseFee()
			}
			setXRPBalance(t, fixture.env, fixture.buyer, balance)
			preBalance := fixture.env.Balance(fixture.buyer)
			preSequence := fixture.env.Seq(fixture.buyer)
			jtx.WithTicketSeq(fixture.txn, ticketSeq)

			result := fixture.env.Submit(fixture.txn)
			if result.Result != tc.want {
				t.Fatalf("ticketed NFTokenAcceptOffer result = %s, want %s", result.Result, tc.want)
			}
			fixture.env.Close()
			if result.Fee != fixture.env.BaseFee() {
				t.Fatalf("ticketed NFTokenAcceptOffer fee = %d, want %d", result.Fee, fixture.env.BaseFee())
			}
			jtx.RequireSequence(t, fixture.env, fixture.buyer, preSequence)
			jtx.RequireTicketCount(t, fixture.env, fixture.buyer, 0)
			if tc.want.IsTec() {
				jtx.RequireBalance(t, fixture.env, fixture.buyer, preBalance-result.Fee)
				jtx.RequireOwnerCount(t, fixture.env, fixture.buyer, 0)
				if !fixture.env.LedgerEntryExists(fixture.offerKeys[0]) {
					t.Fatal("sell offer was removed after insufficient ticketed funds")
				}
				if !ownsNFToken(t, fixture.env.Ledger(), fixture.seller.ID, fixture.tokenID) {
					t.Fatal("seller lost the NFT after insufficient ticketed funds")
				}
				if ownsNFToken(t, fixture.env.Ledger(), fixture.buyer.ID, fixture.tokenID) {
					t.Fatal("buyer received the NFT despite insufficient ticketed funds")
				}
				return
			}

			jtx.RequireBalance(t, fixture.env, fixture.buyer, preBalance-result.Fee-fixture.price)
			jtx.RequireOwnerCount(t, fixture.env, fixture.buyer, 1)
			if fixture.env.LedgerEntryExists(fixture.offerKeys[0]) {
				t.Fatal("sell offer remains after successful ticketed acceptance")
			}
			if ownsNFToken(t, fixture.env.Ledger(), fixture.seller.ID, fixture.tokenID) {
				t.Fatal("seller still owns the NFT after successful ticketed acceptance")
			}
			if !ownsNFToken(t, fixture.env.Ledger(), fixture.buyer.ID, fixture.tokenID) {
				t.Fatal("buyer did not receive the NFT after successful ticketed acceptance")
			}
		})
	}
}

func TestNFTokenAcceptSellOfferSequenceFundsControl(t *testing.T) {
	fixture := newAcceptXRPFixture(t, "direct sell", false)
	sequence := fixture.env.Seq(fixture.buyer)
	reserve := fixture.env.ReserveBase() + fixture.env.ReserveIncrement()
	setXRPBalance(t, fixture.env, fixture.buyer, reserve+fixture.price+fixture.env.BaseFee())
	preBalance := fixture.env.Balance(fixture.buyer)
	result := fixture.env.Submit(fixture.txn)
	if result.Result != ter.TesSUCCESS {
		t.Fatalf("sequence NFTokenAcceptOffer result = %s, want %s", result.Result, ter.TesSUCCESS)
	}
	fixture.env.Close()
	if result.Fee != fixture.env.BaseFee() {
		t.Fatalf("sequence NFTokenAcceptOffer fee = %d, want %d", result.Fee, fixture.env.BaseFee())
	}
	jtx.RequireBalance(t, fixture.env, fixture.buyer, preBalance-result.Fee-fixture.price)
	jtx.RequireSequence(t, fixture.env, fixture.buyer, sequence+1)
	jtx.RequireOwnerCount(t, fixture.env, fixture.buyer, 1)
	jtx.RequireTicketCount(t, fixture.env, fixture.buyer, 0)
	if fixture.env.LedgerEntryExists(fixture.offerKeys[0]) {
		t.Fatal("sell offer remains after successful sequence acceptance")
	}
	if !ownsNFToken(t, fixture.env.Ledger(), fixture.buyer.ID, fixture.tokenID) {
		t.Fatal("buyer did not receive the NFT after successful sequence acceptance")
	}
}
