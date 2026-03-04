package offer

// Offer tick size and gateway cross-currency tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testTickSize (lines 4739-4861)
//   - testGatewayCrossCurrency (lines 2242-2350)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/accountset"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// testTickSize
// ---------------------------------------------------------------------------

// TestOffer_TickSize tests tick size validation and offer amount truncation.
// Part 1: Setting tick size out of range on an account via AccountSet.
// Part 2: Offer amounts are truncated according to the tick size.
// Reference: rippled Offer_test.cpp testTickSize (lines 4739-4861)
func TestOffer_TickSize(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testTickSizeRange(t, fs.disabled)
			testTickSizeTruncation(t, fs.disabled)
		})
	}
}

// testTickSizeRange tests setting tick size out of range via AccountSet.
// Quality::minTickSize = 3, Quality::maxTickSize = 15.
// Tick size of 0 or maxTickSize (15) clears the tick size field.
// Values < minTickSize (except 0) or > maxTickSize are rejected with temBAD_TICK_SIZE.
// Reference: rippled Offer_test.cpp testTickSize part 1 (lines 4741-4802)
func testTickSizeRange(t *testing.T, disabledFeatures []string) {
	t.Run("range", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw := jtx.NewAccount("gateway")
		env.FundAmount(gw, uint64(jtx.XRP(10000)))
		env.Close()

		// Try to set tick size below minimum (2 < minTickSize=3) -> temBAD_TICK_SIZE
		result := env.Submit(accountset.AccountSet(gw).TickSize(2).Build())
		require.Equal(t, "temBAD_TICK_SIZE", result.Code,
			"TickSize 2 should fail with temBAD_TICK_SIZE, got %s", result.Code)

		// Set tick size to minTickSize (3) -> success
		result = env.Submit(accountset.AccountSet(gw).TickSize(3).Build())
		jtx.RequireTxSuccess(t, result)

		// Set tick size to maxTickSize (15) -> success, but clears the field
		result = env.Submit(accountset.AccountSet(gw).TickSize(15).Build())
		jtx.RequireTxSuccess(t, result)

		// Set tick size to maxTickSize - 1 (14) -> success
		result = env.Submit(accountset.AccountSet(gw).TickSize(14).Build())
		jtx.RequireTxSuccess(t, result)

		// Try to set tick size above maximum (16 > maxTickSize=15) -> temBAD_TICK_SIZE
		result = env.Submit(accountset.AccountSet(gw).TickSize(16).Build())
		require.Equal(t, "temBAD_TICK_SIZE", result.Code,
			"TickSize 16 should fail with temBAD_TICK_SIZE, got %s", result.Code)

		// Set tick size to 0 -> success, clears the field
		result = env.Submit(accountset.AccountSet(gw).TickSize(0).Build())
		jtx.RequireTxSuccess(t, result)
	})
}

// testTickSizeTruncation tests that offer amounts are truncated according to the
// issuer's tick size setting.
// When an offer is placed with currencies from an issuer that has a tick size set,
// the offer amounts are adjusted (truncated) so that the quality has at most
// that many significant digits.
// For a non-sell offer: TakerGets is rounded down.
// For a sell offer: TakerPays is rounded up.
// Reference: rippled Offer_test.cpp testTickSize part 2 (lines 4804-4861)
func testTickSizeTruncation(t *testing.T, disabledFeatures []string) {
	t.Run("truncation", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")

		XTS := func(amount float64) tx.Amount { return jtx.IssuedCurrency(gw, "XTS", amount) }
		XXX := func(amount float64) tx.Amount { return jtx.IssuedCurrency(gw, "XXX", amount) }

		env.FundAmount(gw, uint64(jtx.XRP(10000)))
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.Close()

		// Gateway sets tick size to 5
		result := env.Submit(accountset.AccountSet(gw).TickSize(5).Build())
		jtx.RequireTxSuccess(t, result)

		env.Trust(alice, XTS(1000))
		env.Trust(alice, XXX(1000))

		result = env.Submit(payment.PayIssued(gw, alice, XTS(100)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw, alice, XXX(100)).Build())
		jtx.RequireTxSuccess(t, result)

		// Place 4 offers with different amounts to test truncation behavior.
		// Offer 1: non-sell, XTS(10) / XXX(30) -> quality = 3.0
		//   With tick size 5, TakerGets (XXX) is rounded down.
		//   TakerPays = XTS(10), TakerGets < XXX(30) && > XXX(29.999)
		result = env.Submit(OfferCreate(alice, XTS(10), XXX(30)).Build())
		jtx.RequireTxSuccess(t, result)

		// Offer 2: non-sell, XTS(30) / XXX(10) -> quality = 0.333...
		//   No truncation needed (quality already fits within 5 significant digits).
		//   TakerPays = XTS(30), TakerGets = XXX(10)
		result = env.Submit(OfferCreate(alice, XTS(30), XXX(10)).Build())
		jtx.RequireTxSuccess(t, result)

		// Offer 3: sell, XTS(10) / XXX(30) -> quality = 3.0
		//   With sell flag and tick size 5, TakerPays (XTS) is rounded up.
		//   TakerPays slightly > XTS(10), TakerGets = XXX(30)
		result = env.Submit(OfferCreate(alice, XTS(10), XXX(30)).Sell().Build())
		jtx.RequireTxSuccess(t, result)

		// Offer 4: sell, XTS(30) / XXX(10) -> quality = 0.333...
		//   No truncation needed.
		//   TakerPays = XTS(30), TakerGets = XXX(10)
		result = env.Submit(OfferCreate(alice, XTS(30), XXX(10)).Sell().Build())
		jtx.RequireTxSuccess(t, result)

		// Verify the offers sorted by sequence number
		offers := SortedOffersOnAccount(env, alice)
		require.Equal(t, 4, len(offers), "Expected 4 offers on alice's account")

		// Offer 1: TakerPays = XTS(10), TakerGets < XXX(30) && > XXX(29.999)
		// The tick size truncation rounds TakerGets down.
		require.True(t, amountsEqual(offers[0].TakerPays, XTS(10)),
			"Offer 1 TakerPays should be XTS(10), got %v", offers[0].TakerPays)
		require.True(t, offers[0].TakerGets.Compare(XXX(30)) < 0,
			"Offer 1 TakerGets should be less than XXX(30), got %v", offers[0].TakerGets)
		require.True(t, offers[0].TakerGets.Compare(XXX(29.999)) > 0,
			"Offer 1 TakerGets should be greater than XXX(29.999), got %v", offers[0].TakerGets)

		// Offer 2: exact amounts, no truncation needed
		require.True(t, amountsEqual(offers[1].TakerPays, XTS(30)),
			"Offer 2 TakerPays should be XTS(30), got %v", offers[1].TakerPays)
		require.True(t, amountsEqual(offers[1].TakerGets, XXX(10)),
			"Offer 2 TakerGets should be XXX(10), got %v", offers[1].TakerGets)

		// Offer 3: sell flag rounds TakerPays up.
		// TakerPays slightly > XTS(10), TakerGets = XXX(30)
		// In rippled: TakerPays = XTS(10.001) (approximately, depending on tick size algo)
		require.True(t, offers[2].TakerPays.Compare(XTS(10)) > 0,
			"Offer 3 TakerPays should be greater than XTS(10), got %v", offers[2].TakerPays)
		require.True(t, offers[2].TakerPays.Compare(XTS(10.001)) < 0,
			"Offer 3 TakerPays should be less than XTS(10.001), got %v", offers[2].TakerPays)
		require.True(t, amountsEqual(offers[2].TakerGets, XXX(30)),
			"Offer 3 TakerGets should be XXX(30), got %v", offers[2].TakerGets)

		// Offer 4: exact amounts, no truncation needed
		require.True(t, amountsEqual(offers[3].TakerPays, XTS(30)),
			"Offer 4 TakerPays should be XTS(30), got %v", offers[3].TakerPays)
		require.True(t, amountsEqual(offers[3].TakerGets, XXX(10)),
			"Offer 4 TakerGets should be XXX(10), got %v", offers[3].TakerGets)
	})
}

// ---------------------------------------------------------------------------
// testGatewayCrossCurrency
// ---------------------------------------------------------------------------

// TestOffer_GatewayCrossCurrency tests a cross-currency payment through a
// gateway's offer book. Alice places an offer to exchange XTS for XXX (both
// issued by gw). Bob then makes a cross-currency self-payment from his XTS
// to his XXX, consuming alice's offer.
// Reference: rippled Offer_test.cpp testGatewayCrossCurrency (lines 2242-2350)
func TestOffer_GatewayCrossCurrency(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testGatewayCrossCurrency(t, fs.disabled)
		})
	}
}

func testGatewayCrossCurrency(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	XTS := func(amount float64) tx.Amount { return jtx.IssuedCurrency(gw, "XTS", amount) }
	XXX := func(amount float64) tx.Amount { return jtx.IssuedCurrency(gw, "XXX", amount) }

	// starting_xrp = XRP(100.1) + reserve(env, 1) + env.current()->fees().base * 2
	startingXRP := uint64(jtx.XRP(100)) + 100000 + Reserve(env, 1) + env.BaseFee()*2

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, startingXRP)
	env.FundAmount(bob, startingXRP)
	env.Close()

	env.Trust(alice, XTS(1000))
	env.Trust(alice, XXX(1000))
	env.Trust(bob, XTS(1000))
	env.Trust(bob, XXX(1000))

	result := env.Submit(payment.PayIssued(gw, alice, XTS(100)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, alice, XXX(100)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, bob, XTS(100)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, bob, XXX(100)).Build())
	jtx.RequireTxSuccess(t, result)

	// Alice places an offer: wants XTS(100), gives XXX(100) at 1:1 rate.
	// This means alice is willing to buy XTS(100) by selling XXX(100).
	result = env.Submit(OfferCreate(alice, XTS(100), XXX(100)).Build())
	jtx.RequireTxSuccess(t, result)

	// Bob does a cross-currency self-payment: sends XTS, receives XXX.
	// Amount: XXX(1) (destination amount = what bob receives)
	// SendMax: XTS(1.5) (maximum bob is willing to send)
	// The payment goes through alice's offer book (XTS -> XXX).
	// Reference: rippled uses build_path=true (server auto-pathfinding),
	// which is equivalent to relying on the default path with the gateway
	// as intermediary. No explicit path elements needed.
	result = env.Submit(payment.PayIssued(bob, bob, XXX(1)).
		SendMax(XTS(1.5)).
		Build())
	jtx.RequireTxSuccess(t, result)

	// After the cross-currency payment:
	// Alice's offer consumed 1 XTS from bob and gave 1 XXX to bob.
	// alice: XTS(100+1=101), XXX(100-1=99)
	// bob: XTS(100-1=99), XXX(100+1=101)
	jtx.RequireIOUBalance(t, env, alice, gw, "XTS", 101)
	jtx.RequireIOUBalance(t, env, alice, gw, "XXX", 99)
	jtx.RequireIOUBalance(t, env, bob, gw, "XTS", 99)
	jtx.RequireIOUBalance(t, env, bob, gw, "XXX", 101)
}
