package delegate_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func orderedAccounts(sourceLow bool) (*jtx.Account, *jtx.Account) {
	first := jtx.NewAccount("burn-source")
	second := jtx.NewAccount("burn-destination")
	if keylet.IsLowAccount(first.ID, second.ID) == sourceLow {
		return first, second
	}
	return second, first
}

func requirePaymentBurnRejectedAtomic(
	t *testing.T,
	env *jtx.TestEnv,
	source, delegate, destination *jtx.Account,
	amount, sourceIOUBalance float64,
) {
	t.Helper()
	stateHash, err := env.Ledger().StateMapHash()
	require.NoError(t, err)
	sourceBalance := env.Balance(source)
	delegateBalance := env.Balance(delegate)
	sourceSequence := env.Seq(source)
	delegateSequence := env.Seq(delegate)

	result := payDelegated(env, source, delegate, destination,
		tx.NewIssuedAmountFromFloat64(amount, "USD", destination.Address))
	require.Equal(t, "terNO_DELEGATE_PERMISSION", result.Code)
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Nil(t, result.Metadata)
	afterHash, hashErr := env.Ledger().StateMapHash()
	require.NoError(t, hashErr)
	require.Equal(t, stateHash, afterHash)
	require.Equal(t, sourceIOUBalance, env.BalanceIOU(source, "USD", destination))
	require.Equal(t, sourceBalance, env.Balance(source))
	require.Equal(t, delegateBalance, env.Balance(delegate))
	require.Equal(t, sourceSequence, env.Seq(source))
	require.Equal(t, delegateSequence, env.Seq(delegate))
}

func TestDelegate_PaymentBurnCleanupBalanceBoundary(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, sourceLow := range []bool{false, true} {
			for _, amount := range []float64{20, 50, 100} {
				name := fmt.Sprintf("cleanup=%t/source_low=%t/amount=%.0f", cleanup, sourceLow, amount)
				t.Run(name, func(t *testing.T) {
					env := jtx.NewTestEnv(t)
					env.EnableFeature("PermissionDelegationV1_1")
					if cleanup {
						env.EnableFeature("fixCleanup3_4_0")
					} else {
						env.DisableFeature("fixCleanup3_4_0")
					}

					source, destination := orderedAccounts(sourceLow)
					delegate := jtx.NewAccount("burn-delegate")
					env.Fund(source, destination, delegate)
					env.Close()
					require.Equal(t, cleanup, env.Rules().FixCleanup3_4_0Enabled())
					require.Equal(t, sourceLow, keylet.IsLowAccount(source.ID, destination.ID))

					env.Trust(source, tx.NewIssuedAmountFromFloat64(200, "USD", destination.Address))
					env.Close()
					env.PayIOU(destination, source, destination, "USD", 50)
					env.Close()
					env.Trust(destination, tx.NewIssuedAmountFromFloat64(200, "USD", source.Address))
					env.Close()
					require.Equal(t, "tesSUCCESS", grantPermissions(env, source, delegate, "PaymentBurn").Code)
					env.Close()

					if cleanup && amount > 50 {
						requirePaymentBurnRejectedAtomic(t, env, source, delegate, destination, amount, 50)
						return
					}

					sourceBalance := env.Balance(source)
					delegateBalance := env.Balance(delegate)
					sourceSequence := env.Seq(source)
					delegateSequence := env.Seq(delegate)

					result := payDelegated(env, source, delegate, destination,
						tx.NewIssuedAmountFromFloat64(amount, "USD", destination.Address))

					require.Equal(t, "tesSUCCESS", result.Code)
					require.True(t, result.Applied)
					require.NotNil(t, result.Metadata)
					require.Equal(t, float64(50)-amount, env.BalanceIOU(source, "USD", destination))
					require.Equal(t, sourceBalance, env.Balance(source))
					require.Equal(t, delegateBalance-result.Fee, env.Balance(delegate))
					require.Equal(t, sourceSequence+1, env.Seq(source))
					require.Equal(t, delegateSequence, env.Seq(delegate))
				})
			}
		}
	}
}

func TestDelegate_PaymentBurnCleanupRejectsNonPositiveHolding(t *testing.T) {
	for _, startingBalance := range []float64{0, -50} {
		t.Run(fmt.Sprintf("starting_balance=%.0f", startingBalance), func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.EnableFeature("PermissionDelegationV1_1")
			env.EnableFeature("fixCleanup3_4_0")
			source := jtx.NewAccount("nonpositive-source")
			destination := jtx.NewAccount("nonpositive-destination")
			delegate := jtx.NewAccount("nonpositive-delegate")
			env.Fund(source, destination, delegate)
			env.Close()

			env.Trust(source, tx.NewIssuedAmountFromFloat64(200, "USD", destination.Address))
			env.Close()
			env.Trust(destination, tx.NewIssuedAmountFromFloat64(200, "USD", source.Address))
			env.Close()
			if startingBalance < 0 {
				env.PayIOU(source, destination, source, "USD", -startingBalance)
				env.Close()
			}
			require.Equal(t, startingBalance, env.BalanceIOU(source, "USD", destination))
			require.Equal(t, "tesSUCCESS", grantPermissions(env, source, delegate, "PaymentBurn").Code)
			env.Close()

			requirePaymentBurnRejectedAtomic(t, env, source, delegate, destination, 1, startingBalance)
		})
	}
}

func TestDelegate_PaymentBurnCleanupCrossZeroRequiresMintAndLimit(t *testing.T) {
	for _, destinationLimit := range []float64{0, 200} {
		t.Run(fmt.Sprintf("destination_limit=%.0f", destinationLimit), func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.EnableFeature("PermissionDelegationV1_1")
			env.EnableFeature("fixCleanup3_4_0")
			source := jtx.NewAccount("cross-source")
			destination := jtx.NewAccount("cross-destination")
			delegate := jtx.NewAccount("cross-delegate")
			env.Fund(source, destination, delegate)
			env.Close()

			env.Trust(source, tx.NewIssuedAmountFromFloat64(200, "USD", destination.Address))
			env.Close()
			env.PayIOU(destination, source, destination, "USD", 50)
			env.Close()
			if destinationLimit > 0 {
				env.Trust(destination, tx.NewIssuedAmountFromFloat64(destinationLimit, "USD", source.Address))
				env.Close()
			}
			require.Equal(t, "tesSUCCESS",
				grantPermissions(env, source, delegate, "PaymentBurn", "PaymentMint").Code)
			env.Close()

			result := payDelegated(env, source, delegate, destination,
				tx.NewIssuedAmountFromFloat64(100, "USD", destination.Address))
			if destinationLimit == 0 {
				require.Equal(t, "terNO_DELEGATE_PERMISSION", result.Code)
				require.Equal(t, float64(50), env.BalanceIOU(source, "USD", destination))
				return
			}

			require.Equal(t, "tesSUCCESS", result.Code)
			require.Equal(t, float64(-50), env.BalanceIOU(source, "USD", destination))
			require.Equal(t, float64(50), env.BalanceIOU(destination, "USD", source))
		})
	}
}
