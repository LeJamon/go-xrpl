package offer

// Offer create then cross tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testOfferCreateThenCross (lines 2098-2151)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// ledgerEntryStateBalance reads the raw sfBalance value string from a trust line
// between two accounts. This matches rippled's ledgerEntryState(env, a, b, currency)
// which returns the raw sfBalance.value from the RippleState ledger entry.
// The balance is returned as stored in the ledger (not adjusted for perspective).
func ledgerEntryStateBalance(t *testing.T, env *jtx.TestEnv, acc1, acc2 *jtx.Account, currency string) string {
	t.Helper()

	lineKey := keylet.Line(acc1.ID, acc2.ID, currency)
	data, err := env.LedgerEntry(lineKey)
	require.NoError(t, err, "Failed to read trust line between %s and %s for %s", acc1.Name, acc2.Name, currency)
	require.NotEmpty(t, data, "Trust line does not exist between %s and %s for %s", acc1.Name, acc2.Name, currency)

	rs, err := state.ParseRippleState(data)
	require.NoError(t, err, "Failed to parse trust line between %s and %s for %s", acc1.Name, acc2.Name, currency)

	return rs.Balance.Value()
}

// TestOffer_CreateThenCross tests creating an offer and then crossing it,
// with transfer rates and NumberSwitchOver (fixUniversalNumber) feature.
// The transfer rate of 1.005 causes small differences in the final balances,
// and the fixUniversalNumber amendment changes the precision of the calculation
// for bob's final balance.
// Reference: rippled Offer_test.cpp testOfferCreateThenCross (lines 2098-2151)
func TestOffer_CreateThenCross(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferCreateThenCross(t, fs.disabled)
		})
	}
}

func testOfferCreateThenCross(t *testing.T, disabledFeatures []string) {
	for _, numberSwitchOver := range []bool{false, true} {
		name := "NumberSwitchOver_false"
		if numberSwitchOver {
			name = "NumberSwitchOver_true"
		}
		t.Run(name, func(t *testing.T) {
			env := newEnvWithFeatures(t, disabledFeatures)

			if numberSwitchOver {
				env.EnableFeature("fixUniversalNumber")
			} else {
				env.DisableFeature("fixUniversalNumber")
			}

			gw := jtx.NewAccount("gateway")
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")

			USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

			env.FundAmount(gw, uint64(jtx.XRP(10000)))
			env.FundAmount(alice, uint64(jtx.XRP(10000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000)))
			env.Close()

			// rate(gw, 1.005) -> rippled JTX uses (uint32)(1.005 * 1e9) = 1004999999 (IEEE 754 float)
			env.SetTransferRate(gw, 1004999999)

			// trust(alice, USD(1000))
			env.Trust(alice, USD(1000))
			// trust(bob, USD(1000))
			env.Trust(bob, USD(1000))
			// trust(gw, alice["USD"](50)) - gateway trusts alice for 50 USD
			env.Trust(gw, jtx.IssuedCurrency(alice, "USD", 50))

			// pay(gw, bob, bob["USD"](1)) - gateway pays bob 1 bob-issued USD
			result := env.Submit(payment.PayIssued(gw, bob, jtx.IssuedCurrency(bob, "USD", 1)).Build())
			jtx.RequireTxSuccess(t, result)

			// pay(alice, gw, USD(50)) - alice pays gateway 50 USD
			result = env.Submit(payment.PayIssued(alice, gw, USD(50)).Build())
			jtx.RequireTxSuccess(t, result)

			// trust(gw, alice["USD"](0)) - gateway removes trust for alice
			env.Trust(gw, jtx.IssuedCurrency(alice, "USD", 0))

			// offer(alice, USD(50), XRP(150000))
			// Alice wants 50 USD, offers 150000 XRP
			result = env.Submit(
				OfferCreate(alice, USD(50), jtx.XRPTxAmountFromXRP(150000)).Build())
			jtx.RequireTxSuccess(t, result)

			// offer(bob, XRP(100), USD(0.1))
			// Bob wants 100 XRP, offers 0.1 USD
			result = env.Submit(
				OfferCreate(bob, jtx.XRPTxAmountFromXRP(100), USD(0.1)).Build())
			jtx.RequireTxSuccess(t, result)

			// Check alice's balance: raw sfBalance should be "49.96666666666667"
			aliceBalance := ledgerEntryStateBalance(t, env, alice, gw, "USD")
			require.Equal(t, "49.96666666666667", aliceBalance,
				"Alice's raw trust line balance mismatch")

			// Check bob's balance: depends on NumberSwitchOver
			bobBalance := ledgerEntryStateBalance(t, env, bob, gw, "USD")
			if !numberSwitchOver {
				require.Equal(t, "-0.966500000033334", bobBalance,
					"Bob's raw trust line balance mismatch (NumberSwitchOver=false)")
			} else {
				require.Equal(t, "-0.9665000000333333", bobBalance,
					"Bob's raw trust line balance mismatch (NumberSwitchOver=true)")
			}
		})
	}
}
