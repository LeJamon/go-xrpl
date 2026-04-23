package p2p

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/ledger/inbound"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReplayDelta_Apply_Integration verifies end-to-end that
// ReplayDelta.Apply derives the same closed ledger that the consensus
// engine would have produced from the same parent + tx set, using a
// real signed Payment transaction. This is the integration counterpart
// to the synthetic Apply tests in
// internal/ledger/inbound/replay_delta_apply_test.go.
//
// Setup:
//  1. Use TestEnv to fund two accounts and close (parent: seq 2).
//  2. Build + sign a Payment alice→bob explicitly, producing a known
//     binary blob + tx hash.
//  3. Apply the Payment manually via tx.Engine against an open child
//     of the parent, install the tx-with-meta blob into the child's
//     tx map, and Close — that is the "successor" we expect Apply to
//     reproduce. (TestEnv's Close path doesn't populate the tx map
//     reliably for this purpose, so we drive the engine directly.)
//  4. Build a wire ReplayDeltaResponse from the successor's header +
//     tx leaves.
//  5. Run GotResponse → Apply against the parent.
//  6. Assert the derived ledger's hash, AccountHash, and TxHash
//     match the successor.
func TestReplayDelta_Apply_Integration(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)
	env.VerifySignatures = true // make sure signed txs round-trip cleanly

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	parent := env.LastClosedLedger()
	require.NotNil(t, parent)

	// Build + sign a real Payment alice → bob using the env's signer.
	aliceSeq := env.Seq(alice)
	pay := payment.NewPayment(alice.Address, bob.Address,
		tx.NewXRPAmount(xrplgoTesting.XRP(123)))
	pay.Sequence = &aliceSeq
	pay.Fee = "10"
	env.SignWith(pay, alice)

	// Serialize the signed tx to get the canonical wire blob. The tx
	// package's Encode/Decode round-trip is what every other path uses
	// (peer relay, RPC submit, replay) so we mirror it here.
	txMap, err := pay.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	txBlob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	pay.SetRawBytes(txBlob)
	txHash, err := tx.ComputeTransactionHash(pay)
	require.NoError(t, err)

	// Build the successor manually: open a child of `parent`, run the
	// engine to apply the Payment, install the tx-with-meta leaf, then
	// Close. This produces a real, hash-stable successor ledger that
	// Apply must be able to reproduce from the same parent + tx blob.
	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	successor, txMetaBlob := buildClosedSuccessor(t, parent, pay, txBlob, txHash, closeTime)

	// Cook a wire response that descends from `parent` and carries
	// the successor's verified header + the single tx leaf.
	hdrBytes, err := header.AddRaw(successor.Header(), false)
	require.NoError(t, err)
	successorHash := successor.Hash()
	resp := &message.ReplayDeltaResponse{
		LedgerHash:   successorHash[:],
		LedgerHeader: hdrBytes,
		Transactions: [][]byte{txMetaBlob},
	}

	rd := inbound.NewReplayDelta(successorHash, 7, parent, nil)
	require.NoError(t, rd.GotResponse(resp), "GotResponse must verify the wire framing")
	require.True(t, rd.IsComplete())

	// Apply must reproduce the successor's hash byte-for-byte.
	derived, err := rd.Apply(tx.EngineConfig{
		BaseFee:                   10,
		ReserveBase:               200_000_000,
		ReserveIncrement:          50_000_000,
		SkipSignatureVerification: false,
	})
	require.NoError(t, err)
	require.NotNil(t, derived)

	assert.Equal(t, successorHash, derived.Hash(),
		"Apply must derive a ledger whose canonical hash matches the verified header")
	assert.Equal(t, successor.Sequence(), derived.Sequence())

	gotState, err := derived.StateMapHash()
	require.NoError(t, err)
	wantState, err := successor.StateMapHash()
	require.NoError(t, err)
	assert.Equal(t, wantState, gotState, "derived AccountHash must match successor's")

	gotTx, err := derived.TxMapHash()
	require.NoError(t, err)
	wantTx, err := successor.TxMapHash()
	require.NoError(t, err)
	assert.Equal(t, wantTx, gotTx, "derived TxHash must match successor's")
}

// TestReplayDelta_Apply_DivergenceFromTef verifies the tef-result
// divergence error: when Apply replays a tx that the engine rejects
// with a tef* result (here: a duplicate tx, triggering tefALREADY),
// it no longer hard-fails — R6.4 brings parity with rippled's
// BuildLedger.cpp:244-247 which DISCARDS the ApplyResult during
// replay. tef/tem/tel are log-and-continue; the state-hash check at
// the end of Apply catches real divergence.
//
// We still build a duplicate-tx scenario because it's a reliable way
// to trigger tef, but the assertion flips: Apply must NOT return
// ErrReplayTxDiverged. Whatever error surfaces (or nil + wrong
// state hash) must originate from a later arbitration stage, not
// the pre-R6.4 hard-fail in the apply switch.
func TestReplayDelta_Apply_TefDuringReplay_IsSilentlySkipped(t *testing.T) {
	env := xrplgoTesting.NewTestEnv(t)
	env.VerifySignatures = true

	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	parent := env.LastClosedLedger()

	aliceSeq := env.Seq(alice)
	pay := payment.NewPayment(alice.Address, bob.Address,
		tx.NewXRPAmount(xrplgoTesting.XRP(123)))
	pay.Sequence = &aliceSeq
	pay.Fee = "10"
	env.SignWith(pay, alice)

	txMap, err := pay.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	txBlob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	pay.SetRawBytes(txBlob)
	txHash, err := tx.ComputeTransactionHash(pay)
	require.NoError(t, err)

	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	successor, txMetaBlob := buildClosedSuccessor(t, parent, pay, txBlob, txHash, closeTime)

	hdrBytes, err := header.AddRaw(successor.Header(), false)
	require.NoError(t, err)
	successorHash := successor.Hash()

	// Wire response carrying ONE tx (legitimate). GotResponse builds
	// r.txs from this. We then poke a second DecodedTx with the same
	// hash + blob into r.txs to simulate a divergent peer feeding us
	// a duplicate — the engine produces tefALREADY on the second
	// apply, which Apply must surface as an explicit divergence error.
	resp := &message.ReplayDeltaResponse{
		LedgerHash:   successorHash[:],
		LedgerHeader: hdrBytes,
		Transactions: [][]byte{txMetaBlob},
	}
	rd := inbound.NewReplayDelta(successorHash, 7, parent, nil)
	require.NoError(t, rd.GotResponse(resp))

	// Force-inject a second copy of the same DecodedTx — equivalent to
	// receiving a delta whose tx set contains the same hash twice.
	// The package-level helper below (in the same package since this
	// test lives in p2p/) reaches into ReplayDelta to mutate r.txs.
	injectDuplicateTx(rd)

	_, err = rd.Apply(tx.EngineConfig{
		BaseFee:                   10,
		ReserveBase:               200_000_000,
		ReserveIncrement:          50_000_000,
		SkipSignatureVerification: false,
	})
	// R6.4: tef* during replay must NOT surface as ErrReplayTxDiverged.
	// Any other error (or success with a later state-hash mismatch)
	// is acceptable — the critical guarantee is that we no longer
	// hard-fail here, which the pre-R6.4 behavior did.
	if err != nil {
		assert.NotErrorIs(t, err, inbound.ErrReplayTxDiverged,
			"tef during replay must no longer produce ErrReplayTxDiverged (rippled parity — BuildLedger.cpp:244-247 discards ApplyResult)")
	}
}

// injectDuplicateTx appends a duplicate of r.txs[0] to r.txs so Apply
// will replay the same hash twice and observe tefALREADY on the
// second pass. Cross-package access to the unexported field is OK
// because this is a test file linked into the inbound package's
// test binary indirectly via the inbound import — here we use the
// public TxsForTest hook below.
func injectDuplicateTx(rd *inbound.ReplayDelta) {
	rd.AppendTxForTest(rd.OrderedTxs()[0])
}

// buildClosedSuccessor opens a child of parent, applies a single
// transaction through the engine (recording the tx-with-meta blob into
// the tx map exactly the way rippled would), and Closes — yielding a
// hash-stable successor ledger and the tx leaf for the wire response.
func buildClosedSuccessor(
	t *testing.T,
	parent *ledger.Ledger,
	txn tx.Transaction,
	txBlob []byte,
	txHash [32]byte,
	closeTime time.Time,
) (*ledger.Ledger, []byte) {
	t.Helper()

	child, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)

	parentCloseTime := uint32(0)
	if !parent.CloseTime().IsZero() {
		const rippleEpochUnix int64 = 946684800
		parentCloseTime = uint32(parent.CloseTime().Unix() - rippleEpochUnix)
	}

	engine := tx.NewEngine(child, tx.EngineConfig{
		BaseFee:                   10,
		ReserveBase:               200_000_000,
		ReserveIncrement:          50_000_000,
		LedgerSequence:            child.Sequence(),
		SkipSignatureVerification: false,
		ParentCloseTime:           parentCloseTime,
		ParentHash:                parent.Hash(),
		OpenLedger:                false,
		ApplyFlags:                tx.TapNONE,
	})
	res := engine.Apply(txn)
	require.True(t, res.Result.IsApplied(),
		"setup tx must apply cleanly (got %s: %s)", res.Result.String(), res.Message)

	// Build the tx-with-meta blob and install it as a leaf — this is
	// what the peer would have serialized and what Apply expects to
	// rebuild the tx map root from.
	leaf, err := tx.CreateTxWithMetaBlob(txBlob, res.Metadata)
	require.NoError(t, err)
	require.NoError(t, child.AddTransactionWithMeta(txHash, leaf))

	require.NoError(t, child.Close(closeTime, 0))
	return child, leaf
}
