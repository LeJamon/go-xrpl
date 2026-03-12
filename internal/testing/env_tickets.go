package testing

import (
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/ticket"
)

// CreateTickets creates N tickets for an account.
// Returns the first ticket sequence number.
// Reference: rippled's ticket::create(account, count) in ticket.h
func (e *TestEnv) CreateTickets(acc *Account, count uint32) uint32 {
	e.t.Helper()

	// The starting ticket sequence is the account's current sequence
	startSeq := e.Seq(acc)

	tc := ticket.NewTicketCreate(acc.Address, count)
	tc.Fee = formatUint64(e.baseFee)
	seq := startSeq
	tc.Sequence = &seq

	result := e.Submit(tc)
	if !result.Success {
		e.t.Fatalf("Failed to create %d tickets for %s: %s", count, acc.Name, result.Code)
	}

	return startSeq + 1 // Tickets start at seq+1 (seq itself is consumed by TicketCreate)
}

// WithTicketSeq sets TicketSequence on a transaction (Sequence becomes 0).
// Reference: rippled's ticket::use(ticketSeq) in ticket.h
func WithTicketSeq(transaction tx.Transaction, ticketSeq uint32) tx.Transaction {
	common := transaction.GetCommon()
	zero := uint32(0)
	common.Sequence = &zero
	common.TicketSequence = &ticketSeq
	return transaction
}

// TicketCount returns the ticket count for an account (0 if account doesn't exist).
// Reference: rippled sfTicketCount field on AccountRoot
func (e *TestEnv) TicketCount(acc *Account) uint32 {
	e.t.Helper()
	info := e.AccountInfo(acc)
	if info == nil {
		return 0
	}
	return info.TicketCount
}
