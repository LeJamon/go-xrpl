package ticket

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	tickettx "github.com/LeJamon/goXRPLd/internal/core/tx/ticket"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

// TicketCreateBuilder provides a fluent interface for building TicketCreate transactions.
// Reference: rippled's ticket::create() in test/jtx/ticket.h
type TicketCreateBuilder struct {
	account   *testing.Account
	count     uint32
	fee       int64
	sequence  *uint32
	flags     uint32
	ticketSeq *uint32
}

// TicketCreate creates a new TicketCreateBuilder.
// count is the number of tickets to create (1-250).
func TicketCreate(account *testing.Account, count uint32) *TicketCreateBuilder {
	return &TicketCreateBuilder{
		account: account,
		count:   count,
		fee:     10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *TicketCreateBuilder) Fee(f int64) *TicketCreateBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *TicketCreateBuilder) Sequence(seq uint32) *TicketCreateBuilder {
	b.sequence = &seq
	return b
}

// Flags sets the transaction flags.
func (b *TicketCreateBuilder) Flags(flags uint32) *TicketCreateBuilder {
	b.flags = flags
	return b
}

// TicketSeq sets the TicketSequence (consumes a ticket to create new tickets).
// Reference: rippled's ticket::use() in test/jtx/ticket.h
func (b *TicketCreateBuilder) TicketSeq(ticketSeq uint32) *TicketCreateBuilder {
	b.ticketSeq = &ticketSeq
	seq := uint32(0)
	b.sequence = &seq
	return b
}

// Build constructs the TicketCreate transaction.
func (b *TicketCreateBuilder) Build() tx.Transaction {
	tc := tickettx.NewTicketCreate(b.account.Address, b.count)
	tc.Fee = fmt.Sprintf("%d", b.fee)

	if b.sequence != nil {
		tc.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		tc.SetFlags(b.flags)
	}
	if b.ticketSeq != nil {
		tc.TicketSequence = b.ticketSeq
	}

	return tc
}
