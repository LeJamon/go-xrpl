package payment_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	offertest "github.com/LeJamon/go-xrpl/internal/testing/offer"
	paytest "github.com/LeJamon/go-xrpl/internal/testing/payment"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const (
	aggregateLargeInput   = int64(5_000_000_000_000_000_000)
	aggregateLargeOutput  = int64(2_500_000_000_000_000_000)
	aggregateSmallInput   = int64(2)
	aggregateSmallOutput  = int64(1)
	aggregateOutputSupply = uint64(5_000_000_000_000_000_001)
)

type mptAggregateOffer struct {
	account *jtx.Account
	seq     uint32
	pays    int64
	gets    int64
}

type mptAggregateFixture struct {
	env       *jtx.TestEnv
	tokenA    *mpt.MPTTester
	tokenB    *mpt.MPTTester
	taker     *jtx.Account
	recipient *jtx.Account
	offers    []mptAggregateOffer
}

func newMPTAggregateFixture(t *testing.T) mptAggregateFixture {
	t.Helper()

	env := jtx.NewTestEnv(t)
	env.EnableFeature("MPTokensV2")

	issuerA := jtx.NewAccount("issuerA")
	issuerB := jtx.NewAccount("issuerB")
	taker := jtx.NewAccount("taker")
	recipient := jtx.NewAccount("recipient")
	first := jtx.NewAccount("first")
	second := jtx.NewAccount("second")
	last := jtx.NewAccount("last")
	for _, account := range []*jtx.Account{issuerA, issuerB, taker, recipient, first, second, last} {
		env.FundAmount(account, uint64(jtx.XRP(10_000)))
	}
	env.Close()

	tokenA := mpt.NewMPTTesterNoFund(t, env, issuerA)
	tokenA.Create(mpt.CreateOpts{
		MaxAmt:      mpt.PtrUint64(mpt.MaxMPTokenAmount),
		TransferFee: mpt.PtrUint16(50_000),
		Flags:       mpt.TfMPTCanTransfer | mpt.TfMPTCanTrade,
	})
	tokenB := mpt.NewMPTTesterNoFund(t, env, issuerB)
	tokenB.Create(mpt.CreateOpts{
		MaxAmt: mpt.PtrUint64(aggregateOutputSupply),
		Flags:  mpt.TfMPTCanTransfer | mpt.TfMPTCanTrade,
	})
	for _, account := range []*jtx.Account{taker, recipient, first, second, last} {
		tokenA.Authorize(mpt.AuthorizeOpts{Account: account})
		tokenB.Authorize(mpt.AuthorizeOpts{Account: account})
	}

	tokenA.Pay(issuerA, taker, int64(mpt.MaxMPTokenAmount))
	tokenB.Pay(issuerB, first, aggregateLargeOutput)
	tokenB.Pay(issuerB, second, aggregateLargeOutput)
	tokenB.Pay(issuerB, last, aggregateSmallOutput)

	offers := []mptAggregateOffer{
		{account: first, pays: aggregateLargeInput, gets: aggregateLargeOutput},
		{account: second, pays: aggregateLargeInput, gets: aggregateLargeOutput},
		{account: last, pays: aggregateSmallInput, gets: aggregateSmallOutput},
	}
	for i := range offers {
		offers[i].seq = env.Seq(offers[i].account)
		jtx.RequireTxSuccess(t, env.CreatePassiveOffer(
			offers[i].account,
			tokenB.MPTAmount(offers[i].gets),
			tokenA.MPTAmount(offers[i].pays),
		))
	}
	env.Close()

	return mptAggregateFixture{
		env:       env,
		tokenA:    tokenA,
		tokenB:    tokenB,
		taker:     taker,
		recipient: recipient,
		offers:    offers,
	}
}

func (f mptAggregateFixture) submit(t *testing.T, useTicket, partial bool) {
	t.Helper()

	takerXRP := f.env.Balance(f.taker)
	takerSequence := f.env.Seq(f.taker)
	ticketSequence := uint32(0)
	if useTicket {
		ticketSequence = f.env.CreateTickets(f.taker, 1)
		f.env.Close()
		takerXRP = f.env.Balance(f.taker)
		takerSequence = f.env.Seq(f.taker)
	}
	ownerCounts := make(map[*jtx.Account]uint32)
	for _, offer := range f.offers {
		ownerCounts[offer.account] = f.env.AccountInfo(offer.account).OwnerCount
	}

	builder := paytest.PayIssued(f.taker, f.recipient, f.tokenB.MPTAmount(int64(aggregateOutputSupply))).
		SendMax(f.tokenA.MPTAmount(int64(mpt.MaxMPTokenAmount))).
		Paths([][]paymenttx.PathStep{{{MPTIssuanceID: f.tokenB.IssuanceID()}}}).
		NoDirectRipple()
	if partial {
		builder.PartialPayment()
	}
	payment := builder.Build()
	if useTicket {
		jtx.WithTicketSeq(payment, ticketSequence)
	}
	result := f.env.Submit(payment)

	expectedCode := jtx.TecPATH_PARTIAL
	if partial {
		expectedCode = jtx.TecPATH_DRY
	}
	jtx.RequireTxClaimed(t, result, expectedCode)
	require.True(t, result.Applied)
	require.False(t, result.Success)
	require.Equal(t, f.env.BaseFee(), result.Fee)
	jtx.RequireBalance(t, f.env, f.taker, takerXRP-f.env.BaseFee())
	if useTicket {
		jtx.RequireSequence(t, f.env, f.taker, takerSequence)
		require.Equal(t, uint32(0), f.env.TicketCount(f.taker))
		require.False(t, f.env.LedgerEntryExists(keylet.Ticket(f.taker.ID, ticketSequence)))
	} else {
		jtx.RequireSequence(t, f.env, f.taker, takerSequence+1)
	}

	f.tokenA.RequireMPTokenAmount(f.taker, int64(mpt.MaxMPTokenAmount))
	f.tokenB.RequireMPTokenAmount(f.recipient, 0)
	for _, offer := range f.offers {
		f.tokenA.RequireMPTokenAmount(offer.account, 0)
		f.tokenB.RequireMPTokenAmount(offer.account, offer.gets)
		offertest.RequireOfferInLedger(t, f.env, offer.account, offer.seq)
		offertest.RequireOfferCount(t, f.env, offer.account, 1)
		jtx.RequireOwnerCount(t, f.env, offer.account, ownerCounts[offer.account])
		entry := offertest.GetOffer(f.env, offer.account, offer.seq)
		require.NotNil(t, entry)
		pays, paysOK := entry.TakerPays.MPTRaw()
		gets, getsOK := entry.TakerGets.MPTRaw()
		require.True(t, paysOK)
		require.True(t, getsOK)
		require.Equal(t, offer.pays, pays)
		require.Equal(t, offer.gets, gets)
	}
	require.True(t, f.tokenA.CheckMPTokenOutstandingAmount(int64(mpt.MaxMPTokenAmount)))
	require.True(t, f.tokenB.CheckMPTokenOutstandingAmount(int64(aggregateOutputSupply)))
}

func TestMPTBookAggregateOverflowRollsBackRealPayment(t *testing.T) {
	for _, test := range []struct {
		name       string
		useTicket  bool
		partialPay bool
	}{
		{name: "sequence partial", partialPay: true},
		{name: "ticket partial", useTicket: true, partialPay: true},
		{name: "sequence non-partial"},
		{name: "ticket non-partial", useTicket: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMPTAggregateFixture(t)
			fixture.submit(t, test.useTicket, test.partialPay)
		})
	}
}
