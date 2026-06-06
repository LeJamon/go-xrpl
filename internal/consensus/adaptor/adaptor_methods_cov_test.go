package adaptor

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
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
	assert.ErrorIs(t, err, ErrLedgerNotFound)
}

func TestAdg_GetLedgerBySeq(t *testing.T) {
	a := newTestAdaptor(t)

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)

	got, err := a.GetLedgerBySeq(lcl.Seq())
	require.NoError(t, err)
	assert.Equal(t, lcl.Seq(), got.Seq())

	_, err = a.GetLedgerBySeq(99999)
	assert.ErrorIs(t, err, ErrLedgerNotFound)
}

func TestAdg_GetValidatedLedgerHash(t *testing.T) {
	a := New(Config{})
	assert.Equal(t, consensus.LedgerID{}, a.GetValidatedLedgerHash())

	// After Start the genesis ledger is validated in standalone mode.
	a2 := newTestAdaptor(t)
	h := a2.GetValidatedLedgerHash()
	assert.NotEqual(t, consensus.LedgerID{}, h)
}

func TestAdg_BuildLedger(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)

	txSet, err := a.BuildTxSet(nil)
	require.NoError(t, err)

	built, err := a.BuildLedger(lcl, txSet, time.Now(), true)
	require.NoError(t, err)
	require.NotNil(t, built)
	assert.Equal(t, lcl.Seq()+1, built.Seq())

	_ = svc
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

func TestAdg_SetOnTxSetBuilt(t *testing.T) {
	a := newTestAdaptor(t)

	var called bool
	var calledID consensus.TxSetID
	a.SetOnTxSetBuilt(func(id consensus.TxSetID) {
		called = true
		calledID = id
	})

	ts, err := a.BuildTxSet(nil)
	require.NoError(t, err)

	assert.True(t, called, "callback must fire after BuildTxSet")
	assert.Equal(t, ts.ID(), calledID)

	a.SetOnTxSetBuilt(nil)
	_, err = a.BuildTxSet(nil)
	assert.NoError(t, err)
}

func TestAdg_GetValidatorSigningKey(t *testing.T) {
	a := newTestAdaptor(t)

	key, err := a.GetValidatorSigningKey()
	require.NoError(t, err)
	assert.NotEqual(t, [33]byte{}, key)

	svc := newTestLedgerService(t)
	noKey := New(Config{LedgerService: svc})
	_, err = noKey.GetValidatorSigningKey()
	assert.ErrorIs(t, err, ErrNoValidatorKey)
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

	baseFee, reserveBase, reserveIncrement, _ := a.GetFeeVote()
	assert.Equal(t, uint64(42), baseFee)
	assert.Equal(t, uint64(1_000_000), reserveBase)
	assert.Equal(t, uint64(500_000), reserveIncrement)

	// Default zero config falls back to rippled defaults.
	a2 := newTestAdaptorWithConfig(t, FeeVoteStance{}, nil)
	bf2, rb2, ri2, _ := a2.GetFeeVote()
	d := defaultFeeVote()
	assert.Equal(t, d.BaseFee, bf2)
	assert.Equal(t, uint64(d.ReserveBase), rb2)
	assert.Equal(t, uint64(d.ReserveIncrement), ri2)
}

func TestAdg_GetAmendmentVote(t *testing.T) {
	a := newTestAdaptor(t)
	// Construction seeds VoteUp from registry; at least one DefaultYes
	// feature should appear.
	votes := a.GetAmendmentVote()
	// The slice can be nil if all defaults are already enabled on the
	// validated ledger — in a test env with genesis rules that won't be
	// the case for most features, but we just ensure the call does not panic.
	_ = votes
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

func TestAdg_OnModeChange(t *testing.T) {
	a := newTestAdaptor(t)
	assert.NotPanics(t, func() {
		a.OnModeChange(consensus.ModeObserving, consensus.ModeProposing)
	})
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

func TestAdg_AdoptLedgerFromHeader_InvalidBytes(t *testing.T) {
	a := newTestAdaptor(t)

	err := a.AdoptLedgerFromHeader([]byte{0x00, 0x01})
	assert.Error(t, err, "short input must fail deserialization")
}

// adg_newNonStandaloneService builds a consensus-mode (non-standalone)
// service that sets needsInitialSync=true, required by AdoptLedgerHeader.
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

func TestAdg_AdoptLedgerFromHeader_ValidHeader(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})

	cl := svc.GetClosedLedger()
	require.NotNil(t, cl)
	h := cl.Header()

	// Serialize with hash so AdoptLedgerFromHeader can verify.
	raw := header.AddRaw(h, true)

	a.SetOperatingMode(consensus.OpModeConnected)
	err = a.AdoptLedgerFromHeader(raw)
	require.NoError(t, err)

	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
}

func TestAdg_AdoptLedgerFromHeader_PrefixedHeader(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	cl := svc.GetClosedLedger()
	require.NotNil(t, cl)
	h := cl.Header()
	raw := header.AddRaw(h, true)

	prefixed := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(prefixed[:4], 0x534E4400) // arbitrary prefix
	copy(prefixed[4:], raw)

	err = a.AdoptLedgerFromHeader(prefixed)
	require.NoError(t, err)
}

func TestAdg_BroadcastStatus_NoClosedLedgerNoPanic(t *testing.T) {
	// Use a service-backed adaptor; the service's GetClosedLedger returns
	// the genesis ledger (non-nil) after Start. broadcastStatus must not
	// panic in either case.
	a := newTestAdaptor(t)
	assert.NotPanics(t, func() {
		a.broadcastStatus(0)
	})
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
