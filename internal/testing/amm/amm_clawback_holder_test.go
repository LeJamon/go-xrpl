package amm_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func setupClawbackHolderPool(t *testing.T, mantissa int64, exponent int, sameIssuer, bothLarge bool) (*amm.AMMTestEnv, *jtx.Account, tx.Asset) {
	t.Helper()
	env := setupClawbackEnv(t, 1_000_000, 1_000_000)
	env.EnableFeature("fixCleanup3_4_0")
	issuer2 := env.GW
	if !sameIssuer {
		issuer2 = jtx.NewAccount("gw2")
		env.FundAmount(issuer2, uint64(jtx.XRP(1_000_000)))
	}
	eur := tx.Asset{Currency: "EUR", Issuer: issuer2.Address}
	usdPool := tx.NewIssuedAmount(mantissa, exponent, "USD", env.GW.Address)
	eurPool := tx.NewIssuedAmount(100, 0, "EUR", issuer2.Address)
	if bothLarge {
		eurPool = tx.NewIssuedAmount(mantissa, exponent, "EUR", issuer2.Address)
	}
	for _, amount := range []tx.Amount{usdPool, eurPool} {
		limit := tx.NewIssuedAmount(99, 94, amount.Currency, amount.Issuer)
		jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(env.Alice, limit).Build()))
		issuer := env.GW
		if amount.Currency == "EUR" {
			issuer = issuer2
		}
		jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(issuer, env.Alice, amount).Build()))
	}
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.Alice, usdPool, eurPool).Build()))
	env.Close()
	return env, issuer2, eur
}

func TestAMMClawbackRoundsThroughHolderLine(t *testing.T) {
	for _, assets := range []struct {
		name       string
		sameIssuer bool
		clawBoth   bool
	}{
		{name: "SeparateIssuers"},
		{name: "SameIssuer", sameIssuer: true},
		{name: "BothAssets", sameIssuer: true, clawBoth: true},
	} {
		for _, all := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/All=%t", assets.name, all), func(t *testing.T) {
				env, issuer2, eur := setupClawbackHolderPool(t, 2, 17, assets.sameIssuer, assets.clawBoth)
				dust := tx.NewIssuedAmount(123456789, -8, "USD", env.GW.Address)
				jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(env.GW, env.Alice, dust).Build()))
				eurOutside := tx.NewIssuedAmount(900, 0, "EUR", issuer2.Address)
				if assets.clawBoth {
					eurOutside = tx.NewIssuedAmount(123456789, -8, "EUR", issuer2.Address)
				}
				jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(issuer2, env.Alice, eurOutside).Build()))
				env.Close()
				require.Equal(t, "1.23456789", env.IOUBalance(env.Alice, env.GW, "USD").Value())

				claw := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, eur)
				if !all {
					claw.Amount(tx.NewIssuedAmount(1, 17, "USD", env.GW.Address))
				}
				if assets.clawBoth {
					claw.ClawTwoAssets()
				}
				result := env.Submit(claw.Build())
				jtx.RequireTxSuccess(t, result)
				require.Equal(t, "0", env.IOUBalance(env.Alice, env.GW, "USD").Value())
				wantEUR := "950"
				if all {
					wantEUR = "1000"
				}
				if assets.clawBoth {
					wantEUR = "0"
				}
				require.Equal(t, wantEUR, env.IOUBalance(env.Alice, issuer2, "EUR").Value())
				require.Equal(t, all, ammIsDeleted(t, env, env.USD, eur))

				require.NotNil(t, result.Metadata)
				currencies := []string{"USD"}
				if assets.clawBoth {
					currencies = append(currencies, "EUR")
				}
				for _, currency := range currencies {
					line := keylet.Line(env.Alice.ID, env.GW.ID, currency)
					found := false
					for _, node := range result.Metadata.AffectedNodes {
						if node.LedgerIndex == fmt.Sprintf("%X", line.Key) {
							require.Equal(t, "ModifiedNode", node.NodeType)
							require.Contains(t, node.PreviousFields, "Balance")
							found = true
						}
					}
					require.True(t, found, "missing holder %s line in metadata", currency)
				}
			})
		}
	}
}

func TestAMMClawbackHolderCreditOverflow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		amount   int64
		expected ter.Result
	}{
		{name: "PartialAmount", amount: 1, expected: ter.TefEXCEPTION},
		{name: "AllTokens", expected: ter.TecINTERNAL},
		{name: "AmountExhaustsHolder", amount: 5, expected: ter.TecINTERNAL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, issuer2, eur := setupClawbackHolderPool(t, 5, 95, false, false)
			outside := tx.NewIssuedAmount(95, 94, "USD", env.GW.Address)
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(env.GW, env.Alice, outside).Build()))
			env.Close()
			ammAccount := amm.AMMAccount(t, env, env.USD, eur)
			ammBefore := env.ReadAMMData(env.USD, eur)
			require.NotNil(t, ammBefore)
			poolUSD, poolEUR, lpSupply := env.AMMIOUBalances(env.USD, eur)
			holderLP := env.IOUBalance(env.Alice, ammAccount, lpSupply.Currency)
			ownerCount := env.OwnerCount(env.Alice)
			issuerBalance, issuerSequence := env.Balance(env.GW), env.Seq(env.GW)

			claw := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, eur)
			if tc.amount != 0 {
				claw.Amount(tx.NewIssuedAmount(tc.amount, 95, "USD", env.GW.Address))
			}
			result := env.Submit(claw.Build())
			require.Equal(t, tc.expected.String(), result.Code)
			require.False(t, result.Success)
			require.Zero(t, outside.Compare(*env.IOUBalance(env.Alice, env.GW, "USD")))
			require.True(t, env.IOUBalance(env.Alice, issuer2, "EUR").IsZero())
			usdAfter, eurAfter, lpAfter := env.AMMIOUBalances(env.USD, eur)
			require.Zero(t, poolUSD.Compare(usdAfter))
			require.Zero(t, poolEUR.Compare(eurAfter))
			require.Zero(t, lpSupply.Compare(lpAfter))
			require.Zero(t, holderLP.Compare(*env.IOUBalance(env.Alice, ammAccount, lpSupply.Currency)))
			require.Equal(t, ownerCount, env.OwnerCount(env.Alice))
			if tc.expected == ter.TecINTERNAL {
				jtx.RequireTxClaimed(t, result, tc.expected.String())
				require.Equal(t, issuerBalance-env.BaseFee(), env.Balance(env.GW))
				require.Equal(t, issuerSequence+1, env.Seq(env.GW))
				require.NotNil(t, result.Metadata)
				require.Len(t, result.Metadata.AffectedNodes, 1)
				require.Equal(t, "AccountRoot", result.Metadata.AffectedNodes[0].LedgerEntryType)
			} else {
				require.Equal(t, issuerBalance, env.Balance(env.GW))
				require.Equal(t, issuerSequence, env.Seq(env.GW))
			}
		})
	}
}
