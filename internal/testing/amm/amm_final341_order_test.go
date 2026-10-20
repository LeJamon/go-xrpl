package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestAMMCreatePreservesAmountOrderAtOwnerDirectoryBoundary(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.Fund()
	env.Trust(env.Alice, env.GW, "USD", 1000)
	env.Trust(env.Alice, env.GW, "EUR", 1000)
	env.Close()
	env.PayIOU(env.GW, env.Alice, "USD", 500)
	env.PayIOU(env.GW, env.Alice, "EUR", 500)
	env.Close()

	// Leave one free slot on the issuer's owner-directory page. The first
	// AMM trustline must occupy that slot and the second must spill to page 1.
	dirKey := keylet.OwnerDir(env.GW.ID)
	raw, err := env.LedgerEntry(dirKey)
	require.NoError(t, err)
	dir, err := state.ParseDirectoryNode(raw)
	require.NoError(t, err)
	for len(dir.Indexes) < 31 {
		var filler [32]byte
		filler[0] = 0xf0
		filler[31] = byte(len(dir.Indexes))
		dir.Indexes = append(dir.Indexes, filler)
	}
	require.Len(t, dir.Indexes, 31)
	dir.Owner = env.GW.ID
	raw, err = state.SerializeDirectoryNode(dir, false)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(dirKey, raw))

	usd := amm.IOUAmount(env.GW, "USD", 100)
	eur := amm.IOUAmount(env.GW, "EUR", 100)
	result := env.Submit(amm.AMMCreate(env.Alice, usd, eur).Build())
	jtx.RequireTxSuccess(t, result)

	data := env.ReadAMMData(env.USD, env.EUR)
	require.NotNil(t, data)
	usdLine := keylet.Line(data.Account, env.GW.ID, "USD").Key
	eurLine := keylet.Line(data.Account, env.GW.ID, "EUR").Key

	rootRaw, err := env.LedgerEntry(dirKey)
	require.NoError(t, err)
	root, err := state.ParseDirectoryNode(rootRaw)
	require.NoError(t, err)
	pageRaw, err := env.LedgerEntry(keylet.OwnerDirPage(env.GW.ID, 1))
	require.NoError(t, err)
	page, err := state.ParseDirectoryNode(pageRaw)
	require.NoError(t, err)
	require.Contains(t, root.Indexes, usdLine)
	require.NotContains(t, root.Indexes, eurLine)
	require.Contains(t, page.Indexes, eurLine)
}

func setupTwoIssuerAMM(t *testing.T) (*amm.AMMTestEnv, *jtx.Account, tx.Asset, tx.Asset) {
	t.Helper()
	env := amm.NewAMMTestEnv(t)
	issuer2 := jtx.NewAccount("gw2")
	env.Fund()
	env.TestEnv.FundAmount(issuer2, uint64(jtx.XRP(30000)))
	env.Close()

	env.TestEnv.EnableRequireAuth(issuer2)
	env.Close()
	env.TestEnv.AuthorizeTrustLine(issuer2, env.Alice, "EUR")
	env.Close()
	env.Trust(env.Alice, env.GW, "USD", 1000)
	env.Trust(env.Alice, issuer2, "EUR", 1000)
	env.Close()
	env.TestEnv.PayIOU(env.GW, env.Alice, env.GW, "USD", 500)
	env.TestEnv.PayIOU(issuer2, env.Alice, issuer2, "EUR", 500)
	env.Close()

	usd := tx.Asset{Currency: "USD", Issuer: env.GW.Address}
	eur := tx.Asset{Currency: "EUR", Issuer: issuer2.Address}
	result := env.Submit(amm.AMMCreate(env.Alice,
		amm.IOUAmount(env.GW, "USD", 100),
		amm.IOUAmount(issuer2, "EUR", 100),
	).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	return env, issuer2, usd, eur
}

func TestAMMDepositChecksLegacyAuthAndFreezePerAsset(t *testing.T) {
	env, issuer2, usd, eur := setupTwoIssuerAMM(t)

	env.Trust(env.Carol, env.GW, "USD", 100)
	env.Close()
	env.TestEnv.PayIOU(env.GW, env.Carol, env.GW, "USD", 10)
	env.Close()
	env.FreezeTrustLine(env.GW, env.Carol, "USD")
	env.EnableFeature("AMMClawback")
	env.DisableFeature("fixCleanup3_3_0")
	env.Close()

	result := env.Submit(amm.AMMDeposit(env.Carol, usd, eur).
		Amount(amm.IOUAmount(env.GW, "USD", 1)).
		Amount2(amm.IOUAmount(issuer2, "EUR", 1)).
		TwoAsset().
		Build())
	amm.ExpectTER(t, result, "tecFROZEN")
}

func TestAMMWithdrawAllChecksCanonicalAssetOrder(t *testing.T) {
	env, _, usd, eur := setupTwoIssuerAMM(t)
	ammAccount := amm.AMMAccount(t, env, usd, eur)
	ammData := env.ReadAMMData(usd, eur)
	require.NotNil(t, ammData)

	// Move LP tokens to an account that has no EUR trust line. Its USD line is
	// frozen so the result depends on whether validation follows tx or pool order.
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
	env.Close()
	env.Trust(env.Bob, ammAccount, ammData.LPTokenBalance.Currency, 1000)
	env.Close()
	lpt := amm.LPTokenAmount(env, usd, eur, 10)
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(env.Alice, env.Bob, lpt).Build()))
	env.Close()
	env.Trust(env.Bob, env.GW, "USD", 100)
	env.Close()
	env.FreezeTrustLine(env.GW, env.Bob, "USD")
	env.DisableFeature("fixCleanup3_3_0")
	env.Close()

	// EUR sorts before USD in the AMM entry, while the transaction deliberately
	// presents USD first. With no Amount fields, rippled checks canonical EUR
	// first and returns tecNO_LINE for Bob's missing EUR trust line.
	result := env.Submit(amm.AMMWithdraw(env.Bob, usd, eur).WithdrawAll().Build())
	amm.ExpectTER(t, result, "tecNO_LINE")
}
