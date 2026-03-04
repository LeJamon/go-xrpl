package offer

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	offertx "github.com/LeJamon/goXRPLd/internal/core/tx/offer"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

// OfferCreateBuilder provides a fluent interface for building OfferCreate transactions.
type OfferCreateBuilder struct {
	account       *testing.Account
	takerPays     tx.Amount
	takerGets     tx.Amount
	expiration    *uint32
	offerSequence *uint32
	fee           uint64
	sequence      *uint32
	ticketSeq     *uint32
	flags         uint32
}

// OfferCreate creates a new OfferCreateBuilder.
// takerPays is what the offer creator receives, takerGets is what they pay.
func OfferCreate(account *testing.Account, takerPays, takerGets tx.Amount) *OfferCreateBuilder {
	return &OfferCreateBuilder{
		account:   account,
		takerPays: takerPays,
		takerGets: takerGets,
		fee:       10, // Default fee: 10 drops
	}
}

// OfferCreateXRP creates an offer where one side is XRP.
// If buyXRP is true, creates an offer to buy XRP with issued currency.
// If buyXRP is false, creates an offer to sell XRP for issued currency.
func OfferCreateXRP(account *testing.Account, xrpAmount uint64, issuedAmount tx.Amount, buyXRP bool) *OfferCreateBuilder {
	xrp := tx.NewXRPAmount(int64(xrpAmount))
	if buyXRP {
		// Offer creator receives XRP, pays issued currency
		return OfferCreate(account, xrp, issuedAmount)
	}
	// Offer creator receives issued currency, pays XRP
	return OfferCreate(account, issuedAmount, xrp)
}

// Expiration sets the expiration time (in Ripple epoch seconds).
func (b *OfferCreateBuilder) Expiration(exp uint32) *OfferCreateBuilder {
	b.expiration = &exp
	return b
}

// OfferSequence sets an existing offer to cancel when this one is created.
func (b *OfferCreateBuilder) OfferSequence(seq uint32) *OfferCreateBuilder {
	b.offerSequence = &seq
	return b
}

// Fee sets the transaction fee in drops.
func (b *OfferCreateBuilder) Fee(f uint64) *OfferCreateBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *OfferCreateBuilder) Sequence(seq uint32) *OfferCreateBuilder {
	b.sequence = &seq
	return b
}

// Passive makes the offer passive (won't consume matching offers).
func (b *OfferCreateBuilder) Passive() *OfferCreateBuilder {
	b.flags |= offertx.OfferCreateFlagPassive
	return b
}

// ImmediateOrCancel makes the offer immediate-or-cancel.
// The offer will only take what's available immediately and cancel the rest.
func (b *OfferCreateBuilder) ImmediateOrCancel() *OfferCreateBuilder {
	b.flags |= offertx.OfferCreateFlagImmediateOrCancel
	return b
}

// FillOrKill makes the offer fill-or-kill.
// The offer must be fully filled or it will be cancelled entirely.
func (b *OfferCreateBuilder) FillOrKill() *OfferCreateBuilder {
	b.flags |= offertx.OfferCreateFlagFillOrKill
	return b
}

// Sell makes this a sell offer.
// The taker gets at least as much as TakerGets, possibly more.
func (b *OfferCreateBuilder) Sell() *OfferCreateBuilder {
	b.flags |= offertx.OfferCreateFlagSell
	return b
}

// Flags sets the raw flags value, replacing any previously set flags.
// Use this for testing malformed flag combinations.
func (b *OfferCreateBuilder) Flags(f uint32) *OfferCreateBuilder {
	b.flags = f
	return b
}

// TicketSeq sets the ticket sequence and sets Sequence to 0.
func (b *OfferCreateBuilder) TicketSeq(ticketSeq uint32) *OfferCreateBuilder {
	b.ticketSeq = &ticketSeq
	seq := uint32(0)
	b.sequence = &seq
	return b
}

// Build constructs the OfferCreate transaction.
func (b *OfferCreateBuilder) Build() tx.Transaction {
	o := offertx.NewOfferCreate(b.account.Address, b.takerGets, b.takerPays)
	o.Fee = fmt.Sprintf("%d", b.fee)

	if b.expiration != nil {
		o.Expiration = b.expiration
	}
	if b.offerSequence != nil {
		o.OfferSequence = b.offerSequence
	}
	if b.sequence != nil {
		o.SetSequence(*b.sequence)
	}
	if b.ticketSeq != nil {
		o.TicketSequence = b.ticketSeq
	}
	if b.flags != 0 {
		o.SetFlags(b.flags)
	}

	return o
}

// BuildOfferCreate is a convenience method that returns the concrete *offertx.OfferCreate type.
func (b *OfferCreateBuilder) BuildOfferCreate() *offertx.OfferCreate {
	return b.Build().(*offertx.OfferCreate)
}

// OfferCancelBuilder provides a fluent interface for building OfferCancel transactions.
type OfferCancelBuilder struct {
	account   *testing.Account
	offerSeq  uint32
	fee       uint64
	sequence  *uint32
	ticketSeq *uint32
}

// OfferCancel creates a new OfferCancelBuilder.
// offerSeq is the sequence number of the OfferCreate transaction to cancel.
func OfferCancel(account *testing.Account, offerSeq uint32) *OfferCancelBuilder {
	return &OfferCancelBuilder{
		account:  account,
		offerSeq: offerSeq,
		fee:      10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *OfferCancelBuilder) Fee(f uint64) *OfferCancelBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *OfferCancelBuilder) Sequence(seq uint32) *OfferCancelBuilder {
	b.sequence = &seq
	return b
}

// TicketSeq sets the ticket sequence and sets Sequence to 0.
func (b *OfferCancelBuilder) TicketSeq(ticketSeq uint32) *OfferCancelBuilder {
	b.ticketSeq = &ticketSeq
	seq := uint32(0)
	b.sequence = &seq
	return b
}

// Build constructs the OfferCancel transaction.
func (b *OfferCancelBuilder) Build() tx.Transaction {
	o := offertx.NewOfferCancel(b.account.Address, b.offerSeq)
	o.Fee = fmt.Sprintf("%d", b.fee)

	if b.sequence != nil {
		o.SetSequence(*b.sequence)
	}
	if b.ticketSeq != nil {
		o.TicketSequence = b.ticketSeq
	}

	return o
}

// BuildOfferCancel is a convenience method that returns the concrete *offertx.OfferCancel type.
func (b *OfferCancelBuilder) BuildOfferCancel() *offertx.OfferCancel {
	return b.Build().(*offertx.OfferCancel)
}
