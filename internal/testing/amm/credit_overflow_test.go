package amm_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreamm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const creditOverflowCleanup = "fixCleanup3_4_0"

func setCreditOverflowCleanup(t *testing.T, env *amm.AMMTestEnv, enabled bool) {
	t.Helper()
	if enabled {
		env.EnableFeature(creditOverflowCleanup)
	} else {
		env.DisableFeature(creditOverflowCleanup)
	}
	require.Equal(t, enabled, env.FeatureEnabled(creditOverflowCleanup))
	env.Close()
	require.Equal(t, enabled, env.Rules().Enabled(amendment.FeatureFixCleanup3_4_0))
}

type withdrawalCreditOverflowFixture struct {
	env    *amm.AMMTestEnv
	lpHalf tx.Amount
}

func setupWithdrawalCreditOverflow(t *testing.T, cleanup bool) *withdrawalCreditOverflowFixture {
	t.Helper()
	env := amm.NewAMMTestEnv(t)
	setCreditOverflowCleanup(t, env, cleanup)

	for _, account := range []*jtx.Account{env.GW, env.Alice} {
		env.FundAmount(account, uint64(jtx.XRP(1_000_000)))
	}
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(env.GW).DefaultRipple().Build()))
	env.Close()

	maxUSD := jtx.IssuedCurrencyFromMantissa(env.GW, "USD", 9_999_999_999_999_999, 80)
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(env.Alice, maxUSD).Build()))
	env.Close()

	initialUSD := jtx.IssuedCurrencyFromMantissa(env.GW, "USD", 9, 95)
	env.PayIOUAmount(env.GW, env.Alice, initialUSD)
	env.Close()

	poolUSD := jtx.IssuedCurrencyFromMantissa(env.GW, "USD", 5, 95)
	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.Alice, amm.XRPAmount(100), poolUSD).Build()))
	env.Close()

	topUpUSD := jtx.IssuedCurrencyFromMantissa(env.GW, "USD", 55, 94)
	env.PayIOUAmount(env.GW, env.Alice, topUpUSD)
	env.Close()

	ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
	require.NotNil(t, ammAccount)
	lpCurrency := coreamm.GenerateAMMLPTCurrencyForAssets(amm.XRP(), env.USD)
	holderLP := env.IOUBalance(env.Alice, ammAccount, lpCurrency)
	require.NotNil(t, holderLP)
	require.NotEqual(t, "0", holderLP.Value())
	ammData := env.ReadAMMData(amm.XRP(), env.USD)
	require.NotNil(t, ammData)
	require.Equal(t, holderLP.Value(), ammData.LPTokenBalance.Value())
	require.Equal(t,
		jtx.IssuedCurrencyFromMantissa(env.GW, "USD", 95, 94).Value(),
		env.IOUBalance(env.Alice, env.GW, "USD").Value(),
	)

	return &withdrawalCreditOverflowFixture{
		env:    env,
		lpHalf: holderLP.MulRatio(1, 2, false),
	}
}

func requireCreditOverflowRollback(t *testing.T, env *amm.AMMTestEnv, payer *jtx.Account, transaction tx.Transaction, want string) {
	t.Helper()
	snapshot := func() map[[32]byte][]byte {
		entries := make(map[[32]byte][]byte)
		require.NoError(t, env.Ledger().ForEach(func(key [32]byte, data []byte) bool {
			entries[key] = bytes.Clone(data)
			return true
		}))
		return entries
	}
	before := snapshot()
	result := env.Submit(transaction)
	require.Equal(t, want, result.Code)
	require.False(t, result.Success)
	after := snapshot()
	if want == "tefEXCEPTION" {
		require.False(t, result.Applied)
		require.Zero(t, result.Fee)
		require.Nil(t, result.Metadata)
		require.Equal(t, before, after)
		return
	}

	require.True(t, result.Applied)
	require.Equal(t, env.BaseFee(), result.Fee)
	require.NotNil(t, result.Metadata)
	require.Equal(t, want, result.Metadata.TransactionResult.String())
	require.Len(t, result.Metadata.AffectedNodes, 1)
	payerKey := keylet.Account(payer.ID).Key
	expected, err := state.ParseAccountRoot(before[payerKey])
	require.NoError(t, err)
	actual, err := state.ParseAccountRoot(after[payerKey])
	require.NoError(t, err)
	expected.Balance -= env.BaseFee()
	expected.Sequence++
	expected.PreviousTxnID = actual.PreviousTxnID
	expected.PreviousTxnLgrSeq = actual.PreviousTxnLgrSeq
	before[payerKey], err = state.SerializeAccountRoot(expected)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestAMMWithdrawCreditOverflowResultsAndRollback(t *testing.T) {
	cases := []struct {
		name        string
		build       func(*withdrawalCreditOverflowFixture) tx.Transaction
		legacyCode  string
		cleanupCode string
	}{
		{
			name: "tfWithdrawAll",
			build: func(fixture *withdrawalCreditOverflowFixture) tx.Transaction {
				return amm.AMMWithdraw(fixture.env.Alice, amm.XRP(), fixture.env.USD).WithdrawAll().Build()
			},
			legacyCode:  "tecINTERNAL",
			cleanupCode: "tecINTERNAL",
		},
		{
			name: "tfLPToken",
			build: func(fixture *withdrawalCreditOverflowFixture) tx.Transaction {
				return amm.AMMWithdraw(fixture.env.Alice, amm.XRP(), fixture.env.USD).
					LPTokenIn(fixture.lpHalf).
					LPToken().
					Build()
			},
			legacyCode:  "tecINTERNAL",
			cleanupCode: "tecINTERNAL",
		},
		{
			name: "tfSingleAsset",
			build: func(fixture *withdrawalCreditOverflowFixture) tx.Transaction {
				usd := jtx.IssuedCurrencyFromMantissa(fixture.env.GW, "USD", 1, 95)
				return amm.AMMWithdraw(fixture.env.Alice, amm.XRP(), fixture.env.USD).
					Amount(usd).
					SingleAsset().
					Build()
			},
			legacyCode:  "tefEXCEPTION",
			cleanupCode: "tecAMM_FAILED",
		},
		{
			name: "tfOneAssetLPToken",
			build: func(fixture *withdrawalCreditOverflowFixture) tx.Transaction {
				return amm.AMMWithdraw(fixture.env.Alice, amm.XRP(), fixture.env.USD).
					Amount(amm.IOUAmount(fixture.env.GW, "USD", 0)).
					LPTokenIn(fixture.lpHalf).
					OneAssetLPToken().
					Build()
			},
			legacyCode:  "tefEXCEPTION",
			cleanupCode: "tecAMM_FAILED",
		},
		{
			name: "tfTwoAsset",
			build: func(fixture *withdrawalCreditOverflowFixture) tx.Transaction {
				usd := jtx.IssuedCurrencyFromMantissa(fixture.env.GW, "USD", 5, 95)
				return amm.AMMWithdraw(fixture.env.Alice, amm.XRP(), fixture.env.USD).
					Amount(amm.XRPAmount(50)).
					Amount2(usd).
					TwoAsset().
					Build()
			},
			legacyCode:  "tefEXCEPTION",
			cleanupCode: "tecAMM_FAILED",
		},
	}

	for _, cleanup := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/cleanup=%t", tc.name, cleanup), func(t *testing.T) {
				fixture := setupWithdrawalCreditOverflow(t, cleanup)
				want := tc.legacyCode
				if cleanup {
					want = tc.cleanupCode
				}
				requireCreditOverflowRollback(t, fixture.env, fixture.env.Alice, tc.build(fixture), want)
			})
		}
	}
}

type clawbackCreditOverflowFixture struct {
	env *amm.AMMTestEnv
	eur tx.Asset
}

func setupClawbackCreditOverflow(t *testing.T, cleanup bool) *clawbackCreditOverflowFixture {
	t.Helper()
	env := amm.NewAMMTestEnv(t)
	setCreditOverflowCleanup(t, env, cleanup)
	gw2 := jtx.NewAccount("gw2")
	eur := tx.Asset{Currency: "EUR", Issuer: gw2.Address}

	for _, account := range []*jtx.Account{env.GW, gw2, env.Alice} {
		env.FundAmount(account, uint64(jtx.XRP(1_000_000)))
	}
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(env.GW).DefaultRipple().AllowClawback().Build()))
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(gw2).DefaultRipple().Build()))
	env.Close()

	usd100 := jtx.IssuedCurrencyFromMantissa(env.GW, "USD", 1, 2)
	maxEUR := jtx.IssuedCurrencyFromMantissa(gw2, "EUR", 9_999_999_999_999_999, 80)
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(env.Alice, usd100).Build()))
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(env.Alice, maxEUR).Build()))
	env.Close()

	env.PayIOUAmount(env.GW, env.Alice, usd100)
	eurPool := jtx.IssuedCurrencyFromMantissa(gw2, "EUR", 5, 95)
	env.PayIOUAmount(gw2, env.Alice, eurPool)
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.Alice, usd100, eurPool).Build()))
	env.Close()

	eurOutside := jtx.IssuedCurrencyFromMantissa(gw2, "EUR", 95, 94)
	env.PayIOUAmount(gw2, env.Alice, eurOutside)
	env.Close()

	ammAccount := env.ReadAMMAccount(env.USD, eur)
	require.NotNil(t, ammAccount)
	lpCurrency := coreamm.GenerateAMMLPTCurrencyForAssets(env.USD, eur)
	holderEUR := env.IOUBalance(env.Alice, gw2, "EUR")
	require.Equal(t, eurOutside.Value(), holderEUR.Value())
	ammData := env.ReadAMMData(env.USD, eur)
	require.NotNil(t, ammData)
	require.Equal(t, env.IOUBalance(env.Alice, ammAccount, lpCurrency).Value(), ammData.LPTokenBalance.Value())

	return &clawbackCreditOverflowFixture{
		env: env,
		eur: eur,
	}
}

func TestAMMClawbackCreditOverflowResultsAndRollback(t *testing.T) {
	cases := []struct {
		name        string
		amount      func(*clawbackCreditOverflowFixture) tx.Amount
		legacyCode  string
		cleanupCode string
	}{
		{name: "NoAmount", legacyCode: "tecINTERNAL", cleanupCode: "tecINTERNAL"},
		{
			name: "Amount50USD",
			amount: func(fixture *clawbackCreditOverflowFixture) tx.Amount {
				return amm.IOUAmount(fixture.env.GW, "USD", 50)
			},
			legacyCode:  "tefEXCEPTION",
			cleanupCode: "tefEXCEPTION",
		},
		{
			name: "AmountGreaterThanPosition",
			amount: func(fixture *clawbackCreditOverflowFixture) tx.Amount {
				return amm.IOUAmount(fixture.env.GW, "USD", 101)
			},
			legacyCode:  "tecINTERNAL",
			cleanupCode: "tecINTERNAL",
		},
		{
			name: "ExactPosition",
			amount: func(fixture *clawbackCreditOverflowFixture) tx.Amount {
				return amm.IOUAmount(fixture.env.GW, "USD", 100)
			},
			legacyCode:  "tefEXCEPTION",
			cleanupCode: "tecINTERNAL",
		},
	}

	for _, cleanup := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/cleanup=%t", tc.name, cleanup), func(t *testing.T) {
				fixture := setupClawbackCreditOverflow(t, cleanup)
				clawback := amm.AMMClawback(fixture.env.GW, fixture.env.Alice.Address, fixture.env.USD, fixture.eur)
				if tc.amount != nil {
					clawback.Amount(tc.amount(fixture))
				}
				want := tc.legacyCode
				if cleanup {
					want = tc.cleanupCode
				}
				requireCreditOverflowRollback(t, fixture.env, fixture.env.GW, clawback.Build(), want)
			})
		}
	}
}
