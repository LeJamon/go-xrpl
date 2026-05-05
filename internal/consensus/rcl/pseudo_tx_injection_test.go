package rcl

import (
	"bytes"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloseLedger_InjectsFlagLedgerPseudoTxs pins the producer-side
// half of the rippled flag-ledger pseudo-tx contract
// (RCLConsensus.cpp:351-381). When prevLedger is a flag ledger and we
// are in ModeProposing, closeLedger MUST call
// Adaptor.GenerateFlagLedgerPseudoTxs and inject the returned blobs
// into the proposal tx set BEFORE BuildTxSet runs, so the tx-set hash
// rippled and goXRPL compute for the same round agrees on the
// presence of the fee/amendment vote pseudo-txs.
//
// Issue #367 delivers this seam. The actual vote-tally producers
// ship in #368/#369/#370.
func TestCloseLedger_InjectsFlagLedgerPseudoTxs(t *testing.T) {
	flagBlob := []byte{0xFE, 0xE0, 0x01, 0x02, 0x03}
	gotTxs := closeWithPseudoTxs(t, 256, [][]byte{flagBlob}, nil)
	require.True(t, containsBlob(gotTxs, flagBlob),
		"flag-ledger pseudo-tx blob missing from tx set; got %d txs", len(gotTxs))
}

// TestCloseLedger_InjectsNegativeUNLPseudoTx pins the voting-ledger
// branch (RCLConsensus.cpp:368-380). When prevLedger is a voting
// ledger AND the NegativeUNL feature is enabled, closeLedger MUST
// inject the NegUNL pseudo-tx returned by GenerateNegativeUNLPseudoTx.
func TestCloseLedger_InjectsNegativeUNLPseudoTx(t *testing.T) {
	negUNLBlob := []byte{0x4E, 0x55, 0x4C, 0x01}
	gotTxs := closeWithPseudoTxs(t, 255, nil, negUNLBlob)
	require.True(t, containsBlob(gotTxs, negUNLBlob),
		"negUNL pseudo-tx missing from tx set; got %d txs", len(gotTxs))
}

// TestCloseLedger_NoInjectionOnRegularLedger verifies the negative
// case: on a non-flag, non-voting prevLedger, neither producer is
// invoked.
func TestCloseLedger_NoInjectionOnRegularLedger(t *testing.T) {
	flagBlob := []byte{0xFF, 0xFF}
	negUNLBlob := []byte{0xEE, 0xEE}
	gotTxs := closeWithPseudoTxs(t, 100, [][]byte{flagBlob}, negUNLBlob)

	assert.False(t, containsBlob(gotTxs, flagBlob),
		"flag pseudo-tx must NOT be injected on a regular ledger")
	assert.False(t, containsBlob(gotTxs, negUNLBlob),
		"negUNL pseudo-tx must NOT be injected on a regular ledger")
}

// TestCloseLedger_NoInjectionWhenNotProposing pins the gate matching
// rippled's "proposing && !wrongLCL" check at RCLConsensus.cpp:352.
// In ModeObserving, neither producer is called.
func TestCloseLedger_NoInjectionWhenNotProposing(t *testing.T) {
	flagBlob := []byte{0xAB, 0xCD}
	gotTxs := closeAtModeWith(t, consensus.ModeObserving, 256, [][]byte{flagBlob}, nil)
	assert.False(t, containsBlob(gotTxs, flagBlob),
		"non-proposing mode must NOT inject pseudo-txs")
}

// TestCloseLedger_NegUNLGatedOnFeature pins the rippled gate at
// RCLConsensus.cpp:368-370: NegativeUNL pseudo-tx is only injected
// when the featureNegativeUNL amendment is enabled.
func TestCloseLedger_NegUNLGatedOnFeature(t *testing.T) {
	prev := &mockLedger{id: consensus.LedgerID{0x55, 0xAA}, seq: 255}
	a := newMockAdaptor()
	a.lastLCL = prev
	a.ledgers[prev.ID()] = prev
	a.disabledFeatures = map[string]bool{"NegativeUNL": true}

	negUNLBlob := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	a.negativeUNLPseudoTx = negUNLBlob

	engine := NewEngine(a, DefaultConfig())
	round := consensus.RoundID{Seq: prev.Seq() + 1, ParentHash: prev.ID()}
	require.NoError(t, engine.StartRound(round, true))

	engine.mu.Lock()
	engine.prevLedger = prev
	engine.setMode(consensus.ModeProposing)
	engine.setPhase(consensus.PhaseOpen)
	engine.mu.Unlock()

	engine.closeLedger()
	require.NotNil(t, engine.ourTxSet)
	assert.False(t, containsBlob(engine.ourTxSet.Txs(), negUNLBlob),
		"NegUNL pseudo-tx must NOT be injected when featureNegativeUNL is disabled")
}

func closeWithPseudoTxs(t *testing.T, prevSeq uint32, flagBlobs [][]byte, negUNLBlob []byte) [][]byte {
	t.Helper()
	return closeAtModeWith(t, consensus.ModeProposing, prevSeq, flagBlobs, negUNLBlob)
}

func closeAtModeWith(t *testing.T, mode consensus.Mode, prevSeq uint32, flagBlobs [][]byte, negUNLBlob []byte) [][]byte {
	t.Helper()

	prev := &mockLedger{id: consensus.LedgerID{byte(prevSeq), 0xAA}, seq: prevSeq}
	a := newMockAdaptor()
	a.lastLCL = prev
	a.ledgers[prev.ID()] = prev
	a.flagLedgerPseudoTxs = flagBlobs
	a.negativeUNLPseudoTx = negUNLBlob

	engine := NewEngine(a, DefaultConfig())
	round := consensus.RoundID{Seq: prev.Seq() + 1, ParentHash: prev.ID()}
	require.NoError(t, engine.StartRound(round, mode == consensus.ModeProposing))

	engine.mu.Lock()
	engine.prevLedger = prev
	engine.setMode(mode)
	engine.setPhase(consensus.PhaseOpen)
	engine.mu.Unlock()

	engine.closeLedger()

	require.NotNil(t, engine.ourTxSet, "closeLedger must build our tx set")
	return engine.ourTxSet.Txs()
}

func containsBlob(blobs [][]byte, want []byte) bool {
	for _, b := range blobs {
		if bytes.Equal(b, want) {
			return true
		}
	}
	return false
}
