package amm_test

import (
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestAMMCreateChecksPostFeeXRPBalance(t *testing.T) {
	const (
		depositDrops = int64(100_000_000)
		depositUSD   = float64(10_000)
		feeDrops     = uint64(400_000_000)
	)

	for _, xrpFirst := range []bool{true, false} {
		name := "amount2"
		if xrpFirst {
			name = "amount"
		}
		t.Run(name, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			env.FundWithIOUs(depositUSD, 0)
			env.SetOpenLedger(false)
			setAMMCreateBalance(t, env.TestEnv, env.Alice, uint64(depositDrops)+feeDrops-1)

			xrp := tx.NewXRPAmount(depositDrops)
			usd := amm.IOUAmount(env.GW, "USD", depositUSD)
			amount, amount2 := usd, xrp
			if xrpFirst {
				amount, amount2 = xrp, usd
			}

			balanceBefore := env.Balance(env.Alice)
			ownerCountBefore := env.OwnerCount(env.Alice)
			sequenceBefore := env.Seq(env.Alice)
			create := amm.AMMCreate(env.Alice, amount, amount2).
				Fee(strconv.FormatUint(feeDrops, 10)).
				Build()

			result := env.Submit(create)

			jtx.RequireTxClaimed(t, result, jtx.TecFAILED_PROCESSING)
			jtx.RequireBalance(t, env.TestEnv, env.Alice, balanceBefore-feeDrops)
			jtx.RequireOwnerCount(t, env.TestEnv, env.Alice, ownerCountBefore)
			jtx.RequireIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", depositUSD)
			require.Equal(t, sequenceBefore+1, env.Seq(env.Alice))
			require.Nil(t, env.ReadAMMAccount(amm.XRP(), env.USD))
		})
	}
}

func TestAMMCreateTransfersXRPFromEitherAmountPosition(t *testing.T) {
	const (
		depositDrops = int64(100_000_000)
		depositUSD   = float64(100)
		feeDrops     = uint64(400_000_000)
	)

	for _, xrpFirst := range []bool{true, false} {
		name := "amount2"
		if xrpFirst {
			name = "amount"
		}
		t.Run(name, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			env.FundWithIOUs(1_000, 0)
			env.SetOpenLedger(false)
			setAMMCreateBalance(t, env.TestEnv, env.Alice, uint64(depositDrops)+feeDrops)

			xrp := tx.NewXRPAmount(depositDrops)
			usd := amm.IOUAmount(env.GW, "USD", depositUSD)
			amount, amount2 := usd, xrp
			if xrpFirst {
				amount, amount2 = xrp, usd
			}

			balanceBefore := env.Balance(env.Alice)
			ownerCountBefore := env.OwnerCount(env.Alice)
			result := env.Submit(amm.AMMCreate(env.Alice, amount, amount2).
				Fee(strconv.FormatUint(feeDrops, 10)).
				Build())

			jtx.RequireTxSuccess(t, result)
			jtx.RequireBalance(t, env.TestEnv, env.Alice, balanceBefore-feeDrops-uint64(depositDrops))
			jtx.RequireOwnerCount(t, env.TestEnv, env.Alice, ownerCountBefore+1)
			jtx.RequireIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1_000-depositUSD)
			ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
			require.NotNil(t, ammAccount)
			jtx.RequireBalance(t, env.TestEnv, ammAccount, uint64(depositDrops))
			require.InDelta(t, depositUSD, env.AMMPoolIOU(ammAccount, env.GW, "USD"), 1e-10)
		})
	}
}

func setAMMCreateBalance(t *testing.T, env *jtx.TestEnv, account *jtx.Account, balance uint64) {
	t.Helper()
	accountKey := keylet.Account(account.ID)
	raw, err := env.LedgerEntry(accountKey)
	require.NoError(t, err)
	root, err := state.ParseAccountRoot(raw)
	require.NoError(t, err)
	root.Balance = balance
	raw, err = state.SerializeAccountRoot(root)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(accountKey, raw))
}
