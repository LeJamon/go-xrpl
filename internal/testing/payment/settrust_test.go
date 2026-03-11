// Package payment contains integration tests for trust line behavior.
// Tests ported from rippled's SetTrust_test.cpp
package payment

import (
	"testing"

	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/stretchr/testify/require"
)

// TestSetTrust_TrustLineDelete tests deletion of trust lines by setting limit to zero.
// From rippled: SetTrust_test::testTrustLineDelete
func TestSetTrust_TrustLineDelete(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	becky := xrplgoTesting.NewAccount("becky")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(becky, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// becky wants to hold at most 50 tokens of alice["USD"]
	result := env.Submit(trustset.TrustLine(becky, "USD", alice, "50").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify trust line exists
	exists := env.TrustLineExists(becky, alice, "USD")
	require.True(t, exists, "Trust line should exist after creation")

	// Reset the trust line limit to zero
	result = env.Submit(trustset.TrustLine(becky, "USD", alice, "0").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify trust line is deleted
	exists = env.TrustLineExists(becky, alice, "USD")
	require.False(t, exists, "Trust line should be deleted when limit set to 0")

	t.Log("SetTrust trust line delete test passed")
}

// TestSetTrust_ResetWithAuthFlag tests trust line reset with authorized flag.
// From rippled: SetTrust_test::testTrustLineResetWithAuthFlag
func TestSetTrust_ResetWithAuthFlag(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	becky := xrplgoTesting.NewAccount("becky")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(becky, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice wants authorized trust lines
	env.EnableRequireAuth(alice)
	env.Close()

	// becky creates a trust line
	result := env.Submit(trustset.TrustLine(becky, "USD", alice, "50").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice authorizes becky (using tfSetfAuth flag on TrustSet)
	authTrustLine := trustset.TrustLine(alice, "USD", becky, "0")
	authTrustLine.SetAuth()
	result = env.Submit(authTrustLine.Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify trust line exists
	exists := env.TrustLineExists(becky, alice, "USD")
	require.True(t, exists, "Trust line should exist")

	// Reset the trust line limit to zero
	result = env.Submit(trustset.TrustLine(becky, "USD", alice, "0").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Trust line should be deleted despite authorization
	exists = env.TrustLineExists(becky, alice, "USD")
	require.False(t, exists, "Trust line should be deleted even when authorized")

	t.Log("SetTrust reset with auth flag test passed")
}

// TestSetTrust_FreeTrustlines tests that the first two trust lines are "free"
// (don't require extra reserve beyond the base account reserve).
// From rippled: SetTrust_test::testFreeTrustlines (thirdLineCreatesLE=true variant)
//
// When OwnerCount < 2, the reserve for a new ledger object is 0.
// This means an account funded with only the base reserve (200 XRP) can create
// up to 2 trust lines without any additional reserve. The 3rd trust line
// requires the full accountReserve(3) = baseReserve + 3*increment.
// Reference: rippled SetTrust.cpp lines 405-407:
//   (uOwnerCount < 2) ? XRPAmount(beast::zero) : view().fees().accountReserve(uOwnerCount + 1)
func TestSetTrust_FreeTrustlines(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gwA := xrplgoTesting.NewAccount("gwA")
	gwB := xrplgoTesting.NewAccount("gwB")
	creator := xrplgoTesting.NewAccount("creator")
	assistor := xrplgoTesting.NewAccount("assistor")

	// Fund gateways and assistor generously
	env.FundAmount(gwA, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(gwB, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(assistor, uint64(xrplgoTesting.XRP(10000)))

	// Fund creator with just enough: baseReserve + 3 tx fees
	// This is exactly enough to hold an account and pay for 3 transactions.
	baseReserve := env.ReserveBase()          // 200_000_000 drops (200 XRP)
	baseFee := env.BaseFee()                  // 10 drops
	creatorFunding := baseReserve + 3*baseFee // 200000030 drops
	env.FundAmount(creator, creatorFunding)
	env.Close()

	// First trust line: creator has OwnerCount=0 (< 2), so reserve is 0.
	// Creator can afford this.
	result := env.Submit(trustset.TrustLine(creator, "USD", gwA, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, env.TrustLineExists(creator, gwA, "USD"), "First trust line should exist")
	xrplgoTesting.RequireOwnerCount(t, env, creator, 1)

	// Second trust line: creator has OwnerCount=1 (< 2), so reserve is still 0.
	// Creator can afford this.
	result = env.Submit(trustset.TrustLine(creator, "USD", gwB, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, env.TrustLineExists(creator, gwB, "USD"), "Second trust line should exist")
	xrplgoTesting.RequireOwnerCount(t, env, creator, 2)

	// Third trust line: creator has OwnerCount=2 (>= 2), so reserve is
	// accountReserve(3) = baseReserve + 3*increment = 200 + 150 = 350 XRP.
	// Creator only has ~200 XRP (minus fees), so this fails.
	result = env.Submit(trustset.TrustLine(creator, "USD", assistor, "100").Build())
	require.Equal(t, "tecNO_LINE_INSUF_RESERVE", result.Code,
		"Third trust line should fail with insufficient reserve")
	env.Close()
	require.False(t, env.TrustLineExists(creator, assistor, "USD"), "Third trust line should NOT exist")
	xrplgoTesting.RequireOwnerCount(t, env, creator, 2)

	// Fund creator with additional XRP to cover the reserve for 3 trust lines.
	// Need threelineReserve - baseReserve = (baseReserve + 3*increment) - baseReserve = 3*increment
	// Also add baseFee to cover the fee deducted before Apply runs.
	threeLineExtra := 3*env.ReserveIncrement() + baseFee // 150_000_010 drops
	master := env.MasterAccount()
	payTx := Pay(master, creator, threeLineExtra).Build()
	result = env.Submit(payTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Now the third trust line should succeed.
	result = env.Submit(trustset.TrustLine(creator, "USD", assistor, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, env.TrustLineExists(creator, assistor, "USD"), "Third trust line should now exist")
	xrplgoTesting.RequireOwnerCount(t, env, creator, 3)
}

// TestSetTrust_UsingTicket tests trust line creation with ticket.
// From rippled: SetTrust_test::testUsingTicket
func TestSetTrust_UsingTicket(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	gw := xrplgoTesting.NewAccount("gateway")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create tickets
	ticketSeq := env.CreateTickets(alice, 2)
	env.Close()

	seqBefore := env.Seq(alice)

	// Create trust line using a ticket
	trustTx := trustset.TrustLine(alice, "USD", gw, "1000").Build()
	xrplgoTesting.WithTicketSeq(trustTx, ticketSeq)
	result := env.Submit(trustTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify trust line exists
	exists := env.TrustLineExists(alice, gw, "USD")
	require.True(t, exists, "Trust line should exist after creation with ticket")

	// Verify sequence did not advance
	require.Equal(t, seqBefore, env.Seq(alice), "Sequence should not advance when using ticket")
}

// TestSetTrust_Malformed tests malformed TrustSet transactions.
// From rippled: SetTrust_test::testMalformed
func TestSetTrust_Malformed(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Trust line to self should fail
	result := env.Submit(trustset.TrustLine(alice, "USD", alice, "1000").Build())
	require.Contains(t, result.Code, "tem",
		"Trust line to self should fail with tem* error")

	// Trust line with XRP currency should fail
	xrpTrustLine := trustset.TrustLine(alice, "XRP", bob, "1000")
	result = env.Submit(xrpTrustLine.Build())
	require.Contains(t, result.Code, "tem",
		"Trust line with XRP currency should fail with tem* error")

	t.Log("SetTrust malformed test passed")
}

// TestSetTrust_TrustNonexistentAccount tests trust line to non-existent account.
// From rippled: TrustAndBalance_test::testTrustNonexistentAccount
func TestSetTrust_TrustNonexistentAccount(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	unfunded := xrplgoTesting.NewAccount("unfunded")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Trust line to non-existent account should fail with tecNO_DST
	result := env.Submit(trustset.TrustLine(alice, "USD", unfunded, "1000").Build())
	require.Equal(t, "tecNO_DST", result.Code,
		"Trust line to non-existent account should fail with tecNO_DST")

	t.Log("SetTrust to non-existent account test passed")
}

// TestSetTrust_DisallowIncoming tests trust lines with disallow incoming flag.
// From rippled: SetTrust_test::testDisallowIncoming
func TestSetTrust_DisallowIncoming(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)
	env.EnableFeature("DisallowIncoming")

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set DisallowIncomingTrustline on gateway
	env.EnableDisallowIncomingTrustline(gw)
	env.Close()

	// Alice tries to create trust line to gateway — should fail
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code,
		"Trust line to account with DisallowIncomingTrustline should fail")

	// Unset the flag on gateway
	env.DisableDisallowIncomingTrustline(gw)
	env.Close()

	// Now alice can create a trust line
	result = env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Set flag on gateway again
	env.EnableDisallowIncomingTrustline(gw)
	env.Close()

	// Existing trust line still works — gateway can pay alice
	env.PayIOU(gw, alice, gw, "USD", 200)
	env.Close()

	// Set flag on bob too
	env.EnableDisallowIncomingTrustline(bob)
	env.Close()

	// Bob can't open a trust line to gateway (both have the flag but bob doesn't have one yet)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	require.Equal(t, "tecNO_PERMISSION", result.Code,
		"Trust line should fail when counterparty has DisallowIncomingTrustline set")

	// Unset the flag only on gateway
	env.DisableDisallowIncomingTrustline(gw)
	env.Close()

	// Now bob can open a trust line (bob has flag but gw doesn't)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway can send bob a balance
	env.PayIOU(gw, bob, gw, "USD", 200)
	env.Close()
}

// TestSetTrust_PaymentsWithPathsAndFees tests payments with paths and fees.
// From rippled: TrustAndBalance_test::testPaymentsWithPathsAndFees
func TestSetTrust_PaymentsWithPathsAndFees(t *testing.T) {
	t.Skip("TODO: Payments with paths and fees requires path finding and transfer rate")

	t.Log("SetTrust payments with paths and fees test: requires path support")
}

// TestSetTrust_InvoiceID tests setting invoice ID on payment.
// From rippled: TrustAndBalance_test::testInvoiceID
func TestSetTrust_InvoiceID(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Payment with invoice ID
	invoiceID := "0000000000000000000000000000000000000000000000000000000000000001"
	payTx := Pay(alice, bob, 100_000_000).InvoiceID(invoiceID).Build()
	result := env.Submit(payTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	t.Log("SetTrust invoice ID test passed")
}

// TestSetTrust_NegativeLimit tests that negative limit is rejected.
// From rippled: SetTrust validation
func TestSetTrust_NegativeLimit(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	gw := xrplgoTesting.NewAccount("gateway")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Trust line with negative limit should fail
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "-100").Build())
	// Should fail with tem* error (malformed)
	require.Contains(t, result.Code, "tem",
		"Negative trust line limit should fail")

	t.Log("SetTrust negative limit test passed")
}

// TestSetTrust_QualityInOut tests QualityIn and QualityOut fields.
// From rippled: trust quality settings
func TestSetTrust_QualityInOut(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	gw := xrplgoTesting.NewAccount("gateway")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust line with QualityIn and QualityOut
	trustTx := trustset.TrustLine(alice, "USD", gw, "1000").
		QualityIn(900_000_000).   // 0.9 quality factor
		QualityOut(1_100_000_000) // 1.1 quality factor
	result := env.Submit(trustTx.Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify trust line exists
	exists := env.TrustLineExists(alice, gw, "USD")
	require.True(t, exists, "Trust line with quality should exist")

	// Modify quality on existing trust line
	trustTx2 := trustset.TrustLine(alice, "USD", gw, "1000").
		QualityIn(1_000_000_000). // reset to 1.0
		QualityOut(1_000_000_000) // reset to 1.0
	result = env.Submit(trustTx2.Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()
}

// TestSetTrust_NoRippleFlag tests NoRipple flag on trust lines.
// From rippled: NoRipple trust line flag
func TestSetTrust_NoRippleFlag(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	gw := xrplgoTesting.NewAccount("gateway")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust line with NoRipple flag
	trustLine := trustset.TrustLine(alice, "USD", gw, "1000")
	trustLine.NoRipple()
	result := env.Submit(trustLine.Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify trust line exists
	exists := env.TrustLineExists(alice, gw, "USD")
	require.True(t, exists, "Trust line should exist with NoRipple flag")

	t.Log("SetTrust NoRipple flag test passed")
}

// TestSetTrust_FreezeTrustLine tests freezing a trust line.
// From rippled: Freeze trust line via TrustSet
func TestSetTrust_FreezeTrustLine(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice creates trust line to gw
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// gw freezes alice's trust line
	freezeTrustLine := trustset.TrustLine(gw, "USD", alice, "0")
	freezeTrustLine.Freeze()
	result = env.Submit(freezeTrustLine.Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	t.Log("SetTrust freeze trust line test passed")
}
