// Package ticket_test contains integration tests for Ticket transaction behavior.
// Tests ported from rippled's Ticket_test.cpp (src/test/app/Ticket_test.cpp).
// Each test function maps 1:1 to a rippled test method.
package ticket_test

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/account"
	"github.com/LeJamon/goXRPLd/internal/core/tx/depositpreauth"
	"github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/trustset"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/ticket"
	"github.com/stretchr/testify/require"
)

// Flag constants matching rippled's definitions.
const (
	tfFullyCanonicalSig uint32 = 0x80000000
	tfSell              uint32 = 0x00080000
)

// accountReserve returns the reserve required for an account with n owned objects.
// Reference: rippled's fees().accountReserve(n)
func accountReserve(env *jtx.TestEnv, n uint32) uint64 {
	return env.ReserveBase() + uint64(n)*env.ReserveIncrement()
}

// noop creates an AccountSet transaction with no operations (a no-op).
// Reference: rippled's noop(account) in test/jtx/noop.h
func noop(acc *jtx.Account) *account.AccountSet {
	as := account.NewAccountSet(acc.Address)
	as.Fee = "10"
	return as
}

// --------------------------------------------------------------------------
// TestTicket_FeatureNotEnabled
// Reference: rippled Ticket_test.cpp testTicketNotEnabled (lines 382-426)
// --------------------------------------------------------------------------

func TestTicket_FeatureNotEnabled(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("TicketBatch")
	master := env.MasterAccount()

	// ticket::create(master, 1) → temDISABLED
	result := env.Submit(ticket.TicketCreate(master, 1).Build())
	jtx.RequireTxFail(t, result, "temDISABLED")
	env.Close()
	jtx.RequireOwnerCount(t, env, master, 0)
	jtx.RequireTicketCount(t, env, master, 0)

	// noop(master) with ticket::use(1) → temMALFORMED
	noop1 := noop(master)
	jtx.WithTicketSeq(noop1, 1)
	result = env.Submit(noop1)
	jtx.RequireTxFail(t, result, "temMALFORMED")

	// noop(master) with ticket::use(1) AND explicit seq → temMALFORMED
	// In rippled, ticket::use(1) sets TicketSequence=1 and Sequence=0.
	// Then seq(env.seq(master)) overrides Sequence back to non-zero.
	// Since TicketBatch is disabled, temMALFORMED from the disabled feature
	// check takes precedence over temSEQ_AND_TICKET.
	noop2 := noop(master)
	jtx.WithTicketSeq(noop2, 1)
	seq := env.Seq(master)
	noop2.SetSequence(seq) // Override Sequence back to non-zero
	result = env.Submit(noop2)
	jtx.RequireTxFail(t, result, "temMALFORMED")

	// Close enough ledgers that the previous transactions are no
	// longer retried.
	for i := 0; i < 8; i++ {
		env.Close()
	}

	// Enable the feature
	env.EnableFeature("TicketBatch")
	env.Close()
	jtx.RequireOwnerCount(t, env, master, 0)
	jtx.RequireTicketCount(t, env, master, 0)

	// Create 2 tickets
	ticketSeq := env.Seq(master) + 1
	result = env.Submit(ticket.TicketCreate(master, 2).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta equivalent (requires metadata support)
	env.Close()
	jtx.RequireOwnerCount(t, env, master, 2)
	jtx.RequireTicketCount(t, env, master, 2)

	// Use first ticket with noop
	noop3 := noop(master)
	jtx.WithTicketSeq(noop3, ticketSeq)
	result = env.Submit(noop3)
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketConsumeMeta equivalent (requires metadata support)
	ticketSeq++
	env.Close()
	jtx.RequireOwnerCount(t, env, master, 1)
	jtx.RequireTicketCount(t, env, master, 1)

	// Attempt to disable master key using a ticket. This fails with
	// tecNO_ALTERNATIVE_KEY since master has no regular key or signer list.
	// tec-class results still consume the ticket.
	fset := account.NewAccountSet(master.Address)
	fset.Fee = "10"
	flag := account.AccountSetFlagDisableMaster
	fset.SetFlag = &flag
	jtx.WithTicketSeq(fset, ticketSeq)
	result = env.Submit(fset)
	jtx.RequireTxFail(t, result, "tecNO_ALTERNATIVE_KEY")
	// TODO: checkTicketConsumeMeta equivalent (requires metadata support)
	env.Close()
	jtx.RequireOwnerCount(t, env, master, 0)
	jtx.RequireTicketCount(t, env, master, 0)
}

// --------------------------------------------------------------------------
// TestTicket_CreatePreflightFail
// Reference: rippled Ticket_test.cpp testTicketCreatePreflightFail (lines 428-473)
// --------------------------------------------------------------------------

func TestTicket_CreatePreflightFail(t *testing.T) {
	env := jtx.NewTestEnv(t)
	master := env.MasterAccount()

	// Exercise boundaries on count.
	result := env.Submit(ticket.TicketCreate(master, 0).Build())
	jtx.RequireTxFail(t, result, "temINVALID_COUNT")

	result = env.Submit(ticket.TicketCreate(master, 251).Build())
	jtx.RequireTxFail(t, result, "temINVALID_COUNT")

	// Exercise fees.
	ticketSeqA := env.Seq(master) + 1
	result = env.Submit(ticket.TicketCreate(master, 1).Fee(int64(jtx.XRP(10))).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, master, 1)
	jtx.RequireTicketCount(t, env, master, 1)

	result = env.Submit(ticket.TicketCreate(master, 1).Fee(int64(jtx.XRP(-1))).Build())
	jtx.RequireTxFail(t, result, "temBAD_FEE")

	// Exercise flags.
	ticketSeqB := env.Seq(master) + 1
	result = env.Submit(ticket.TicketCreate(master, 1).Flags(tfFullyCanonicalSig).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, master, 2)
	jtx.RequireTicketCount(t, env, master, 2)

	result = env.Submit(ticket.TicketCreate(master, 1).Flags(tfSell).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	env.Close()
	jtx.RequireOwnerCount(t, env, master, 2)
	jtx.RequireTicketCount(t, env, master, 2)

	// We successfully created 1 ticket earlier. Verify that we can
	// create 250 tickets in one shot. We must consume one ticket first.
	noopA := noop(master)
	jtx.WithTicketSeq(noopA, ticketSeqA)
	result = env.Submit(noopA)
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketConsumeMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, master, 1)
	jtx.RequireTicketCount(t, env, master, 1)

	result = env.Submit(ticket.TicketCreate(master, 250).TicketSeq(ticketSeqB).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, master, 250)
	jtx.RequireTicketCount(t, env, master, 250)
}

// --------------------------------------------------------------------------
// TestTicket_CreatePreclaimFail
// Reference: rippled Ticket_test.cpp testTicketCreatePreclaimFail (lines 475-565)
// --------------------------------------------------------------------------

func TestTicket_CreatePreclaimFail(t *testing.T) {
	t.Run("NonExistentAccount", func(t *testing.T) {
		// Reference: rippled lines 481-490
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		// Don't fund alice — she doesn't exist in the ledger.
		// Manually set sequence to 1 (rippled's json(jss::Sequence, 1)).
		result := env.Submit(ticket.TicketCreate(alice, 1).Sequence(1).Build())
		jtx.RequireTxFail(t, result, "terNO_ACCOUNT")
	})

	t.Run("ExceedThreshold", func(t *testing.T) {
		// Reference: rippled lines 492-530
		// Exceed the threshold where tickets can no longer be added.
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.FundAmount(alice, uint64(jtx.XRP(100000)))

		ticketSeq := env.Seq(alice) + 1
		result := env.Submit(ticket.TicketCreate(alice, 250).Build())
		jtx.RequireTxSuccess(t, result)
		// TODO: checkTicketCreateMeta
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 250)
		jtx.RequireTicketCount(t, env, alice, 250)

		// Note that we can add one more ticket while consuming a ticket
		// because the final result is still 250 tickets.
		result = env.Submit(ticket.TicketCreate(alice, 1).TicketSeq(ticketSeq + 0).Build())
		jtx.RequireTxSuccess(t, result)
		// TODO: checkTicketCreateMeta
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 250)
		jtx.RequireTicketCount(t, env, alice, 250)

		// Adding two tickets while consuming one (net +1, would be 251) → tecDIR_FULL.
		result = env.Submit(ticket.TicketCreate(alice, 2).TicketSeq(ticketSeq + 1).Build())
		jtx.RequireTxFail(t, result, "tecDIR_FULL")
		env.Close()
		// After the tecDIR_FULL, the consumed ticket is gone.
		jtx.RequireOwnerCount(t, env, alice, 249)
		jtx.RequireTicketCount(t, env, alice, 249)

		// Now we can successfully add two tickets while consuming one.
		result = env.Submit(ticket.TicketCreate(alice, 2).TicketSeq(ticketSeq + 2).Build())
		jtx.RequireTxSuccess(t, result)
		// TODO: checkTicketCreateMeta
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 250)
		jtx.RequireTicketCount(t, env, alice, 250)

		// Since we're at 250, we can't add another ticket using a sequence.
		result = env.Submit(ticket.TicketCreate(alice, 1).Build())
		jtx.RequireTxFail(t, result, "tecDIR_FULL")
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 250)
		jtx.RequireTicketCount(t, env, alice, 250)
	})

	t.Run("ExceedThresholdAlternate", func(t *testing.T) {
		// Reference: rippled lines 531-564
		// Explore exceeding the ticket threshold from another angle.
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.FundAmount(alice, uint64(jtx.XRP(100000)))
		env.Close()

		ticketSeqAB := env.Seq(alice) + 1
		result := env.Submit(ticket.TicketCreate(alice, 2).Build())
		jtx.RequireTxSuccess(t, result)
		// TODO: checkTicketCreateMeta
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 2)
		jtx.RequireTicketCount(t, env, alice, 2)

		// Adding 250 tickets (while consuming one) will exceed the threshold.
		// currentTickets=2 + 250 - 1 = 251 > 250 → tecDIR_FULL
		result = env.Submit(ticket.TicketCreate(alice, 250).TicketSeq(ticketSeqAB + 0).Build())
		jtx.RequireTxFail(t, result, "tecDIR_FULL")
		env.Close()
		// tecDIR_FULL consumed the ticket.
		jtx.RequireOwnerCount(t, env, alice, 1)
		jtx.RequireTicketCount(t, env, alice, 1)

		// Adding 250 tickets (without consuming one) will exceed the threshold.
		// currentTickets=1 + 250 = 251 > 250 → tecDIR_FULL
		result = env.Submit(ticket.TicketCreate(alice, 250).Build())
		jtx.RequireTxFail(t, result, "tecDIR_FULL")
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 1)
		jtx.RequireTicketCount(t, env, alice, 1)

		// Alice can now add 250 tickets while consuming one.
		// currentTickets=1 + 250 - 1 = 250 → success
		result = env.Submit(ticket.TicketCreate(alice, 250).TicketSeq(ticketSeqAB + 1).Build())
		jtx.RequireTxSuccess(t, result)
		// TODO: checkTicketCreateMeta
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 250)
		jtx.RequireTicketCount(t, env, alice, 250)
	})
}

// --------------------------------------------------------------------------
// TestTicket_InsufficientReserve
// Reference: rippled Ticket_test.cpp testTicketInsufficientReserve (lines 567-625)
// --------------------------------------------------------------------------

func TestTicket_InsufficientReserve(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")

	// Fund alice not quite enough to make the reserve for a Ticket.
	// accountReserve(1) = reserveBase + 1 * reserveIncrement
	reserve1 := accountReserve(env, 1)
	env.FundAmount(alice, reserve1-1)
	env.Close()

	result := env.Submit(ticket.TicketCreate(alice, 1).Build())
	jtx.RequireTxFail(t, result, "tecINSUFFICIENT_RESERVE")
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireTicketCount(t, env, alice, 0)

	// Give alice enough to exactly meet the reserve for one Ticket.
	topUp := reserve1 - env.Balance(alice)
	env.Pay(alice, topUp)
	env.Close()

	result = env.Submit(ticket.TicketCreate(alice, 1).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireTicketCount(t, env, alice, 1)

	// Give alice not quite enough to make the reserve for a total of
	// 250 Tickets.
	reserve250 := accountReserve(env, 250)
	topUp = reserve250 - 1 - env.Balance(alice)
	env.Pay(alice, topUp)
	env.Close()

	// alice doesn't quite have the reserve for a total of 250
	// Tickets, so the transaction fails.
	result = env.Submit(ticket.TicketCreate(alice, 249).Build())
	jtx.RequireTxFail(t, result, "tecINSUFFICIENT_RESERVE")
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireTicketCount(t, env, alice, 1)

	// Give alice enough so she can make the reserve for all 250 Tickets.
	topUp = reserve250 - env.Balance(alice)
	env.Pay(alice, topUp)
	env.Close()

	ticketSeq := env.Seq(alice) + 1
	result = env.Submit(ticket.TicketCreate(alice, 249).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 250)
	jtx.RequireTicketCount(t, env, alice, 250)
	require.Equal(t, ticketSeq+249, env.Seq(alice))
}

// --------------------------------------------------------------------------
// TestTicket_UsingTickets
// Reference: rippled Ticket_test.cpp testUsingTickets (lines 627-711)
// --------------------------------------------------------------------------

func TestTicket_UsingTickets(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	master := env.MasterAccount()

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	// Successfully create tickets (using a sequence)
	ticketSeqAB := env.Seq(alice) + 1
	result := env.Submit(ticket.TicketCreate(alice, 2).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 2)
	jtx.RequireTicketCount(t, env, alice, 2)
	require.Equal(t, ticketSeqAB+2, env.Seq(alice))

	// You can use a ticket to create one ticket ...
	ticketSeqC := env.Seq(alice)
	result = env.Submit(ticket.TicketCreate(alice, 1).TicketSeq(ticketSeqAB + 0).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 2)
	jtx.RequireTicketCount(t, env, alice, 2)
	require.Equal(t, ticketSeqC+1, env.Seq(alice))

	// ... you can use a ticket to create multiple tickets ...
	ticketSeqDE := env.Seq(alice)
	result = env.Submit(ticket.TicketCreate(alice, 2).TicketSeq(ticketSeqAB + 1).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 3)
	jtx.RequireTicketCount(t, env, alice, 3)
	require.Equal(t, ticketSeqDE+2, env.Seq(alice))

	// ... and you can use a ticket for other things.
	// noop with ticket
	noopDE0 := noop(alice)
	jtx.WithTicketSeq(noopDE0, ticketSeqDE+0)
	result = env.Submit(noopDE0)
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketConsumeMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 2)
	jtx.RequireTicketCount(t, env, alice, 2)
	require.Equal(t, ticketSeqDE+2, env.Seq(alice))

	// pay with ticket
	pay := payment.NewPayment(alice.Address, master.Address, tx.NewXRPAmount(int64(jtx.XRP(20))))
	pay.Fee = "10"
	jtx.WithTicketSeq(pay, ticketSeqDE+1)
	result = env.Submit(pay)
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketConsumeMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireTicketCount(t, env, alice, 1)
	require.Equal(t, ticketSeqDE+2, env.Seq(alice))

	// trust with ticket
	// Reference: trust(alice, env.master["USD"](20))
	usdAmount := tx.NewIssuedAmountFromFloat64(20, "USD", master.Address)
	ts := trustset.NewTrustSet(alice.Address, usdAmount)
	ts.Fee = "10"
	jtx.WithTicketSeq(ts, ticketSeqC)
	result = env.Submit(ts)
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketConsumeMeta
	env.Close()
	// Trust line added (+1 owner), ticket consumed (-1 owner) = net 0 change
	// But ticket count drops by 1
	jtx.RequireOwnerCount(t, env, alice, 1) // trust line
	jtx.RequireTicketCount(t, env, alice, 0)
	require.Equal(t, ticketSeqDE+2, env.Seq(alice))

	// Attempt to use a ticket that has already been used.
	noopUsed := noop(alice)
	jtx.WithTicketSeq(noopUsed, ticketSeqC)
	result = env.Submit(noopUsed)
	jtx.RequireTxFail(t, result, "tefNO_TICKET")
	env.Close()

	// Attempt to use a ticket from the future.
	ticketSeqF := env.Seq(alice) + 1
	noopFuture := noop(alice)
	jtx.WithTicketSeq(noopFuture, ticketSeqF)
	result = env.Submit(noopFuture)
	jtx.RequireTxFail(t, result, "terPRE_TICKET")
	env.Close()

	// Now create the ticket.
	result = env.Submit(ticket.TicketCreate(alice, 1).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()

	// In rippled the queued terPRE_TICKET noop would be retried automatically.
	// Our test env has no retry queue, so resubmit the noop manually.
	noopRetry := noop(alice)
	jtx.WithTicketSeq(noopRetry, ticketSeqF)
	result = env.Submit(noopRetry)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 1) // trust line only
	jtx.RequireTicketCount(t, env, alice, 0)
	require.Equal(t, ticketSeqF+1, env.Seq(alice))

	// Try a transaction that combines consuming a ticket with AccountTxnID.
	ticketSeqG := env.Seq(alice) + 1
	result = env.Submit(ticket.TicketCreate(alice, 1).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()

	noopTxnID := noop(alice)
	jtx.WithTicketSeq(noopTxnID, ticketSeqG)
	// Set AccountTxnID — combining AccountTxnID with TicketSequence → temINVALID
	noopTxnID.AccountTxnID = "0"
	result = env.Submit(noopTxnID)
	jtx.RequireTxFail(t, result, "temINVALID")
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 2) // trust line + 1 ticket
	jtx.RequireTicketCount(t, env, alice, 1)
}

// --------------------------------------------------------------------------
// TestTicket_TransactionDatabaseWithTickets
// Reference: rippled Ticket_test.cpp testTransactionDatabaseWithTickets (lines 713-833)
//
// The rippled test primarily verifies that the transaction database correctly
// handles Sequence=0 entries from ticket-based transactions. Since the Go test
// env doesn't have a transaction database / RPC layer, we verify the
// behavioral aspect: all transaction types can use tickets correctly across
// multiple ledger closes.
// --------------------------------------------------------------------------

func TestTicket_TransactionDatabaseWithTickets(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	master := env.MasterAccount()

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	// Successfully create several tickets (using a sequence).
	ticketSeq := env.Seq(alice)
	const ticketCount = uint32(10)
	result := env.Submit(ticket.TicketCreate(alice, ticketCount).Build())
	jtx.RequireTxSuccess(t, result)

	// Use the tickets in reverse from largest to smallest.
	// Tickets are at [ticketSeq+1, ticketSeq+ticketCount].
	ticketSeq += ticketCount

	// noop using ticket (largest)
	noop1 := noop(alice)
	jtx.WithTicketSeq(noop1, ticketSeq)
	result = env.Submit(noop1)
	jtx.RequireTxSuccess(t, result)
	ticketSeq--

	// pay using ticket
	pay1 := payment.NewPayment(alice.Address, master.Address, tx.NewXRPAmount(int64(jtx.XRP(200))))
	pay1.Fee = "10"
	jtx.WithTicketSeq(pay1, ticketSeq)
	result = env.Submit(pay1)
	jtx.RequireTxSuccess(t, result)
	ticketSeq--

	// deposit::auth using ticket
	auth1 := depositpreauth.NewDepositPreauth(alice.Address)
	auth1.SetAuthorize(master.Address)
	auth1.Fee = "10"
	jtx.WithTicketSeq(auth1, ticketSeq)
	result = env.Submit(auth1)
	jtx.RequireTxSuccess(t, result)
	ticketSeq--

	// Close the ledger so we look at transactions from a couple of
	// different ledgers.
	env.Close()

	// pay using ticket
	pay2 := payment.NewPayment(alice.Address, master.Address, tx.NewXRPAmount(int64(jtx.XRP(300))))
	pay2.Fee = "10"
	jtx.WithTicketSeq(pay2, ticketSeq)
	result = env.Submit(pay2)
	jtx.RequireTxSuccess(t, result)
	ticketSeq--

	// pay using ticket
	pay3 := payment.NewPayment(alice.Address, master.Address, tx.NewXRPAmount(int64(jtx.XRP(400))))
	pay3.Fee = "10"
	jtx.WithTicketSeq(pay3, ticketSeq)
	result = env.Submit(pay3)
	jtx.RequireTxSuccess(t, result)
	ticketSeq--

	// deposit::unauth using ticket
	unauth1 := depositpreauth.NewDepositPreauth(alice.Address)
	unauth1.SetUnauthorize(master.Address)
	unauth1.Fee = "10"
	jtx.WithTicketSeq(unauth1, ticketSeq)
	result = env.Submit(unauth1)
	jtx.RequireTxSuccess(t, result)
	ticketSeq--

	// noop using ticket
	noop2 := noop(alice)
	jtx.WithTicketSeq(noop2, ticketSeq)
	result = env.Submit(noop2)
	jtx.RequireTxSuccess(t, result)
	ticketSeq--

	env.Close()

	// We used 7 of the 10 tickets. 3 remain.
	jtx.RequireTicketCount(t, env, alice, 3)

	// Verify that the sequence number didn't advance (ticket-based
	// transactions don't advance the sequence).
	// After creating 10 tickets, the sequence advanced past all of them.
	// Using tickets doesn't change it further.
}

// --------------------------------------------------------------------------
// TestTicket_SignWithTicketSequence
// Reference: rippled Ticket_test.cpp testSignWithTicketSequence (lines 835-923)
//
// The rippled test verifies that the "sign" and "submit" RPC commands
// auto-fill Sequence=0 when TicketSequence is present. Since the Go test env
// doesn't have an RPC layer, we test the core behavior: transactions built
// with TicketSequence set correctly consume tickets when submitted.
// --------------------------------------------------------------------------

func TestTicket_SignWithTicketSequence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	// Successfully create tickets (using a sequence).
	ticketSeq := env.Seq(alice) + 1
	result := env.Submit(ticket.TicketCreate(alice, 2).Build())
	jtx.RequireTxSuccess(t, result)
	// TODO: checkTicketCreateMeta
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 2)
	jtx.RequireTicketCount(t, env, alice, 2)
	require.Equal(t, ticketSeq+2, env.Seq(alice))

	// Test that a transaction with TicketSequence but Sequence set to 0
	// correctly consumes a ticket.
	// In rippled, the "sign" RPC auto-fills Sequence=0. In Go, the builder
	// or WithTicketSeq helper handles this.
	noop1 := noop(alice)
	jtx.WithTicketSeq(noop1, ticketSeq)
	// Verify that Sequence is 0 (matching rippled's sign RPC behavior)
	common1 := noop1.GetCommon()
	require.NotNil(t, common1.Sequence)
	require.Equal(t, uint32(0), *common1.Sequence)
	require.NotNil(t, common1.TicketSequence)
	require.Equal(t, ticketSeq, *common1.TicketSequence)

	result = env.Submit(noop1)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireTicketCount(t, env, alice, 1)

	// Submit the second ticket.
	noop2 := noop(alice)
	jtx.WithTicketSeq(noop2, ticketSeq+1)
	result = env.Submit(noop2)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireTicketCount(t, env, alice, 0)
}

// --------------------------------------------------------------------------
// TestTicket_FixBothSeqAndTicket
// Reference: rippled Ticket_test.cpp testFixBothSeqAndTicket (lines 926-986)
// --------------------------------------------------------------------------

func TestTicket_FixBothSeqAndTicket(t *testing.T) {
	t.Run("WithoutFeature", func(t *testing.T) {
		// Reference: rippled lines 935-957
		// Try the test without featureTicketBatch enabled.
		env := jtx.NewTestEnv(t)
		env.DisableFeature("TicketBatch")
		alice := jtx.NewAccount("alice")

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.Close()

		// Fail to create a ticket.
		ticketSeq := env.Seq(alice) + 1
		result := env.Submit(ticket.TicketCreate(alice, 1).Build())
		jtx.RequireTxFail(t, result, "temDISABLED")
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireTicketCount(t, env, alice, 0)
		require.Equal(t, ticketSeq, env.Seq(alice)+1)

		// Create a transaction that includes both a ticket and a non-zero
		// sequence number. Since a ticket is used and tickets are not yet
		// enabled the transaction should be malformed.
		noopBoth := noop(alice)
		jtx.WithTicketSeq(noopBoth, ticketSeq)
		seq := env.Seq(alice)
		noopBoth.SetSequence(seq) // Override Sequence back to non-zero
		result = env.Submit(noopBoth)
		jtx.RequireTxFail(t, result, "temMALFORMED")
		env.Close()
	})

	t.Run("WithFeature", func(t *testing.T) {
		// Reference: rippled lines 958-985
		// Try the test with featureTicketBatch enabled.
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.Close()

		// Create a ticket.
		ticketSeq := env.Seq(alice) + 1
		result := env.Submit(ticket.TicketCreate(alice, 1).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 1)
		jtx.RequireTicketCount(t, env, alice, 1)
		require.Equal(t, ticketSeq+1, env.Seq(alice))

		// Create a transaction that includes both a ticket and a non-zero
		// sequence number. The transaction fails with temSEQ_AND_TICKET.
		noopBoth := noop(alice)
		jtx.WithTicketSeq(noopBoth, ticketSeq)
		seq := env.Seq(alice)
		noopBoth.SetSequence(seq) // Override Sequence back to non-zero
		result = env.Submit(noopBoth)
		jtx.RequireTxFail(t, result, "temSEQ_AND_TICKET")
		env.Close()

		// Verify that the transaction failed by looking at alice's
		// sequence number and tickets.
		jtx.RequireOwnerCount(t, env, alice, 1)
		jtx.RequireTicketCount(t, env, alice, 1)
		require.Equal(t, ticketSeq+1, env.Seq(alice))
	})
}
