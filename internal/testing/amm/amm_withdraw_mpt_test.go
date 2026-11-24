package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	ammtest "github.com/LeJamon/go-xrpl/internal/testing/amm"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type ammWithdrawMPTFixture struct {
	env   *ammtest.AMMTestEnv
	token *mpttest.MPTTester
	asset tx.Asset
}

func setupAMMWithdrawMPTFixture(t *testing.T, poolMPT int64) *ammWithdrawMPTFixture {
	t.Helper()
	env := ammtest.NewAMMTestEnv(t)
	env.EnableFeature("MPTokensV2")
	env.EnableFeature("fixAMMv1_2")
	for _, account := range []*jtx.Account{env.GW, env.Alice, env.Bob, env.Carol} {
		env.FundAmount(account, uint64(jtx.XRP(100_000)))
	}
	env.Close()

	token := mpttest.NewMPTTesterNoFund(t, env.TestEnv, env.GW)
	token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTrade | mpttest.TfMPTCanTransfer})
	token.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
	token.Pay(env.GW, env.Alice, poolMPT)
	asset := tx.Asset{MPTIssuanceID: token.IssuanceID()}
	jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMCreate(
		env.Alice,
		ammtest.XRPAmount(1_000),
		token.MPTAmount(poolMPT),
	).Build()))
	env.Close()

	// Bob receives only LP tokens. The unrelated trust line leaves one spare
	// owner object so the missing MPT holding is reserve-gated.
	jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMDeposit(env.Bob, ammtest.XRP(), asset).
		Amount(ammtest.XRPAmount(100)).
		SingleAsset().
		Build()))
	env.Close()
	env.Trust(env.Bob, env.GW, "USD", 1)
	env.Close()
	require.Equal(t, uint32(2), env.OwnerCount(env.Bob))

	id, err := mptutil.DecodeID(token.IssuanceID())
	require.NoError(t, err)
	require.False(t, env.LedgerEntryExists(keylet.MPTokenByID(id, env.Bob.ID)))

	return &ammWithdrawMPTFixture{env: env, token: token, asset: asset}
}

func (f *ammWithdrawMPTFixture) constrainReserve(t *testing.T) {
	t.Helper()
	required := f.env.ReserveBase() + uint64(f.env.OwnerCount(f.env.Bob)+1)*f.env.ReserveIncrement()
	target := required - 1
	jtx.RequireTxSuccess(t, f.env.Submit(payment.Pay(
		f.env.Bob,
		f.env.Carol,
		f.env.Balance(f.env.Bob)-target-f.env.BaseFee(),
	).Build()))
	f.env.Close()
	require.Equal(t, target, f.env.Balance(f.env.Bob))
}

func (f *ammWithdrawMPTFixture) withdraw(t *testing.T, mptFirst bool, mptAmount int64) jtx.TxResult {
	t.Helper()
	withdraw := ammtest.AMMWithdraw(f.env.Bob, ammtest.XRP(), f.asset)
	if mptFirst {
		withdraw.Amount(f.token.MPTAmount(mptAmount)).Amount2(ammtest.XRPAmount(2))
	} else {
		withdraw.Amount(ammtest.XRPAmount(2)).Amount2(f.token.MPTAmount(mptAmount))
	}
	return f.env.Submit(withdraw.TwoAsset().Build())
}

func (f *ammWithdrawMPTFixture) holding(t *testing.T) *state.MPTokenData {
	t.Helper()
	id, err := mptutil.DecodeID(f.token.IssuanceID())
	require.NoError(t, err)
	raw, err := f.env.LedgerEntry(keylet.MPTokenByID(id, f.env.Bob.ID))
	require.NoError(t, err)
	require.NotNil(t, raw)
	holding, err := state.ParseMPToken(raw)
	require.NoError(t, err)
	return holding
}

func TestAMMWithdrawTwoAssetMPTReserveOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mptFirst bool
		want     string
	}{
		{name: "XRPFirstRaisesLiveBalance", want: "tesSUCCESS"},
		{name: "MPTFirstChecksReserve", mptFirst: true, want: "tecINSUFFICIENT_RESERVE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupAMMWithdrawMPTFixture(t, 1_000)
			fixture.constrainReserve(t)
			result := fixture.withdraw(t, tc.mptFirst, 2)
			ammtest.ExpectTER(t, result, tc.want)
			if tc.mptFirst {
				id, err := mptutil.DecodeID(fixture.token.IssuanceID())
				require.NoError(t, err)
				require.False(t, fixture.env.LedgerEntryExists(keylet.MPTokenByID(id, fixture.env.Bob.ID)))
				return
			}
			holding := fixture.holding(t)
			require.Positive(t, holding.MPTAmount)
		})
	}
}

func TestAMMWithdrawTwoAssetMPTZeroRoundedReserve(t *testing.T) {
	for _, tc := range []struct {
		name              string
		sufficientReserve bool
	}{
		{name: "SufficientReserveCreatesZeroHolding", sufficientReserve: true},
		{name: "InsufficientReserveLeavesHoldingAbsent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupAMMWithdrawMPTFixture(t, 1)
			if !tc.sufficientReserve {
				fixture.constrainReserve(t)
			}
			result := fixture.withdraw(t, true, 1)
			if !tc.sufficientReserve {
				ammtest.ExpectTER(t, result, "tecINSUFFICIENT_RESERVE")
				id, err := mptutil.DecodeID(fixture.token.IssuanceID())
				require.NoError(t, err)
				require.False(t, fixture.env.LedgerEntryExists(keylet.MPTokenByID(id, fixture.env.Bob.ID)))
				return
			}
			jtx.RequireTxSuccess(t, result)
			holding := fixture.holding(t)
			require.Zero(t, holding.MPTAmount)
		})
	}
}
