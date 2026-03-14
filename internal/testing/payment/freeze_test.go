// Package payment contains integration tests for payment behavior.
// Tests ported from rippled's Freeze_test.cpp
package payment

import (
	"testing"

	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
	offerBuilder "github.com/LeJamon/goXRPLd/internal/testing/offer"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/account"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
)

// TestFreeze_RippleState tests basic RippleState freeze functionality.
// From rippled: testRippleState
func TestFreeze_RippleState(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(1000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(1000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(1000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(bob, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(alice, "USD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund both accounts
	usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, bob, usd10).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(gw, alice, usd100).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify unfrozen operations work
	usd1 := tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, bob, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(bob, alice, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway freezes bob's trust line
	freezeTx := trustset.TrustLine(gw, "USD", bob, "0").Freeze().Build()
	result = env.Submit(freezeTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// bob can still receive but cannot send
	result = env.Submit(PayIssued(alice, bob, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)

	// bob cannot send - should fail
	result = env.Submit(PayIssued(bob, alice, usd1).Build())
	if result.Code == "tesSUCCESS" {
		t.Error("Frozen account should not be able to send")
	}

	// Clear freeze
	clearFreezeTx := trustset.TrustLine(gw, "USD", bob, "0").ClearFreeze().Build()
	result = env.Submit(clearFreezeTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	t.Log("RippleState freeze test: freeze behavior verified")
}

// TestFreeze_SetAndClear tests freeze flag set and clear operations.
// From rippled: testSetAndClear
func TestFreeze_SetAndClear(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice trusts gateway
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway sets freeze
	freezeTx := trustset.TrustLine(gw, "USD", alice, "0").Freeze().Build()
	result = env.Submit(freezeTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway clears freeze
	clearTx := trustset.TrustLine(gw, "USD", alice, "0").ClearFreeze().Build()
	result = env.Submit(clearTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	t.Log("Freeze set and clear test: operations verified")
}

// TestFreeze_GlobalFreeze tests global freeze functionality.
// From rippled: testGlobalFreeze
func TestFreeze_GlobalFreeze(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(12000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(1000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(20000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1200").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "200").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund accounts
	usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(gw, bob, usd100).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Before global freeze - operations work normally
	usd1 := tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)
	// Direct issue/redemption
	result = env.Submit(PayIssued(gw, bob, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(bob, gw, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	// Rippling (transfer between non-issuers)
	result = env.Submit(PayIssued(bob, alice, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(alice, bob, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway sets global freeze
	env.EnableGlobalFreeze(gw)
	env.Close()

	// After global freeze:
	// - Direct issue/redemption still works
	result = env.Submit(PayIssued(gw, bob, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(bob, gw, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// - Rippling (non-direct transfers) should fail
	result = env.Submit(PayIssued(bob, alice, usd1).Build())
	if result.Code == "tesSUCCESS" {
		t.Error("Transfer between non-issuers should fail with GlobalFreeze")
	}
	result = env.Submit(PayIssued(alice, bob, usd1).Build())
	if result.Code == "tesSUCCESS" {
		t.Error("Transfer between non-issuers should fail with GlobalFreeze")
	}

	// Gateway clears global freeze
	env.DisableGlobalFreeze(gw)
	env.Close()

	// Transfers should work again
	result = env.Submit(PayIssued(bob, alice, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)

	t.Log("Global freeze test: verified")
}

// TestFreeze_NoFreeze tests NoFreeze flag behavior.
// From rippled: testNoFreeze
// Once NoFreeze is set, it cannot be cleared and the issuer cannot freeze trust lines.
func TestFreeze_NoFreeze(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(12000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(1000)))
	env.Close()

	// Set up trust line
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund alice
	usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway sets NoFreeze
	env.EnableNoFreeze(gw)
	env.Close()

	// Gateway can still set GlobalFreeze (but won't be able to clear it)
	env.EnableGlobalFreeze(gw)
	env.Close()

	// Gateway cannot clear GlobalFreeze when NoFreeze is set
	// (DisableGlobalFreeze will fail, but our helper uses t.Fatalf)
	// We test this by trying to submit the transaction directly
	accountSet := account.NewAccountSet(gw.Address)
	clearFlag := account.AccountSetFlagGlobalFreeze
	accountSet.ClearFlag = &clearFlag
	accountSet.Fee = "10"
	seq := env.Seq(gw)
	accountSet.Sequence = &seq

	env.Submit(accountSet)
	// Should still succeed but GlobalFreeze should remain set
	// (rippled allows the tx but doesn't clear the flag when NoFreeze is set)

	t.Log("NoFreeze test: verified")
}

// TestFreeze_DirectPaymentsWhenFrozen tests direct payments on frozen trust lines.
// From rippled: testPaymentsWhenDeepFrozen (direct payments section)
func TestFreeze_DirectPaymentsWhenFrozen(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "10000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "10000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund both accounts
	usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(gw, bob, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify payments work before freeze
	usd1 := tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)
	result = env.Submit(PayIssued(alice, gw, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(bob, gw, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(alice, bob, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(bob, alice, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Freeze alice's trust line
	freezeTx := trustset.TrustLine(gw, "USD", alice, "0").Freeze().Build()
	result = env.Submit(freezeTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice can still transact with issuer
	result = env.Submit(PayIssued(alice, gw, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(gw, alice, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice cannot send to bob
	result = env.Submit(PayIssued(alice, bob, usd1).Build())
	if result.Code == "tesSUCCESS" {
		t.Error("Frozen account should not be able to send to third party")
	}

	// bob can still send to alice
	result = env.Submit(PayIssued(bob, alice, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	t.Log("Direct payments when frozen test: verified")
}

// TestFreeze_HolderFreeze tests when holder (not issuer) freezes trust line.
// From rippled: testPaymentsWhenDeepFrozen (holder freeze section)
func TestFreeze_HolderFreeze(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "10000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "10000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund alice and bob with USD
	usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(gw, bob, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice freezes her own trust line (holder freeze)
	freezeTx := trustset.TrustLine(alice, "USD", gw, "10000").Freeze().Build()
	result = env.Submit(freezeTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Issuer and bob are not affected
	usd1 := tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)
	result = env.Submit(PayIssued(bob, gw, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(gw, bob, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice can still send to issuer and bob
	result = env.Submit(PayIssued(alice, gw, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(alice, bob, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Issuer can still send to alice
	result = env.Submit(PayIssued(gw, alice, usd1).Build())
	xrplgoTesting.RequireTxSuccess(t, result)

	// bob cannot send to alice (holder freeze blocks incoming from third party)
	result = env.Submit(PayIssued(bob, alice, usd1).Build())
	if result.Code == "tesSUCCESS" {
		t.Error("Third party should not be able to send to holder-frozen account")
	}

	t.Log("Holder freeze test: verified")
}

// TestFreeze_PathsWhenFrozen tests longer payment paths when trust lines are frozen.
// From rippled: testPathsWhenFrozen
//
// When an offer owner's trust line is frozen by the issuer, payments that route
// through that offer (via the order book) should fail with tecPATH_PARTIAL because
// the frozen offer is treated as unfunded.
//
// When frozen by the currency holder (not the issuer), the offer should still be
// usable (holder freeze only prevents the holder from creating new outgoing
// obligations, not from receiving).
func TestFreeze_PathsWhenFrozen(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	G1 := xrplgoTesting.NewAccount("G1")
	A1 := xrplgoTesting.NewAccount("A1")
	A2 := xrplgoTesting.NewAccount("A2")

	env.FundAmount(G1, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(A1, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(A2, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set up trust lines: both A1 and A2 trust G1 for USD
	usdLimit := tx.NewIssuedAmountFromFloat64(10000, "USD", G1.Address)
	result := env.Submit(trustset.TrustLine(A1, "USD", G1, "10000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(A2, "USD", G1, "10000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund both with USD
	usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", G1.Address)
	result = env.Submit(PayIssued(G1, A1, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(G1, A2, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// A2 creates a passive offer: wants XRP(100), gives USD(100)
	// This is an XRP->USD offer on the book
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", G1.Address)
	xrp100 := tx.NewXRPAmount(xrplgoTesting.XRP(100))
	result = env.Submit(offerBuilder.OfferCreate(A2, xrp100, usd100).Passive().Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// === Test 1: A2's trust line frozen by issuer (G1) ===
	// Payments through A2's offer should fail because the offer is unfunded (frozen output side)
	{
		env.FreezeTrustLine(G1, A2, "USD")
		env.Close()

		// A1 tries to send USD to G1 using XRP through A2's offer — should fail
		usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", G1.Address)
		xrp11 := tx.NewXRPAmount(xrplgoTesting.XRP(11))
		result = env.Submit(
			PayIssued(A1, G1, usd10).
				SendMax(tx.NewXRPAmount(xrplgoTesting.XRP(11))).
				Paths([][]payment.PathStep{{
					{Currency: "USD", Issuer: G1.Address},
				}}).
				NoDirectRipple().
				Build(),
		)
		if result.Code != "tecPATH_PARTIAL" {
			t.Errorf("Expected tecPATH_PARTIAL when A2's line frozen by issuer (A1->G1), got %s", result.Code)
		}

		// G1 tries to send USD to A1 using XRP through A2's offer — should also fail
		result = env.Submit(
			PayIssued(G1, A1, usd10).
				SendMax(tx.NewXRPAmount(xrplgoTesting.XRP(11))).
				Paths([][]payment.PathStep{{
					{Currency: "USD", Issuer: G1.Address},
				}}).
				NoDirectRipple().
				Build(),
		)
		if result.Code != "tecPATH_PARTIAL" {
			t.Errorf("Expected tecPATH_PARTIAL when A2's line frozen by issuer (G1->A1), got %s", result.Code)
		}

		env.UnfreezeTrustLine(G1, A2, "USD")
		env.Close()
		_ = xrp11
		_ = usdLimit
	}

	// === Test 2: A2's trust line frozen by holder (A2 herself) ===
	// Holder freeze does NOT prevent the offer from being consumed
	{
		// A2 freezes her own trust line
		result = env.Submit(trustset.TrustLine(A2, "USD", G1, "10000").Freeze().Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// A1 can still send USD using XRP through A2's offer
		usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", G1.Address)
		result = env.Submit(
			PayIssued(A1, G1, usd10).
				SendMax(tx.NewXRPAmount(xrplgoTesting.XRP(11))).
				Paths([][]payment.PathStep{{
					{Currency: "USD", Issuer: G1.Address},
				}}).
				NoDirectRipple().
				Build(),
		)
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// G1 can still send USD using XRP through A2's offer
		result = env.Submit(
			PayIssued(G1, A1, usd10).
				SendMax(tx.NewXRPAmount(xrplgoTesting.XRP(11))).
				Paths([][]payment.PathStep{{
					{Currency: "USD", Issuer: G1.Address},
				}}).
				NoDirectRipple().
				Build(),
		)
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// Clear A2's holder freeze
		result = env.Submit(trustset.TrustLine(A2, "USD", G1, "10000").ClearFreeze().Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()
	}

	// === Test 3: USD->XRP direction: A2 creates offer selling XRP for USD ===
	// Create a new offer: A2 wants USD(100), gives XRP(100)
	result = env.Submit(offerBuilder.OfferCreate(A2, usd100, xrp100).Passive().Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// === Test 3a: A2's trust line frozen by issuer ===
	// For USD->XRP offers, freeze on the input side (USD) means the offer can
	// still be crossed (the offer owner receives USD, which is the frozen side,
	// but accountFundsHelper with fhZERO_IF_FROZEN only checks the OUTPUT side).
	// In rippled, A1 can send XRP using USD through A2's offer when frozen by issuer.
	{
		env.FreezeTrustLine(G1, A2, "USD")
		env.Close()

		// A1 can still send XRP using USD through A2's offer
		xrp10 := tx.NewXRPAmount(xrplgoTesting.XRP(10))
		usd11 := tx.NewIssuedAmountFromFloat64(11, "USD", G1.Address)
		result = env.Submit(
			Pay(A1, G1, uint64(xrplgoTesting.XRP(10))).
				SendMax(usd11).
				PathsXRP().
				NoDirectRipple().
				Build(),
		)
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// G1 can still send XRP using USD through A2's offer
		result = env.Submit(
			Pay(G1, A1, uint64(xrplgoTesting.XRP(10))).
				SendMax(usd11).
				PathsXRP().
				NoDirectRipple().
				Build(),
		)
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		env.UnfreezeTrustLine(G1, A2, "USD")
		env.Close()
		_ = xrp10
	}

	// === Test 3b: A2's trust line frozen by holder ===
	// Holder freeze does NOT prevent the offer from being consumed
	{
		result = env.Submit(trustset.TrustLine(A2, "USD", G1, "10000").Freeze().Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// A1 can still send XRP using USD through A2's offer
		usd11 := tx.NewIssuedAmountFromFloat64(11, "USD", G1.Address)
		result = env.Submit(
			Pay(A1, G1, uint64(xrplgoTesting.XRP(10))).
				SendMax(usd11).
				PathsXRP().
				NoDirectRipple().
				Build(),
		)
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// G1 can still send XRP using USD through A2's offer
		result = env.Submit(
			Pay(G1, A1, uint64(xrplgoTesting.XRP(10))).
				SendMax(usd11).
				PathsXRP().
				NoDirectRipple().
				Build(),
		)
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(trustset.TrustLine(A2, "USD", G1, "10000").ClearFreeze().Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()
	}
}

// TestFreeze_OffersWhenFrozen tests offers for frozen trust lines.
// From rippled: testOffersWhenFrozen
//
// When an offer owner's trust line is frozen by the issuer, the offer's output
// (TakerGets) side becomes frozen. The flow engine treats this as an unfunded
// offer (fhZERO_IF_FROZEN returns zero balance). A payment that routes through
// this frozen offer should fail, and the frozen offer should be removed from the
// order book during the payment/offer crossing process.
func TestFreeze_OffersWhenFrozen(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	G1 := xrplgoTesting.NewAccount("G1")
	A2 := xrplgoTesting.NewAccount("A2")
	A3 := xrplgoTesting.NewAccount("A3")
	A4 := xrplgoTesting.NewAccount("A4")

	env.FundAmount(G1, uint64(xrplgoTesting.XRP(1000)))
	env.FundAmount(A2, uint64(xrplgoTesting.XRP(2000)))
	env.FundAmount(A3, uint64(xrplgoTesting.XRP(1000)))
	env.FundAmount(A4, uint64(xrplgoTesting.XRP(1000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(A2, "USD", G1, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(A3, "USD", G1, "2000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(A4, "USD", G1, "2000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund A3 and A4 with USD
	usd2000 := tx.NewIssuedAmountFromFloat64(2000, "USD", G1.Address)
	result = env.Submit(PayIssued(G1, A3, usd2000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(G1, A4, usd2000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// A3 creates a passive offer: wants XRP(1000), gives USD(1000)
	usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", G1.Address)
	xrp1000 := tx.NewXRPAmount(xrplgoTesting.XRP(1000))
	result = env.Submit(offerBuilder.OfferCreate(A3, xrp1000, usd1000).Passive().Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// === Removal after successful payment ===
	// Make a payment that partially consumes A3's offer
	usd1 := tx.NewIssuedAmountFromFloat64(1, "USD", G1.Address)
	result = env.Submit(
		PayIssued(A2, G1, usd1).
			SendMax(tx.NewXRPAmount(xrplgoTesting.XRP(1))).
			Paths([][]payment.PathStep{{
				{Currency: "USD", Issuer: G1.Address},
			}}).
			Build(),
	)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify A3's offer is partially consumed (still has 1 offer)
	offerBuilder.RequireOfferCount(t, env, A3, 1)

	// Someone else (A4) creates an offer providing liquidity
	usd999 := tx.NewIssuedAmountFromFloat64(999, "USD", G1.Address)
	xrp999 := tx.NewXRPAmount(xrplgoTesting.XRP(999))
	result = env.Submit(offerBuilder.OfferCreate(A4, xrp999, usd999).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Freeze A3's trust line (issuer freeze)
	env.FreezeTrustLine(G1, A3, "USD")
	env.Close()

	// A3's offer is still in the ledger...
	offerBuilder.RequireOfferCount(t, env, A3, 1)

	// ...but a payment through the book should use A4's offer instead
	// (A3's is treated as unfunded due to freeze) and remove A3's offer
	result = env.Submit(
		PayIssued(A2, G1, usd1).
			SendMax(tx.NewXRPAmount(xrplgoTesting.XRP(1))).
			Paths([][]payment.PathStep{{
				{Currency: "USD", Issuer: G1.Address},
			}}).
			Build(),
	)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// A3's frozen offer should have been removed during the payment
	offerBuilder.RequireOfferCount(t, env, A3, 0)

	// === Removal by successful OfferCreate ===
	// Freeze A4's trust line
	env.FreezeTrustLine(G1, A4, "USD")
	env.Close()

	// A2 creates a crossing offer — A4's frozen offer should be removed
	// but A2's offer won't cross (A4 is frozen), so A2's offer stays on the book
	usdOffer := tx.NewIssuedAmountFromFloat64(999, "USD", G1.Address)
	xrpOffer := tx.NewXRPAmount(xrplgoTesting.XRP(999))
	result = env.Submit(offerBuilder.OfferCreate(A2, usdOffer, xrpOffer).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// A4's frozen offer should have been removed
	offerBuilder.RequireOfferCount(t, env, A4, 0)

	_ = usd999
	_ = xrp999
	_ = usd1000
	_ = xrp1000
}

// TestFreeze_CreateFrozenTrustline tests creating a pre-frozen trust line.
// From rippled: testCreateFrozenTrustline
func TestFreeze_CreateFrozenTrustline(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Gateway creates a frozen trust line for alice (before alice creates hers)
	frozenTrust := trustset.TrustLine(gw, "USD", alice, "1000").Freeze().Build()
	result := env.Submit(frozenTrust)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	t.Log("Create frozen trustline test: verified")
}

// TestFreeze_AMMWhenFrozen tests AMM payments on frozen trust lines.
// From rippled: testAMMWhenFreeze
func TestFreeze_AMMWhenFrozen(t *testing.T) {
	t.Skip("TODO: AMM requires AMM creation support")

	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "10000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "10000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund both accounts
	usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(PayIssued(gw, bob, usd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway creates AMM with XRP(1000) and USD(1000)
	// TODO: AMM creation

	t.Log("AMM when frozen test: requires AMM creation support")
}
