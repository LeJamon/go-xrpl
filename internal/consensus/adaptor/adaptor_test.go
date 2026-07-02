package adaptor

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestLedgerService(t *testing.T) *service.Service {
	t.Helper()
	cfg := service.Config{
		Standalone:    true, // standalone for tests that expect seq 2
		GenesisConfig: genesis.DefaultConfig(),
	}
	svc, err := service.New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	return svc
}

func newTestAdaptor(t *testing.T) *Adaptor {
	t.Helper()
	svc := newTestLedgerService(t)

	// Create a validator identity from a known test seed
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	require.NotNil(t, identity)

	validators := []consensus.NodeID{identity.NodeID}

	return New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    validators,
	})
}

func TestAdaptorCreation(t *testing.T) {
	a := newTestAdaptor(t)
	require.NotNil(t, a)

	// Should be a validator
	assert.True(t, a.IsValidator())

	// Should have a validator key
	key, err := a.GetValidatorKey()
	assert.NoError(t, err)
	assert.NotEqual(t, consensus.NodeID{}, key)

	// Quorum for 1 validator should be 1
	assert.Equal(t, 1, a.GetQuorum())

	// Initial operating mode should be disconnected
	assert.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())

	// The single validator should be trusted
	assert.True(t, a.IsTrusted(key))

	// A random node should not be trusted
	assert.False(t, a.IsTrusted(consensus.NodeID{0x99}))

	// GetTrustedValidators should return our validator
	validators := a.GetTrustedValidators()
	assert.Len(t, validators, 1)
	assert.Equal(t, key, validators[0])
}

// TestComputeQuorum pins R6b.3 dynamic quorum math. Mirrors rippled's
// calculateQuorum (ValidatorList.cpp:1906-1907,2061-2087):
// max(ceil(0.8 * (trusted - disabled)), ceil(0.6 * trusted)). The second
// term is the negative-UNL AbsoluteMinimumQuorum floor — within the 25%
// negUNL cap the 0.8 term dominates, so the floor only engages beyond the
// cap. Pre-R6b.3 quorum was frozen at ceil(0.8 * trusted) at Adaptor
// construction and never recomputed, so partial-UNL outages slowed
// finality vs. rippled.
func TestComputeQuorum(t *testing.T) {
	tests := []struct {
		name     string
		trusted  int
		disabled int
		want     int
	}{
		{"standalone", 0, 0, 0},
		{"single_validator_no_negunl", 1, 0, 1},
		{"five_validators_no_negunl", 5, 0, 4},
		{"five_validators_two_negunl", 5, 2, 3},   // max(ceil(0.8*3),ceil(0.6*5)) = max(3,3) = 3
		{"ten_validators_three_negunl", 10, 3, 6}, // max(ceil(0.8*7),ceil(0.6*10)) = max(6,6) = 6
		// Floor binds: disabled beyond the 25% cap, so ceil(0.6*trusted)
		// dominates ceil(0.8*effective) — matching rippled's clamp.
		{"five_validators_four_negunl", 5, 4, 3},         // max(ceil(0.8*1),ceil(0.6*5))  = max(1,3)  = 3
		{"twenty_validators_ten_negunl", 20, 10, 12},     // max(ceil(0.8*10),ceil(0.6*20)) = max(8,12) = 12
		{"hundred_validators_forty_negunl", 100, 40, 60}, // max(ceil(0.8*60),ceil(0.6*100)) = max(48,60) = 60
		// Edge: all trusted on negUNL → unreachable quorum.
		{"all_disabled", 5, 5, math.MaxInt},
		// More disabled than trusted (shouldn't happen but must be safe)
		{"over_disabled", 5, 7, math.MaxInt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeQuorum(tc.trusted, tc.disabled)
			assert.Equal(t, tc.want, got,
				"computeQuorum(trusted=%d, disabled=%d)", tc.trusted, tc.disabled)
		})
	}
}

// TestAdaptor_CookieIsAlwaysNonZero pins the boot-cookie contract
// rippled relies on for sfCookie under HardenedValidations
// (RCLConsensus.cpp:813-818): the cookie generated at construction is
// never zero, so soeDEFAULT serialization always includes the field.
// Issue #363 E2.
func TestAdaptor_CookieIsAlwaysNonZero(t *testing.T) {
	for range 16 {
		a := newTestAdaptor(t)
		assert.NotZero(t, a.GetCookie(),
			"Adaptor.cookie must be non-zero at boot; serializer omits zero (soeDEFAULT)")
	}
}

func TestAdaptorNonValidator(t *testing.T) {
	svc := newTestLedgerService(t)
	a := New(Config{
		LedgerService: svc,
		Identity:      nil, // no validator identity
	})

	assert.False(t, a.IsValidator())

	_, err := a.GetValidatorKey()
	assert.ErrorIs(t, err, ErrNoValidatorKey)
}

func TestAdaptorOperatingMode(t *testing.T) {
	a := newTestAdaptor(t)

	assert.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())

	a.SetOperatingMode(consensus.OpModeConnected)
	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())

	a.SetOperatingMode(consensus.OpModeFull)
	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
}

func TestAdaptorGetLastClosedLedger(t *testing.T) {
	a := newTestAdaptor(t)

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)
	require.NotNil(t, lcl)

	// After Start(), the LCL should be sequence 2
	assert.Equal(t, uint32(2), lcl.Seq())
	assert.NotEqual(t, consensus.LedgerID{}, lcl.ID())
}

func TestAdaptorQuorumCalculation(t *testing.T) {
	svc := newTestLedgerService(t)

	tests := []struct {
		numValidators  int
		expectedQuorum int
	}{
		{1, 1},
		{2, 2},
		{3, 3},
		{4, 4},
		{5, 4},
		{10, 8},
		{20, 16},
		{100, 80},
	}

	for _, tt := range tests {
		validators := make([]consensus.NodeID, tt.numValidators)
		for i := range validators {
			validators[i] = consensus.NodeID{byte(i)}
		}
		a := New(Config{
			LedgerService: svc,
			Validators:    validators,
		})
		assert.Equal(t, tt.expectedQuorum, a.GetQuorum(),
			"quorum for %d validators", tt.numValidators)
	}
}

// TestSetTrustedValidators_AtomicSwap pins the runtime UNL-reload
// primitive: every reader (GetTrustedValidators, IsTrusted, GetQuorum,
// and the master-key snapshot consumed by NegativeUNL voting) must
// observe the new set as a unit. Mirrors the writable surface of
// rippled's ValidatorList::updateTrusted (ValidatorList.cpp:2061-2087).
func TestSetTrustedValidators_AtomicSwap(t *testing.T) {
	svc := newTestLedgerService(t)
	initial := []consensus.NodeID{{0x01}, {0x02}, {0x03}}
	a := New(Config{
		LedgerService: svc,
		Validators:    initial,
	})
	require.Equal(t, 3, a.GetQuorum(), "initial quorum: ceil(0.8*3) = 3")
	require.True(t, a.IsTrusted(consensus.NodeID{0x01}))
	require.False(t, a.IsTrusted(consensus.NodeID{0x04}))

	next := []consensus.NodeID{{0x01}, {0x04}, {0x05}, {0x06}, {0x07}}
	masterKeys := [][33]byte{
		{0x02, 0xAA}, {0x02, 0xBB}, {0x02, 0xCC}, {0x02, 0xDD}, {0x02, 0xEE},
	}
	a.SetTrustedValidators(next, masterKeys)

	got := a.GetTrustedValidators()
	assert.ElementsMatch(t, next, got, "GetTrustedValidators reflects new set")
	assert.True(t, a.IsTrusted(consensus.NodeID{0x04}), "newly added is trusted")
	assert.False(t, a.IsTrusted(consensus.NodeID{0x02}), "removed is no longer trusted")
	assert.Equal(t, 4, a.GetQuorum(), "quorum recomputes: ceil(0.8*5) = 4")

	// Master keys must be visible to the NegativeUNL voting path. Read
	// under the lock the same way GenerateNegativeUNLPseudoTx does.
	a.mu.Lock()
	mkLen := len(a.trustedMasterKeys)
	a.mu.Unlock()
	assert.Equal(t, len(masterKeys), mkLen, "trustedMasterKeys swapped atomically")

	// Empty swap clears the set (standalone-mode transition).
	a.SetTrustedValidators(nil, nil)
	assert.Empty(t, a.GetTrustedValidators())
	assert.Equal(t, 0, a.GetQuorum(), "empty trusted set → quorum 0 (no gate)")
}

func TestTxSetCreateAndLookup(t *testing.T) {
	a := newTestAdaptor(t)

	// Blobs must be >= 12 bytes (SHAMap transaction leaf minimum)
	blobs := [][]byte{
		{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C},
		{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C},
	}

	ts, err := a.BuildTxSet(blobs)
	require.NoError(t, err)
	assert.Equal(t, 2, ts.Size())
	assert.NotEqual(t, consensus.TxSetID{}, ts.ID())

	// Should be retrievable from cache
	retrieved, err := a.GetTxSet(ts.ID())
	require.NoError(t, err)
	assert.Equal(t, ts.ID(), retrieved.ID())

	// Unknown ID should error
	_, err = a.GetTxSet(consensus.TxSetID{0xFF})
	assert.Error(t, err)
}

func TestTxSetContains(t *testing.T) {
	blob1 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C}
	blob2 := []byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C}
	blob3 := []byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2A, 0x2B, 0x2C}

	ts, err := NewTxSet([][]byte{blob1, blob2})
	require.NoError(t, err)

	id1 := computeTxID(blob1)
	id2 := computeTxID(blob2)
	id3 := computeTxID(blob3)

	assert.True(t, ts.Contains(id1))
	assert.True(t, ts.Contains(id2))
	assert.False(t, ts.Contains(id3))
}

func TestTxSetAddRemove(t *testing.T) {
	blob1 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C}
	blob2 := []byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C}

	ts, err := NewTxSet([][]byte{blob1})
	require.NoError(t, err)
	assert.Equal(t, 1, ts.Size())

	originalID := ts.ID()

	// Add
	err = ts.Add(blob2)
	require.NoError(t, err)
	assert.Equal(t, 2, ts.Size())
	assert.NotEqual(t, originalID, ts.ID()) // ID should change

	// Remove
	id1 := computeTxID(blob1)
	err = ts.Remove(id1)
	require.NoError(t, err)
	assert.Equal(t, 1, ts.Size())
	assert.False(t, ts.Contains(id1))
}

func TestProposalSignVerify(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	proposal := &consensus.Proposal{
		Round: consensus.RoundID{
			Seq:        3,
			ParentHash: [32]byte{0x01},
		},
		NodeID:         identity.NodeID,
		Position:       0,
		TxSet:          consensus.TxSetID{0x02},
		CloseTime:      time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		PreviousLedger: consensus.LedgerID{0x03},
		Timestamp:      time.Now(),
	}

	// Sign
	err = identity.SignProposal(proposal)
	require.NoError(t, err)
	assert.NotEmpty(t, proposal.Signature)

	// Verify
	err = VerifyProposal(proposal)
	assert.NoError(t, err)

	// Tamper and verify fails
	proposal.Position = 99
	err = VerifyProposal(proposal)
	assert.Error(t, err)
}

func TestValidationSignVerify(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	validation := &consensus.Validation{
		LedgerID:  consensus.LedgerID{0x01},
		LedgerSeq: 5,
		NodeID:    identity.NodeID,
		SignTime:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Full:      true,
		Cookie:    12345,
		LoadFee:   256,
	}

	// Sign
	err = identity.SignValidation(validation)
	require.NoError(t, err)
	assert.NotEmpty(t, validation.Signature)

	// Verify
	err = VerifyValidation(validation)
	assert.NoError(t, err)

	// Tamper and verify fails
	validation.LedgerSeq = 99
	err = VerifyValidation(validation)
	assert.Error(t, err)
}

func TestValidatorIdentityFromSeed(t *testing.T) {
	// Nil seed should return nil identity
	identity, err := NewValidatorIdentity("")
	assert.NoError(t, err)
	assert.Nil(t, identity)

	// Valid seed should produce an identity
	identity, err = NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	assert.NoError(t, err)
	assert.NotNil(t, identity)
	// Seed-only identity: master == signing == NodeID, no manifest.
	assert.Equal(t, identity.MasterKey, identity.SigningKey)
	assert.NotEqual(t, consensus.NodeID{}, identity.NodeID)
	assert.Nil(t, identity.Manifest)
	assert.Nil(t, identity.SerializedMfst)
}

func TestLedgerWrapper(t *testing.T) {
	svc := newTestLedgerService(t)

	l := svc.GetClosedLedger()
	require.NotNil(t, l)

	wrapper := WrapLedger(l)
	assert.Equal(t, uint32(2), wrapper.Seq())
	assert.NotEqual(t, consensus.LedgerID{}, wrapper.ID())
	assert.NotNil(t, wrapper.Bytes())
	assert.Equal(t, l, wrapper.Unwrap())
}

func TestNetworkSenderNoopDefault(t *testing.T) {
	svc := newTestLedgerService(t)
	a := New(Config{
		LedgerService: svc,
	})

	// Network operations should not panic with noop sender
	assert.NoError(t, a.BroadcastProposal(&consensus.Proposal{}))
	assert.NoError(t, a.BroadcastValidation(&consensus.Validation{}))
	assert.NoError(t, a.RelayProposal(&consensus.Proposal{}, 0))
	assert.NoError(t, a.RelayValidation(&consensus.Validation{}, 0))
	assert.NoError(t, a.RequestTxSet(consensus.TxSetID{}))
	assert.NoError(t, a.RequestLedger(consensus.LedgerID{}))
}

// TestAdaptor_OnLedgerFullyValidated_FlipsValidatedLedger covers the
// quorum-gate end-to-end at the adaptor layer:
// the consensus engine's ValidationTracker fires
// adaptor.OnLedgerFullyValidated(hash, seq) when trusted-validation
// quorum is met, and the adaptor then advances the ledger service's
// validatedLedger pointer iff the local ledger at that seq matches
// the quorum-validated hash.
func TestAdaptor_OnLedgerFullyValidated_FlipsValidatedLedger(t *testing.T) {
	a := newTestAdaptor(t)

	// Drive a closed ledger so we have something in history beyond
	// genesis. AcceptLedger advances closed and (in standalone mode)
	// validated to the same ledger.
	svc := a.ledgerService
	closedSeq, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	require.Equal(t, closedSeq, closed.Sequence())

	// Re-stage so validatedLedger is genesis (the standalone-init
	// auto-validate path is independent of our quorum gate). For the
	// purposes of this test we want to verify that calling
	// OnLedgerFullyValidated explicitly flips validatedLedger.
	// The ledger is already in history, so the fork-guarded
	// SetValidatedLedger inside OnLedgerFullyValidated should accept
	// it as a match for closedSeq + closedHash.
	a.OnLedgerFullyValidated(consensus.LedgerID(closed.Hash()), closedSeq)

	got := svc.GetValidatedLedger()
	require.NotNil(t, got)
	assert.Equal(t, closedSeq, got.Sequence(),
		"OnLedgerFullyValidated should advance validated_ledger to the quorum-validated seq")
	assert.Equal(t, closed.Hash(), got.Hash(),
		"OnLedgerFullyValidated should advance to the matching hash")
}

// TestAdaptor_OnLedgerFullyValidated_HashMismatchIsNoop verifies the
// fork guard: if the engine signals quorum on a hash that doesn't
// match what we have at that seq locally, we must NOT silently flip
// validated_ledger to the wrong-fork ledger we hold. Mirrors
// rippled's checkAccept which works off the ledger pointer (hash +
// seq), not seq alone.
func TestAdaptor_OnLedgerFullyValidated_HashMismatchIsNoop(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService

	// Get to seq=2 via standalone close; capture the existing
	// validated state so we can prove it didn't move.
	closedSeq, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)
	priorValidated := svc.GetValidatedLedger()
	require.NotNil(t, priorValidated)

	// Construct a deliberately-different hash at the same seq.
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	var foreignHash [32]byte
	closedHash := closed.Hash()
	copy(foreignHash[:], closedHash[:])
	foreignHash[0] ^= 0xFF

	a.OnLedgerFullyValidated(consensus.LedgerID(foreignHash), closedSeq)

	// validatedLedger must still be the previous one — we don't have
	// the foreign hash in history, so the fork guard must reject.
	after := svc.GetValidatedLedger()
	require.NotNil(t, after)
	assert.Equal(t, priorValidated.Hash(), after.Hash(),
		"validated_ledger must not flip to a hash we don't hold")
}

// TestGetParentLedgerForReplay_RejectsOpenLedger guards against the
// replay-delta live-lock that bit goxrpl-1 in the consensus enclave.
// When LCL is at seq N-1 and openLedger is at seq N, asking for the
// parent of seq N+1 must NOT return openLedger — openLedger has
// header.Hash == [32]byte{} until Close(), so callers using the
// returned ledger's hash as a chain anchor (the replay-delta
// verifier in particular) would receive 0x000...
func TestGetParentLedgerForReplay_RejectsOpenLedger(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.LedgerService()

	// LCL is at the genesis seq after Start. Accept once so closedLedger
	// is at seq 2 and openLedger advances to seq 3.
	_, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)
	closedSeq := svc.GetClosedLedgerIndex()
	openSeq := svc.GetCurrentLedgerIndex()
	require.Equal(t, closedSeq+1, openSeq,
		"openLedger should be one above LCL")

	// Asking for parent of openSeq+1 → parent should be at openSeq,
	// which is the OPEN ledger. The fix returns nil instead of an
	// unclosed ledger whose Hash() is zero.
	parent := a.GetParentLedgerForReplay(openSeq + 1)
	if parent != nil {
		assert.NotEqual(t, [32]byte{}, parent.Hash(),
			"parent returned for replay must have a non-zero hash; "+
				"openLedger has zero Hash until Close() and is unsafe "+
				"to use as a chain anchor")
	}
}

// stubLedger is a minimal consensus.Ledger whose CloseTime, ID, and parent
// link are fully controllable by tests. CloseTime drives the FULL-promote
// recency window; ID lets us exercise the peer-LCL-disagrees gate; parentID
// backs the preferredLCL ancestor resolution.
type stubLedger struct {
	id        consensus.LedgerID
	seq       uint32
	parentID  consensus.LedgerID
	closeTime time.Time
}

func (s stubLedger) ID() consensus.LedgerID       { return s.id }
func (s stubLedger) Seq() uint32                  { return s.seq }
func (s stubLedger) ParentID() consensus.LedgerID { return s.parentID }
func (s stubLedger) CloseTime() time.Time         { return s.closeTime }
func (s stubLedger) TxSetID() consensus.TxSetID   { return consensus.TxSetID{} }
func (s stubLedger) Bytes() []byte                { return nil }

// TestOnConsensusReached_AutoPromote pins the rippled-faithful
// endConsensus auto-promote (NetworkOPs.cpp:2197-2213) that lets a
// fresh genesis bootstrap escape OpModeConnected. Without this, the
// only paths to OpModeTracking require a peer ahead of us — which is
// impossible on a clean network start where everyone is at genesis.
//
// Mirrors:
//
//	CONNECTED | SYNCING  → TRACKING
//	CONNECTED | TRACKING → FULL  (if now < closeTime + 2 * resolution)
func TestOnConsensusReached_AutoPromote(t *testing.T) {
	t.Run("connected_with_recent_close_promotes_to_full", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeConnected)
		l := stubLedger{seq: 3, closeTime: a.Now()}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
	})

	t.Run("syncing_with_stale_close_promotes_only_to_tracking", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeSyncing)
		// CloseTime far in the past — outside the 2*resolution window.
		l := stubLedger{seq: 3, closeTime: a.Now().Add(-10 * time.Minute)}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
	})

	t.Run("tracking_with_recent_close_promotes_to_full", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeTracking)
		l := stubLedger{seq: 3, closeTime: a.Now()}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
	})

	t.Run("disconnected_does_not_promote", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeDisconnected)
		l := stubLedger{seq: 3, closeTime: a.Now()}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode(),
			"Disconnected must stay Disconnected — no peers means no consensus, "+
				"and a stale lingering callback must not bypass the peer-count gate")
	})

	t.Run("full_stays_full", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeFull)
		l := stubLedger{seq: 3, closeTime: a.Now().Add(-10 * time.Minute)}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode(),
			"once Full, OnConsensusReached must not demote — demotions are "+
				"driven by wrongLedger/peer-disconnect paths, not by close-time freshness")
	})

	// peer_lcl_disagreement pins the rippled `!ledgerChange` gate
	// (NetworkOPs.cpp:2192, 2203): when peer-reported LCLs majority-
	// disagree with the just-built ledger, promotion is deferred so we
	// don't advertise FULL while seated on a contested chain.
	t.Run("peer_lcl_disagreement_defers_promote", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeConnected)
		ourLCL := consensus.LedgerID{0xAA}
		theirLCL := consensus.LedgerID{0xBB}
		a.UpdatePeerLCL(1, theirLCL)
		a.UpdatePeerLCL(2, theirLCL)
		a.UpdatePeerLCL(3, ourLCL)
		l := stubLedger{id: ourLCL, seq: 3, closeTime: a.Now()}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode(),
			"majority-disagreeing peer LCLs must defer promotion (proxy for rippled !ledgerChange)")
	})

	// no_peer_lcl_data falls through and promotes — the conservative
	// fall-through ensures fresh-bootstrap (no TMStatusChange messages
	// yet seen) still escapes OpModeConnected.
	t.Run("no_peer_lcl_data_still_promotes", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeConnected)
		l := stubLedger{id: consensus.LedgerID{0xAA}, seq: 3, closeTime: a.Now()}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode(),
			"with no peer LCL evidence, promotion proceeds — the genesis-bootstrap path")
	})

	// peer_lcl_majority_agrees promotes normally.
	t.Run("peer_lcl_majority_agrees_promotes", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeConnected)
		ourLCL := consensus.LedgerID{0xAA}
		theirLCL := consensus.LedgerID{0xBB}
		a.UpdatePeerLCL(1, ourLCL)
		a.UpdatePeerLCL(2, ourLCL)
		a.UpdatePeerLCL(3, theirLCL)
		l := stubLedger{id: ourLCL, seq: 3, closeTime: a.Now()}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode(),
			"peer LCL majority agreement permits promotion")
	})

	// trusted_preferred_overrides_peer_agreement is the core of #611:
	// trusted validations weighted through the ancestry trie take
	// priority over a raw peer-LCL majority (getPreferredLCL:941-946).
	// Even though every peer agrees with our ledger, a trusted preferred
	// LCL on a different chain must defer promotion.
	t.Run("trusted_preferred_overrides_peer_agreement", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeConnected)
		ourLCL := consensus.LedgerID{0xAA}
		theirLCL := consensus.LedgerID{0xBB}
		a.UpdatePeerLCL(1, ourLCL)
		a.UpdatePeerLCL(2, ourLCL)
		a.SetValidationHistorian(&stubHistorian{
			preferredID:  theirLCL,
			preferredSeq: 3,
			preferredOK:  true,
		})
		l := stubLedger{id: ourLCL, seq: 3, closeTime: a.Now()}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode(),
			"trusted-validation preferred LCL on a different chain must defer promotion despite peer agreement")
	})

	// trusted_preferred_overrides_peer_disagreement is the inverse: peers
	// majority-disagree, but the trusted preferred LCL is our own ledger,
	// so promotion proceeds — the trie's verdict supersedes peer counts.
	t.Run("trusted_preferred_overrides_peer_disagreement", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeConnected)
		ourLCL := consensus.LedgerID{0xAA}
		theirLCL := consensus.LedgerID{0xBB}
		a.UpdatePeerLCL(1, theirLCL)
		a.UpdatePeerLCL(2, theirLCL)
		a.UpdatePeerLCL(3, theirLCL)
		a.SetValidationHistorian(&stubHistorian{
			preferredID:  ourLCL,
			preferredSeq: 3,
			preferredOK:  true,
		})
		l := stubLedger{id: ourLCL, seq: 3, closeTime: a.Now()}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode(),
			"trusted-validation preferred LCL matching ours permits promotion despite peer disagreement")
	})

	// trusted_ancestor_sticks_with_current mirrors rippled's
	// "Parent of preferred → stick with ledger" (Validations_test.cpp:840-845
	// / Validations.h:881-898): when the trie's preferred tip is behind our
	// just-closed ledger AND on our own chain (our parent — typical right
	// after close, before trusted validations for our seq land),
	// getPreferredLCL returns our own LCL, so promotion must proceed.
	t.Run("trusted_ancestor_sticks_with_current", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeConnected)
		ourLCL := consensus.LedgerID{0xAA}
		parentLCL := consensus.LedgerID{0x99}
		a.UpdatePeerLCL(1, ourLCL)
		a.UpdatePeerLCL(2, ourLCL)
		a.SetValidationHistorian(&stubHistorian{
			preferredID:  parentLCL,
			preferredSeq: 2,
			preferredOK:  true,
		})
		l := stubLedger{id: ourLCL, seq: 3, parentID: parentLCL, closeTime: a.Now()}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode(),
			"a preferred LCL that is our own parent is not a switch — promotion must proceed")
	})

	// trusted_lower_seq_fork_defers is the rippled counterpart
	// (Validations.h:892-895): a preferred tip at a LOWER seq on a chain we
	// cannot place in our ancestry IS a switch — the old seq>=ourSeq gate
	// wrongly promoted onto a minority fork here.
	t.Run("trusted_lower_seq_fork_defers", func(t *testing.T) {
		a := newTestAdaptor(t)
		a.SetOperatingMode(consensus.OpModeConnected)
		ourLCL := consensus.LedgerID{0xAA}
		forkLCL := consensus.LedgerID{0xBB}
		a.SetValidationHistorian(&stubHistorian{
			preferredID:  forkLCL,
			preferredSeq: 2,
			preferredOK:  true,
		})
		l := stubLedger{id: ourLCL, seq: 3, parentID: consensus.LedgerID{0x99}, closeTime: a.Now()}
		a.OnConsensusReached(l, nil, 0)
		assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode(),
			"a lower-seq preferred tip on a different chain is a switch — promotion must defer")
	})
}

// TestNew_SeedsRestartValidationFloor pins the boot-time seeding of the
// anti-double-sign floor (rippled setMaxDisallowedLedger,
// Application.cpp:2170): a validator whose relational DB already holds
// ledgers must record the max persisted seq at construction so the
// engine never re-signs a pre-restart sequence. Non-validators skip the
// read (rippled gates on validatorKeys_.keys).
func TestNew_SeedsRestartValidationFloor(t *testing.T) {
	ctx := context.Background()

	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { _ = rm.Close(ctx) })

	// Persist ledgers up to seq 742 as a prior run would have.
	for _, seq := range []relationaldb.LedgerIndex{740, 742, 741} {
		info := &relationaldb.LedgerInfo{Sequence: seq}
		info.Hash[0] = byte(seq)
		require.NoError(t, rm.Ledger().SaveValidatedLedger(ctx, info, true))
	}

	cfg := service.DefaultConfig()
	cfg.Standalone = true
	cfg.GenesisConfig = genesis.DefaultConfig()
	cfg.RelationalDB = rm
	svc, err := service.New(cfg)
	require.NoError(t, err)

	require.Equal(t, uint32(742), svc.MaxPersistedLedgerSeq(ctx))

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	a := New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	assert.Equal(t, uint32(742), a.GetMaxDisallowedLedgerSeq(),
		"validator must seed the restart floor from the persisted tip")

	observer := New(Config{LedgerService: svc})
	assert.Equal(t, uint32(0), observer.GetMaxDisallowedLedgerSeq(),
		"non-validators never emit, so the floor stays 0")
}
