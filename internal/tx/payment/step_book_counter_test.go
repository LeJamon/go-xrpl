package payment

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// insertBookOffer writes a single offer SLE into the mock view and returns its key.
// The offer sells XRP (TakerGets) for USD (TakerPays); the Out side is XRP so the
// funding check reads the owner's XRP balance/reserve.
func insertBookOffer(t *testing.T, view *paymentMockLedgerView, owner [20]byte, gwStr string, seq, expiration uint32, bookDir [32]byte) [32]byte {
	t.Helper()
	offer := &state.LedgerOffer{
		Account:       state.EncodeAccountIDSafe(owner),
		Sequence:      seq,
		TakerPays:     tx.NewIssuedAmountFromFloat64(100, "USD", gwStr),
		TakerGets:     tx.NewXRPAmount(100_000_000),
		BookDirectory: bookDir,
		Expiration:    expiration,
	}
	data, err := state.SerializeLedgerOffer(offer)
	require.NoError(t, err)
	key := keylet.Offer(owner, seq).Key
	view.data[key] = data
	return key
}

// TestBookStep_OffersUsed_CountsEveryWalkedOffer proves the unified step counter
// counts every CLOB offer the book walk advances past — expired offers removed
// inside getNextOfferSkipVisited and unfunded offers removed in the consumption
// loop — exactly once, mirroring rippled's OfferStream::step where counter_.step()
// runs before the expiry/funding/removal checks.
//
// Before the fix, expired/missing/domain-removed offers were skipped without
// being counted (under-count), so offersUsed reported only the unfunded offers.
//
// Reference: rippled OfferStream.cpp step() counter_.step() placement (line 245),
// before the expiry (256), unfunded (315) and removal checks.
func TestBookStep_OffersUsed_CountsEveryWalkedOffer(t *testing.T) {
	var gw, owner [20]byte
	copy(gw[:], []byte("gateway123456789012"))
	copy(owner[:], []byte("owner1234567890123456")[:20])
	gwStr := state.EncodeAccountIDSafe(gw)

	view := newPaymentMockLedgerView()
	// Owner holds less XRP than the base reserve, so every offer that reaches the
	// funding check is unfunded on the XRP (TakerGets) side.
	view.createAccount(owner, 5_000_000, 0)

	inIssue := Issue{Currency: "USD", Issuer: gw}
	outIssue := Issue{Currency: "XRP"}
	var strandSrc, strandDst [20]byte
	copy(strandSrc[:], []byte("src12345678901234567"))
	copy(strandDst[:], []byte("dst12345678901234567"))

	step := NewBookStep(inIssue, outIssue, strandSrc, strandDst, nil, false)
	step.parentCloseTime = 1000

	// Single book directory at one quality level holding all offers.
	dirKey := step.bookBaseKey()
	binary.BigEndian.PutUint64(dirKey[24:], 0x5500000000000000)

	const expiredCount = 3
	const unfundedCount = 3
	var indexes [][32]byte
	seq := uint32(1)
	// Expired offers: Expiration <= parentCloseTime, removed during the walk.
	for range expiredCount {
		indexes = append(indexes, insertBookOffer(t, view, owner, gwStr, seq, step.parentCloseTime-1, dirKey))
		seq++
	}
	// Unfunded offers: no expiration, removed by the funding check in the loop.
	for range unfundedCount {
		indexes = append(indexes, insertBookOffer(t, view, owner, gwStr, seq, 0, dirKey))
		seq++
	}

	dirNode := &state.DirectoryNode{
		RootIndex:         dirKey,
		Indexes:           indexes,
		TakerPaysCurrency: keylet.CurrencyBytes("USD"),
		TakerPaysIssuer:   gw,
	}
	dirData, err := state.SerializeDirectoryNode(dirNode, true)
	require.NoError(t, err)
	view.data[dirKey] = dirData

	sandbox := NewPaymentSandbox(view)
	sandbox.SetTransactionContext([32]byte{}, 1)

	// Request a large output. No offer is funded, so nothing is consumed; the walk
	// removes every offer and exhausts the book.
	out := NewXRPEitherAmount(1_000_000_000)
	ofrsToRm := make(map[[32]byte]bool)
	gotIn, gotOut := step.Rev(sandbox, sandbox, ofrsToRm, out)

	require.True(t, gotOut.IsZero(), "no funded liquidity, so output must be zero")
	require.True(t, gotIn.IsZero(), "no funded liquidity, so input must be zero")
	require.Equal(t, uint32(expiredCount+unfundedCount), step.OffersUsed(),
		"every offer the book walk advances past (expired + unfunded) must be counted exactly once")
}
