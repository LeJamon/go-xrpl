// Package invariants_test verifies that goXRPL's ledger invariant checker correctly
// detects protocol violations after transactions are applied.
//
// Plan:
//  1. Positive tests: verify invariants hold after valid transactions (Payment,
//     OfferCreate, EscrowCreate, TrustSet, AccountDelete).
//  2. XRP conservation: verify total XRP is conserved across transactions.
//  3. XRP trust line: verify that XRP cannot be used as a trust line currency.
//
// The invariant checker is hooked into the engine (invariants_check.go) and runs
// before table.Apply(). Violations return TecINVARIANT_FAILED.
//
// Reference: rippled/src/test/app/Invariants_test.cpp
//
//	rippled/src/xrpld/app/tx/detail/InvariantCheck.cpp
package invariants_test

import (
	"fmt"
	"testing"
	"time"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/escrow"
	offerbuild "github.com/LeJamon/goXRPLd/internal/testing/offer"
	paymentbuild "github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	acctx "github.com/LeJamon/goXRPLd/internal/tx/account"
)

const baseFee = uint64(10)

// --------------------------------------------------------------------------
// Positive tests — invariants hold after valid transactions
// --------------------------------------------------------------------------

// TestInvariant_Payment verifies that a simple XRP payment leaves a valid ledger state.
// The invariant checker must not trigger for a valid transaction.
func TestInvariant_Payment(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	env.Fund(bob)

	aliceBefore := env.Balance(alice)
	bobBefore := env.Balance(bob)

	tx := paymentbuild.Pay(alice, bob, uint64(jtx.XRP(100))).Build()
	result := env.Submit(tx)
	jtx.RequireTxSuccess(t, result)

	aliceAfter := env.Balance(alice)
	bobAfter := env.Balance(bob)
	if aliceAfter >= aliceBefore {
		t.Errorf("alice balance should decrease: before=%d after=%d", aliceBefore, aliceAfter)
	}
	if bobAfter <= bobBefore {
		t.Errorf("bob balance should increase: before=%d after=%d", bobBefore, bobAfter)
	}
}

// TestInvariant_IOU_Payment verifies invariants hold for IOU payment.
func TestInvariant_IOU_Payment(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(gw)
	env.Fund(alice)
	env.Fund(bob)

	// Set up trust lines
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build()))
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build()))

	// Gateway issues USD to alice
	jtx.RequireTxSuccess(t, env.Submit(paymentbuild.PayIssued(gw, alice, gw.IOU("USD", 100)).Build()))

	// Alice sends USD to bob
	result := env.Submit(paymentbuild.PayIssued(alice, bob, gw.IOU("USD", 50)).Build())
	jtx.RequireTxSuccess(t, result)

	bobBalance := env.BalanceIOU(bob, "USD", gw)
	if bobBalance != 50 {
		t.Errorf("bob USD balance: want 50, got %v", bobBalance)
	}
}

// TestInvariant_Offer verifies invariants hold for offer creation and crossing.
func TestInvariant_Offer(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(gw)
	env.Fund(alice)
	env.Fund(bob)

	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustLine(alice, "USD", gw, "10000").Build()))
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustLine(bob, "USD", gw, "10000").Build()))
	jtx.RequireTxSuccess(t, env.Submit(paymentbuild.PayIssued(gw, alice, gw.IOU("USD", 1000)).Build()))

	// Bob places an offer: wants 100 USD, offers 100 XRP
	jtx.RequireTxSuccess(t, env.Submit(offerbuild.OfferCreate(bob, gw.IOU("USD", 100), jtx.XRPTxAmount(jtx.XRP(100))).Build()))

	// Alice places crossing offer: wants 100 XRP, offers 100 USD
	result := env.Submit(offerbuild.OfferCreate(alice, jtx.XRPTxAmount(jtx.XRP(100)), gw.IOU("USD", 100)).Build())
	jtx.RequireTxSuccess(t, result)
}

// TestInvariant_Escrow verifies invariants hold for XRP escrow lifecycle.
func TestInvariant_Escrow(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	env.Fund(bob)

	finishTime := env.Now().Add(2 * time.Second)
	ec := escrow.EscrowCreate(alice, bob, jtx.XRP(100)).FinishTime(finishTime).Build()
	seq := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(ec))

	// Advance time past FinishAfter and close so ParentCloseTime is updated.
	// EscrowFinish reads ParentCloseTime from the ledger header, not the clock.
	env.AdvanceTime(5 * time.Second)
	env.Close()
	ef := escrow.EscrowFinish(bob, alice, seq).Build()
	jtx.RequireTxSuccess(t, env.Submit(ef))
}

// TestInvariant_AccountDelete verifies AccountDelete can delete an AccountRoot
// without triggering the AccountRootsNotDeleted invariant violation.
func TestInvariant_AccountDelete(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	env.Fund(bob)

	// AccountDelete requires sequence number 256+ ahead of account's sequence
	// Advance the ledger sequence sufficiently
	for i := 0; i < 260; i++ {
		env.Close()
	}

	// Delete alice's account. AccountDelete's special fee is one
	// ReserveIncrement (50 XRP). Reference: rippled AccountDelete::calculateBaseFee.
	delTx := acctx.NewAccountDelete(alice.Address, bob.Address)
	delTx.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
	result := env.Submit(delTx)
	jtx.RequireTxSuccess(t, result)
}

// --------------------------------------------------------------------------
// XRP conservation tests
// --------------------------------------------------------------------------

// TestInvariant_Payment_DoesNotCreateXRP verifies XRP is conserved after a payment.
// The XRPNotCreated invariant catches any transaction that increases total XRP.
func TestInvariant_Payment_DoesNotCreateXRP(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	env.Fund(bob)

	aliceStart := env.Balance(alice)
	bobStart := env.Balance(bob)
	totalStart := aliceStart + bobStart

	jtx.RequireTxSuccess(t, env.Submit(paymentbuild.Pay(alice, bob, uint64(jtx.XRP(200))).Build()))

	totalEnd := env.Balance(alice) + env.Balance(bob)

	// Total XRP must decrease by exactly the fee
	if totalEnd > totalStart {
		t.Errorf("XRP was created: start=%d end=%d (increased by %d)", totalStart, totalEnd, totalEnd-totalStart)
	}
	if totalEnd != totalStart-baseFee {
		t.Errorf("XRP conservation: want decrease of %d, got start=%d end=%d", baseFee, totalStart, totalEnd)
	}
}

// TestInvariant_EscrowCreate_AmountConserved verifies XRP is conserved through escrow lifecycle.
func TestInvariant_EscrowCreate_AmountConserved(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	env.Fund(bob)

	aliceStart := env.Balance(alice)
	escrowed := uint64(jtx.XRP(100))
	finishTime := env.Now().Add(2 * time.Second)
	seq := env.Seq(alice)

	jtx.RequireTxSuccess(t, env.Submit(escrow.EscrowCreate(alice, bob, jtx.XRP(100)).FinishTime(finishTime).Build()))

	aliceAfterCreate := env.Balance(alice)
	if aliceAfterCreate != aliceStart-escrowed-baseFee {
		t.Errorf("after EscrowCreate: alice balance want %d, got %d", aliceStart-escrowed-baseFee, aliceAfterCreate)
	}

	// Finish: bob receives the escrowed amount.
	// Advance time past FinishAfter and close so ParentCloseTime is updated.
	env.AdvanceTime(5 * time.Second)
	env.Close()
	bobStart := env.Balance(bob)
	jtx.RequireTxSuccess(t, env.Submit(escrow.EscrowFinish(bob, alice, seq).Build()))
	bobEnd := env.Balance(bob)

	if bobEnd != bobStart+escrowed-baseFee {
		t.Errorf("after EscrowFinish: bob balance want %d, got %d", bobStart+escrowed-baseFee, bobEnd)
	}
}

// --------------------------------------------------------------------------
// Protocol enforcement tests
// --------------------------------------------------------------------------

// TestInvariant_TrustSet_NoXRP verifies that XRP cannot be used as a trust line currency.
// The NoXRPTrustLines invariant would catch any violation.
func TestInvariant_TrustSet_NoXRP(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	env.Fund(bob)

	// Attempting to set a trust line with XRP currency should fail at validation
	xrpLine := trustset.TrustLine(alice, "XRP", bob, "1000").Build()
	result := env.Submit(xrpLine)

	// Should be rejected — XRP is not a valid IOU currency
	if result.Code == jtx.TesSUCCESS {
		t.Error("TrustSet with XRP currency should be rejected (NoXRPTrustLines invariant)")
	}
}

// TestInvariant_MultiplePayments_NoXRPCreated verifies XRP conservation across many payments.
func TestInvariant_MultiplePayments_NoXRPCreated(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.Fund(alice)
	env.Fund(bob)
	env.Fund(carol)

	measure := func() uint64 {
		return env.Balance(alice) + env.Balance(bob) + env.Balance(carol)
	}

	start := measure()
	txCount := 0

	// Series of payments in a triangle
	jtx.RequireTxSuccess(t, env.Submit(paymentbuild.Pay(alice, bob, uint64(jtx.XRP(50))).Build()))
	txCount++
	jtx.RequireTxSuccess(t, env.Submit(paymentbuild.Pay(bob, carol, uint64(jtx.XRP(30))).Build()))
	txCount++
	jtx.RequireTxSuccess(t, env.Submit(paymentbuild.Pay(carol, alice, uint64(jtx.XRP(20))).Build()))
	txCount++

	end := measure()
	expectedDecrease := uint64(txCount) * baseFee

	if end > start {
		t.Errorf("XRP was created: start=%d end=%d", start, end)
	}
	if end != start-expectedDecrease {
		t.Errorf("XRP not conserved: start=%d end=%d expected_decrease=%d actual_decrease=%d",
			start, end, expectedDecrease, start-end)
	}
}
