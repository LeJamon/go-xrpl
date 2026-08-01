package adaptor

import (
	"bytes"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/negativeunlvote"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
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
	seq    uint32
	hashes [][32]byte
	err    error
}

func (s *stubSkipListProvider) Sequence() uint32 {
	return s.seq
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

	// keepLow/keepHigh record the last SetSeqToKeep call so tests can
	// assert the negative-UNL vote pins its scan window before reading.
	keepLow  uint32
	keepHigh uint32
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

func (s *stubHistorian) SetSeqToKeep(low, high uint32) {
	s.keepLow, s.keepHigh = low, high
}

func (s *stubHistorian) GetJSONTrie() map[string]any { return nil }

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

func TestAdaptor_NegativeUNL_ExactThresholdLogsError(t *testing.T) {
	a := newTestAdaptor(t)
	var output bytes.Buffer
	a.logger = slog.New(slog.NewTextHandler(&output, nil))

	a.logNegativeUNLVoteError(1024, fmt.Errorf("wrapped: %w", negativeunlvote.ErrLocalCountAtThreshold))

	assert.Contains(t, output.String(), "level=ERROR")
	assert.Contains(t, output.String(), "equals strict voting threshold")
	assert.Contains(t, output.String(), "prev_seq=1024")
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
		DisabledValidators: []pseudo.DisabledValidator{
			{PublicKey: master1[:], FirstLedgerSequence: 256},
			{PublicKey: master2[:], FirstLedgerSequence: 512},
		},
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
	)
	assert.False(t, ok, "skip-list shorter than FlagLedgerInterval must abort")
	assert.Nil(t, scoreTable)
}

func TestBuildScoreTable_DoesNotGateOnLocalParticipation(t *testing.T) {
	a := newTestAdaptor(t)

	ancestors := make([][32]byte, protocol.FlagLedgerInterval)
	for i := range ancestors {
		ancestors[i] = [32]byte{byte(i), 0xAB}
	}

	myID := consensus.NodeID{0x99}
	otherID := consensus.NodeID{0xAA}

	const localCount = protocol.FlagLedgerInterval / 2
	byLedger := make(map[consensus.LedgerID][]*consensus.Validation, len(ancestors))
	for i, h := range ancestors {
		vals := []*consensus.Validation{{NodeID: otherID, LedgerID: consensus.LedgerID(h)}}
		if uint32(i) < localCount {
			vals = append(vals, &consensus.Validation{NodeID: myID, LedgerID: consensus.LedgerID(h)})
		}
		byLedger[consensus.LedgerID(h)] = vals
	}

	scoreTable, ok := a.buildNegativeUNLScoreTable(
		&stubSkipListProvider{seq: 2 * protocol.FlagLedgerInterval, hashes: ancestors},
		&stubHistorian{byLedger: byLedger},
	)
	require.True(t, ok, "a full skip-list must build a table regardless of local participation")
	require.NotNil(t, scoreTable)
	assert.Equal(t, localCount, scoreTable[myID])
}

func TestBuildScoreTable_TalliesAcrossAncestors(t *testing.T) {
	a := newTestAdaptor(t)

	ancestors := make([][32]byte, protocol.FlagLedgerInterval)
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

	const prevSeq = 2 * protocol.FlagLedgerInterval // a flag ledger
	hist := &stubHistorian{byLedger: byLedger}
	scoreTable, ok := a.buildNegativeUNLScoreTable(
		&stubSkipListProvider{seq: prevSeq, hashes: ancestors},
		hist,
	)
	require.True(t, ok, "a full skip-list of FlagLedgerInterval ancestors builds the table")
	require.NotNil(t, scoreTable)

	assert.Equal(t, protocol.FlagLedgerInterval, scoreTable[myID], "local validator scored on every ancestor")
	assert.Equal(t, uint32(50), scoreTable[offline], "offline validator scored only on first 50 ancestors")

	// The vote must pin the scan window before reading so a concurrent
	// ExpireOld can't prune its low end. Window is [prevSeq-interval,
	// prevSeq+1+interval) with upcoming = prevSeq+1.
	upcoming := prevSeq + 1
	assert.Equal(t, upcoming-1-protocol.FlagLedgerInterval, hist.keepLow, "keep-range low pins the oldest scanned ledger")
	assert.Equal(t, upcoming+protocol.FlagLedgerInterval, hist.keepHigh, "keep-range high mirrors rippled's forward window")
}

// At the first flag ledger the parent seq is below FlagLedgerInterval, so the
// pin's widened low end would underflow uint32; it must saturate to 0 (keeping
// the whole window) rather than wrap and self-clear. The pin is also set
// before the skip-list length check, so an incomplete skip-list that can't
// build a table still records the keep-range — as rippled does, calling
// setSeqToKeep before its ancestor-count gate.
func TestBuildScoreTable_PinLowSaturatesAtFirstFlagLedger(t *testing.T) {
	a := newTestAdaptor(t)
	hist := &stubHistorian{}

	const prevSeq = protocol.FlagLedgerInterval - 1 // parent of the first flag ledger
	_, ok := a.buildNegativeUNLScoreTable(
		&stubSkipListProvider{seq: prevSeq}, // empty skip-list → no table
		hist,
	)
	require.False(t, ok, "an incomplete skip-list cannot build a table")

	assert.Equal(t, uint32(0), hist.keepLow, "pin low saturates to 0, not a uint32 underflow")
	assert.Equal(t, prevSeq+1+protocol.FlagLedgerInterval, hist.keepHigh, "pin high is still upcoming+interval")
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

func TestAdaptor_OnUNLChange_GracePeriodAndExpiry(t *testing.T) {
	a := newTestAdaptorWithMasters(t)
	require.NotNil(t, a.negUNLVoter, "fixture must construct a voter")
	voter := a.negUNLVoter
	myKey := a.identity.MasterKey

	stable1 := makeRawMasterKey(0xAA)
	stable2 := makeRawMasterKey(0xBB)
	fresh1 := makeRawMasterKey(0xCC)
	fresh2 := makeRawMasterKey(0xDD)
	unl := [][33]byte{myKey, stable1, stable2, fresh1, fresh2}

	fresh1NodeID := consensus.CalcNodeID(fresh1)
	fresh2NodeID := consensus.CalcNodeID(fresh2)

	scoreTable := map[consensus.NodeID]uint32{
		a.identity.NodeID:             protocol.FlagLedgerInterval,
		consensus.CalcNodeID(stable1): protocol.FlagLedgerInterval,
		consensus.CalcNodeID(stable2): protocol.FlagLedgerInterval,
		fresh1NodeID:                  0,
		fresh2NodeID:                  0,
	}

	const addedAtSeq uint32 = 256

	a.OnUNLChange(addedAtSeq, []consensus.NodeID{fresh1NodeID, fresh2NodeID})

	prevHash := [32]byte{0x42}
	blobs, err := voter.DoVoting(addedAtSeq, prevHash, unl, negativeunlvote.State{}, scoreTable)
	require.NoError(t, err)
	assert.Nil(t, blobs, "fresh validators within the grace window must not be ToDisable candidates")

	expiredPrevSeq := addedAtSeq + 2*protocol.FlagLedgerInterval
	blobs, err = voter.DoVoting(expiredPrevSeq, prevHash, unl, negativeunlvote.State{}, scoreTable)
	require.NoError(t, err)
	require.Len(t, blobs, 1, "after grace expiry, a bad-score new validator is eligible for a single ToDisable pseudo-tx")
}

func TestAdaptor_OnUNLChange_EmptyTrustedSetIsNoOp(t *testing.T) {
	a := newTestAdaptorWithMasters(t)
	require.NotNil(t, a.negUNLVoter)
	a.OnUNLChange(512, nil)
	a.OnUNLChange(512, []consensus.NodeID{})

	weak := makeRawMasterKey(0xEE)
	unl := [][33]byte{a.identity.MasterKey, makeRawMasterKey(0xAA), makeRawMasterKey(0xBB), weak}
	scores := map[consensus.NodeID]uint32{
		a.identity.NodeID:            protocol.FlagLedgerInterval,
		consensus.CalcNodeID(unl[1]): protocol.FlagLedgerInterval,
		consensus.CalcNodeID(unl[2]): protocol.FlagLedgerInterval,
		consensus.CalcNodeID(weak):   0,
	}
	blobs, err := a.negUNLVoter.DoVoting(512, [32]byte{0x42}, unl, negativeunlvote.State{}, scores)
	require.NoError(t, err)
	require.Len(t, blobs, 1)
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
