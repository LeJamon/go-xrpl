package payment

import (
	"testing"

	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
)

// Payment tests ported from rippled's TrustAndBalance_test.cpp and Flow_test.cpp
// Reference: rippled/src/test/app/TrustAndBalance_test.cpp
// Reference: rippled/src/test/app/Flow_test.cpp

// TestPaymentXRPTransfer tests basic XRP transfer between accounts.
// Reference: Flow_test.cpp - testDirectStep - XRP transfer
func TestPaymentXRPTransfer(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	// Fund accounts with 10000 XRP each
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Alice sends Bob 100 XRP
	payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).Build()
	result := env.Submit(payment)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Bob should have 10000 + 100 = 10100 XRP
	xrplgoTesting.RequireBalance(t, env, bob, uint64(xrplgoTesting.XRP(10100)))
	// Alice should have 10000 - 100 - fee
	xrplgoTesting.RequireBalance(t, env, alice, xrplgoTesting.XRPMinusFees(env, 10000-100, 1))
}

// TestPaymentToNonexistent tests payment to a nonexistent account.
// Reference: TrustAndBalance_test.cpp - testPayNonexistent
func TestPaymentToNonexistent(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob") // Not funded - doesn't exist

	// Fund alice
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Alice tries to send Bob 1 XRP - should fail with tecNO_DST_INSUF_XRP
	// because the amount is too small to create the account (below reserve)
	payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(1))).Build()
	result := env.Submit(payment)
	xrplgoTesting.RequireTxFail(t, result, xrplgoTesting.TecNO_DST_INSUF_XRP)
}

// TestPaymentCreatesAccount tests that payment creates destination account when sufficient.
func TestPaymentCreatesAccount(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob") // Will be created by payment

	// Fund alice
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Verify bob doesn't exist yet
	xrplgoTesting.RequireAccountNotExists(t, env, bob)

	// Alice sends Bob enough to create account (reserve + some)
	// Reserve is 200 XRP by default
	payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(250))).Build()
	result := env.Submit(payment)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Bob should now exist with 250 XRP
	xrplgoTesting.RequireAccountExists(t, env, bob)
	xrplgoTesting.RequireBalance(t, env, bob, uint64(xrplgoTesting.XRP(250)))
}

// TestPaymentInsufficientFunds tests payment with insufficient balance.
func TestPaymentInsufficientFunds(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	// Fund accounts - alice with minimal amount
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(250))) // Just enough to exist
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(1000)))
	env.Close()

	// Alice tries to send more XRP than she has
	// She has 250 XRP but needs to keep reserve (200 XRP), so spendable is ~50 XRP
	payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).Build()
	result := env.Submit(payment)
	xrplgoTesting.RequireTxFail(t, result, xrplgoTesting.TecUNFUNDED_PAYMENT)

	// Bob's balance should be unchanged
	xrplgoTesting.RequireBalance(t, env, bob, uint64(xrplgoTesting.XRP(1000)))
}

// TestPaymentSelfPayment tests that self-payment is not allowed.
// Reference: rippled Payment.cpp:159-166 - returns temREDUNDANT for self-payment
func TestPaymentSelfPayment(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Alice tries to pay herself
	payment := Pay(alice, alice, uint64(xrplgoTesting.XRP(100))).Build()
	result := env.Submit(payment)
	// Self-payment should fail with temREDUNDANT (not temDST_IS_SRC)
	// This matches rippled's behavior in Payment.cpp:159-166
	xrplgoTesting.RequireTxFail(t, result, xrplgoTesting.TemREDUNDANT)
}

// TestPaymentZeroAmount tests that zero amount payment is rejected.
func TestPaymentZeroAmount(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Try to send 0 XRP
	payment := Pay(alice, bob, 0).Build()
	result := env.Submit(payment)
	// Zero amount should fail with temBAD_AMOUNT
	// Reference: rippled Payment.cpp preflight() lines 148-152
	xrplgoTesting.RequireTxFail(t, result, xrplgoTesting.TemBAD_AMOUNT)
}

// TestPaymentMultiplePayments tests multiple sequential payments.
func TestPaymentMultiplePayments(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	carol := xrplgoTesting.NewAccount("carol")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(carol, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Alice sends Bob 100 XRP
	payment1 := Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).Build()
	result1 := env.Submit(payment1)
	xrplgoTesting.RequireTxSuccess(t, result1)

	// Bob sends Carol 50 XRP
	payment2 := Pay(bob, carol, uint64(xrplgoTesting.XRP(50))).Build()
	result2 := env.Submit(payment2)
	xrplgoTesting.RequireTxSuccess(t, result2)

	// Carol sends Alice 25 XRP
	payment3 := Pay(carol, alice, uint64(xrplgoTesting.XRP(25))).Build()
	result3 := env.Submit(payment3)
	xrplgoTesting.RequireTxSuccess(t, result3)

	env.Close()

	// Verify final balances (accounting for fees)
	// Alice: 10000 - 100 - fee + 25 = 9925 - fee
	xrplgoTesting.RequireBalance(t, env, alice, uint64(xrplgoTesting.XRP(10000)-xrplgoTesting.XRP(100)+xrplgoTesting.XRP(25))-env.BaseFee())
	// Bob: 10000 + 100 - 50 - fee = 10050 - fee
	xrplgoTesting.RequireBalance(t, env, bob, uint64(xrplgoTesting.XRP(10000)+xrplgoTesting.XRP(100)-xrplgoTesting.XRP(50))-env.BaseFee())
	// Carol: 10000 + 50 - 25 - fee = 10025 - fee
	xrplgoTesting.RequireBalance(t, env, carol, uint64(xrplgoTesting.XRP(10000)+xrplgoTesting.XRP(50)-xrplgoTesting.XRP(25))-env.BaseFee())
}

// TestPaymentWithSequenceNumber tests explicit sequence number handling.
func TestPaymentWithSequenceNumber(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Get Alice's current sequence
	seq := env.Seq(alice)

	// Submit payment with explicit sequence
	payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).Sequence(seq).Build()
	result := env.Submit(payment)
	xrplgoTesting.RequireTxSuccess(t, result)

	// Verify sequence incremented
	xrplgoTesting.RequireSequence(t, env, alice, seq+1)
}

// TestPaymentWrongSequence tests that wrong sequence number fails.
func TestPaymentWrongSequence(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Get Alice's current sequence
	seq := env.Seq(alice)

	// Submit payment with wrong sequence (past)
	payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).Sequence(seq - 1).Build()
	result := env.Submit(payment)
	// Should fail with tefPAST_SEQ
	xrplgoTesting.RequireTxFail(t, result, xrplgoTesting.TefPAST_SEQ)
}

// TestPaymentFutureSequence tests payment with future sequence number.
func TestPaymentFutureSequence(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Get Alice's current sequence
	seq := env.Seq(alice)

	// Submit payment with future sequence
	payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).Sequence(seq + 10).Build()
	result := env.Submit(payment)
	// Should fail with terPRE_SEQ (needs to wait for earlier sequences)
	xrplgoTesting.RequireTxFail(t, result, xrplgoTesting.TerPRE_SEQ)
}

// TestPaymentWithDestinationTag tests payment with destination tag.
func TestPaymentWithDestinationTag(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Send with destination tag
	payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).DestTag(12345).Build()
	result := env.Submit(payment)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	xrplgoTesting.RequireBalance(t, env, bob, uint64(xrplgoTesting.XRP(10100)))
}

// TestPaymentWithSourceTag tests payment with source tag.
func TestPaymentWithSourceTag(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Send with source tag
	payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(100))).SourceTag(54321).Build()
	result := env.Submit(payment)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	xrplgoTesting.RequireBalance(t, env, bob, uint64(xrplgoTesting.XRP(10100)))
}

// TestPaymentDrainsAccount tests sending maximum possible amount.
func TestPaymentDrainsAccount(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(1000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(1000)))
	env.Close()

	// Alice tries to send everything except reserve and fee
	// Reserve is 200 XRP, fee is 10 drops
	// So max sendable is 1000 - 200 - 0.00001 = ~799.99999 XRP
	// Let's try to send exactly what should be spendable

	// First calculate what alice can spend:
	// Balance: 1000 XRP
	// Reserve: 200 XRP (base reserve, no owner objects)
	// Spendable: 800 XRP minus fee

	// Send 789 XRP (leaving some buffer for fee)
	payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(789))).Build()
	result := env.Submit(payment)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Alice should have 1000 - 789 - fee = ~211 XRP - fee
	xrplgoTesting.RequireBalance(t, env, alice, uint64(xrplgoTesting.XRP(1000)-xrplgoTesting.XRP(789))-env.BaseFee())
	// Bob should have 1000 + 789 = 1789 XRP
	xrplgoTesting.RequireBalance(t, env, bob, uint64(xrplgoTesting.XRP(1789)))
}
