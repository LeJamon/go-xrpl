package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCatchupHandoffRouter(t *testing.T) (*Router, *Adaptor, *mockEngine) {
	t.Helper()
	r, a, _, _ := makeRouter(t)
	engine := &mockEngine{}
	r.engine = engine
	a.SetOperatingMode(consensus.OpModeConnected)
	return r, a, engine
}

func TestFarCatchupCompletionRemainsStoreOnly(t *testing.T) {
	r, a, engine := newCatchupHandoffRouter(t)
	r.recordCatchupTarget(105, [32]byte{0xA5}, 7)

	r.completeStoredConsensusRecovery(100, [32]byte{0xA0}, [32]byte{0x9F}, false)

	assert.Empty(t, engine.getLedgers())
	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())
	assert.Equal(t, uint32(0), r.lastHandoffSeq)
}

func TestCatchupCompletionAtFrontierNotifiesOnce(t *testing.T) {
	r, a, engine := newCatchupHandoffRouter(t)
	hash := [32]byte{0xB0}
	r.recordCatchupTarget(101, [32]byte{0xB1}, 7)

	r.completeStoredConsensusRecovery(100, hash, [32]byte{0xAF}, false)
	r.completeStoredConsensusRecovery(100, hash, [32]byte{0xAF}, false)

	require.Equal(t, []consensus.LedgerID{consensus.LedgerID(hash)}, engine.getLedgers())
	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
	assert.Equal(t, uint32(100), r.lastHandoffSeq)
}

func TestExactConsensusRecoveryBypassesFrontierAndHandoffGuard(t *testing.T) {
	r, a, engine := newCatchupHandoffRouter(t)
	target := [32]byte{0xC0}
	r.recordCatchupTarget(300, [32]byte{0xC3}, 7)
	r.lastHandoffSeq = 200
	r.consensusRecovery = consensusRecovery{targetHash: target, stepHash: target}

	r.completeStoredConsensusRecovery(100, target, [32]byte{0xBF}, false)

	require.Equal(t, []consensus.LedgerID{consensus.LedgerID(target)}, engine.getLedgers())
	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
	assert.Equal(t, uint32(200), r.lastHandoffSeq)
	assert.Equal(t, consensusRecovery{anchorHash: target, anchorSeq: 100}, r.consensusRecovery)
}

func TestOlderCatchupCompletionDoesNotRegressConsensus(t *testing.T) {
	r, _, engine := newCatchupHandoffRouter(t)
	newer := [32]byte{0xD0}
	older := [32]byte{0xCF}
	r.recordCatchupTarget(100, newer, 7)

	r.completeStoredConsensusRecovery(100, newer, [32]byte{0xCF}, false)
	r.completeStoredConsensusRecovery(99, older, [32]byte{0xCE}, false)

	require.Equal(t, []consensus.LedgerID{consensus.LedgerID(newer)}, engine.getLedgers())
	assert.Equal(t, uint32(100), r.lastHandoffSeq)
}

func TestMovingCatchupFrontierEventuallyNotifies(t *testing.T) {
	r, _, engine := newCatchupHandoffRouter(t)
	r.recordCatchupTarget(105, [32]byte{0xE5}, 7)

	r.completeStoredConsensusRecovery(100, [32]byte{0xE0}, [32]byte{0xDF}, false)
	assert.Empty(t, engine.getLedgers())

	hash := [32]byte{0xE4}
	r.completeStoredConsensusRecovery(104, hash, [32]byte{0xE3}, false)

	require.Equal(t, []consensus.LedgerID{consensus.LedgerID(hash)}, engine.getLedgers())
	assert.Equal(t, uint32(104), r.lastHandoffSeq)
}

func TestInitialBootstrapNotifiesBehindFrontier(t *testing.T) {
	r, a, engine := newCatchupHandoffRouter(t)
	engine.switchResult = consensus.LedgerSwitchAccepted
	hash := [32]byte{0xF0}
	r.recordCatchupTarget(200, [32]byte{0xF2}, 7)

	r.completeStoredConsensusRecovery(100, hash, [32]byte{0xEF}, true)

	require.Equal(t, []consensus.LedgerID{consensus.LedgerID(hash)}, engine.getLedgers())
	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
	assert.Equal(t, uint32(100), r.lastHandoffSeq)
}

func TestRejectedInitialCandidateRemainsStoreOnlyAnchor(t *testing.T) {
	r, a, engine := newCatchupHandoffRouter(t)
	engine.switchResult = consensus.LedgerSwitchRejected
	hash := [32]byte{0xF1}
	r.consensusRecovery = consensusRecovery{targetHash: hash, stepHash: hash}

	switched := r.completeStoredConsensusRecovery(100, hash, [32]byte{0xF0}, true)

	assert.False(t, switched)
	require.Equal(t, []consensus.LedgerID{consensus.LedgerID(hash)}, engine.getLedgers())
	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())
	assert.Equal(t, uint32(0), r.lastHandoffSeq)
	assert.Equal(t, consensusRecovery{anchorHash: hash, anchorSeq: 100}, r.consensusRecovery)
}

func TestBusyInitialCandidateRetainsTargetForRetry(t *testing.T) {
	r, a, engine := newCatchupHandoffRouter(t)
	engine.switchResult = consensus.LedgerSwitchBusy
	hash := [32]byte{0xF2}
	r.consensusRecovery = consensusRecovery{targetHash: hash, stepHash: hash}

	switched := r.completeStoredConsensusRecovery(100, hash, [32]byte{0xF1}, true)

	assert.False(t, switched)
	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())
	assert.Equal(t, uint32(0), r.lastHandoffSeq)
	assert.Equal(t, consensusRecovery{targetHash: hash}, r.consensusRecovery)

	engine.switchResult = consensus.LedgerSwitchAccepted
	switched = r.completeStoredConsensusRecovery(100, hash, [32]byte{0xF1}, true)

	assert.True(t, switched)
	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
	assert.Equal(t, uint32(100), r.lastHandoffSeq)
	assert.Equal(t, consensusRecovery{anchorHash: hash, anchorSeq: 100}, r.consensusRecovery)
}
