package offer

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// TestOffer_DrainedMakerSiblingsGroomed reproduces the mainnet divergence at
// ledger 99248432 (issue #1114): a maker has several same-quality offers, the
// crossing consumes the first up to the maker's funded cap — draining the maker
// and leaving a non-zero remainder — and that exactly satisfies the taker's
// demand, so the walk stops before reaching the siblings.
//
// rippled's OfferStream::step reads the maker's funds live from the working view,
// so each same-quality sibling it steps over reads became-unfunded and is groomed
// off the book on the same pass. goXRPL previously anchored the trailing-drain
// became test to the iteration base, which still showed the maker funded this
// pass, and left the siblings behind. With the single funded rule now applied to
// every stepped-over offer against the live sandbox, the siblings are groomed and
// the maker ends with zero offers.
//
// Reference: rippled OfferStream.cpp step() 306-340 (live ownerFunds, found vs
// became unfunded); BookStep.cpp forEachOffer do-while 855-865.
func TestOffer_DrainedMakerSiblingsGroomed(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			env := newEnvWithFeatures(t, fs.disabled)

			gw := jtx.NewAccount("gateway")
			maker := jtx.NewAccount("maker")
			taker := jtx.NewAccount("taker")

			USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

			env.FundAmount(gw, uint64(jtx.XRP(10000)))
			env.FundAmount(maker, uint64(jtx.XRP(10000)))
			env.FundAmount(taker, uint64(jtx.XRP(10000)))
			env.Close()

			env.Trust(maker, USD(1000))
			env.Trust(taker, USD(1000))

			// The maker holds exactly 100 USD — enough to fund only one offer's
			// funded cap. The taker pays the maker XRP for that USD.
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, maker, USD(100)).Build()))
			env.Close()

			// Three same-quality (1 USD : 1 XRP) offers, each selling 150 USD. Every
			// offer's funded cap is the maker's 100 USD balance, so crossing the first
			// drains the maker and leaves a 50 USD remainder on it (a funded-cap full
			// take, not an exact consume).
			const nSiblings = 3
			for range nSiblings {
				jtx.RequireTxSuccess(t, env.Submit(
					OfferCreate(maker, jtx.XRPTxAmountFromXRP(150), USD(150)).Build()))
			}
			env.Close()
			require.Equal(t, uint32(nSiblings), CountOffers(env, maker),
				"maker should start with all sibling offers on the book")

			// The taker buys exactly 100 USD for 100 XRP — satisfied in full by the
			// first offer's funded-cap take, which drains the maker before the walk
			// reaches the siblings.
			jtx.RequireTxSuccess(t, env.Submit(
				OfferCreate(taker, USD(100), jtx.XRPTxAmountFromXRP(100)).Build()))
			env.Close()

			// Every sibling read became-unfunded from the live sandbox and was
			// groomed: the maker ends with no offers. Before the fix two siblings
			// were left on the book.
			require.Equal(t, uint32(0), CountOffers(env, maker),
				"all same-quality siblings of the drained maker must be groomed")
			// The taker's demand was met in full, so its offer crossed completely and
			// left nothing on the book.
			require.Equal(t, uint32(0), CountOffers(env, taker),
				"the taker's offer should fully cross and not rest on the book")
		})
	}
}
