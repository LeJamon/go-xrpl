package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/require"
)

func TestFreshNodeSwitchesToNetworkLedgerTwoBeforeFirstValidation(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	t.Cleanup(svc.Stop)
	a := New(Config{LedgerService: svc})
	a.SetOperatingMode(consensus.OpModeConnected)

	local := svc.GetClosedLedger()
	require.NotNil(t, local)
	require.Equal(t, uint32(2), local.Sequence())

	cfg := rcl.DefaultConfig()
	cfg.ManualTick = true
	engine := rcl.NewEngine(a, cfg)
	require.NoError(t, engine.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Stop()) })
	require.NoError(t, engine.StartRound(consensus.RoundID{
		Seq:        local.Sequence() + 1,
		ParentHash: consensus.LedgerID(local.Hash()),
	}, false))

	stateMap, err := local.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := local.TxMapSnapshot()
	require.NoError(t, err)
	networkHeader := local.Header()
	networkHeader.CloseTime = networkHeader.CloseTime.Add(time.Second)
	networkHeader.Hash = header.CalculateHash(networkHeader)
	require.NotEqual(t, local.Hash(), networkHeader.Hash)

	initialCandidate, err := svc.BootstrapLedgerWithState(t.Context(), &networkHeader, stateMap, txMap)
	require.NoError(t, err)
	require.True(t, initialCandidate)
	require.True(t, svc.NeedsInitialSync())
	require.Equal(t, consensus.LedgerID{}, a.GetValidatedLedgerHash())

	a.UpdatePeerLCL(1, consensus.LedgerID(networkHeader.Hash))
	a.UpdatePeerLCL(2, consensus.LedgerID(networkHeader.Hash))
	engine.TimerEntry()

	require.False(t, svc.NeedsInitialSync())
	require.Equal(t, consensus.ModeSwitchedLedger, engine.Mode())
	require.Equal(t, networkHeader.Hash, svc.GetClosedLedger().Hash())
	require.Equal(t, networkHeader.LedgerIndex+1, svc.GetCurrentLedgerIndex())
}

func TestSlowInitialAcquisitionWaitsForCurrentConsensusSwitch(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	t.Cleanup(svc.Stop)
	a := New(Config{LedgerService: svc})
	a.SetOperatingMode(consensus.OpModeConnected)

	local := svc.GetClosedLedger()
	require.NotNil(t, local)
	require.Equal(t, uint32(2), local.Sequence())

	cfg := rcl.DefaultConfig()
	cfg.ManualTick = true
	engine := rcl.NewEngine(a, cfg)
	require.NoError(t, engine.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Stop()) })
	require.NoError(t, engine.StartRound(consensus.RoundID{
		Seq:        local.Sequence() + 1,
		ParentHash: consensus.LedgerID(local.Hash()),
	}, false))
	router := NewRouter(engine, a, make(chan *peermanagement.InboundMessage, 1))

	now := a.Now()
	stale := completedCatchUpAcquisitionWithHeader(t, header.LedgerHeader{
		LedgerIndex:         10_000,
		ParentHash:          [32]byte{0xA0},
		ParentCloseTime:     now.Add(-7 * time.Minute),
		CloseTime:           now.Add(-7*time.Minute + time.Second),
		CloseTimeResolution: 10,
	})
	router.fetchTracker.Track(stale)
	a.UpdatePeerLCL(1, consensus.LedgerID(stale.Hash()))
	a.UpdatePeerLCL(2, consensus.LedgerID(stale.Hash()))
	engine.TimerEntry()
	require.Equal(t, consensus.ModeWrongLedger, engine.Mode())

	router.completeInboundLedger(stale)

	storedStale, err := svc.GetLedgerByHash(stale.Hash())
	require.NoError(t, err)
	require.Equal(t, stale.Hash(), storedStale.Hash())
	require.Equal(t, local.Hash(), svc.GetClosedLedger().Hash())
	require.True(t, svc.NeedsInitialSync())
	require.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())
	require.Equal(t, consensus.ModeWrongLedger, engine.Mode())
	require.False(t, engine.IsProposing())

	fresh := completedCatchUpAcquisitionWithHeader(t, header.LedgerHeader{
		LedgerIndex:         stale.Seq() + 1,
		ParentHash:          stale.Hash(),
		ParentCloseTime:     now.Add(-2 * time.Second),
		CloseTime:           now,
		CloseTimeResolution: 10,
	})
	router.fetchTracker.Track(fresh)
	a.UpdatePeerLCL(1, consensus.LedgerID(fresh.Hash()))
	a.UpdatePeerLCL(2, consensus.LedgerID(fresh.Hash()))
	engine.TimerEntry()
	require.Equal(t, consensus.ModeWrongLedger, engine.Mode())

	router.completeInboundLedger(fresh)

	require.False(t, svc.NeedsInitialSync())
	require.Equal(t, fresh.Hash(), svc.GetClosedLedger().Hash())
	require.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
	require.Equal(t, consensus.ModeSwitchedLedger, engine.Mode())
	require.False(t, engine.IsProposing())
	require.Equal(t, fresh.Seq()+1, svc.GetCurrentLedgerIndex())

	svc.SetValidatedLedger(fresh.Seq(), fresh.Hash())
	router.maintenanceTick()

	historicalStale, err := svc.AdoptedLedgerBySequence(stale.Seq())
	require.NoError(t, err)
	require.Equal(t, stale.Hash(), historicalStale.Hash())
	require.Equal(t, fresh.Hash(), svc.GetClosedLedger().Hash())
}

func TestAcquiredValidatedTipSurvivesRecoveryTimerTick(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService
	stale := svc.GetClosedLedger()
	require.NotNil(t, stale)
	cfg := rcl.DefaultConfig()
	cfg.ManualTick = true
	engine := rcl.NewEngine(a, cfg)
	require.NoError(t, engine.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Stop()) })

	stateMap, err := stale.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := stale.TxMapSnapshot()
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	targetHeader := stale.Header()
	targetHeader.LedgerIndex = stale.Sequence() + 5
	targetHeader.ParentHash = [32]byte{0xA4}
	targetHeader.AccountHash = stateRoot
	targetHeader.TxHash = txRoot
	targetHeader.ParentCloseTime = time.Now().Add(-4 * time.Second)
	targetHeader.CloseTime = time.Now().Add(-2 * time.Second)
	targetHeader.CloseTimeResolution = 10
	targetHeader.Validated = false
	targetHeader.Hash = header.CalculateHash(targetHeader)

	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &targetHeader, stateMap, txMap))
	validation := &consensus.Validation{
		LedgerSeq: targetHeader.LedgerIndex,
		LedgerID:  consensus.LedgerID(targetHeader.Hash),
		SignTime:  a.Now(),
		SeenTime:  a.Now(),
		Full:      true,
	}
	require.NoError(t, a.SignValidation(validation))
	require.NoError(t, engine.OnValidation(validation, 0))
	require.Equal(t, stale.Hash(), svc.GetValidatedLedger().Hash())
	require.Equal(t, stale.Hash(), svc.GetClosedLedger().Hash())

	require.NoError(t, engine.StartRound(consensus.RoundID{
		Seq:        stale.Sequence() + 1,
		ParentHash: consensus.LedgerID(stale.Hash()),
	}, false))

	require.NoError(t, engine.OnLedger(consensus.LedgerID(targetHeader.Hash), nil))
	require.Equal(t, targetHeader.Hash, svc.GetClosedLedger().Hash())
	require.Equal(t, targetHeader.LedgerIndex+1, svc.GetCurrentLedgerIndex())
	require.Equal(t, consensus.ModeSwitchedLedger, engine.Mode())

	engine.TimerEntry()

	require.Equal(t, targetHeader.Hash, svc.GetClosedLedger().Hash())
	require.NotEqual(t, consensus.ModeWrongLedger, engine.Mode())
}

func TestAcquiredValidatedTipSurvivesMovingRecoveryTarget(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService
	stale := svc.GetClosedLedger()
	require.NotNil(t, stale)
	cfg := rcl.DefaultConfig()
	cfg.ManualTick = true
	engine := rcl.NewEngine(a, cfg)
	require.NoError(t, engine.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Stop()) })

	stateMap, err := stale.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := stale.TxMapSnapshot()
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	targetHeader := stale.Header()
	targetHeader.LedgerIndex = stale.Sequence() + 5
	targetHeader.ParentHash = [32]byte{0xB4}
	targetHeader.AccountHash = stateRoot
	targetHeader.TxHash = txRoot
	targetHeader.ParentCloseTime = time.Now().Add(-4 * time.Second)
	targetHeader.CloseTime = time.Now().Add(-2 * time.Second)
	targetHeader.CloseTimeResolution = 10
	targetHeader.Validated = false
	targetHeader.Hash = header.CalculateHash(targetHeader)

	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &targetHeader, stateMap, txMap))
	validation := &consensus.Validation{
		LedgerSeq: targetHeader.LedgerIndex,
		LedgerID:  consensus.LedgerID(targetHeader.Hash),
		SignTime:  a.Now(),
		SeenTime:  a.Now(),
		Full:      true,
	}
	require.NoError(t, a.SignValidation(validation))
	require.NoError(t, engine.OnValidation(validation, 0))
	require.Equal(t, stale.Hash(), svc.GetValidatedLedger().Hash())
	require.NoError(t, engine.StartRound(consensus.RoundID{
		Seq:        stale.Sequence() + 1,
		ParentHash: consensus.LedgerID(stale.Hash()),
	}, false))

	newerPreferred := [32]byte{0xB8}
	router := NewRouter(engine, a, make(chan *peermanagement.InboundMessage, 1))
	router.consensusRecovery = consensusRecovery{
		targetHash: newerPreferred,
		stepHash:   newerPreferred,
	}
	router.completeStoredConsensusRecovery(
		targetHeader.LedgerIndex,
		targetHeader.Hash,
		targetHeader.ParentHash,
		false,
	)

	require.Equal(t, targetHeader.Hash, svc.GetClosedLedger().Hash())
	require.Equal(t, targetHeader.LedgerIndex+1, svc.GetCurrentLedgerIndex())
	require.Equal(t, consensus.ModeSwitchedLedger, engine.Mode())

	engine.TimerEntry()

	require.Equal(t, targetHeader.Hash, svc.GetClosedLedger().Hash())
	require.NotEqual(t, consensus.ModeWrongLedger, engine.Mode())
}
