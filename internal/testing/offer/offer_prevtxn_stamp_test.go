package offer

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// TestOffer_PartialConsumeRoundsBack_NoGhostModifiedNode pins the issue #1081
// fix: when a crossing draws a tiny amount out of a near-maximum 1:1 offer, the
// offer's recomputed TakerPays/TakerGets round back to byte-identical values, so
// the resting offer must be left completely untouched — no ModifiedNode at all.
//
// The mainnet account_hash fork at ledger 99238379 (tx #57) happened because
// consumeOffer's partial-consume branch stamped PreviousTxnID/PreviousTxnLgrSeq
// on the offer before serializing. When the consumed amount is far below the
// 16-significant-digit IOU precision of the offer's amounts (here ~1e96), the
// recomputed amounts are byte-identical to the originals, so the stamp was the
// ONLY difference between the original and current SLE — defeating the
// bytes.Equal(Original, Current) skip in the meta loop and emitting a ghost
// ModifiedNode (one extra node vs rippled). Threading is the ApplyStateTable's
// job and runs only after that changed-check, mirroring rippled:
//
//	rippled/src/xrpld/app/tx/detail/Offer.h:120-122 (consume() only subtracts)
//	rippled/src/xrpld/ledger/detail/ApplyStateTable.cpp:156 (skip when *cur==*orig)
//
// Existing offer/payment suites did not catch this — the divergence only appears
// when a partial consume rounds back byte-identical, which needs an offer at
// near-maximum magnitude. This test constructs exactly that case.
func TestOffer_PartialConsumeRoundsBack_NoGhostModifiedNode(t *testing.T) {
	env := jtx.NewTestEnv(t)

	gw := jtx.NewAccount("gateway")
	mm := jtx.NewAccount("marketmaker")
	taker := jtx.NewAccount("taker")

	env.FundAmount(gw, uint64(jtx.XRP(100000)))
	env.FundAmount(mm, uint64(jtx.XRP(100000)))
	env.FundAmount(taker, uint64(jtx.XRP(100000)))
	env.Close()

	// Trust lines: both parties hold USD and EUR issued by gw.
	env.Trust(mm, jtx.USD(gw, 2_000_000))
	env.Trust(mm, jtx.EUR(gw, 2_000_000))
	env.Trust(taker, jtx.USD(gw, 2_000_000))
	env.Trust(taker, jtx.EUR(gw, 2_000_000))
	env.Close()

	// mm holds USD to deliver; taker holds EUR to pay with.
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, mm, jtx.USD(gw, 1_000_000)).Build()))
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, taker, jtx.EUR(gw, 1_000_000)).Build()))
	env.Close()

	// Near-maximum 1:1 offer: mm gives USD, receives EUR, both at 9999999999999999e80.
	// The maximum-magnitude amounts make any normal-sized fill round back to the
	// same 16-significant-digit value.
	maxUSD := jtx.IssuedCurrencyFromMantissa(gw, "USD", 9999999999999999, 80)
	maxEUR := jtx.IssuedCurrencyFromMantissa(gw, "EUR", 9999999999999999, 80)

	mmOfferSeq := env.Seq(mm)
	jtx.RequireTxSuccess(t, env.Submit(OfferCreate(mm, maxEUR, maxUSD).Build()))
	env.Close()

	// taker crosses, taking 100 USD for 100 EUR — a tiny slice of the huge offer.
	cross := env.Submit(OfferCreate(taker, jtx.USD(gw, 100), jtx.EUR(gw, 100)).Build())
	jtx.RequireTxSuccess(t, cross)

	// Sanity: the crossing really executed against mm's offer (otherwise the
	// no-ghost-node assertion below would pass vacuously).
	jtx.RequireIOUBalance(t, env, taker, gw, "USD", 100)
	jtx.RequireIOUBalance(t, env, taker, gw, "EUR", 999_900)
	jtx.RequireIOUBalance(t, env, mm, gw, "USD", 999_900)
	jtx.RequireIOUBalance(t, env, mm, gw, "EUR", 100)

	require.NotNil(t, cross.Metadata, "crossing OfferCreate has nil Metadata")

	// mm's offer rounded back byte-identical, so it must NOT appear in the
	// crossing's AffectedNodes at all — not as a ModifiedNode, not as anything.
	mmOfferKey := keylet.Offer(mm.ID, mmOfferSeq).Key
	mmOfferIndex := strings.ToUpper(hex.EncodeToString(mmOfferKey[:]))
	offerMods := 0
	mmOfferTouched := false
	for _, n := range cross.Metadata.AffectedNodes {
		if n.LedgerEntryType == "Offer" && n.NodeType == "ModifiedNode" {
			offerMods++
		}
		if strings.EqualFold(n.LedgerIndex, mmOfferIndex) {
			mmOfferTouched = true
		}
	}

	require.Equal(t, 0, offerMods,
		"partial consume that rounds back byte-identical emitted %d ghost Offer ModifiedNode(s); "+
			"expected 0 (matches rippled ApplyStateTable.cpp:156)", offerMods)
	require.False(t, mmOfferTouched,
		"mm's resting offer (%s) appeared in AffectedNodes; a round-back-identical consume must leave it untouched",
		mmOfferIndex)

	// The offer is still in the ledger with its amounts unchanged.
	resting := GetOffer(env, mm, mmOfferSeq)
	require.NotNil(t, resting, "mm's resting offer should still exist")
	require.True(t, amountsEqual(resting.TakerGets, maxUSD),
		"mm offer TakerGets changed: got %v, want %v", resting.TakerGets, maxUSD)
	require.True(t, amountsEqual(resting.TakerPays, maxEUR),
		"mm offer TakerPays changed: got %v, want %v", resting.TakerPays, maxEUR)
}
