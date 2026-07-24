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

	bootstrapped, err := svc.BootstrapLedgerWithState(t.Context(), &networkHeader, stateMap, txMap)
	require.NoError(t, err)
	require.True(t, bootstrapped)
	require.Equal(t, consensus.LedgerID{}, a.GetValidatedLedgerHash())

	engine.TimerEntry()

	require.Equal(t, consensus.ModeSwitchedLedger, engine.Mode())
	state := engine.State()
	require.NotNil(t, state)
	require.Equal(t, uint32(3), state.Round.Seq)
	require.Equal(t, networkHeader.Hash, state.Round.ParentHash)
}

func TestAcquiredValidatedTipSurvivesRecoveryTimerTick(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService
	stale := svc.GetClosedLedger()
	require.NotNil(t, stale)

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
	targetHeader.Hash = [32]byte{0xA5}
	targetHeader.AccountHash = stateRoot
	targetHeader.TxHash = txRoot
	targetHeader.ParentCloseTime = time.Now().Add(-4 * time.Second)
	targetHeader.CloseTime = time.Now().Add(-2 * time.Second)
	targetHeader.CloseTimeResolution = 10
	targetHeader.Validated = false

	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &targetHeader, stateMap, txMap))
	svc.SetValidatedLedger(targetHeader.LedgerIndex, targetHeader.Hash)
	require.Equal(t, targetHeader.Hash, svc.GetValidatedLedger().Hash())
	require.Equal(t, stale.Hash(), svc.GetClosedLedger().Hash())

	cfg := rcl.DefaultConfig()
	cfg.ManualTick = true
	engine := rcl.NewEngine(a, cfg)
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
	state := engine.State()
	require.NotNil(t, state)
	require.Equal(t, targetHeader.LedgerIndex+1, state.Round.Seq)
	require.Equal(t, targetHeader.Hash, state.Round.ParentHash)
}

func TestAcquiredValidatedTipSurvivesMovingRecoveryTarget(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService
	stale := svc.GetClosedLedger()
	require.NotNil(t, stale)

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
	targetHeader.Hash = [32]byte{0xB5}
	targetHeader.AccountHash = stateRoot
	targetHeader.TxHash = txRoot
	targetHeader.ParentCloseTime = time.Now().Add(-4 * time.Second)
	targetHeader.CloseTime = time.Now().Add(-2 * time.Second)
	targetHeader.CloseTimeResolution = 10
	targetHeader.Validated = false

	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &targetHeader, stateMap, txMap))
	svc.SetValidatedLedger(targetHeader.LedgerIndex, targetHeader.Hash)
	require.Equal(t, targetHeader.Hash, svc.GetValidatedLedger().Hash())

	cfg := rcl.DefaultConfig()
	cfg.ManualTick = true
	engine := rcl.NewEngine(a, cfg)
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
	state := engine.State()
	require.NotNil(t, state)
	require.Equal(t, targetHeader.LedgerIndex+1, state.Round.Seq)
	require.Equal(t, targetHeader.Hash, state.Round.ParentHash)
}
