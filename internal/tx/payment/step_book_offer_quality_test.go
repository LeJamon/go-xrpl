package payment

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// TestBookStep_CrossUsesDirectoryTierQuality proves an offer is crossed at the
// quality baked into its BookDirectory key at placement time, not the quality
// recomputed from its current (partially-filled) TakerPays/TakerGets.
//
// rippled fixes an offer's quality when it is placed and never recomputes it for
// the lifetime of the offer; partial fills use the original quality (Offer.h
// quality() business rule). Recomputing from the drifted remainder makes the
// crossing fill consume a slightly different amount than rippled — the divergence
// behind issue #1016 (~1 ULP at mainnet ledger 99226374). This test exaggerates
// the drift so the operand choice is unambiguous: with the directory tier the
// taker pays 50 USD for 25 XRP; with the recomputed amounts it would pay ~45.45.
func TestBookStep_CrossUsesDirectoryTierQuality(t *testing.T) {
	var gw, owner [20]byte
	copy(gw[:], []byte("gateway123456789012"))
	copy(owner[:], []byte("owner1234567890123456")[:20])
	gwStr := state.EncodeAccountIDSafe(gw)

	view := newPaymentMockLedgerView()
	// Owner is amply funded so the offer's full XRP (TakerGets) side is available
	// and never capped by owner funds.
	view.createAccount(owner, 10_000_000_000, 1)
	view.createAccount(gw, 10_000_000_000, 0)

	// Book: taker pays USD (In), receives XRP (Out). The resting offer sells XRP
	// for USD, funded by the owner's XRP balance.
	inIssue := Issue{Currency: "USD", Issuer: gw}
	outIssue := Issue{Currency: "XRP"}
	var strandSrc, strandDst [20]byte
	copy(strandSrc[:], []byte("src12345678901234567"))
	copy(strandDst[:], []byte("dst12345678901234567"))

	step := NewBookStep(inIssue, outIssue, strandSrc, strandDst, nil, false)
	step.parentCloseTime = 1000
	// Both reduced-offers amendments are active at mainnet ledger 99226374, so the
	// strict ceil paths are the live ones.
	step.fixReducedOffersV1 = true
	step.fixReducedOffersV2 = true

	// Placement quality is 100 USD : 50 XRP. The directory key encodes this tier.
	ofrIn := NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(100, "USD", gwStr))
	placementOut := NewXRPEitherAmount(50_000_000)
	dirQuality := QualityFromAmounts(ofrIn, placementOut)

	dirKey := step.bookBaseKey()
	binary.BigEndian.PutUint64(dirKey[24:], dirQuality.Value)

	// The offer's STORED amounts have drifted to 100 USD : 55 XRP — a better tier
	// for the taker than its placement quality, simulating a partially-filled
	// offer whose remainder no longer reproduces the directory quality exactly.
	offer := &state.LedgerOffer{
		Account:       state.EncodeAccountIDSafe(owner),
		Sequence:      1,
		TakerPays:     tx.NewIssuedAmountFromFloat64(100, "USD", gwStr),
		TakerGets:     tx.NewXRPAmount(55_000_000),
		BookDirectory: dirKey,
	}
	ofrOut := NewXRPEitherAmount(offer.TakerGets.Drops())
	amountsQuality := QualityFromAmounts(ofrIn, ofrOut)

	// Sanity: the stored-amounts quality really does differ from the directory
	// tier, otherwise the test would pass vacuously.
	require.NotEqual(t, dirQuality.Value, amountsQuality.Value,
		"drift setup invalid: stored-amounts quality must differ from directory tier")

	offerData, err := state.SerializeLedgerOffer(offer)
	require.NoError(t, err)
	offerKey := keylet.Offer(owner, 1).Key
	view.data[offerKey] = offerData

	dirNode := &state.DirectoryNode{
		RootIndex:         dirKey,
		Indexes:           [][32]byte{offerKey},
		TakerPaysCurrency: keylet.CurrencyBytes("USD"),
		TakerPaysIssuer:   gw,
	}
	dirData, err := state.SerializeDirectoryNode(dirNode, true)
	require.NoError(t, err)
	view.data[dirKey] = dirData

	sandbox := NewPaymentSandbox(view)
	sandbox.SetTransactionContext([32]byte{}, 1)

	// Request 25 XRP out — a partial take of the 55-XRP offer, driving the
	// limitStepOut / CeilOutStrict path that recomputes the input from the offer
	// quality.
	outReq := NewXRPEitherAmount(25_000_000)
	gotIn, gotOut := step.Rev(sandbox, sandbox, make(map[[32]byte]bool), outReq)

	require.Equal(t, 0, gotOut.Compare(outReq), "partial take should deliver the requested 25 XRP")

	// The input the taker must pay, computed independently from each candidate
	// quality. Only the operand differs; the strict-rounding math is identical.
	dirExpectedIn, _ := dirQuality.CeilOutStrict(ofrIn, ofrOut, outReq, true)
	amountsExpectedIn, _ := amountsQuality.CeilOutStrict(ofrIn, ofrOut, outReq, true)
	require.NotEqual(t, 0, dirExpectedIn.Compare(amountsExpectedIn),
		"candidate inputs must differ for the assertion to be meaningful")

	require.Equal(t, 0, gotIn.Compare(dirExpectedIn),
		"cross must price the fill at the directory-tier quality")
	require.NotEqual(t, 0, gotIn.Compare(amountsExpectedIn),
		"cross must NOT price the fill at the recomputed stored-amounts quality")
}
