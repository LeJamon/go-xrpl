package offer_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	ammtest "github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	paymentbuild "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const (
	issue2137FillerOffers = 129
	issue2137OwnerCount   = issue2137FillerOffers + 1 // USD trust line + offers
	issue2137ReserveBase  = uint64(1_000_000)
	issue2137ReserveInc   = uint64(200_000)
	issue2137PostFee      = uint64(29_743_827)
	issue2137CorrectSpend = uint64(2_943_827)
	issue2137AMMUSD       = "2.942960643299"
	issue2137AMMPoolUSD   = "9997.057039356701"
)

type issue2137Fixture struct {
	env           *jtx.TestEnv
	ammEnv        *ammtest.AMMTestEnv
	gw            *jtx.Account
	payer         *jtx.Account
	ammAccount    *jtx.Account
	cancelSeq     uint32
	remainingSeq  uint32
	balanceBefore uint64
}

func newIssue2137Fixture(t *testing.T, withAMM, withCLOB bool) *issue2137Fixture {
	t.Helper()

	fixture := &issue2137Fixture{}
	if withAMM {
		fixture.ammEnv = ammtest.NewAMMTestEnv(t)
		fixture.env = fixture.ammEnv.TestEnv
		fixture.gw = fixture.ammEnv.GW

		fixture.ammEnv.FundWithIOUs(30_000, 0)
		fixture.ammEnv.Close()
		result := fixture.env.Submit(ammtest.AMMCreate(
			fixture.ammEnv.Alice,
			ammtest.XRPAmount(10_000),
			ammtest.IOUAmount(fixture.gw, "USD", 10_000),
		).Build())
		jtx.RequireTxSuccess(t, result)
		fixture.ammEnv.Close()
		fixture.ammAccount = ammtest.AMMAccount(t, fixture.ammEnv, ammtest.XRP(), fixture.ammEnv.USD)
	} else {
		fixture.env = jtx.NewTestEnvBacked(t)
		fixture.gw = jtx.NewAccount("issue2137-gw")
		maker := jtx.NewAccount("issue2137-maker")
		fixture.env.FundAmount(fixture.gw, uint64(jtx.XRP(30_000)))
		fixture.env.FundAmount(maker, uint64(jtx.XRP(30_000)))
		fixture.env.Close()
		fixture.env.Trust(maker, jtx.USD(fixture.gw, 30_000))
		fixture.env.Close()
		result := fixture.env.Submit(paymentbuild.PayIssued(
			fixture.gw, maker, jtx.USD(fixture.gw, 10_000)).Build())
		jtx.RequireTxSuccess(t, result)
		fixture.env.Close()

		if withCLOB {
			fixture.env.SetReserves(issue2137ReserveBase, issue2137ReserveInc)
			fixture.env.Close()
			result = fixture.env.Submit(offerbuild.OfferCreate(
				maker,
				jtx.XRPTxAmountFromXRP(10_000),
				jtx.USD(fixture.gw, 10_000),
			).Passive().Build())
			jtx.RequireTxSuccess(t, result)
			fixture.env.Close()
		}
	}

	if withAMM {
		fixture.env.SetReserves(issue2137ReserveBase, issue2137ReserveInc)
		fixture.env.Close()
	}

	fixture.payer = jtx.NewAccount("issue2137-payer")
	initialFunding := issue2137PostFee + fixture.env.BaseFee()*(issue2137FillerOffers+1)
	fixture.env.FundAmount(fixture.payer, initialFunding)
	fixture.env.Close()
	fixture.env.Trust(fixture.payer, jtx.USD(fixture.gw, 1_000_000))
	fixture.env.Close()

	fixture.cancelSeq = fixture.env.Seq(fixture.payer)
	for range issue2137FillerOffers {
		result := fixture.env.Submit(offerbuild.OfferCreate(
			fixture.payer,
			jtx.XRPTxAmountFromXRP(1),
			jtx.USD(fixture.payer, 1),
		).Passive().Build())
		jtx.RequireTxSuccess(t, result)
	}
	fixture.remainingSeq = fixture.cancelSeq + 1
	fixture.balanceBefore = fixture.env.Balance(fixture.payer)

	require.Equal(t, issue2137PostFee+fixture.env.BaseFee(), fixture.balanceBefore)
	require.Equal(t, uint32(issue2137OwnerCount), fixture.env.OwnerCount(fixture.payer))
	offerbuild.RequireOfferCount(t, fixture.env, fixture.payer, issue2137FillerOffers)
	offerbuild.RequireOfferInLedger(t, fixture.env, fixture.payer, fixture.cancelSeq)
	offerbuild.RequireOfferInLedger(t, fixture.env, fixture.payer, fixture.remainingSeq)
	return fixture
}

func issue2137Cross(f *issue2137Fixture, cancel bool) *offerbuild.OfferCreateBuilder {
	builder := offerbuild.OfferCreate(
		f.payer,
		jtx.USD(f.gw, 9_000),
		jtx.XRPTxAmountFromXRP(10_000),
	).ImmediateOrCancel()
	if cancel {
		builder.OfferSequence(f.cancelSeq)
	}
	return builder
}

func issue2137RegularAMMCross(f *issue2137Fixture, cancel bool) *offerbuild.OfferCreateBuilder {
	builder := offerbuild.OfferCreate(
		f.payer,
		jtx.IssuedCurrencyFromMantissa(f.gw, "USD", 2_942_960_643_299, -12),
		tx.NewXRPAmount(int64(issue2137CorrectSpend)),
	)
	if cancel {
		builder.OfferSequence(f.cancelSeq)
	}
	return builder
}

func TestOffer_Issue2137CancellationReleasesReserveForAMMCross(t *testing.T) {
	f := newIssue2137Fixture(t, true, false)
	poolXRPBefore := f.ammEnv.AMMPoolXRP(f.ammAccount)
	wantUSD := jtx.IssuedCurrencyFromMantissa(f.gw, "USD", 2_942_960_643_299, -12)
	crossSeq := f.env.Seq(f.payer)

	result := f.env.Submit(issue2137RegularAMMCross(f, true).Build())
	jtx.RequireTxSuccess(t, result)
	bookBase := keylet.BookDir(keylet.CurrencyBytes("USD"), f.gw.ID, [20]byte{}, [20]byte{})
	next, _, found, err := f.env.Ledger().Succ(bookBase.Key)
	require.NoError(t, err)
	if found {
		require.NotEqual(t, bookBase.Key[:24], next[:24], "crossing must not leave a new book directory")
	}
	poolXRPAfter := f.ammEnv.AMMPoolXRP(f.ammAccount)
	poolUSDAfter := f.ammEnv.AMMPoolIOUPrecise(f.ammAccount, f.gw, "USD")

	require.Equal(t, issue2137CorrectSpend, poolXRPAfter-poolXRPBefore,
		"AMM crossing must consume the liquid XRP released by OfferSequence")
	require.Equal(t, issue2137AMMUSD, wantUSD.Value())
	payerUSD, found := f.env.LookupIOUBalance(f.payer, f.gw, "USD")
	require.True(t, found, "payer USD trust line must remain present")
	require.Equal(t, 0, payerUSD.Compare(wantUSD), "payer must receive the exact AMM USD output")
	require.Equal(t, issue2137AMMUSD, payerUSD.Value())
	require.Equal(t, issue2137AMMPoolUSD, poolUSDAfter.Value(), "AMM USD pool balance must decrease by the delivered output")
	require.Equal(t, f.balanceBefore-f.env.BaseFee()-issue2137CorrectSpend, f.env.Balance(f.payer))
	require.Equal(t, offerbuild.Reserve(f.env, 129), f.env.Balance(f.payer),
		"successful crossing must leave exactly the post-cancellation reserve")
	jtx.RequireOwnerCount(t, f.env, f.payer, 129)
	offerbuild.RequireOfferCount(t, f.env, f.payer, issue2137FillerOffers-1)
	offerbuild.RequireNoOfferInLedger(t, f.env, f.payer, f.cancelSeq)
	offerbuild.RequireOfferInLedger(t, f.env, f.payer, f.remainingSeq)
	offerbuild.RequireNoOfferInLedger(t, f.env, f.payer, crossSeq)
}

func TestOffer_Issue2137NoCancellationAMMControl(t *testing.T) {
	f := newIssue2137Fixture(t, true, false)
	poolBefore := f.ammEnv.AMMPoolXRP(f.ammAccount)

	result := f.env.Submit(issue2137Cross(f, false).Build())
	jtx.RequireTxSuccess(t, result)
	poolAfter := f.ammEnv.AMMPoolXRP(f.ammAccount)

	const wantSpend = uint64(2_743_827)
	require.Equal(t, wantSpend, poolAfter-poolBefore)
	require.Equal(t, offerbuild.Reserve(f.env, issue2137OwnerCount), f.env.Balance(f.payer))
	jtx.RequireOwnerCount(t, f.env, f.payer, issue2137OwnerCount)
	offerbuild.RequireOfferCount(t, f.env, f.payer, issue2137FillerOffers)
	offerbuild.RequireOfferInLedger(t, f.env, f.payer, f.cancelSeq)
}

func TestOffer_Issue2137OrderBookOnlyControl(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cancel     bool
		wantSpend  uint64
		ownerCount uint32
	}{
		{name: "NoCancellation", wantSpend: 2_743_827, ownerCount: issue2137OwnerCount},
		{name: "Cancellation", cancel: true, wantSpend: issue2137CorrectSpend, ownerCount: 129},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssue2137Fixture(t, false, true)
			crossSeq := f.env.Seq(f.payer)

			result := f.env.Submit(issue2137Cross(f, tc.cancel).Build())
			jtx.RequireTxSuccess(t, result)

			require.Equal(t, tc.wantSpend, f.balanceBefore-f.env.BaseFee()-f.env.Balance(f.payer))
			require.Equal(t, offerbuild.Reserve(f.env, tc.ownerCount), f.env.Balance(f.payer))
			payerUSD, found := f.env.LookupIOUBalance(f.payer, f.gw, "USD")
			require.True(t, found, "payer USD trust line must remain present")
			require.Equal(t, 0, payerUSD.Compare(jtx.IssuedCurrencyFromMantissa(f.gw, "USD", int64(tc.wantSpend), -6)))
			jtx.RequireOwnerCount(t, f.env, f.payer, tc.ownerCount)
			if tc.cancel {
				offerbuild.RequireOfferCount(t, f.env, f.payer, issue2137FillerOffers-1)
				offerbuild.RequireNoOfferInLedger(t, f.env, f.payer, f.cancelSeq)
			} else {
				offerbuild.RequireOfferCount(t, f.env, f.payer, issue2137FillerOffers)
				offerbuild.RequireOfferInLedger(t, f.env, f.payer, f.cancelSeq)
			}
			offerbuild.RequireNoOfferInLedger(t, f.env, f.payer, crossSeq)
		})
	}
}

func TestOffer_Issue2137ZeroFundsAfterFee(t *testing.T) {
	f := newIssue2137Fixture(t, true, false)
	targetBeforeFinal := offerbuild.Reserve(f.env, issue2137OwnerCount) + 1
	drainAmount := f.balanceBefore - f.env.BaseFee() - targetBeforeFinal
	sink := jtx.NewAccount("issue2137-sink")
	f.env.FundAmount(sink, uint64(jtx.XRP(1)))
	f.env.Close()

	result := f.env.Submit(paymentbuild.Pay(f.payer, sink, drainAmount).Build())
	jtx.RequireTxSuccess(t, result)
	f.env.Close()
	require.Equal(t, targetBeforeFinal, f.env.Balance(f.payer))
	crossSeq := f.env.Seq(f.payer)

	result = f.env.Submit(offerbuild.OfferCreate(
		f.payer,
		jtx.USD(f.gw, 1),
		tx.NewXRPAmount(1),
	).Fee(issue2137ReserveInc + 1).OfferSequence(f.cancelSeq).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecUNFUNDED_OFFER)

	require.Equal(t, offerbuild.Reserve(f.env, 129), f.env.Balance(f.payer),
		"the fee leaves the account below its restored owner reserve")
	jtx.RequireOwnerCount(t, f.env, f.payer, issue2137OwnerCount)
	offerbuild.RequireOfferCount(t, f.env, f.payer, issue2137FillerOffers)
	offerbuild.RequireOfferInLedger(t, f.env, f.payer, f.cancelSeq)
	offerbuild.RequireNoOfferInLedger(t, f.env, f.payer, crossSeq)
}

func TestOffer_Issue2137CancellationFundsOtherwiseUnfundedCross(t *testing.T) {
	f := newIssue2137Fixture(t, true, false)
	poolBefore := f.ammEnv.AMMPoolXRP(f.ammAccount)
	fee := f.balanceBefore - offerbuild.Reserve(f.env, issue2137OwnerCount)
	result := f.env.Submit(issue2137Cross(f, true).Fee(fee).Build())
	jtx.RequireTxSuccess(t, result)
	require.Equal(t, issue2137ReserveInc, f.ammEnv.AMMPoolXRP(f.ammAccount)-poolBefore)
	require.Equal(t, offerbuild.Reserve(f.env, 129), f.env.Balance(f.payer))
	jtx.RequireOwnerCount(t, f.env, f.payer, 129)
	offerbuild.RequireNoOfferInLedger(t, f.env, f.payer, f.cancelSeq)
}

func TestOffer_Issue2137CancellationRollsBackOnKill(t *testing.T) {
	for _, mode := range []struct {
		name  string
		build func(*offerbuild.OfferCreateBuilder) *offerbuild.OfferCreateBuilder
	}{
		{name: "FillOrKill", build: func(builder *offerbuild.OfferCreateBuilder) *offerbuild.OfferCreateBuilder {
			return builder.FillOrKill()
		}},
		{name: "ImmediateOrCancel", build: func(builder *offerbuild.OfferCreateBuilder) *offerbuild.OfferCreateBuilder {
			return builder.ImmediateOrCancel()
		}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			f := newIssue2137Fixture(t, true, false)
			builder := offerbuild.OfferCreate(
				f.payer,
				jtx.XRPTxAmountFromXRP(1),
				jtx.EUR(f.payer, 1),
			).OfferSequence(f.cancelSeq)
			result := f.env.Submit(mode.build(builder).Build())
			jtx.RequireTxClaimed(t, result, jtx.TecKILLED)

			require.Equal(t, f.balanceBefore-f.env.BaseFee(), f.env.Balance(f.payer))
			jtx.RequireOwnerCount(t, f.env, f.payer, issue2137OwnerCount)
			offerbuild.RequireOfferCount(t, f.env, f.payer, issue2137FillerOffers)
			offerbuild.RequireOfferInLedger(t, f.env, f.payer, f.cancelSeq)
			offerbuild.RequireOfferInLedger(t, f.env, f.payer, f.remainingSeq)
		})
	}
}
