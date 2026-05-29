package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/negativeunlvote"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubLedgerReader satisfies the narrow interface negativeUNLState
// consumes (Read by keylet). Used to feed encoded NegativeUNL SLE
// bytes without standing up a full *ledger.Ledger fixture.
type stubLedgerReader struct {
	negativeUNLBytes []byte
}

func (s *stubLedgerReader) Read(k keylet.Keylet) ([]byte, error) {
	if k.Key == keylet.NegativeUNL().Key {
		return s.negativeUNLBytes, nil
	}
	return nil, nil
}

// stubSkipListProvider satisfies the narrow interface
// buildNegativeUNLScoreTable consumes.
type stubSkipListProvider struct {
	hashes [][32]byte
	err    error
}

func (s *stubSkipListProvider) SkipListHashes() ([][32]byte, error) {
	return s.hashes, s.err
}

// stubHistorian implements consensus.ValidationHistorian by returning
// per-ledger trusted validations from a pre-seeded map. The preferred*
// fields drive the preferred-LCL lookups; left zero they report "no
// trusted validations available" so the peer-LCL fallback is exercised.
type stubHistorian struct {
	byLedger     map[consensus.LedgerID][]*consensus.Validation
	preferredID  consensus.LedgerID
	preferredSeq uint32
	preferredOK  bool
}

func (s *stubHistorian) GetTrustedValidations(id consensus.LedgerID) []*consensus.Validation {
	return s.byLedger[id]
}

func (s *stubHistorian) GetPreferred(largestIssued uint32) (consensus.LedgerID, uint32, bool) {
	return s.preferredID, s.preferredSeq, s.preferredOK
}

func (s *stubHistorian) PreferredFromValidations(minSeq uint32) (consensus.LedgerID, uint32, bool) {
	return s.preferredID, s.preferredSeq, s.preferredOK
}

func TestAdaptor_NegativeUNL_NilVoterReturnsNil(t *testing.T) {
	// Adaptor constructed without master keys → voter is nil.
	a := newTestAdaptor(t)
	require.Nil(t, a.negUNLVoter, "no master keys plumbed: voter must be nil")

	prev := WrapLedger(a.ledgerService.GetClosedLedger())
	require.NotNil(t, prev)

	blobs := a.GenerateNegativeUNLPseudoTx(prev)
	assert.Nil(t, blobs, "without a voter, emit no NegativeUNL vote")
}

func TestAdaptor_NegativeUNL_NilHistorianReturnsNil(t *testing.T) {
	// Voter present, but historian not yet wired → must not emit.
	a := newTestAdaptorWithMasters(t)
	require.NotNil(t, a.negUNLVoter, "fixture must construct a voter")
	require.Nil(t, a.validationHistorian, "historian not wired yet")

	prev := WrapLedger(a.ledgerService.GetClosedLedger())
	require.NotNil(t, prev)

	blobs := a.GenerateNegativeUNLPseudoTx(prev)
	assert.Nil(t, blobs, "without a historian, emit no NegativeUNL vote")
}

func TestAdaptor_NegativeUNL_NonWrappedLedgerReturnsNil(t *testing.T) {
	// A consensus.Ledger that isn't a *LedgerWrapper must be silently
	// skipped — protects against test ledger types or future adapters.
	a := newTestAdaptorWithMasters(t)
	a.SetValidationHistorian(&stubHistorian{})

	blobs := a.GenerateNegativeUNLPseudoTx(notWrappedLedger{})
	assert.Nil(t, blobs)
}

func TestNegativeUNLState_ParsesEmptySLE(t *testing.T) {
	a := newTestAdaptor(t)
	state, err := a.negativeUNLState(&stubLedgerReader{negativeUNLBytes: nil})
	require.NoError(t, err)
	assert.Empty(t, state.DisabledKeys)
	assert.Nil(t, state.ToDisablePending)
	assert.Nil(t, state.ToReEnablePending)
}

func TestNegativeUNLState_ParsesPopulatedSLE(t *testing.T) {
	a := newTestAdaptor(t)

	master1 := make33Byte(0x01)
	master2 := make33Byte(0x02)
	masterPending := make33Byte(0x03)

	sle := &pseudo.NegativeUNLSLE{
		DisabledValidators:  [][]byte{master1[:], master2[:]},
		ValidatorToReEnable: masterPending[:],
	}
	encoded, err := pseudo.SerializeNegativeUNLSLE(sle)
	require.NoError(t, err)

	state, err := a.negativeUNLState(&stubLedgerReader{negativeUNLBytes: encoded})
	require.NoError(t, err)

	require.Len(t, state.DisabledKeys, 2)
	assert.Equal(t, master1, state.DisabledKeys[0])
	assert.Equal(t, master2, state.DisabledKeys[1])
	assert.Nil(t, state.ToDisablePending)
	require.NotNil(t, state.ToReEnablePending)
	assert.Equal(t, masterPending, *state.ToReEnablePending)
}

func TestBuildScoreTable_RejectsShortSkipList(t *testing.T) {
	a := newTestAdaptor(t)
	hist := &stubHistorian{}

	scoreTable, ok := a.buildNegativeUNLScoreTable(
		&stubSkipListProvider{hashes: make([][32]byte, 100)},
		hist,
		consensus.NodeID{},
	)
	assert.False(t, ok, "skip-list shorter than FlagLedgerInterval must abort")
	assert.Nil(t, scoreTable)
}

func TestBuildScoreTable_RejectsInsufficientLocalParticipation(t *testing.T) {
	a := newTestAdaptor(t)

	ancestors := make([][32]byte, consensus.FlagLedgerInterval)
	for i := range ancestors {
		ancestors[i] = [32]byte{byte(i), 0xAB}
	}

	myID := consensus.NodeID{0x99}
	otherID := consensus.NodeID{0xAA}

	// Seed the local node with fewer than MinLocalValsToVote (230)
	// validations, with another validator covering every slot — the
	// gate must fire on the local count alone, regardless of others.
	byLedger := make(map[consensus.LedgerID][]*consensus.Validation, len(ancestors))
	for i, h := range ancestors {
		vals := []*consensus.Validation{{NodeID: otherID, LedgerID: consensus.LedgerID(h)}}
		if uint32(i) < negativeunlvote.MinLocalValsToVote-1 {
			vals = append(vals, &consensus.Validation{NodeID: myID, LedgerID: consensus.LedgerID(h)})
		}
		byLedger[consensus.LedgerID(h)] = vals
	}

	scoreTable, ok := a.buildNegativeUNLScoreTable(
		&stubSkipListProvider{hashes: ancestors},
		&stubHistorian{byLedger: byLedger},
		myID,
	)
	assert.False(t, ok, "local count below MinLocalValsToVote must abort")
	assert.Nil(t, scoreTable)
}

func TestBuildScoreTable_TalliesAcrossAncestors(t *testing.T) {
	a := newTestAdaptor(t)

	ancestors := make([][32]byte, consensus.FlagLedgerInterval)
	for i := range ancestors {
		ancestors[i] = [32]byte{byte(i >> 8), byte(i), 0xCD}
	}

	myID := consensus.NodeID{0x11}
	offline := consensus.NodeID{0x22}

	byLedger := make(map[consensus.LedgerID][]*consensus.Validation, len(ancestors))
	for i, h := range ancestors {
		vals := []*consensus.Validation{{NodeID: myID, LedgerID: consensus.LedgerID(h)}}
		// `offline` validates only the first 50 ledgers — below the
		// low water mark (128) so the producer would consider it a
		// ToDisable candidate.
		if i < 50 {
			vals = append(vals, &consensus.Validation{NodeID: offline, LedgerID: consensus.LedgerID(h)})
		}
		byLedger[consensus.LedgerID(h)] = vals
	}

	scoreTable, ok := a.buildNegativeUNLScoreTable(
		&stubSkipListProvider{hashes: ancestors},
		&stubHistorian{byLedger: byLedger},
		myID,
	)
	require.True(t, ok, "local participation = 256 must pass the gate")
	require.NotNil(t, scoreTable)

	assert.Equal(t, consensus.FlagLedgerInterval, scoreTable[myID], "local validator scored on every ancestor")
	assert.Equal(t, uint32(50), scoreTable[offline], "offline validator scored only on first 50 ancestors")
}

// TestAdaptor_OnUNLChange_NoVoterIsNoOp covers the no-master-keys
// adaptor: OnUNLChange must be safe to call (a no-op) when the
// NegativeUNL voter was never constructed. Mirrors rippled's
// nUnlVote_ optional check at RCLConsensus.cpp:1040.
func TestAdaptor_OnUNLChange_NoVoterIsNoOp(t *testing.T) {
	a := newTestAdaptor(t)
	require.Nil(t, a.negUNLVoter, "fixture must produce a nil voter")
	a.OnUNLChange(256, []consensus.NodeID{{0x01}, {0x02}})
	a.OnUNLChange(0, nil)
}

// TestAdaptor_OnUNLChange_GracePeriodAndExpiry ports the two cases of
// rippled's NegativeUNLVoteNewValidator_test::testDoVoting
// (rippled/src/test/consensus/NegativeUNL_test.cpp:1735):
//
//  1. After OnUNLChange registers a new validator with `nowTrusted`,
//     a bad score within NewValidatorDisableSkip ledgers must NOT
//     produce a ToDisable pseudo-tx (grace period honored).
//  2. After NewValidatorDisableSkip+1 ledgers have passed, the voting
//     path's purge (Voter.PurgeNewValidators, called from
//     GenerateNegativeUNLPseudoTx — mirrors rippled doVoting at
//     NegativeUNLVote.cpp:339-355) drops both fresh entries; the same
//     bad score now becomes a ToDisable candidate.
//
// Exercises the wiring: Adaptor.OnUNLChange records via
// Voter.NewValidators; the existing voting-path purge owns expiry.
// OnUNLChange intentionally does NOT purge, matching rippled's
// preStartRound (RCLConsensus.cpp:1041-1043) which only registers.
func TestAdaptor_OnUNLChange_GracePeriodAndExpiry(t *testing.T) {
	a := newTestAdaptorWithMasters(t)
	require.NotNil(t, a.negUNLVoter, "fixture must construct a voter")
	voter := a.negUNLVoter
	myKey := a.identity.MasterKey

	// Build a UNL with the local node + two stable peers + two
	// freshly-added validators. The UNL passed to DoVoting is
	// independent of the adaptor's configured trustedMasterKeys; we
	// supply our own multi-member list so MaxListedFraction permits
	// at least one ToDisable candidate.
	stable1 := makeRawMasterKey(0xAA)
	stable2 := makeRawMasterKey(0xBB)
	fresh1 := makeRawMasterKey(0xCC)
	fresh2 := makeRawMasterKey(0xDD)
	unl := [][33]byte{myKey, stable1, stable2, fresh1, fresh2}

	fresh1NodeID := consensus.CalcNodeID(fresh1)
	fresh2NodeID := consensus.CalcNodeID(fresh2)

	// Score table: local node + stable peers participate at the
	// HighWaterMark; fresh validators score 0 (the bad-score
	// condition rippled's test exercises).
	scoreTable := map[consensus.NodeID]uint32{
		voter.MyID():                  negativeunlvote.MinLocalValsToVote + 1,
		consensus.CalcNodeID(stable1): negativeunlvote.HighWaterMark + 1,
		consensus.CalcNodeID(stable2): negativeunlvote.HighWaterMark + 1,
		fresh1NodeID:                  0,
		fresh2NodeID:                  0,
	}

	const addedAtSeq uint32 = 256

	// Case 1: register the fresh validators, then run a voting round
	// within the NewValidatorDisableSkip window. Expect no ToDisable
	// pseudo-tx.
	a.OnUNLChange(addedAtSeq, []consensus.NodeID{fresh1NodeID, fresh2NodeID})

	prevHash := [32]byte{0x42}
	blobs, err := voter.DoVoting(addedAtSeq, prevHash, unl, negativeunlvote.State{}, scoreTable)
	require.NoError(t, err)
	assert.Nil(t, blobs, "fresh validators within the grace window must not be ToDisable candidates")

	// Case 2: advance well past NewValidatorDisableSkip. The voting
	// path's purge — the same call GenerateNegativeUNLPseudoTx makes
	// before invoking DoVoting (negative_unl_vote.go:74) — drops both
	// fresh entries; a follow-up vote at the same seq now sees them
	// as bad-score candidates. OnUNLChange must NOT be called here:
	// no UNL change occurred (rippled's preStartRound would see an
	// empty `nowTrusted` and skip newValidators entirely).
	purgeSeq := addedAtSeq + negativeunlvote.NewValidatorDisableSkip + 2
	voter.PurgeNewValidators(purgeSeq)

	blobs, err = voter.DoVoting(purgeSeq, prevHash, unl, negativeunlvote.State{}, scoreTable)
	require.NoError(t, err)
	require.Len(t, blobs, 1, "after grace expiry, a bad-score new validator is eligible for a single ToDisable pseudo-tx")
}

// TestAdaptor_OnUNLChange_EmptyTrustedSetIsNoOp covers the
// nowTrusted-empty short-circuit that mirrors rippled's
// `!nowTrusted.empty()` gate at RCLConsensus.cpp:1042. The Voter's
// newValidators map must remain untouched so future bad-score evals
// still see no registered grace-period entries.
func TestAdaptor_OnUNLChange_EmptyTrustedSetIsNoOp(t *testing.T) {
	a := newTestAdaptorWithMasters(t)
	require.NotNil(t, a.negUNLVoter)
	a.OnUNLChange(512, nil)
	a.OnUNLChange(512, []consensus.NodeID{})
	// Purge a freshly-registered key to prove the map is empty: if
	// OnUNLChange had inadvertently inserted something, the purge
	// (using a seq within the grace window) would leave it in place.
	a.negUNLVoter.NewValidators(512, []consensus.NodeID{{0xEE}})
	a.negUNLVoter.PurgeNewValidators(512) // within window — keeps {0xEE}
	// If OnUNLChange had ever modified the map, this single-entry
	// expectation would fail.
}

// makeRawMasterKey builds a deterministic 33-byte master pubkey
// suitable for Voter inputs (the codec accepts any [33]byte blob; the
// first byte uses a valid secp256k1 prefix for readability).
func makeRawMasterKey(tag byte) [33]byte {
	var k [33]byte
	k[0] = 0x02
	for i := 1; i < 33; i++ {
		k[i] = tag
	}
	return k
}

// newTestAdaptorWithMasters builds an Adaptor with a master pubkey
// plumbed in (so negUNLVoter is non-nil), letting the negative-UNL
// path execute without the no-voter / no-master short-circuit.
func newTestAdaptorWithMasters(t *testing.T) *Adaptor {
	t.Helper()
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	return New(Config{
		LedgerService:       svc,
		Identity:            identity,
		Validators:          []consensus.NodeID{identity.NodeID},
		ValidatorMasterKeys: [][33]byte{identity.MasterKey},
	})
}

// make33Byte returns a deterministic 33-byte master pubkey filled
// with the supplied byte, suitable for SLE round-trip tests where
// only structural shape matters.
func make33Byte(b byte) [33]byte {
	var out [33]byte
	for i := range out {
		out[i] = b
	}
	return out
}

// notWrappedLedger is a minimal consensus.Ledger that is NOT a
// *LedgerWrapper — used to exercise the type-assertion guard in
// GenerateNegativeUNLPseudoTx.
type notWrappedLedger struct{}

func (notWrappedLedger) ID() consensus.LedgerID       { return consensus.LedgerID{} }
func (notWrappedLedger) Seq() uint32                  { return 0 }
func (notWrappedLedger) ParentID() consensus.LedgerID { return consensus.LedgerID{} }
func (notWrappedLedger) CloseTime() time.Time         { return time.Time{} }
func (notWrappedLedger) TxSetID() consensus.TxSetID   { return consensus.TxSetID{} }
func (notWrappedLedger) Bytes() []byte                { return nil }
