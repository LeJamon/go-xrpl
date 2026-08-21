package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdg_GetLedger(t *testing.T) {
	a := newTestAdaptor(t)

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)
	require.NotNil(t, lcl)

	got, err := a.GetLedger(lcl.ID())
	require.NoError(t, err)
	assert.Equal(t, lcl.ID(), got.ID())

	_, err = a.GetLedger(consensus.LedgerID{0xDE, 0xAD})
	assert.ErrorIs(t, err, errLedgerNotFound)
}

func TestAdg_GetLedgerBySeq(t *testing.T) {
	a := newTestAdaptor(t)

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)

	got, err := a.GetLedgerBySeq(lcl.Seq())
	require.NoError(t, err)
	assert.Equal(t, lcl.Seq(), got.Seq())

	_, err = a.GetLedgerBySeq(99999)
	assert.ErrorIs(t, err, errLedgerNotFound)
}

func TestAdg_GetValidatedLedgerHash(t *testing.T) {
	a := New(Config{})
	assert.Equal(t, consensus.LedgerID{}, a.GetValidatedLedgerHash())

	// After Start the genesis ledger is validated in standalone mode.
	a2 := newTestAdaptor(t)
	h := a2.GetValidatedLedgerHash()
	assert.NotEqual(t, consensus.LedgerID{}, h)
}

func TestAdaptorHasTxWithoutLedgerService(t *testing.T) {
	a := New(Config{})
	exists, err := a.HasTx(consensus.TxID{0x01})
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestAdg_BuildLedger(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)

	txSet, err := a.BuildTxSet(nil)
	require.NoError(t, err)

	built, err := a.BuildLedger(lcl, txSet, time.Now(), true, nil)
	require.NoError(t, err)
	require.NotNil(t, built)
	assert.Equal(t, lcl.Seq()+1, built.Seq())

	_ = svc
}

func TestOnLedgerSwitchedMovesCanonicalFrontierBeforeBuild(t *testing.T) {
	a := newTestAdaptor(t)
	txSet, err := a.BuildTxSet(nil)
	require.NoError(t, err)
	preferred, err := a.GetLastClosedLedger()
	require.NoError(t, err)

	closeTime := time.Unix(1_700_000_000, 0)
	frontier := preferred
	for range 2 {
		closeTime = closeTime.Add(2 * time.Second)
		frontier, err = a.BuildLedger(frontier, txSet, closeTime, true, nil)
		require.NoError(t, err)
	}

	_, err = a.BuildLedger(preferred, txSet, closeTime.Add(2*time.Second), true, nil)
	require.ErrorIs(t, err, service.ErrConsensusParentMismatch)
	assert.Equal(t, frontier.ID(), consensus.LedgerID(a.ledgerService.GetClosedLedger().Hash()))

	a.OnLedgerSwitched(preferred)
	assert.Equal(t, preferred.ID(), consensus.LedgerID(a.ledgerService.GetClosedLedger().Hash()))
	assert.Equal(t, preferred.Seq()+1, a.ledgerService.GetCurrentLedgerIndex())
	built, err := a.BuildLedger(preferred, txSet, closeTime.Add(4*time.Second), true, nil)
	require.NoError(t, err)
	assert.Equal(t, preferred.Seq()+1, built.Seq())
	assert.Equal(t, preferred.ID(), built.ParentID())
}

func TestAdg_ValidateLedger(t *testing.T) {
	a := newTestAdaptor(t)

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)

	assert.NoError(t, a.ValidateLedger(lcl))

	assert.Error(t, a.ValidateLedger(stubLedger{id: consensus.LedgerID{0x01}}))
}

func TestAdg_StoreLedger(t *testing.T) {
	a := newTestAdaptor(t)
	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)
	assert.NoError(t, a.StoreLedger(lcl))
}

func TestAdg_GetPendingTxs(t *testing.T) {
	a := New(Config{})
	assert.Nil(t, a.GetPendingTxs())

	a2 := newTestAdaptor(t)
	txs := a2.GetPendingTxs()
	assert.NotNil(t, txs) // may be empty slice but should not be nil after Start
	_ = txs
}

func TestAdg_OnTxSetBuilt(t *testing.T) {
	var calls int
	var calledID consensus.TxSetID
	a := New(Config{
		OnTxSetBuilt: func(id consensus.TxSetID) {
			calls++
			calledID = id
		},
	})

	// The empty set (all-zero ID) is never announced — it recurs every
	// empty round and peers charge duplicate tsHAVE as useless data.
	_, err := a.BuildTxSet(nil)
	require.NoError(t, err)
	assert.Zero(t, calls, "empty set must not be announced")

	blob := []byte{0x12, 0x00, 0x34, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09}
	ts, err := a.BuildTxSet([][]byte{blob})
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "callback must fire for a fresh non-empty set")
	assert.Equal(t, ts.ID(), calledID)

	// Rebuilding the same set must not re-announce it.
	_, err = a.BuildTxSet([][]byte{blob})
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "same set hash must be announced at most once")
}

func TestAdg_OnTxSetBuiltConcurrentUniqueSets(t *testing.T) {
	const setCount = 64

	announced := make(chan consensus.TxSetID, setCount)
	a := New(Config{
		OnTxSetBuilt: func(id consensus.TxSetID) {
			announced <- id
		},
	})

	var wg sync.WaitGroup
	errs := make(chan error, setCount)
	for i := range setCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			blob := []byte{
				0x12, 0x00, 0x34, 0x01, 0x02, 0x03,
				0x04, 0x05, 0x06, 0x07, byte(i >> 8), byte(i),
			}
			_, err := a.BuildTxSet([][]byte{blob})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(announced)

	for err := range errs {
		require.NoError(t, err)
	}
	ids := make(map[consensus.TxSetID]struct{}, setCount)
	for id := range announced {
		ids[id] = struct{}{}
	}
	assert.Len(t, ids, setCount)
}

func TestAdg_GetValidatorSigningKey(t *testing.T) {
	a := newTestAdaptor(t)

	key, err := a.GetValidatorSigningKey()
	require.NoError(t, err)
	assert.NotEqual(t, [33]byte{}, key)

	svc := newTestLedgerService(t)
	noKey := New(Config{LedgerService: svc})
	_, err = noKey.GetValidatorSigningKey()
	assert.ErrorIs(t, err, errNoValidatorKey)
}

func TestAdg_GetNegativeUNLMasters(t *testing.T) {
	a := New(Config{})
	assert.Nil(t, a.GetNegativeUNLMasters())

	a2 := newTestAdaptor(t)
	assert.Nil(t, a2.GetNegativeUNLMasters())
}

func TestAdg_GetServerVersion(t *testing.T) {
	a := newTestAdaptor(t)
	v := a.GetServerVersion()
	assert.NotZero(t, v)
	// Must NOT have the rippled top bit set (0x8000...)
	assert.Zero(t, v&0x8000_0000_0000_0000, "go-xrpl must not set the rippled top bit")
	assert.Equal(t, goxrplServerVersionTag, v)
}

func TestAdg_GetFeeVote(t *testing.T) {
	a := newTestAdaptorWithConfig(t, FeeVoteStance{
		BaseFee:          42,
		ReserveBase:      1_000_000,
		ReserveIncrement: 500_000,
	}, nil)

	fv := a.GetFeeVote(nil)
	assert.Equal(t, uint64(42), fv.BaseFee)
	assert.Equal(t, uint64(1_000_000), fv.ReserveBase)
	assert.Equal(t, uint64(500_000), fv.ReserveIncrement)
	assert.True(t, fv.HasBaseFee())
	assert.True(t, fv.HasReserveBase())
	assert.True(t, fv.HasReserveIncrement())

	// Default zero config falls back to rippled defaults.
	a2 := newTestAdaptorWithConfig(t, FeeVoteStance{}, nil)
	fv2 := a2.GetFeeVote(nil)
	d := defaultFeeVote()
	assert.Equal(t, d.BaseFee, fv2.BaseFee)
	assert.Equal(t, uint64(d.ReserveBase), fv2.ReserveBase)
	assert.Equal(t, uint64(d.ReserveIncrement), fv2.ReserveIncrement)

	explicitZero := newTestAdaptorWithConfig(t, FeeVoteStance{
		BaseFeeSet:          true,
		ReserveBaseSet:      true,
		ReserveIncrementSet: true,
	}, nil).GetFeeVote(nil)
	assert.Zero(t, explicitZero.BaseFee)
	assert.Zero(t, explicitZero.ReserveBase)
	assert.Zero(t, explicitZero.ReserveIncrement)
	assert.True(t, explicitZero.HasBaseFee())
	assert.True(t, explicitZero.HasReserveBase())
	assert.True(t, explicitZero.HasReserveIncrement())

	current := a2.GetFeeVote(WrapLedger(a2.ledgerService.GetClosedLedger()))
	assert.False(t, current.HasBaseFee())
	assert.False(t, current.HasReserveBase())
	assert.False(t, current.HasReserveIncrement())
}

func TestAdg_IsFeatureEnabled(t *testing.T) {
	// nil service → defaults to true (safe mainnet behaviour).
	a := New(Config{})
	assert.True(t, a.IsFeatureEnabled("HardenedValidations"))

	a2 := newTestAdaptor(t)
	// Unknown feature name → true (safe default).
	assert.True(t, a2.IsFeatureEnabled("NonExistentFeatureXYZ"))

	// A feature that exists but may or may not be enabled — just confirm
	// no panic and a bool is returned.
	got := a2.IsFeatureEnabled("XRPFees")
	_ = got // could be true or false depending on genesis rules
}

func TestAdg_IsFeatureEnabledOnLedger(t *testing.T) {
	a := newTestAdaptor(t)

	assert.False(t, a.IsFeatureEnabledOnLedger(nil, "HardenedValidations"))

	assert.False(t, a.IsFeatureEnabledOnLedger(stubLedger{}, "HardenedValidations"))

	// Valid wrapped ledger + unknown feature → false (strict gate).
	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)
	assert.False(t, a.IsFeatureEnabledOnLedger(lcl, "NonExistentFeatureXYZ"))

	_ = a.IsFeatureEnabledOnLedger(lcl, "XRPFees")
	assert.True(t, a.IsFeatureEnabledOnLedger(lcl, "NegativeUNL"))
}

func TestAdg_IsStandalone(t *testing.T) {
	a := New(Config{})
	assert.False(t, a.IsStandalone())

	a2 := newTestAdaptor(t) // newTestLedgerService sets Standalone: true
	assert.True(t, a2.IsStandalone())
}

func TestAdg_CloseOffset(t *testing.T) {
	a := newTestAdaptor(t)

	assert.Equal(t, time.Duration(0), a.CloseOffset())

	a.closeOffsetNs.Store(int64(5 * time.Second))
	assert.Equal(t, 5*time.Second, a.CloseOffset())
}

// TestAdg_StateAccounting checks that the adaptor exposes a non-zero snapshot
// after construction (the disconnected entry is pre-populated) and that the
// snapshot changes after a mode transition.
func TestAdg_StateAccounting(t *testing.T) {
	a := newTestAdaptor(t)

	snap := a.StateAccounting()
	disc, ok := snap.Modes["disconnected"]
	assert.True(t, ok, "disconnected mode must appear in snapshot")
	assert.Equal(t, uint64(1), disc.Transitions)

	a.SetOperatingMode(consensus.OpModeConnected)
	snap2 := a.StateAccounting()
	conn, ok := snap2.Modes["connected"]
	assert.True(t, ok)
	assert.Equal(t, uint64(1), conn.Transitions)
}

func TestAdg_OnPhaseChange(t *testing.T) {
	a := newTestAdaptor(t)

	// Drive a ledger so the closed ledger is non-nil (broadcastStatus reads it).
	_, err := a.ledgerService.AcceptLedger(context.TODO())
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		a.OnPhaseChange(consensus.PhaseOpen, consensus.PhaseEstablish)
		a.OnPhaseChange(consensus.PhaseEstablish, consensus.PhaseAccepted)
		a.OnPhaseChange(consensus.PhaseAccepted, consensus.PhaseOpen)
	})
}

// adg_newNonStandaloneService builds a consensus-mode service.
func adg_newNonStandaloneService(t *testing.T) *service.Service {
	t.Helper()
	cfg := service.Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
	}
	svc, err := service.New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	return svc
}

func TestAdg_SetOnTxSetRequested(t *testing.T) {
	a := newTestAdaptor(t)

	var called bool
	var capturedID consensus.TxSetID
	a.SetOnTxSetRequested(func(id consensus.TxSetID) {
		called = true
		capturedID = id
	})

	want := consensus.TxSetID{0xAB, 0xCD}
	_ = a.RequestTxSet(want)

	assert.True(t, called)
	assert.Equal(t, want, capturedID)
}
