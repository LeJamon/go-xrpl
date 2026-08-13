package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCatchupHandoffRouter(t *testing.T) (*Router, *Adaptor, *mockEngine) {
	t.Helper()
	r, a, _, _ := makeRouter(t)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
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
	svc := adg_newNonStandaloneService(t)
	t.Cleanup(svc.Stop)
	a := New(Config{LedgerService: svc})
	a.SetOperatingMode(consensus.OpModeConnected)
	engine := &mockEngine{}
	engine.switchHook = func(id consensus.LedgerID) {
		selected, err := a.GetLedger(id)
		require.NoError(t, err)
		require.NoError(t, a.OnLedgerSwitched(selected))
	}
	engine.switchResult = consensus.LedgerSwitchBusy
	r := newTestRouter(engine, a, nil)

	local := svc.GetClosedLedger()
	require.NotNil(t, local)
	stateMap, err := local.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := local.TxMapSnapshot()
	require.NoError(t, err)
	hdr := local.Header()
	hdr.LedgerIndex = local.Sequence() + 5
	hdr.ParentHash = [32]byte{0xF1}
	hdr.Hash = header.CalculateHash(hdr)
	initialCandidate, err := svc.BootstrapLedgerWithState(t.Context(), &hdr, stateMap, txMap)
	require.NoError(t, err)
	require.True(t, initialCandidate)

	switched := r.completeStoredConsensusRecovery(
		hdr.LedgerIndex,
		hdr.Hash,
		hdr.ParentHash,
		initialCandidate,
	)

	assert.False(t, switched)
	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())
	assert.Equal(t, uint32(0), r.lastHandoffSeq)
	assert.Equal(t, consensusRecovery{targetHash: hdr.Hash}, r.consensusRecovery)

	engine.switchResult = consensus.LedgerSwitchAccepted
	r.maintenanceTick()

	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
	assert.Equal(t, hdr.LedgerIndex, r.lastHandoffSeq)
	assert.Equal(t, consensusRecovery{anchorHash: hdr.Hash, anchorSeq: hdr.LedgerIndex}, r.consensusRecovery)
	assert.Equal(t, []consensus.LedgerID{
		consensus.LedgerID(hdr.Hash),
		consensus.LedgerID(hdr.Hash),
	}, engine.getLedgers())

	r.historyMu.Lock()
	assert.Equal(t, catchupTarget{
		seq:  hdr.LedgerIndex - 1,
		hash: hdr.ParentHash,
	}, r.history)
	assert.Zero(t, r.historyFloor)
	r.historyMu.Unlock()
}

func TestStoredConsensusCandidateRetriesUntilEngineAccepts(t *testing.T) {
	tests := []struct {
		name        string
		firstResult consensus.LedgerSwitchResult
	}{
		{name: "busy", firstResult: consensus.LedgerSwitchBusy},
		{name: "quorum unavailable", firstResult: consensus.LedgerSwitchIrrelevant},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := adg_newNonStandaloneService(t)
			t.Cleanup(svc.Stop)
			local := svc.GetClosedLedger()
			require.NotNil(t, local)
			require.NoError(t, svc.SwitchToPreferredLedger(local))

			a := New(Config{LedgerService: svc})
			a.SetOperatingMode(consensus.OpModeConnected)
			engine := &mockEngine{switchResult: tt.firstResult}
			engine.switchHook = func(id consensus.LedgerID) {
				selected, err := a.GetLedger(id)
				require.NoError(t, err)
				require.NoError(t, a.OnLedgerSwitched(selected))
			}
			r := newTestRouter(engine, a, nil)
			_, started := r.startLifecycle(t.Context())
			require.True(t, started)
			t.Cleanup(r.stopLifecycle)

			stateMap, err := local.StateMapSnapshot()
			require.NoError(t, err)
			txMap, err := local.TxMapSnapshot()
			require.NoError(t, err)
			hdr := local.Header()
			hdr.LedgerIndex = local.Sequence() + 5
			hdr.ParentHash = [32]byte{0xF2}
			hdr.Hash = header.CalculateHash(hdr)
			initialCandidate, err := svc.BootstrapLedgerWithState(t.Context(), &hdr, stateMap, txMap)
			require.NoError(t, err)
			require.False(t, initialCandidate)

			r.onLedgerFullyValidated(hdr.LedgerIndex, hdr.Hash)
			require.Eventually(t, func() bool {
				if len(engine.getLedgers()) != 1 {
					return false
				}
				r.acquisitionMu.Lock()
				defer r.acquisitionMu.Unlock()
				return r.consensusRecovery.targetHash == hdr.Hash
			}, time.Second, time.Millisecond)

			assert.Equal(t, local.Hash(), svc.GetClosedLedger().Hash())
			assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())
			assert.Equal(t, uint32(0), r.lastHandoffSeq)
			assert.Equal(t, hdr.Hash, r.consensusRecovery.targetHash)

			engine.switchResult = consensus.LedgerSwitchAccepted
			r.maintenanceTick()

			assert.Equal(t, hdr.Hash, svc.GetClosedLedger().Hash())
			assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
			assert.Equal(t, hdr.LedgerIndex, r.lastHandoffSeq)
			assert.Equal(t, consensusRecovery{
				anchorHash: hdr.Hash,
				anchorSeq:  hdr.LedgerIndex,
			}, r.consensusRecovery)
			assert.Equal(t, []consensus.LedgerID{
				consensus.LedgerID(hdr.Hash),
				consensus.LedgerID(hdr.Hash),
			}, engine.getLedgers())
		})
	}
}
