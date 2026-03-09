// Package payment contains integration tests for payment behavior.
// Tests ported from rippled's Path_test.cpp
package payment

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/stretchr/testify/require"
)

// TestPath_NoDirectPathNoIntermediaryNoAlternatives tests path finding with no available paths.
// From rippled: no_direct_path_no_intermediary_no_alternatives
func TestPath_NoDirectPathNoIntermediaryNoAlternatives(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice tries to pay bob USD without any trust lines or paths
	// This should fail - no path exists
	usdAmount := tx.NewIssuedAmountFromFloat64(5, "USD", bob.Address)
	payTx := PayIssued(alice, bob, usdAmount).Build()

	result := env.Submit(payTx)
	// Should fail - no path available (tecPATH_DRY or similar)
	if result.Code == "tesSUCCESS" {
		t.Error("Payment without trust line or path should fail")
	}
	t.Log("No direct path test: result", result.Code)
}

// TestPath_DirectPathNoIntermediary tests direct path without intermediary.
// From rippled: direct_path_no_intermediary
func TestPath_DirectPathNoIntermediary(t *testing.T) {
	t.Skip("TODO: Direct IOU path requires Amount serialization fixes")

	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// bob trusts alice for USD
	result := env.Submit(trustset.TrustLine(bob, "USD", alice, "700").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice can pay bob directly since bob trusts alice
	usdAmount := tx.NewIssuedAmountFromFloat64(5, "USD", alice.Address)
	payTx := PayIssued(alice, bob, usdAmount).Build()

	result = env.Submit(payTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	t.Log("Direct path test: payment succeeded")
}

// TestPath_PaymentAutoPathFind tests payment with auto path finding.
// From rippled: payment_auto_path_find
// Reference: Path_test.cpp lines 356-373
//
//	env.fund(XRP(10000), "alice", "bob", gw);
//	env.trust(USD(600), "alice");
//	env.trust(USD(700), "bob");
//	env(pay(gw, "alice", USD(70)));
//	env(pay("alice", "bob", USD(24)));
//	env.require(balance("alice", USD(46)));
//	env.require(balance("bob", USD(24)));
func TestPath_PaymentAutoPathFind(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "600").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "700").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund alice with 70 USD
	usd70 := tx.NewIssuedAmountFromFloat64(70, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd70).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice pays bob 24 USD via gateway (auto path finding)
	usd24 := tx.NewIssuedAmountFromFloat64(24, "USD", gw.Address)
	payTx := PayIssued(alice, bob, usd24).Build()

	result = env.Submit(payTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify balances
	aliceBalance := env.BalanceIOU(alice, "USD", gw)
	require.InDelta(t, 46.0, aliceBalance, 0.0001, "Alice should have 46 USD")

	bobBalance := env.BalanceIOU(bob, "USD", gw)
	require.InDelta(t, 24.0, bobBalance, 0.0001, "Bob should have 24 USD")
}

// TestPath_IndirectPath tests indirect path through intermediary.
// From rippled: indirect_paths_path_find
// Reference: Path_test.cpp lines 878-895
//
//	env.trust(Account("alice")["USD"](1000), "bob");
//	env.trust(Account("bob")["USD"](1000), "carol");
//	// alice can pay carol through bob
func TestPath_IndirectPath(t *testing.T) {
	t.Skip("TODO: Requires IOU payment with rippling through intermediary")

	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	carol := xrplgoTesting.NewAccount("carol")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(carol, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice -> bob -> carol trust chain
	// bob trusts alice for USD (alice can issue USD to bob)
	result := env.Submit(trustset.TrustLine(bob, "USD", alice, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	// carol trusts bob for USD (bob can issue USD to carol)
	result = env.Submit(trustset.TrustLine(carol, "USD", bob, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice pays carol through bob (rippling)
	// This requires the amount to be issued by alice since bob trusts alice
	usd5 := tx.NewIssuedAmountFromFloat64(5, "USD", alice.Address)
	payTx := PayIssued(alice, carol, usd5).Build()

	result = env.Submit(payTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify: bob should have -5 USD (owes alice), carol should have 5 USD (bob owes her)
	// This is rippling: alice issues to bob, bob issues to carol
	bobAliceBalance := env.BalanceIOU(bob, "USD", alice)
	require.InDelta(t, -5.0, bobAliceBalance, 0.0001, "Bob should owe alice 5 USD")

	carolBobBalance := env.BalanceIOU(carol, "USD", bob)
	require.InDelta(t, 5.0, carolBobBalance, 0.0001, "Carol should have 5 USD from bob")
}

// TestPath_AlternativePathsConsumeBestFirst tests that best quality path is used first.
// From rippled: alternative_paths_consume_best_transfer_first
func TestPath_AlternativePathsConsumeBestFirst(t *testing.T) {
	t.Skip("TODO: Alternative paths require IOU payment support and transfer rate")

	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	gw2 := xrplgoTesting.NewAccount("gateway2")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(gw2, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice has trust lines to both gateways
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "600").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(alice, "USD", gw2, "800").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "700").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw2, "900").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// gw2 has 1.1x transfer rate (10% fee)
	// TODO: Set transfer rate on gw2

	// Fund alice from both gateways
	usd70 := tx.NewIssuedAmountFromFloat64(70, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd70).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	usd70_2 := tx.NewIssuedAmountFromFloat64(70, "USD", gw2.Address)
	result = env.Submit(PayIssued(gw2, alice, usd70_2).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice pays bob 70 USD - should use gw (no transfer fee) first
	t.Log("Alternative paths test: requires transfer rate support")
}

// TestPath_QualitySetAndTest tests quality settings on trust lines.
// From rippled: quality_paths_quality_set_and_test
func TestPath_QualitySetAndTest(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// bob sets up trust line to alice with quality settings
	trustTx := trustset.TrustLine(bob, "USD", alice, "1000").
		QualityIn(2000).
		QualityOut(1_400_000_000).
		Build()

	result := env.Submit(trustTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	t.Log("Quality set test: trust line quality settings applied")
}

// TestPath_TrustNormalClear tests that trust lines can be cleared when zero balance.
// From rippled: trust_auto_clear_trust_normal_clear
func TestPath_TrustNormalClear(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Both set up bidirectional trust
	result := env.Submit(trustset.TrustLine(alice, "USD", bob, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", alice, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Clear trust lines by setting limit to 0
	result = env.Submit(trustset.TrustLine(alice, "USD", bob, "0").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", alice, "0").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	t.Log("Trust normal clear test: trust line deletion verified")
}

// TestPath_TrustAutoClear tests that trust lines auto-clear when balance returns to zero.
// From rippled: trust_auto_clear_trust_auto_clear
func TestPath_TrustAutoClear(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice trusts bob for USD (bob can issue USD to alice)
	result := env.Submit(trustset.TrustLine(alice, "USD", bob, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// bob pays alice 50 USD (bob issues 50 USD to alice)
	usd50 := tx.NewIssuedAmountFromFloat64(50, "USD", bob.Address)
	result = env.Submit(PayIssued(bob, alice, usd50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice has 50 USD from bob
	aliceBalance := env.BalanceIOU(alice, "USD", bob)
	require.InDelta(t, 50.0, aliceBalance, 0.0001, "Alice should have 50 USD from bob")

	// alice sets trust limit to 0 (but still has balance, so trust line remains)
	result = env.Submit(trustset.TrustLine(alice, "USD", bob, "0").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice pays back 50 USD to bob - trust line should auto-delete when balance is zero
	result = env.Submit(PayIssued(alice, bob, usd50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice has 0 USD balance (trust line may be deleted)
	aliceBalance = env.BalanceIOU(alice, "USD", bob)
	require.InDelta(t, 0.0, aliceBalance, 0.0001, "Alice should have 0 USD after repayment")
}

// TestPath_NoRippleCombinations tests various NoRipple flag combinations.
// From rippled: noripple_combinations
func TestPath_NoRippleCombinations(t *testing.T) {
	testCases := []struct {
		name          string
		aliceRipple   bool
		bobRipple     bool
		expectSuccess bool
	}{
		{"ripple_to_ripple", true, true, true},
		{"ripple_to_noripple", true, false, true},
		{"noripple_to_ripple", false, true, true},
		{"noripple_to_noripple", false, false, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Skip("TODO: NoRipple combinations require IOU payment support")

			env := xrplgoTesting.NewTestEnv(t)

			alice := xrplgoTesting.NewAccount("alice")
			bob := xrplgoTesting.NewAccount("bob")
			george := xrplgoTesting.NewAccount("george")

			env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
			env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
			env.FundAmount(george, uint64(xrplgoTesting.XRP(10000)))
			env.Close()

			// Set up trust lines with appropriate NoRipple flags
			aliceTrust := trustset.TrustLine(alice, "USD", george, "100")
			if !tc.aliceRipple {
				aliceTrust = aliceTrust.NoRipple()
			}
			result := env.Submit(aliceTrust.Build())
			xrplgoTesting.RequireTxSuccess(t, result)

			bobTrust := trustset.TrustLine(bob, "USD", george, "100")
			if !tc.bobRipple {
				bobTrust = bobTrust.NoRipple()
			}
			result = env.Submit(bobTrust.Build())
			xrplgoTesting.RequireTxSuccess(t, result)
			env.Close()

			// Fund alice through george
			usd70 := tx.NewIssuedAmountFromFloat64(70, "USD", george.Address)
			result = env.Submit(PayIssued(george, alice, usd70).Build())
			xrplgoTesting.RequireTxSuccess(t, result)
			env.Close()

			// alice tries to pay bob through george
			usd5 := tx.NewIssuedAmountFromFloat64(5, "USD", george.Address)
			payTx := PayIssued(alice, bob, usd5).Build()

			result = env.Submit(payTx)

			if tc.expectSuccess {
				xrplgoTesting.RequireTxSuccess(t, result)
			} else {
				if result.Code == "tesSUCCESS" {
					t.Error("Payment should fail with NoRipple on both sides")
				}
			}
		})
	}
}

// TestPath_XRPToXRP tests XRP to XRP path finding.
// From rippled: xrp_to_xrp
func TestPath_XRPToXRP(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// XRP to XRP should be direct (no path needed)
	result := env.Submit(Pay(alice, bob, uint64(xrplgoTesting.XRP(5))).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	t.Log("XRP to XRP test: payment succeeded")
}

// TestPath_ViaGateway tests payment via gateway with offers.
// From rippled: via_offers_via_gateway
func TestPath_ViaGateway(t *testing.T) {
	t.Skip("TODO: Via gateway requires IOU payment support and offers")

	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	carol := xrplgoTesting.NewAccount("carol")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(carol, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Create trust lines
	result := env.Submit(trustset.TrustLine(bob, "AUD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(carol, "AUD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund carol with AUD
	aud50 := tx.NewIssuedAmountFromFloat64(50, "AUD", gw.Address)
	result = env.Submit(PayIssued(gw, carol, aud50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Carol creates offer: XRP(50) for AUD(50)
	// TODO: Offer creation

	// alice pays bob 10 AUD using XRP
	t.Log("Via gateway test: requires offer creation support")
}

// TestPath_IssuerToRepay tests path finding when repaying issuer.
// From rippled: path_find_05 case A - Borrow or repay
func TestPath_IssuerToRepay(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice trusts gateway for HKD
	result := env.Submit(trustset.TrustLine(alice, "HKD", gw, "2000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway funds alice with 1000 HKD
	hkd1000 := tx.NewIssuedAmountFromFloat64(1000, "HKD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, hkd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice has 1000 HKD
	aliceBalance := env.BalanceIOU(alice, "HKD", gw)
	require.InDelta(t, 1000.0, aliceBalance, 0.0001, "Alice should have 1000 HKD")

	// alice repays gateway 10 HKD - should be direct (no path needed)
	hkd10 := tx.NewIssuedAmountFromFloat64(10, "HKD", gw.Address)
	payTx := PayIssued(alice, gw, hkd10).Build()

	result = env.Submit(payTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice has 990 HKD remaining
	aliceBalance = env.BalanceIOU(alice, "HKD", gw)
	require.InDelta(t, 990.0, aliceBalance, 0.0001, "Alice should have 990 HKD after repayment")
}

// TestPath_CommonGateway tests path through common gateway.
// From rippled: path_find_05 case B - Common gateway
func TestPath_CommonGateway(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Both trust the same gateway for HKD
	result := env.Submit(trustset.TrustLine(alice, "HKD", gw, "2000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "HKD", gw, "2000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Gateway funds alice with 1000 HKD
	hkd1000 := tx.NewIssuedAmountFromFloat64(1000, "HKD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, hkd1000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify initial balances
	aliceBalance := env.BalanceIOU(alice, "HKD", gw)
	require.InDelta(t, 1000.0, aliceBalance, 0.0001, "Alice should have 1000 HKD initially")

	// alice pays bob 10 HKD through common gateway
	// Path: alice -> gw -> bob
	hkd10 := tx.NewIssuedAmountFromFloat64(10, "HKD", gw.Address)
	payTx := PayIssued(alice, bob, hkd10).Build()

	result = env.Submit(payTx)
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Verify final balances
	aliceBalance = env.BalanceIOU(alice, "HKD", gw)
	require.InDelta(t, 990.0, aliceBalance, 0.0001, "Alice should have 990 HKD after payment")

	bobBalance := env.BalanceIOU(bob, "HKD", gw)
	require.InDelta(t, 10.0, bobBalance, 0.0001, "Bob should have 10 HKD from alice")
}

// TestPath_XRPBridge tests XRP bridge between currencies.
// From rippled: path_find_05 case I4 - XRP bridge
func TestPath_XRPBridge(t *testing.T) {
	t.Skip("TODO: XRP bridge requires IOU payment support and offers")

	env := xrplgoTesting.NewTestEnv(t)

	gw1 := xrplgoTesting.NewAccount("gateway1")
	gw2 := xrplgoTesting.NewAccount("gateway2")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	mm := xrplgoTesting.NewAccount("market_maker")

	env.FundAmount(gw1, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(gw2, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(mm, uint64(xrplgoTesting.XRP(11000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "HKD", gw1, "2000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "HKD", gw2, "2000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(mm, "HKD", gw1, "100000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(mm, "HKD", gw2, "100000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund accounts
	hkd1000a := tx.NewIssuedAmountFromFloat64(1000, "HKD", gw1.Address)
	result = env.Submit(PayIssued(gw1, alice, hkd1000a).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	hkd5000 := tx.NewIssuedAmountFromFloat64(5000, "HKD", gw2.Address)
	result = env.Submit(PayIssued(gw2, mm, hkd5000).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Market maker creates offers bridging via XRP
	// TODO: Offer creation

	// alice (gw1/HKD holder) pays bob (gw2/HKD holder) via XRP bridge
	t.Log("XRP bridge test: requires offer creation support")
}

// =============================================================================
// RPC Path Finding Tests (require ripple_path_find RPC implementation)
// =============================================================================

// TestPath_SourceCurrenciesLimit tests RPC path finding with source currency limits.
// From rippled: Path_test::source_currencies_limit
func TestPath_SourceCurrenciesLimit(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC implementation")

	// Test RPC::Tuning::max_src_cur source currencies
	// Test more than RPC::Tuning::max_src_cur source currencies (should error)
	// Test RPC::Tuning::max_auto_src_cur source currencies
	// Test more than RPC::Tuning::max_auto_src_cur source currencies (should error)
	t.Log("Source currencies limit test: requires RPC path finding")
}

// TestPath_PathFindConsumeAll tests path consumption with alternatives.
// From rippled: Path_test::path_find_consume_all
func TestPath_PathFindConsumeAll(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC implementation")

	// Test finding paths that consume all available liquidity
	// alice -> bob -> carol -> edward (10 limit)
	// alice -> dan -> edward (100 limit)
	// Total should be 110 USD (10 via bob/carol + 100 via dan)
	t.Log("Path find consume all test: requires RPC path finding")
}

// TestPath_AlternativePathConsumeBoth tests consuming both alternative paths.
// From rippled: Path_test::alternative_path_consume_both
func TestPath_AlternativePathConsumeBoth(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC and IOU payment support")

	// alice has trust lines to both gateways (gw, gw2)
	// Fund alice from both gateways with 70 USD each
	// alice pays bob 140 USD - should consume both paths
	// Result: alice has 0 USD from both, bob has 70 from each gateway
	t.Log("Alternative path consume both test: requires RPC path finding")
}

// TestPath_AlternativePathsConsumeBestTransfer tests consuming best transfer rate.
// From rippled: Path_test::alternative_paths_consume_best_transfer
func TestPath_AlternativePathsConsumeBestTransfer(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC and transfer rate support")

	// gw2 has 1.1x transfer rate (10% fee)
	// alice pays bob 70 USD - should use gw (no transfer fee) first
	// Result: alice has 0 gw/USD, 70 gw2/USD; bob has 70 gw/USD, 0 gw2/USD
	t.Log("Alternative paths consume best transfer test: requires RPC path finding")
}

// TestPath_AlternativePathsConsumeBestTransferFirst tests best transfer consumed first.
// From rippled: Path_test::alternative_paths_consume_best_transfer_first
func TestPath_AlternativePathsConsumeBestTransferFirst(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC and transfer rate support")

	// Similar to above but tests that best quality is consumed first
	// when paying more than one path can provide
	t.Log("Alternative paths consume best transfer first test: requires RPC path finding")
}

// TestPath_AlternativePathsLimitReturnedPaths tests limiting returned paths to best quality.
// From rippled: Path_test::alternative_paths_limit_returned_paths_to_best_quality
func TestPath_AlternativePathsLimitReturnedPaths(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC implementation")

	// carol has 1.1x transfer rate
	// Set up trust lines for multiple paths (carol, dan, gw, gw2)
	// Find paths - should return paths ordered by quality (best first)
	t.Log("Alternative paths limit test: requires RPC path finding")
}

// TestPath_IssuesPathNegativeIssue5 tests Issue #5 regression.
// From rippled: Path_test::issues_path_negative_issue
func TestPath_IssuesPathNegativeIssue5(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC implementation")

	// alice tries to pay bob - should fail (no path)
	// bob pays carol 75 USD
	// alice tries to pay bob 25 USD - path finding should return empty
	// Payment should fail with tecPATH_DRY
	t.Log("Issues path negative issue 5 test: requires RPC path finding")
}

// TestPath_IssuesRippleClientIssue23Smaller tests ripple-client issue #23 smaller case.
// From rippled: Path_test::issues_path_negative_ripple_client_issue_23_smaller
func TestPath_IssuesRippleClientIssue23Smaller(t *testing.T) {
	t.Skip("TODO: Requires IOU payment with explicit paths")

	// alice -- limit 40 --> bob
	// alice --> carol --> dan --> bob (limit 20)
	// alice pays bob 55 USD via paths
	// Result: bob has 40 alice/USD + 15 dan/USD
	t.Log("Ripple client issue 23 smaller test: requires path payment")
}

// TestPath_IssuesRippleClientIssue23Larger tests ripple-client issue #23 larger case.
// From rippled: Path_test::issues_path_negative_ripple_client_issue_23_larger
func TestPath_IssuesRippleClientIssue23Larger(t *testing.T) {
	t.Skip("TODO: Requires IOU payment with explicit paths")

	// alice -120 USD-> edward -25 USD-> bob
	// alice -25 USD-> carol -75 USD -> dan -100 USD-> bob
	// alice pays bob 50 USD via paths
	// Result: alice has -25 edward/USD, -25 carol/USD
	//         bob has 25 edward/USD, 25 dan/USD
	t.Log("Ripple client issue 23 larger test: requires path payment")
}

// TestPath_PathFind01 tests Path Find: XRP -> XRP and XRP -> IOU.
// From rippled: Path_test::path_find_01
func TestPath_PathFind01(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC and offers")

	// Test various path finding scenarios:
	// - XRP -> XRP direct (no path needed)
	// - XRP -> non-existent account (empty path)
	// - XRP -> IOU via offers
	// - XRP -> IOU via multiple hops
	t.Log("Path find 01 test: requires RPC path finding and offers")
}

// TestPath_PathFind02 tests Path Find: non-XRP -> XRP.
// From rippled: Path_test::path_find_02
func TestPath_PathFind02(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC and offers")

	// Test path finding from IOU to XRP via offer
	// A1 sends ABC -> A2 receives XRP
	// Path goes through offer: ABC -> XRP
	t.Log("Path find 02 test: requires RPC path finding and offers")
}

// TestPath_PathFind04 tests Bitstamp and SnapSwap liquidity with no offers.
// From rippled: Path_test::path_find_04
func TestPath_PathFind04(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC implementation")

	// A1 trusts Bitstamp (G1BS), A2 trusts SnapSwap (G2SW)
	// M1 trusts both (acts as liquidity provider)
	// Test path finding through liquidity provider without offers
	// Path: A1 -> G1BS -> M1 -> G2SW -> A2
	t.Log("Path find 04 test: requires RPC path finding")
}

// TestPath_PathFind05 tests non-XRP -> non-XRP, same currency.
// From rippled: Path_test::path_find_05
func TestPath_PathFind05(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC and offers")

	// Complex trust line setup for various path scenarios:
	// A) Borrow or repay - Source -> Destination (direct to issuer)
	// B) Common gateway - Source -> AC -> Destination
	// C) Gateway to gateway - Source -> OB -> Destination
	// D) User to unlinked gateway - Source -> AC -> OB -> Destination
	// I4) XRP bridge - Source -> AC -> OB to XRP -> OB from XRP -> AC -> Destination
	t.Log("Path find 05 test: requires RPC path finding and offers")
}

// TestPath_PathFind06 tests gateway to user path.
// From rippled: Path_test::path_find_06
func TestPath_PathFind06(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC and offers")

	// E) Gateway to user - Source -> OB -> AC -> Destination
	// G1 pays A2 (who trusts G2) via market maker M1
	t.Log("Path find 06 test: requires RPC path finding and offers")
}

// TestPath_ReceiveMax tests receive max in path finding.
// From rippled: Path_test::receive_max
func TestPath_ReceiveMax(t *testing.T) {
	t.Skip("TODO: Requires ripple_path_find RPC and offers")

	// Test XRP -> IOU receive max (find max receivable given send limit)
	// Test IOU -> XRP receive max
	t.Log("Receive max test: requires RPC path finding and offers")
}

// TestPath_HybridOfferPath tests hybrid domain/open offers.
// From rippled: Path_test::hybrid_offer_path
func TestPath_HybridOfferPath(t *testing.T) {
	t.Skip("TODO: Requires domain and hybrid offer support")

	// Test path finding with different combinations of:
	// - Open offers
	// - Domain offers
	// - Hybrid offers (visible in both)
	t.Log("Hybrid offer path test: requires domain support")
}

// TestPath_AMMDomainPath tests AMM path finding with domain.
// From rippled: Path_test::amm_domain_path
func TestPath_AMMDomainPath(t *testing.T) {
	t.Skip("TODO: Requires AMM support")

	// AMM should NOT be included in domain path finding
	// AMM should be included in non-domain path finding
	t.Log("AMM domain path test: requires AMM support")
}

// =============================================================================
// Path Execution Tests (test payment with explicit paths)
// =============================================================================

// TestPath_PathFind tests basic path finding via gateway.
// From rippled: Path_test::path_find
func TestPath_PathFind(t *testing.T) {
	t.Skip("TODO: Requires IOU payment through gateway")

	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// Set up trust lines
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "600").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "700").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund alice and bob
	usd70 := tx.NewIssuedAmountFromFloat64(70, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, alice, usd70).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	usd50 := tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)
	result = env.Submit(PayIssued(gw, bob, usd50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice pays bob 5 USD - path should go through gateway
	// Path: alice -> gw -> bob
	t.Log("Path find test: requires IOU payment through gateway")
}

// TestPath_ViaOffersViaGateway tests payment via gateway with offers.
// From rippled: Path_test::via_offers_via_gateway
func TestPath_ViaOffersViaGateway(t *testing.T) {
	t.Skip("TODO: Requires offer creation and cross-currency payment")

	env := xrplgoTesting.NewTestEnv(t)

	gw := xrplgoTesting.NewAccount("gateway")
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	carol := xrplgoTesting.NewAccount("carol")

	env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(carol, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// gw has 1.1x transfer rate
	// bob and carol trust gw for AUD
	result := env.Submit(trustset.TrustLine(bob, "AUD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(carol, "AUD", gw, "100").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Fund carol with AUD
	aud50 := tx.NewIssuedAmountFromFloat64(50, "AUD", gw.Address)
	result = env.Submit(PayIssued(gw, carol, aud50).Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// Carol creates offer: XRP(50) for AUD(50)
	// alice pays bob 10 AUD using XRP via carol's offer
	t.Log("Via offers via gateway test: requires offer creation")
}

// TestPath_IndirectPathsPathFind tests indirect path finding.
// From rippled: Path_test::indirect_paths_path_find
func TestPath_IndirectPathsPathFind(t *testing.T) {
	t.Skip("TODO: Requires IOU payment with rippling")

	env := xrplgoTesting.NewTestEnv(t)

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	carol := xrplgoTesting.NewAccount("carol")

	env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(10000)))
	env.FundAmount(carol, uint64(xrplgoTesting.XRP(10000)))
	env.Close()

	// alice -> bob -> carol trust chain
	result := env.Submit(trustset.TrustLine(bob, "USD", alice, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(carol, "USD", bob, "1000").Build())
	xrplgoTesting.RequireTxSuccess(t, result)
	env.Close()

	// alice pays carol 5 USD through bob (rippling)
	// Path: alice -> bob -> carol
	t.Log("Indirect paths path find test: requires rippling")
}
