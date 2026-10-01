// Behavioral vectors from rippled's AMMClawback_test.cpp.
package amm_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func setupAMMClawbackExactLPBoundary(t *testing.T, cleanup bool) (*amm.AMMTestEnv, *jtx.Account) {
	t.Helper()
	env := amm.NewAMMTestEnv(t)
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	for _, account := range []*jtx.Account{env.GW, env.Alice, env.Bob} {
		env.FundAmount(account, uint64(jtx.XRP(1_000_000)))
	}
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build()))
	env.Close()

	for _, holder := range []*jtx.Account{env.Alice, env.Bob} {
		for _, currency := range []string{"USD", "EUR"} {
			env.Trust(holder, env.GW, currency, 100)
			env.PayIOU(env.GW, holder, currency, 100)
		}
	}
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(
		env.Alice,
		amm.IOUAmount(env.GW, "USD", 10),
		amm.IOUAmount(env.GW, "EUR", 10),
	).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(amm.AMMDeposit(env.Bob, env.USD, env.EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 5)).
		Amount2(amm.IOUAmount(env.GW, "EUR", 5)).
		TwoAsset().
		Build()))
	env.Close()
	return env, env.ReadAMMAccount(env.USD, env.EUR)
}

func TestAMMClawbackRejectsRoundedLPWithdrawalAboveHolderBalance(t *testing.T) {
	env, ammAccount := setupAMMClawbackExactLPBoundary(t, false)

	usdBefore, eurBefore, lpSupplyBefore := env.AMMIOUBalances(env.USD, env.EUR)
	require.Equal(t, "15", usdBefore.Value())
	require.Equal(t, "15", eurBefore.Value())
	require.Equal(t, "15", lpSupplyBefore.Value())
	require.NotNil(t, ammAccount)
	holderLPBefore := env.IOUBalance(env.Alice, ammAccount, lpSupplyBefore.Currency)
	require.NotNil(t, holderLPBefore)
	require.Equal(t, "10", holderLPBefore.Value())
	holderUSDBefore := env.IOUBalance(env.Alice, env.GW, "USD")
	holderEURBefore := env.IOUBalance(env.Alice, env.GW, "EUR")
	require.NotNil(t, holderUSDBefore)
	require.NotNil(t, holderEURBefore)
	holderOwnerCountBefore := env.OwnerCount(env.Alice)
	issuerBalanceBefore := env.Balance(env.GW)
	issuerSequenceBefore := env.Seq(env.GW)

	result := env.Submit(amm.AMMClawback(env.GW, env.Alice.Address, env.USD, env.EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 10)).
		Build())

	jtx.RequireTxClaimed(t, result, ter.TecAMM_INVALID_TOKENS.String())
	require.Equal(t, env.BaseFee(), result.Fee)
	require.NotNil(t, result.Metadata)
	require.Len(t, result.Metadata.AffectedNodes, 1)
	require.Equal(t, "ModifiedNode", result.Metadata.AffectedNodes[0].NodeType)
	require.Equal(t, "AccountRoot", result.Metadata.AffectedNodes[0].LedgerEntryType)
	require.Equal(t, issuerBalanceBefore-env.BaseFee(), env.Balance(env.GW))
	require.Equal(t, issuerSequenceBefore+1, env.Seq(env.GW))
	require.Equal(t, holderOwnerCountBefore, env.OwnerCount(env.Alice))

	usdAfter, eurAfter, lpSupplyAfter := env.AMMIOUBalances(env.USD, env.EUR)
	require.Zero(t, usdBefore.Compare(usdAfter))
	require.Zero(t, eurBefore.Compare(eurAfter))
	require.Zero(t, lpSupplyBefore.Compare(lpSupplyAfter))
	holderLPAfter := env.IOUBalance(env.Alice, ammAccount, lpSupplyBefore.Currency)
	require.NotNil(t, holderLPAfter)
	require.Zero(t, holderLPBefore.Compare(*holderLPAfter))
	holderUSDAfter := env.IOUBalance(env.Alice, env.GW, "USD")
	holderEURAfter := env.IOUBalance(env.Alice, env.GW, "EUR")
	require.NotNil(t, holderUSDAfter)
	require.NotNil(t, holderEURAfter)
	require.Zero(t, holderUSDBefore.Compare(*holderUSDAfter))
	require.Zero(t, holderEURBefore.Compare(*holderEURAfter))

	withdrawAllResult := env.Submit(amm.AMMClawback(env.GW, env.Alice.Address, env.USD, env.EUR).Build())
	jtx.RequireTxSuccess(t, withdrawAllResult)
	_, _, lpSupplyAfterWithdrawAll := env.AMMIOUBalances(env.USD, env.EUR)
	require.Equal(t, "5", lpSupplyAfterWithdrawAll.Value())
	holderLPAfterWithdrawAll := env.IOUBalance(env.Alice, ammAccount, lpSupplyBefore.Currency)
	if holderLPAfterWithdrawAll != nil {
		require.True(t, holderLPAfterWithdrawAll.IsZero())
	}
}

func TestAMMClawbackExactLPBoundaryWithCleanup(t *testing.T) {
	env, ammAccount := setupAMMClawbackExactLPBoundary(t, true)
	_, _, lpSupplyBefore := env.AMMIOUBalances(env.USD, env.EUR)
	holderLPBefore := env.IOUBalance(env.Alice, ammAccount, lpSupplyBefore.Currency)
	require.NotNil(t, holderLPBefore)
	require.Equal(t, "10", holderLPBefore.Value())

	result := env.Submit(amm.AMMClawback(env.GW, env.Alice.Address, env.USD, env.EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 10)).
		Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	_, _, lpSupplyAfter := env.AMMIOUBalances(env.USD, env.EUR)
	require.Equal(t, "5", lpSupplyAfter.Value())
	holderLPAfter := env.IOUBalance(env.Alice, ammAccount, lpSupplyBefore.Currency)
	if holderLPAfter != nil {
		require.True(t, holderLPAfter.IsZero())
	}
}

func setupAMMClawbackHolderExhaustionPool(t *testing.T, fixAMMv1_3, largeMantissa bool) (*amm.AMMTestEnv, *jtx.Account) {
	t.Helper()

	env := newPinnedAMMTestEnv(t)
	// The expected serialized line vectors are closed-ledger state. Apply every
	// setup and clawback transaction with closed-view threading enabled.
	env.SetOpenLedger(false)
	if !largeMantissa {
		env.DisableFeature("SingleAssetVault")
		env.DisableFeature("LendingProtocol")
	}
	if !fixAMMv1_3 {
		env.DisableFeature("fixAMMv1_3")
	}
	for _, account := range []*jtx.Account{env.GW, env.Alice, env.Bob} {
		env.FundAmount(account, uint64(jtx.XRP(1_000_000)))
	}
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build()))
	env.Close()

	for _, holder := range []*jtx.Account{env.Alice, env.Bob} {
		env.Trust(holder, env.GW, "USD", 1_000)
		env.PayIOU(env.GW, holder, "USD", 400)
	}
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(
		env.Bob,
		amm.XRPAmount(100),
		amm.IOUAmount(env.GW, "USD", 400),
	).Build()))
	env.Close()

	ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
	require.NotNil(t, ammAccount)
	return env, ammAccount
}

func TestAMMClawbackHolderExhaustionUsesSTAmountFraction(t *testing.T) {
	paths := []struct {
		name            string
		build           func(*amm.AMMTestEnv) tx.Transaction
		expectedLineHex string
	}{
		{
			name: "SpecifiedAmount",
			build: func(env *amm.AMMTestEnv) tx.Transaction {
				return amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
					Amount(amm.IOUAmount(env.GW, "USD", 400)).
					Build()
			},
			expectedLineHex: "11007222010100002500000007370000000000000000380000000000000000559FEF63156D4C9F3BE0AC77315FCFF3A8D14D766B3C3397BA489934EC5CAF5F1A62D51418E104164F9C0000000000000000000000005553440000000000000000000000000000000000000000000000000166800000000000000000000000000000000000000055534400000000008F41242483ADD4D2B69900347812D2B0E68E3D0E6780000000000000000000000000000000000000005553440000000000A407AF5856CCF3C42619DAA925813FC955C72983",
		},
		{
			name: "AllHolderTokens",
			build: func(env *amm.AMMTestEnv) tx.Transaction {
				return amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).Build()
			},
			expectedLineHex: "1100722201010000250000000737000000000000000038000000000000000055457DB1AD99D27B68363B7F986A39923649BC8E27B679F94967458EF90AAF8AF362D51418E104164F9C0000000000000000000000005553440000000000000000000000000000000000000000000000000166800000000000000000000000000000000000000055534400000000008F41242483ADD4D2B69900347812D2B0E68E3D0E6780000000000000000000000000000000000000005553440000000000A407AF5856CCF3C42619DAA925813FC955C72983",
		},
	}
	contexts := []struct {
		name          string
		fixAMMv1_3    bool
		largeMantissa bool
		poolXRP       uint64
	}{
		{name: "SmallMantissa/WithFixAMMv1_3", fixAMMv1_3: true, largeMantissa: false, poolXRP: 70_710_679},
		{name: "SmallMantissa/WithoutFixAMMv1_3", fixAMMv1_3: false, largeMantissa: false, poolXRP: 70_710_678},
		{name: "LargeMantissa/WithFixAMMv1_3", fixAMMv1_3: true, largeMantissa: true, poolXRP: 70_710_679},
	}
	for _, path := range paths {
		for _, context := range contexts {
			t.Run(path.name+"/"+context.name, func(t *testing.T) {
				env, ammAccount := setupAMMClawbackHolderExhaustionPool(t, context.fixAMMv1_3, context.largeMantissa)
				jtx.RequireTxSuccess(t, env.Submit(amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).
					Amount(amm.IOUAmount(env.GW, "USD", 400)).
					SingleAsset().
					Build()))
				env.Close()

				ammData := env.ReadAMMData(amm.XRP(), env.USD)
				require.NotNil(t, ammData)
				require.Equal(t, "282842.712474619", ammData.LPTokenBalance.Value())
				poolUSDBefore := env.IOUBalance(ammAccount, env.GW, "USD")
				require.NotNil(t, poolUSDBefore)
				require.Equal(t, "800", poolUSDBefore.Value())
				holderLPTokens := env.IOUBalance(env.Alice, ammAccount, ammData.LPTokenBalance.Currency)
				require.NotNil(t, holderLPTokens)
				require.Equal(t, "82842.712474619", holderLPTokens.Value())

				result := env.Submit(path.build(env))
				jtx.RequireTxSuccess(t, result)

				poolUSD := env.IOUBalance(ammAccount, env.GW, "USD")
				require.NotNil(t, poolUSD)
				require.Equal(t, "565.685424949238", poolUSD.Value())
				require.Equal(t, context.poolXRP, env.Balance(ammAccount))
				ammData = env.ReadAMMData(amm.XRP(), env.USD)
				require.NotNil(t, ammData)
				require.Equal(t, "200000", ammData.LPTokenBalance.Value())
				holderLPTokens = env.IOUBalance(env.Alice, ammAccount, ammData.LPTokenBalance.Currency)
				if holderLPTokens != nil {
					require.True(t, holderLPTokens.IsZero())
				}

				lineData, err := env.LedgerEntry(keylet.Line(ammAccount.ID, env.GW.ID, "USD"))
				require.NoError(t, err)
				lineHex := strings.ToUpper(hex.EncodeToString(lineData))
				require.Equal(t, path.expectedLineHex, lineHex)
			})
		}
	}
}

func TestAMMClawbackHolderExhaustionRejectsZeroRoundedAsset(t *testing.T) {
	tests := []struct {
		name  string
		build func(*amm.AMMTestEnv) tx.Transaction
	}{
		{
			name: "SpecifiedAmount",
			build: func(env *amm.AMMTestEnv) tx.Transaction {
				return amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
					Amount(amm.IOUAmount(env.GW, "USD", 400)).
					Build()
			},
		},
		{
			name: "AllHolderTokens",
			build: func(env *amm.AMMTestEnv) tx.Transaction {
				return amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).Build()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, ammAccount := setupAMMClawbackHolderExhaustionPool(t, true, true)
			lpToken := amm.LPTokenAmount(env, amm.XRP(), env.USD, 0)
			dust := tx.NewIssuedAmount(1, -4, lpToken.Currency, lpToken.Issuer)
			jtx.RequireTxSuccess(t, env.Submit(amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).
				LPTokenOut(dust).
				LPToken().
				Build()))
			env.Close()

			poolUSDBefore := env.IOUBalance(ammAccount, env.GW, "USD")
			require.NotNil(t, poolUSDBefore)
			poolXRPBefore := env.Balance(ammAccount)
			ammDataBefore := env.ReadAMMData(amm.XRP(), env.USD)
			require.NotNil(t, ammDataBefore)
			holderLPBefore := env.IOUBalance(env.Alice, ammAccount, lpToken.Currency)
			require.NotNil(t, holderLPBefore)
			require.Equal(t, "0.0001", holderLPBefore.Value())
			issuerBalanceBefore := env.Balance(env.GW)
			issuerSequenceBefore := env.Seq(env.GW)

			result := env.Submit(test.build(env))
			jtx.RequireTxClaimed(t, result, ter.TecAMM_FAILED.String())
			require.Equal(t, env.BaseFee(), result.Fee)
			require.NotNil(t, result.Metadata)
			require.Len(t, result.Metadata.AffectedNodes, 1)
			require.Equal(t, issuerBalanceBefore-env.BaseFee(), env.Balance(env.GW))
			require.Equal(t, issuerSequenceBefore+1, env.Seq(env.GW))

			poolUSDAfter := env.IOUBalance(ammAccount, env.GW, "USD")
			require.NotNil(t, poolUSDAfter)
			require.Zero(t, poolUSDBefore.Compare(*poolUSDAfter))
			require.Equal(t, poolXRPBefore, env.Balance(ammAccount))
			ammDataAfter := env.ReadAMMData(amm.XRP(), env.USD)
			require.NotNil(t, ammDataAfter)
			require.Zero(t, ammDataBefore.LPTokenBalance.Compare(ammDataAfter.LPTokenBalance))
			holderLPAfter := env.IOUBalance(env.Alice, ammAccount, lpToken.Currency)
			require.NotNil(t, holderLPAfter)
			require.Zero(t, holderLPBefore.Compare(*holderLPAfter))
		})
	}
}
