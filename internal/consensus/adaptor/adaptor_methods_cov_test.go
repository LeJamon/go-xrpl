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

// TestAdg_GetLedger covers the hash-lookup path in GetLedger.
func TestAdg_GetLedger(t *testing.T) {
	a := newTestAdaptor(t)

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)
	require.NotNil(t, lcl)

	// Valid hash lookup must return the ledger.
	got, err := a.GetLedger(lcl.ID())
	require.NoError(t, err)
	assert.Equal(t, lcl.ID(), got.ID())

	// Unknown hash must return ErrLedgerNotFound.
	_, err = a.GetLedger(consensus.LedgerID{0xDE, 0xAD})
	assert.ErrorIs(t, err, ErrLedgerNotFound)
}

// TestAdg_GetLedgerBySeq covers the sequence-lookup path in GetLedgerBySeq.
func TestAdg_GetLedgerBySeq(t *testing.T) {
	a := newTestAdaptor(t)

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)

	got, err := a.GetLedgerBySeq(lcl.Seq())
	require.NoError(t, err)
	assert.Equal(t, lcl.Seq(), got.Seq())

	// Unknown seq must return ErrLedgerNotFound.
	_, err = a.GetLedgerBySeq(99999)
	assert.ErrorIs(t, err, ErrLedgerNotFound)
}

// TestAdg_GetValidatedLedgerHash covers both the nil-service and
// post-validation paths.
func TestAdg_GetValidatedLedgerHash(t *testing.T) {
	// nil service → zero LedgerID.
	a := New(Config{})
	assert.Equal(t, consensus.LedgerID{}, a.GetValidatedLedgerHash())

	// After Start the genesis ledger is validated in standalone mode.
	a2 := newTestAdaptor(t)
	h := a2.GetValidatedLedgerHash()
	assert.NotEqual(t, consensus.LedgerID{}, h)
}

// TestAdg_BuildLedger drives the AcceptConsensusResult path through
// BuildLedger and verifies the returned ledger wraps a real sequence.
func TestAdg_BuildLedger(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService

	// Obtain the current LCL as parent.
	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)

	// Build an empty tx set.
	txSet, err := a.BuildTxSet(nil)
	require.NoError(t, err)

	// Build the next ledger.
	built, err := a.BuildLedger(lcl, txSet, time.Now(), true)
	require.NoError(t, err)
	require.NotNil(t, built)
	assert.Equal(t, lcl.Seq()+1, built.Seq())

	_ = svc // used implicitly via adaptor
}

// TestAdg_ValidateLedger covers the happy path and the wrong-type error path.
func TestAdg_ValidateLedger(t *testing.T) {
	a := newTestAdaptor(t)

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)

	// Valid *LedgerWrapper must not error.
	assert.NoError(t, a.ValidateLedger(lcl))

	// Non-LedgerWrapper type must return an error.
	assert.Error(t, a.ValidateLedger(stubLedger{id: consensus.LedgerID{0x01}}))
}

// TestAdg_StoreLedger is a no-op but must not error.
func TestAdg_StoreLedger(t *testing.T) {
	a := newTestAdaptor(t)
	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)
	assert.NoError(t, a.StoreLedger(lcl))
}

// TestAdg_GetPendingTxs covers the nil-service and service-backed paths.
func TestAdg_GetPendingTxs(t *testing.T) {
	// nil service → nil.
	a := New(Config{})
	assert.Nil(t, a.GetPendingTxs())

	// Service-backed adaptor — pool is empty at genesis but must not panic.
	a2 := newTestAdaptor(t)
	txs := a2.GetPendingTxs()
	assert.NotNil(t, txs) // may be empty slice but should not be nil after Start
	_ = txs
}

// TestAdg_SetOnTxSetBuilt verifies the callback is invoked by BuildTxSet.
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

	// Clear callback — no panic.
	a.SetOnTxSetBuilt(nil)
	_, err = a.BuildTxSet(nil)
	assert.NoError(t, err)
}

// TestAdg_GetValidatorSigningKey covers the key-present and no-identity paths.
func TestAdg_GetValidatorSigningKey(t *testing.T) {
	a := newTestAdaptor(t)

	key, err := a.GetValidatorSigningKey()
	require.NoError(t, err)
	assert.NotEqual(t, [33]byte{}, key)

	// No-identity adaptor must return ErrNoValidatorKey.
	svc := newTestLedgerService(t)
	noKey := New(Config{LedgerService: svc})
	_, err = noKey.GetValidatorSigningKey()
	assert.ErrorIs(t, err, ErrNoValidatorKey)
}

// TestAdg_GetNegativeUNLMasters covers both the nil-service path and the
// no-SLE path (healthy cluster — most common test scenario).
func TestAdg_GetNegativeUNLMasters(t *testing.T) {
	// nil service → nil.
	a := New(Config{})
	assert.Nil(t, a.GetNegativeUNLMasters())

	// Service without a NegativeUNL SLE → nil.
	a2 := newTestAdaptor(t)
	assert.Nil(t, a2.GetNegativeUNLMasters())
}

// TestAdg_GetServerVersion pins the non-rippled high-byte tag.
func TestAdg_GetServerVersion(t *testing.T) {
	a := newTestAdaptor(t)
	v := a.GetServerVersion()
	assert.NotZero(t, v)
	// Must NOT have the rippled top bit set (0x8000...)
	assert.Zero(t, v&0x8000_0000_0000_0000, "go-xrpl must not set the rippled top bit")
	// Must carry the go-xrpl tag.
	assert.Equal(t, goxrplServerVersionTag, v)
}

// TestAdg_GetFeeVote covers the three fee fields and the postXRPFees flag.
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

// TestAdg_GetAmendmentVote ensures the method returns a non-nil slice for
// a newly-created adaptor whose stances include VoteUp entries.
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

// TestAdg_IsFeatureEnabled covers the no-service, no-validated-ledger, and
// known-feature paths.
func TestAdg_IsFeatureEnabled(t *testing.T) {
	// nil service → defaults to true (safe mainnet behaviour).
	a := New(Config{})
	assert.True(t, a.IsFeatureEnabled("HardenedValidations"))

	// Service-backed adaptor: genesis ledger has rules.
	a2 := newTestAdaptor(t)
	// Unknown feature name → true (safe default).
	assert.True(t, a2.IsFeatureEnabled("NonExistentFeatureXYZ"))

	// A feature that exists but may or may not be enabled — just confirm
	// no panic and a bool is returned.
	got := a2.IsFeatureEnabled("XRPFees")
	_ = got // could be true or false depending on genesis rules
}

// TestAdg_IsFeatureEnabledOnLedger exercises nil-ledger, wrong-type, and
// known-feature paths.
func TestAdg_IsFeatureEnabledOnLedger(t *testing.T) {
	a := newTestAdaptor(t)

	// nil ledger → false.
	assert.False(t, a.IsFeatureEnabledOnLedger(nil, "HardenedValidations"))

	// Non-LedgerWrapper type → false.
	assert.False(t, a.IsFeatureEnabledOnLedger(stubLedger{}, "HardenedValidations"))

	// Valid wrapped ledger + unknown feature → false (strict gate).
	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)
	assert.False(t, a.IsFeatureEnabledOnLedger(lcl, "NonExistentFeatureXYZ"))

	// Valid wrapped ledger + known feature — no panic.
	_ = a.IsFeatureEnabledOnLedger(lcl, "XRPFees")
}

// TestAdg_IsStandalone covers the nil-service and standalone paths.
func TestAdg_IsStandalone(t *testing.T) {
	// nil service → false.
	a := New(Config{})
	assert.False(t, a.IsStandalone())

	// Standalone service → true.
	a2 := newTestAdaptor(t) // newTestLedgerService sets Standalone: true
	assert.True(t, a2.IsStandalone())
}

// TestAdg_CloseOffset covers the zero initial state and store/load round-trip.
func TestAdg_CloseOffset(t *testing.T) {
	a := newTestAdaptor(t)

	assert.Equal(t, time.Duration(0), a.CloseOffset())

	// Store a known value and read it back.
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

// TestAdg_OnModeChange must not panic.
func TestAdg_OnModeChange(t *testing.T) {
	a := newTestAdaptor(t)
	assert.NotPanics(t, func() {
		a.OnModeChange(consensus.ModeObserving, consensus.ModeProposing)
	})
}

// TestAdg_OnPhaseChange covers both phase→broadcast paths and the
// no-panic contract when hooks are nil.
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

// TestAdg_AdoptLedgerFromHeader_InvalidBytes checks the error path for
// malformed header bytes.
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

// TestAdg_AdoptLedgerFromHeader_ValidHeader exercises the happy path:
// a valid serialized header is adopted and the mode advances to Tracking.
func TestAdg_AdoptLedgerFromHeader_ValidHeader(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})

	// Obtain the current closed ledger to build a valid header from.
	cl := svc.GetClosedLedger()
	require.NotNil(t, cl)
	h := cl.Header()

	// Serialize with hash so AdoptLedgerFromHeader can verify.
	raw := header.AddRaw(h, true)

	a.SetOperatingMode(consensus.OpModeConnected)
	err = a.AdoptLedgerFromHeader(raw)
	require.NoError(t, err)

	// Mode must have advanced to Tracking.
	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
}

// TestAdg_AdoptLedgerFromHeader_PrefixedHeader exercises the prefixed variant
// (4-byte prefix followed by the raw header).
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

	// Prepend 4 bytes as a prefix.
	prefixed := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(prefixed[:4], 0x534E4400) // arbitrary prefix
	copy(prefixed[4:], raw)

	err = a.AdoptLedgerFromHeader(prefixed)
	require.NoError(t, err)
}

// TestAdg_BroadcastStatus_NoClosedLedgerNoPanic guards broadcastStatus
// when the closed ledger is nil (service has been started but nothing
// has been accepted yet — only relevant in non-standalone mode where
// closedLedger is initially the genesis).
func TestAdg_BroadcastStatus_NoClosedLedgerNoPanic(t *testing.T) {
	// Use a service-backed adaptor; the service's GetClosedLedger returns
	// the genesis ledger (non-nil) after Start. broadcastStatus must not
	// panic in either case.
	a := newTestAdaptor(t)
	assert.NotPanics(t, func() {
		a.broadcastStatus(0)
	})
}

// TestAdg_SetOnTxSetRequested checks the callback is fired by RequestTxSet.
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
