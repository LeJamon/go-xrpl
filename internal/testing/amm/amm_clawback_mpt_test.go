package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	ammtest "github.com/LeJamon/go-xrpl/internal/testing/amm"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func readAMMClawbackMPToken(t *testing.T, env *ammtest.AMMTestEnv, issuanceID string, holder *jtx.Account) *state.MPTokenData {
	t.Helper()
	id, err := mptutil.DecodeID(issuanceID)
	require.NoError(t, err)
	raw, err := env.LedgerEntry(keylet.MPTokenByID(id, holder.ID))
	require.NoError(t, err)
	require.NotNil(t, raw)
	token, err := state.ParseMPToken(raw)
	require.NoError(t, err)
	return token
}

// TestAMMClawbackRecreatesCrossIssuerMPTokens verifies that a clawback can
// recreate deleted recipient holdings, while only authorizing the clawback
// issuer's own MPT. The paired issuer's RequireAuth remains effective.
// Reference: rippled AMMClawbackMPT_test.cpp testClawbackCrossIssuerPairedAssetAuth.
func TestAMMClawbackRecreatesCrossIssuerMPTokens(t *testing.T) {
	env := ammtest.NewAMMTestEnv(t)
	env.EnableFeature("MPTokensV2")
	gw2 := jtx.NewAccount("gw2")
	env.FundAmount(env.GW, uint64(jtx.XRP(100_000)))
	env.FundAmount(gw2, uint64(jtx.XRP(100_000)))
	env.FundAmount(env.Alice, uint64(jtx.XRP(100_000)))
	env.Close()

	btc := mpttest.NewMPTTesterNoFund(t, env.TestEnv, env.GW)
	btc.Create(mpttest.CreateOpts{
		Flags: mpttest.TfMPTCanClawback | mpttest.TfMPTRequireAuth |
			mpttest.TfMPTCanTrade | mpttest.TfMPTCanTransfer,
	})
	btc.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
	btc.Authorize(mpttest.AuthorizeOpts{Account: env.GW, Holder: env.Alice})
	btc.Pay(env.GW, env.Alice, 10_000)

	eth := mpttest.NewMPTTesterNoFund(t, env.TestEnv, gw2)
	eth.Create(mpttest.CreateOpts{
		Flags: mpttest.TfMPTCanClawback | mpttest.TfMPTRequireAuth |
			mpttest.TfMPTCanTrade | mpttest.TfMPTCanTransfer,
	})
	eth.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
	eth.Authorize(mpttest.AuthorizeOpts{Account: gw2, Holder: env.Alice})
	eth.Pay(gw2, env.Alice, 10_000)

	btcAsset := tx.Asset{MPTIssuanceID: btc.IssuanceID()}
	ethAsset := tx.Asset{MPTIssuanceID: eth.IssuanceID()}
	jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMCreate(
		env.Alice,
		btc.MPTAmount(10_000),
		eth.MPTAmount(10_000),
	).Build()))
	env.Close()
	btc.RequireMPTokenAmount(env.Alice, 0)
	eth.RequireMPTokenAmount(env.Alice, 0)

	btc.Authorize(mpttest.AuthorizeOpts{Account: env.Alice, Flags: mpttest.TfMPTUnauthorize})
	eth.Authorize(mpttest.AuthorizeOpts{Account: env.Alice, Flags: mpttest.TfMPTUnauthorize})
	env.Close()
	btcID, err := mptutil.DecodeID(btc.IssuanceID())
	require.NoError(t, err)
	ethID, err := mptutil.DecodeID(eth.IssuanceID())
	require.NoError(t, err)
	require.False(t, env.LedgerEntryExists(keylet.MPTokenByID(btcID, env.Alice.ID)))
	require.False(t, env.LedgerEntryExists(keylet.MPTokenByID(ethID, env.Alice.ID)))

	result := env.Submit(ammtest.AMMClawback(env.GW, env.Alice.Address, btcAsset, ethAsset).
		Amount(btc.MPTAmount(1_000)).
		Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	btcHolding := readAMMClawbackMPToken(t, env, btc.IssuanceID(), env.Alice)
	ethHolding := readAMMClawbackMPToken(t, env, eth.IssuanceID(), env.Alice)
	require.NotZero(t, btcHolding.Flags&entry.LsfMPTAuthorized)
	require.Zero(t, ethHolding.Flags&entry.LsfMPTAuthorized)
	require.Zero(t, btcHolding.MPTAmount)
	require.Greater(t, ethHolding.MPTAmount, uint64(0))
}

func setupAMMClawbackReserveFixture(t *testing.T, cleanup bool) (*ammtest.AMMTestEnv, tx.Asset, *jtx.Account) {
	t.Helper()
	env := ammtest.NewAMMTestEnv(t)
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	}
	gw2 := jtx.NewAccount("gw2")
	carol := env.Carol
	aliceBalance := env.ReserveBase() + 2*env.ReserveIncrement() + 5*env.BaseFee()
	env.FundAmount(env.GW, env.ReserveBase()+10*env.BaseFee())
	env.FundAmount(gw2, uint64(jtx.XRP(100_000)))
	env.FundAmount(carol, uint64(jtx.XRP(100_000)))
	env.FundAmount(env.Alice, aliceBalance)
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build()))
	env.Close()

	eur := tx.Asset{Currency: "EUR", Issuer: gw2.Address}
	env.Trust(carol, env.GW, "USD", 100_000)
	env.PayIOU(env.GW, carol, "USD", 1_000)
	env.Trust(carol, gw2, "EUR", 100_000)
	env.PayIOU(gw2, carol, "EUR", 1_000)
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMCreate(carol,
		ammtest.IOUAmount(env.GW, "USD", 1_000),
		ammtest.IOUAmount(gw2, "EUR", 1_000),
	).Build()))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 100_000)
	env.PayIOU(env.GW, env.Alice, "USD", 1_000)
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMDeposit(env.Alice, env.USD, eur).
		Amount(ammtest.IOUAmount(env.GW, "USD", 100)).
		SingleAsset().
		Build()))
	env.Close()
	return env, eur, gw2
}

// TestAMMClawbackReservePriority verifies the cleanup amendment's reserve
// bypass for a missing paired IOU holding, while regular AMMWithdraw keeps the
// reserve check. The pre-fix path must leave the missing trust line untouched.
// Reference: rippled AMMClawback_test.cpp testClawbackBypassesReserve.
func TestAMMClawbackReservePriority(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "WithoutCleanup", true: "WithCleanup"}[cleanup], func(t *testing.T) {
			env, eur, gw2 := setupAMMClawbackReserveFixture(t, cleanup)
			lineKey := keylet.Line(env.Alice.ID, gw2.ID, "EUR")
			require.False(t, env.LedgerEntryExists(lineKey))
			require.Equal(t, uint32(2), env.OwnerCount(env.Alice))

			withdraw := env.Submit(ammtest.AMMWithdraw(env.Alice, env.USD, eur).
				WithdrawAll().
				Build())
			ammtest.ExpectTER(t, withdraw, "tecINSUFFICIENT_RESERVE")
			env.Close()
			require.False(t, env.LedgerEntryExists(lineKey))
			require.Equal(t, uint32(2), env.OwnerCount(env.Alice))

			result := env.Submit(ammtest.AMMClawback(env.GW, env.Alice.Address, env.USD, eur).
				Amount(ammtest.IOUAmount(env.GW, "USD", 10)).
				Build())
			if cleanup {
				jtx.RequireTxSuccess(t, result)
				env.Close()
				require.True(t, env.LedgerEntryExists(lineKey))
				require.Equal(t, uint32(3), env.OwnerCount(env.Alice))
				balance := env.IOUBalance(env.Alice, gw2, "EUR")
				require.NotNil(t, balance)
				require.Positive(t, balance.Float64())
			} else {
				ammtest.ExpectTER(t, result, "tecINSUFFICIENT_RESERVE")
				env.Close()
				require.False(t, env.LedgerEntryExists(lineKey))
				require.Equal(t, uint32(2), env.OwnerCount(env.Alice))
			}
		})
	}
}

// TestAMMClawbackAMMLineFreezeOverride covers the fixCleanup3_4_0 exception
// for issuer freeze flags on the AMM's own trust line. A clawback may bypass
// an individual or deep freeze on that line only once the cleanup amendment is
// enabled; the pre-amendment path must roll back with tecINVARIANT_FAILED.
func TestAMMClawbackAMMLineFreezeOverride(t *testing.T) {
	for _, deep := range []bool{false, true} {
		freezeName := "IndividualFreeze"
		if deep {
			freezeName = "DeepFreeze"
		}
		for _, cleanup := range []bool{false, true} {
			t.Run(freezeName+map[bool]string{false: "/WithoutCleanup", true: "/WithCleanup"}[cleanup], func(t *testing.T) {
				env, eur, gw2 := setupAMMClawbackReserveFixture(t, cleanup)
				env.FundAmount(env.Alice, uint64(jtx.XRP(100_000)))
				env.Close()
				ammAccount := ammtest.AMMAccount(t, env, env.USD, eur)
				freeze := trustset.TrustLine(gw2, "EUR", ammAccount, "0")
				if deep {
					freeze.Freeze().DeepFreeze()
				} else {
					freeze.Freeze()
				}
				jtx.RequireTxSuccess(t, env.Submit(freeze.Build()))
				env.Close()

				result := env.Submit(ammtest.AMMClawback(env.GW, env.Alice.Address, env.USD, eur).
					Amount(ammtest.IOUAmount(env.GW, "USD", 10)).
					Build())
				if cleanup {
					jtx.RequireTxSuccess(t, result)
				} else {
					ammtest.ExpectTER(t, result, "tecINVARIANT_FAILED")
				}
			})
		}
	}
}

func setupAMMClawbackZeroRoundedMPT(t *testing.T, cleanup bool) (*ammtest.AMMTestEnv, *mpttest.MPTTester, *jtx.Account) {
	t.Helper()
	env := ammtest.NewAMMTestEnv(t)
	env.EnableFeature("MPTokensV2")
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	}
	for _, account := range []*jtx.Account{env.GW, env.Alice, env.Bob} {
		env.FundAmount(account, uint64(jtx.XRP(10_000_000)))
	}
	env.Close()

	mpt := mpttest.NewMPTTesterNoFund(t, env.TestEnv, env.GW)
	mpt.Create(mpttest.CreateOpts{
		Flags: mpttest.TfMPTCanClawback | mpttest.TfMPTCanTrade | mpttest.TfMPTCanTransfer,
	})
	mpt.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
	mpt.Authorize(mpttest.AuthorizeOpts{Account: env.Bob})
	mpt.Pay(env.GW, env.Alice, 3)
	mpt.Pay(env.GW, env.Bob, 3)

	asset := tx.Asset{MPTIssuanceID: mpt.IssuanceID()}
	jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMCreate(env.Alice,
		mpt.MPTAmount(3),
		ammtest.XRPAmount(333_000),
	).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMDeposit(env.Bob, asset, ammtest.XRP()).
		Amount(mpt.MPTAmount(3)).
		Amount2(ammtest.XRPAmount(333_000)).
		TwoAsset().
		Build()))
	env.Close()

	return env, mpt, ammtest.AMMAccount(t, env, asset, ammtest.XRP())
}

func setupAMMClawbackPairedZeroRoundedMPT(t *testing.T, cleanup bool) (*ammtest.AMMTestEnv, *mpttest.MPTTester, *mpttest.MPTTester, *jtx.Account) {
	t.Helper()
	env := ammtest.NewAMMTestEnv(t)
	env.EnableFeature("MPTokensV2")
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	}
	for _, account := range []*jtx.Account{env.GW, env.Alice, env.Bob} {
		env.FundAmount(account, uint64(jtx.XRP(10_000_000)))
	}
	env.Close()

	btc := mpttest.NewMPTTesterNoFund(t, env.TestEnv, env.GW)
	btc.Create(mpttest.CreateOpts{
		Flags: mpttest.TfMPTCanClawback | mpttest.TfMPTCanTrade | mpttest.TfMPTCanTransfer,
	})
	btc.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
	btc.Authorize(mpttest.AuthorizeOpts{Account: env.Bob})
	btc.Pay(env.GW, env.Alice, 3_000)
	btc.Pay(env.GW, env.Bob, 3_000)

	eth := mpttest.NewMPTTesterNoFund(t, env.TestEnv, env.GW)
	eth.Create(mpttest.CreateOpts{
		Flags: mpttest.TfMPTCanClawback | mpttest.TfMPTCanTrade | mpttest.TfMPTCanTransfer,
	})
	eth.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
	eth.Authorize(mpttest.AuthorizeOpts{Account: env.Bob})
	eth.Pay(env.GW, env.Alice, 3)
	eth.Pay(env.GW, env.Bob, 3)

	btcAsset := tx.Asset{MPTIssuanceID: btc.IssuanceID()}
	ethAsset := tx.Asset{MPTIssuanceID: eth.IssuanceID()}
	jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMCreate(env.Alice,
		btc.MPTAmount(3_000),
		eth.MPTAmount(3),
	).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMDeposit(env.Bob, btcAsset, ethAsset).
		Amount(btc.MPTAmount(3_000)).
		Amount2(eth.MPTAmount(3)).
		TwoAsset().
		Build()))
	env.Close()

	return env, btc, eth, ammtest.AMMAccount(t, env, btcAsset, ethAsset)
}

// TestAMMClawbackZeroRoundedMPT verifies #7704's cleanup gate. A one-unit
// clawback from a six-unit MPT pool rounds to zero on the clawed side. Before
// fixCleanup3_4_0 the transaction succeeds and burns LP; with the amendment it
// returns tecAMM_FAILED and leaves every pool and holder balance unchanged.
func TestAMMClawbackZeroRoundedMPT(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "WithoutCleanup", true: "WithCleanup"}[cleanup], func(t *testing.T) {
			env, mpt, ammAccount := setupAMMClawbackZeroRoundedMPT(t, cleanup)
			asset := tx.Asset{MPTIssuanceID: mpt.IssuanceID()}
			poolMPTBefore := readAMMClawbackMPToken(t, env, mpt.IssuanceID(), ammAccount).MPTAmount
			poolXRPBefore := env.Balance(ammAccount)
			ammDataBefore := env.ReadAMMData(asset, ammtest.XRP())
			require.NotNil(t, ammDataBefore)
			holderLPBefore := env.IOUBalance(env.Alice, ammAccount, ammDataBefore.LPTokenBalance.Currency)
			require.NotNil(t, holderLPBefore)

			result := env.Submit(ammtest.AMMClawback(env.GW, env.Alice.Address, asset, ammtest.XRP()).
				Amount(mpt.MPTAmount(1)).
				Build())
			if cleanup {
				jtx.RequireTxClaimed(t, result, ammtest.TecAMM_FAILED)
			} else {
				jtx.RequireTxSuccess(t, result)
			}
			env.Close()

			poolMPTAfter := readAMMClawbackMPToken(t, env, mpt.IssuanceID(), ammAccount).MPTAmount
			ammDataAfter := env.ReadAMMData(asset, ammtest.XRP())
			require.NotNil(t, ammDataAfter)
			holderLPAfter := env.IOUBalance(env.Alice, ammAccount, ammDataBefore.LPTokenBalance.Currency)
			require.NotNil(t, holderLPAfter)
			if cleanup {
				require.Equal(t, poolMPTBefore, poolMPTAfter)
				require.Equal(t, poolXRPBefore, env.Balance(ammAccount))
				require.Zero(t, ammDataBefore.LPTokenBalance.Compare(ammDataAfter.LPTokenBalance))
				require.Zero(t, holderLPBefore.Compare(*holderLPAfter))
			} else {
				require.Equal(t, poolMPTBefore, poolMPTAfter)
				require.Less(t, env.Balance(ammAccount), poolXRPBefore)
				require.Less(t, ammDataAfter.LPTokenBalance.Compare(ammDataBefore.LPTokenBalance), 0)
				require.Less(t, holderLPAfter.Compare(*holderLPBefore), 0)
			}
		})
	}

	for _, cleanup := range []bool{false, true} {
		t.Run("PairedAmount2/"+map[bool]string{false: "WithoutCleanup", true: "WithCleanup"}[cleanup], func(t *testing.T) {
			env, btc, eth, ammAccount := setupAMMClawbackPairedZeroRoundedMPT(t, cleanup)
			btcAsset := tx.Asset{MPTIssuanceID: btc.IssuanceID()}
			ethAsset := tx.Asset{MPTIssuanceID: eth.IssuanceID()}
			btcBefore := readAMMClawbackMPToken(t, env, btc.IssuanceID(), ammAccount).MPTAmount
			ethBefore := readAMMClawbackMPToken(t, env, eth.IssuanceID(), ammAccount).MPTAmount
			ammDataBefore := env.ReadAMMData(btcAsset, ethAsset)
			require.NotNil(t, ammDataBefore)
			holderLPBefore := env.IOUBalance(env.Alice, ammAccount, ammDataBefore.LPTokenBalance.Currency)
			require.NotNil(t, holderLPBefore)

			result := env.Submit(ammtest.AMMClawback(env.GW, env.Alice.Address, btcAsset, ethAsset).
				Amount(btc.MPTAmount(500)).
				Build())
			if cleanup {
				jtx.RequireTxClaimed(t, result, ammtest.TecAMM_FAILED)
			} else {
				jtx.RequireTxSuccess(t, result)
			}
			env.Close()

			btcAfter := readAMMClawbackMPToken(t, env, btc.IssuanceID(), ammAccount).MPTAmount
			ethAfter := readAMMClawbackMPToken(t, env, eth.IssuanceID(), ammAccount).MPTAmount
			ammDataAfter := env.ReadAMMData(btcAsset, ethAsset)
			require.NotNil(t, ammDataAfter)
			holderLPAfter := env.IOUBalance(env.Alice, ammAccount, ammDataBefore.LPTokenBalance.Currency)
			require.NotNil(t, holderLPAfter)
			if cleanup {
				require.Equal(t, btcBefore, btcAfter)
				require.Equal(t, ethBefore, ethAfter)
				require.Zero(t, ammDataBefore.LPTokenBalance.Compare(ammDataAfter.LPTokenBalance))
				require.Zero(t, holderLPBefore.Compare(*holderLPAfter))
			} else {
				require.Less(t, btcAfter, btcBefore)
				require.Equal(t, ethBefore, ethAfter)
				require.Less(t, ammDataAfter.LPTokenBalance.Compare(ammDataBefore.LPTokenBalance), 0)
				require.Less(t, holderLPAfter.Compare(*holderLPBefore), 0)
			}
		})
	}
}
