package negativeunlvote

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeKey produces a deterministic 33-byte master pubkey for a
// given byte tag. The first byte is a valid secp256k1 prefix (0x02
// or 0x03) so the codec accepts the field as a Blob; subsequent
// bytes are filled with the tag for human readability in failures.
func makeKey(tag byte) [33]byte {
	var k [33]byte
	k[0] = 0x02
	for i := 1; i < 33; i++ {
		k[i] = tag
	}
	return k
}

func nodeID(tag byte) consensus.NodeID {
	return consensus.CalcNodeID(makeKey(tag))
}

// fullScoreTable returns scoreTable[nid] = HighWaterMark+1 for every
// non-local key in keys, plus the local node at MinLocalValsToVote+1
// so DoVoting clears the local-participation gate by default. The
// local node is set unconditionally — including the case where myID
// is also a member of keys — so the helper is robust to call sites
// that include myKey in the UNL list.
func fullScoreTable(myID consensus.NodeID, keys [][33]byte) map[consensus.NodeID]uint32 {
	st := map[consensus.NodeID]uint32{
		myID: MinLocalValsToVote + 1,
	}
	for _, k := range keys {
		nid := keyToNodeID(k)
		if nid == myID {
			continue
		}
		st[nid] = HighWaterMark + 1
	}
	return st
}

func TestDoVoting_RefusesWhenLocalParticipationLow(t *testing.T) {
	myKey := makeKey(0xAA)
	other := makeKey(0xBB)
	v := NewVoter(keyToNodeID(myKey))

	scoreTable := map[consensus.NodeID]uint32{
		v.myID:             MinLocalValsToVote - 1, // below threshold
		keyToNodeID(other): 0,                      // would normally be disabled
	}

	blobs, err := v.DoVoting(1024, [32]byte{0xDE, 0xAD}, [][33]byte{myKey, other}, State{}, scoreTable)
	require.NoError(t, err)
	assert.Nil(t, blobs, "must not vote when local participation is below MinLocalValsToVote")
}

func TestDoVoting_NoCandidatesWhenAllParticipating(t *testing.T) {
	myKey := makeKey(0xAA)
	other := makeKey(0xBB)
	v := NewVoter(keyToNodeID(myKey))

	scoreTable := fullScoreTable(v.myID, [][33]byte{myKey, other})
	blobs, err := v.DoVoting(1024, [32]byte{0xDE, 0xAD}, [][33]byte{myKey, other}, State{}, scoreTable)
	require.NoError(t, err)
	assert.Nil(t, blobs, "no candidates → no tx")
}

func TestDoVoting_ToDisableWhenScoreLow(t *testing.T) {
	myKey := makeKey(0xAA)
	good := makeKey(0xBB)
	weak := makeKey(0xCC)
	v := NewVoter(keyToNodeID(myKey))
	unl := [][33]byte{myKey, good, weak}

	scoreTable := fullScoreTable(v.myID, unl)
	scoreTable[keyToNodeID(weak)] = LowWaterMark - 1 // unreliable

	blobs, err := v.DoVoting(1024, [32]byte{0x01}, unl, State{}, scoreTable)
	require.NoError(t, err)
	require.Len(t, blobs, 1, "expected one ToDisable pseudo-tx")

	tx := decodeTx(t, blobs[0])
	assert.EqualValues(t, 1, asUint(tx["UNLModifyDisabling"]),
		"sfUNLModifyDisabling must be 1 for ToDisable")
	assert.Equal(t, hex.EncodeToString(weak[:]),
		stringFold(tx["UNLModifyValidator"]),
		"validator field must be the weak validator's master key")
	assert.EqualValues(t, 1025, asUint(tx["LedgerSequence"]))
}

func TestDoVoting_ToReEnableWhenDisabledScoreHigh(t *testing.T) {
	myKey := makeKey(0xAA)
	good := makeKey(0xBB)
	recovered := makeKey(0xCC)
	v := NewVoter(keyToNodeID(myKey))
	unl := [][33]byte{myKey, good, recovered}

	scoreTable := fullScoreTable(v.myID, unl) // recovered's score is already > HighWaterMark
	state := State{
		DisabledKeys: [][33]byte{recovered}, // currently on negUNL
	}

	blobs, err := v.DoVoting(1024, [32]byte{0x02}, unl, state, scoreTable)
	require.NoError(t, err)
	require.Len(t, blobs, 1)

	tx := decodeTx(t, blobs[0])
	assert.EqualValues(t, 0, asUint(tx["UNLModifyDisabling"]),
		"sfUNLModifyDisabling must be 0 for ToReEnable")
	assert.Equal(t, hex.EncodeToString(recovered[:]),
		stringFold(tx["UNLModifyValidator"]))
}

func TestDoVoting_RespectsMaxListedFraction(t *testing.T) {
	// 4-validator UNL → 25% cap = ceil(1) = 1 listed allowed.
	// One already disabled, one weak: cap reached → no new ToDisable.
	myKey := makeKey(0xAA)
	good := makeKey(0xBB)
	weak := makeKey(0xCC)
	disabled := makeKey(0xDD)
	v := NewVoter(keyToNodeID(myKey))
	unl := [][33]byte{myKey, good, weak, disabled}

	scoreTable := fullScoreTable(v.myID, unl)
	scoreTable[keyToNodeID(weak)] = LowWaterMark - 1

	state := State{DisabledKeys: [][33]byte{disabled}}

	blobs, err := v.DoVoting(1024, [32]byte{0x03}, unl, state, scoreTable)
	require.NoError(t, err)
	for _, b := range blobs {
		tx := decodeTx(t, b)
		// Whatever blobs are returned must NOT be a ToDisable for `weak`.
		if asUint(tx["UNLModifyDisabling"]) == 1 {
			t.Fatalf("MaxListedFraction cap exceeded — got new ToDisable: %v", tx)
		}
	}
}

func TestDoVoting_NewValidatorSkip(t *testing.T) {
	myKey := makeKey(0xAA)
	good := makeKey(0xBB)
	freshWeak := makeKey(0xCC)
	v := NewVoter(keyToNodeID(myKey))
	unl := [][33]byte{myKey, good, freshWeak}

	upcomingSeq := uint32(1025)
	// Register freshWeak as new at seq=900; the upcoming seq=1025 is
	// within the NewValidatorDisableSkip window, so freshWeak is not
	// a ToDisable candidate even with a low score.
	v.NewValidators(900, []consensus.NodeID{keyToNodeID(freshWeak)})
	require.True(t, upcomingSeq-900 <= NewValidatorDisableSkip)

	scoreTable := fullScoreTable(v.myID, unl)
	scoreTable[keyToNodeID(freshWeak)] = LowWaterMark - 1

	blobs, err := v.DoVoting(upcomingSeq-1, [32]byte{0x04}, unl, State{}, scoreTable)
	require.NoError(t, err)
	assert.Nil(t, blobs, "new validator within skip window must not be a ToDisable candidate")
}

func TestDoVoting_RemovedFromUNL_ReEnableFallback(t *testing.T) {
	// A validator currently on the negUNL is no longer in the trusted
	// UNL — fallback path at NegativeUNLVote.cpp:309-318 must
	// re-enable it (so the negUNL doesn't keep a non-validator
	// listed forever).
	myKey := makeKey(0xAA)
	good := makeKey(0xBB)
	stale := makeKey(0xCC)
	v := NewVoter(keyToNodeID(myKey))
	unl := [][33]byte{myKey, good} // stale is NOT in the UNL
	scoreTable := fullScoreTable(v.myID, unl)

	state := State{DisabledKeys: [][33]byte{stale}}
	blobs, err := v.DoVoting(1024, [32]byte{0x05}, unl, state, scoreTable)
	require.NoError(t, err)
	require.Len(t, blobs, 1)

	tx := decodeTx(t, blobs[0])
	assert.EqualValues(t, 0, asUint(tx["UNLModifyDisabling"]),
		"fallback re-enable for validator no longer in UNL")
	assert.Equal(t, hex.EncodeToString(stale[:]),
		stringFold(tx["UNLModifyValidator"]))
}

func TestChoose_DeterministicAcrossCalls(t *testing.T) {
	candidates := []consensus.NodeID{nodeID(0x01), nodeID(0x02), nodeID(0x03)}
	pad := [32]byte{0xCA, 0xFE, 0xBA, 0xBE}

	first := choose(pad, candidates)
	for i := 0; i < 8; i++ {
		got := choose(pad, candidates)
		assert.Equal(t, first, got, "choose must be deterministic across repeated calls")
	}
}

func TestChoose_OrderIndependent(t *testing.T) {
	a, b, c := nodeID(0x01), nodeID(0x02), nodeID(0x03)
	pad := [32]byte{0x42}

	picked := choose(pad, []consensus.NodeID{a, b, c})
	pickedReordered := choose(pad, []consensus.NodeID{c, b, a})
	assert.Equal(t, picked, pickedReordered,
		"choose must be input-order-independent — every validator on the network must converge on the same pick")
}

// decodeTx round-trips the serialized blob back to a JSON-ish map
// for field-level assertions. Mirrors how rippled's wire receiver
// would parse the pseudo-tx; if this fails, the on-wire format is
// malformed.
func decodeTx(t *testing.T, blob []byte) map[string]any {
	t.Helper()
	out, err := binarycodec.Decode(hex.EncodeToString(blob))
	require.NoError(t, err, "serialized UNLModify must round-trip through binarycodec.Decode")
	return out
}

// asUint coerces a JSON-decoded numeric field to uint64. binarycodec.
// Decode returns small unsigned ints as float64 / uint via its codec
// definitions; we accept the common shapes.
func asUint(v any) uint64 {
	switch n := v.(type) {
	case uint8:
		return uint64(n)
	case uint16:
		return uint64(n)
	case uint32:
		return uint64(n)
	case uint64:
		return n
	case int:
		return uint64(n)
	case int64:
		return uint64(n)
	case float64:
		return uint64(n)
	case string:
		// uint64 may decode as a hex string under some codec paths.
		var x uint64
		for _, c := range n {
			x <<= 4
			switch {
			case c >= '0' && c <= '9':
				x |= uint64(c - '0')
			case c >= 'a' && c <= 'f':
				x |= uint64(c-'a') + 10
			case c >= 'A' && c <= 'F':
				x |= uint64(c-'A') + 10
			}
		}
		return x
	}
	return 0
}

func stringFold(v any) string {
	switch s := v.(type) {
	case string:
		// Lowercase hex for stable comparison.
		out := make([]byte, len(s))
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c >= 'A' && c <= 'F' {
				c = c - 'A' + 'a'
			}
			out[i] = c
		}
		return string(out)
	}
	return ""
}

// expectedChoose mirrors choose() byte-for-byte using the same
// NodeID-XOR-pad comparator. Used as the oracle for parity tests
// against rippled's NegativeUNLVote::choose at NegativeUNLVote.cpp:
// 142-161. consensus.NodeID is the 20-byte calcNodeID(masterKey)
// digest already, so the XOR is direct (no rehash). If choose's
// comparator drifts, these tests will catch the divergence.
func expectedChoose(pad [32]byte, cands []consensus.NodeID) consensus.NodeID {
	var bestKey [20]byte
	var best consensus.NodeID
	for i, c := range cands {
		var k [20]byte
		for j := 0; j < 20; j++ {
			k[j] = c[j] ^ pad[j]
		}
		if i == 0 {
			best, bestKey = c, k
			continue
		}
		less := false
		for j := 0; j < 20; j++ {
			if k[j] != bestKey[j] {
				less = k[j] < bestKey[j]
				break
			}
		}
		if less {
			best, bestKey = c, k
		}
	}
	return best
}

// TestChoose_RippledParity_PicksMinCalcNodeIDXorPad asserts that
// choose() picks the candidate whose calcNodeID(pubkey) XOR pad[:20]
// is minimal — the exact comparator rippled uses at
// NegativeUNLVote.cpp:142-161 + PublicKey.cpp:319-327. This test
// would fail if choose() reverted to comparing the raw 33-byte
// pubkey with a 32-byte pad, which would silently desync Go votes
// from rippled votes on a mixed validator network.
func TestChoose_RippledParity_PicksMinCalcNodeIDXorPad(t *testing.T) {
	cands := []consensus.NodeID{nodeID(0x11), nodeID(0x22), nodeID(0x33), nodeID(0x44)}

	for _, padTag := range []byte{0x00, 0xFF, 0xA5, 0x5A} {
		var pad [32]byte
		for i := range pad {
			pad[i] = padTag
		}
		got := choose(pad, cands)
		want := expectedChoose(pad, cands)
		assert.Equal(t, want, got,
			"pad=0x%02X: choose picked the wrong candidate; comparator must use calcNodeID(pubkey) XOR pad[:20]", padTag)
	}
}

// TestChoose_PadAffectsPick asserts that two distinct pads can
// produce different picks. This catches a comparator that ignores
// the pad (e.g. a stub that always returns candidates[0]).
func TestChoose_PadAffectsPick(t *testing.T) {
	cands := []consensus.NodeID{nodeID(0x11), nodeID(0x22), nodeID(0x33), nodeID(0x44), nodeID(0x55)}

	var padZero [32]byte
	var padFF [32]byte
	for i := range padFF {
		padFF[i] = 0xFF
	}

	pickZero := choose(padZero, cands)
	pickFF := choose(padFF, cands)
	assert.NotEqual(t, pickZero, pickFF,
		"choose with all-0 vs all-FF pads must produce different picks for this candidate set")
}

// TestFindAllCandidates_BoundaryScores asserts the strict-inequality
// boundary in NegativeUNLVote.cpp:282 (`score < negativeUNLLowWaterMark`)
// and :292 (`score > negativeUNLHighWaterMark`): a validator with
// score == LowWaterMark is NOT a toDisable candidate, and one with
// score == HighWaterMark is NOT a toReEnable candidate. rippled's
// testFindAllCandidatesCombination at NegativeUNL_test.cpp:1175-1183
// includes these boundary scores in its grid; we cover them
// directly here.
func TestFindAllCandidates_BoundaryScores(t *testing.T) {
	myKey := makeKey(0xAA)
	atLow := makeKey(0xBB)
	atHigh := makeKey(0xCC)
	other := makeKey(0xDD)
	v := NewVoter(keyToNodeID(myKey))

	unlKeys := [][33]byte{myKey, atLow, atHigh, other}
	unl := map[consensus.NodeID][33]byte{}
	for _, k := range unlKeys {
		unl[keyToNodeID(k)] = k
	}
	negUNL := map[consensus.NodeID]struct{}{
		keyToNodeID(atHigh): {},
	}
	scoreTable := map[consensus.NodeID]uint32{
		keyToNodeID(myKey):  HighWaterMark + 1,
		keyToNodeID(atLow):  LowWaterMark,  // exactly at boundary — NOT a candidate
		keyToNodeID(atHigh): HighWaterMark, // exactly at boundary — NOT a candidate
		keyToNodeID(other):  HighWaterMark + 1,
	}

	c := v.findAllCandidates(unl, negUNL, scoreTable)
	assert.Empty(t, c.toDisable, "score == LowWaterMark must not be a toDisable candidate (rippled uses strict `<`)")
	assert.Empty(t, c.toReEnable, "score == HighWaterMark must not be a toReEnable candidate (rippled uses strict `>`)")
}

// TestDoVoting_AllBadScores_CapEnforced exercises the
// MaxListedFraction cap with a UNL large enough that the cap is >1
// and many candidates qualify. With 8 validators and 0% currently
// disabled, ceil(8*0.25)=2 are allowed on the negUNL; canAdd is true
// (since listed=0 < 2), so the candidate scan runs, but DoVoting
// always picks at most one toDisable per round. The 25% cap is
// re-tested when votes are applied, so producing one tx per round
// matches rippled's behavior.
func TestDoVoting_AllBadScores_CapEnforced(t *testing.T) {
	myKey := makeKey(0xAA)
	v := NewVoter(keyToNodeID(myKey))

	unl := [][33]byte{myKey}
	for i := byte(1); i <= 7; i++ {
		unl = append(unl, makeKey(0xB0+i))
	}

	// Local node above MinLocalValsToVote, every other validator
	// below LowWaterMark.
	scoreTable := map[consensus.NodeID]uint32{
		keyToNodeID(myKey): MinLocalValsToVote + 1,
	}
	for _, k := range unl {
		nid := keyToNodeID(k)
		if nid == keyToNodeID(myKey) {
			continue
		}
		scoreTable[nid] = LowWaterMark - 1
	}

	blobs, err := v.DoVoting(1024, [32]byte{0xAB, 0xCD}, unl, State{}, scoreTable)
	require.NoError(t, err)
	require.Len(t, blobs, 1, "DoVoting returns at most one toDisable per round")

	tx := decodeTx(t, blobs[0])
	assert.EqualValues(t, 1, asUint(tx["UNLModifyDisabling"]),
		"the single emitted tx must be a ToDisable")
}

// TestDoVoting_NewValidatorExpired_BecomesCandidate covers rippled's
// case 9 in testFindAllCandidates (NegativeUNL_test.cpp:1136-1144):
// a validator added via NewValidators is exempt from ToDisable while
// inside the skip window; once seq advances past
// NewValidatorDisableSkip, PurgeNewValidators removes it and a bad
// score makes it a candidate. PR #375 shipped only the not-yet-
// expired half of this case.
func TestDoVoting_NewValidatorExpired_BecomesCandidate(t *testing.T) {
	myKey := makeKey(0xAA)
	good := makeKey(0xBB)
	exFresh := makeKey(0xCC)
	v := NewVoter(keyToNodeID(myKey))
	unl := [][33]byte{myKey, good, exFresh}

	addedAt := uint32(900)
	v.NewValidators(addedAt, []consensus.NodeID{keyToNodeID(exFresh)})

	// Upcoming seq is past the skip window:
	//   addedAt + NewValidatorDisableSkip + 2  → strictly > skip,
	//   so PurgeNewValidators removes the entry.
	upcomingSeq := addedAt + NewValidatorDisableSkip + 2
	require.Greater(t, upcomingSeq-addedAt, NewValidatorDisableSkip,
		"sanity: upcoming seq must be past the skip window")

	scoreTable := fullScoreTable(v.myID, unl)
	scoreTable[keyToNodeID(exFresh)] = LowWaterMark - 1

	blobs, err := v.DoVoting(upcomingSeq-1, [32]byte{0x06}, unl, State{}, scoreTable)
	require.NoError(t, err)
	require.Len(t, blobs, 1, "expired new validator with bad score must produce a ToDisable")

	tx := decodeTx(t, blobs[0])
	assert.EqualValues(t, 1, asUint(tx["UNLModifyDisabling"]),
		"sfUNLModifyDisabling must be 1 for the expired-then-bad-score case")
	assert.Equal(t, hex.EncodeToString(exFresh[:]),
		stringFold(tx["UNLModifyValidator"]),
		"validator field must be the (now-expired) fresh validator")
}

// TestDoVoting_BoundaryParticipation covers the rippled boundary
// where myValidationCount == MinLocalValsToVote. Rippled's else-if
// at NegativeUNLVote.cpp:230-231 uses strict `>`, so the boundary
// falls into the "Too many!" else-branch and returns empty. The Go
// port must match: at exactly MinLocalValsToVote, do not vote.
func TestDoVoting_BoundaryParticipation(t *testing.T) {
	myKey := makeKey(0xAA)
	weak := makeKey(0xBB)
	v := NewVoter(keyToNodeID(myKey))
	unl := [][33]byte{myKey, weak}

	scoreTable := map[consensus.NodeID]uint32{
		v.myID:            MinLocalValsToVote, // exactly at boundary
		keyToNodeID(weak): LowWaterMark - 1,   // would be toDisable if we voted
	}

	blobs, err := v.DoVoting(1024, [32]byte{0xDE, 0xAD}, unl, State{}, scoreTable)
	require.NoError(t, err)
	assert.Nil(t, blobs, "myCount == MinLocalValsToVote must NOT vote (rippled uses strict `>`)")
}

// TestDoVoting_LocalCountAboveWindow covers the rippled "Too many!"
// branch at NegativeUNLVote.cpp:236-244 — myValidationCount >
// FlagLedgerInterval. Rippled logs at error severity and returns
// empty (no vote). The Go port returns nil blobs (no vote) AND
// surfaces ErrLocalCountExceedsWindow so the caller can log at
// error severity, matching rippled's observability.
func TestDoVoting_LocalCountAboveWindow(t *testing.T) {
	myKey := makeKey(0xAA)
	weak := makeKey(0xBB)
	v := NewVoter(keyToNodeID(myKey))
	unl := [][33]byte{myKey, weak}

	scoreTable := map[consensus.NodeID]uint32{
		v.myID:            flagLedgerInterval + 1,
		keyToNodeID(weak): LowWaterMark - 1,
	}

	blobs, err := v.DoVoting(1024, [32]byte{0xDE, 0xAD}, unl, State{}, scoreTable)
	require.ErrorIs(t, err, ErrLocalCountExceedsWindow,
		"above-window must surface ErrLocalCountExceedsWindow so the caller can log at error severity")
	assert.Nil(t, blobs, "myCount > FlagLedgerInterval must NOT vote")
}

// TestDoVoting_ScoreTableMissingUNLMember covers the API-boundary
// invariant introduced to match rippled's buildScoreTable at
// NegativeUNLVote.cpp:197-200: every UNL member is treated as score
// 0 if absent from the supplied scoreTable. Without the zero-fill
// inside DoVoting, a UNL member silently absent would never become
// a toDisable candidate even though rippled would always count it
// as a non-validator with score 0.
func TestDoVoting_ScoreTableMissingUNLMember(t *testing.T) {
	myKey := makeKey(0xAA)
	silent := makeKey(0xBB) // never validated — missing from scoreTable
	other := makeKey(0xCC)
	v := NewVoter(keyToNodeID(myKey))
	unl := [][33]byte{myKey, silent, other}

	scoreTable := map[consensus.NodeID]uint32{
		v.myID:             MinLocalValsToVote + 1,
		keyToNodeID(other): HighWaterMark + 1,
		// silent intentionally omitted
	}

	blobs, err := v.DoVoting(1024, [32]byte{0x07}, unl, State{}, scoreTable)
	require.NoError(t, err)
	require.Len(t, blobs, 1, "silent UNL member (missing from scoreTable) must be treated as score 0 → toDisable")

	tx := decodeTx(t, blobs[0])
	assert.EqualValues(t, 1, asUint(tx["UNLModifyDisabling"]))
	assert.Equal(t, hex.EncodeToString(silent[:]),
		stringFold(tx["UNLModifyValidator"]),
		"the silent (missing-from-scoreTable) validator must be the picked candidate")
}

// makeKeyN produces a deterministic 33-byte master pubkey indexed by
// idx. Unlike makeKey (which fills bytes 1..32 with the same byte
// tag and so collides above 256 distinct values), makeKeyN encodes
// the index across the first three bytes so up to 65535 distinct
// pubkeys can be generated for the combination tests.
func makeKeyN(idx int) [33]byte {
	var k [33]byte
	k[0] = 0x02
	k[1] = byte(idx >> 8)
	k[2] = byte(idx)
	return k
}

// buildCombinationFixture builds the (unl, negUNL, scoreTable) tuple
// rippled's testFindAllCandidatesCombination uses for combination 1
// (NegativeUNL_test.cpp:1185-1257): every UNL member gets the same
// score, the first floor(unlSize * nUnlPercent / 100) members are
// placed on the negUNL.
func buildCombinationFixture(unlSize, nUnlPercent int, score uint32) (
	map[consensus.NodeID][33]byte,
	map[consensus.NodeID]struct{},
	map[consensus.NodeID]uint32,
) {
	nodeIDs := make([]consensus.NodeID, unlSize)
	keys := make([][33]byte, unlSize)
	for i := 0; i < unlSize; i++ {
		keys[i] = makeKeyN(i)
		nodeIDs[i] = keyToNodeID(keys[i])
	}
	unl := make(map[consensus.NodeID][33]byte, unlSize)
	scoreTable := make(map[consensus.NodeID]uint32, unlSize)
	for i, n := range nodeIDs {
		unl[n] = keys[i]
		scoreTable[n] = score
	}
	negSize := unlSize * nUnlPercent / 100
	negUNL := make(map[consensus.NodeID]struct{}, negSize)
	for i := 0; i < negSize; i++ {
		negUNL[nodeIDs[i]] = struct{}{}
	}
	return unl, negUNL, scoreTable
}

// TestFindAllCandidates_RippledCombination1 mirrors rippled's
// testFindAllCandidatesCombination combination 1
// (NegativeUNL_test.cpp:1185-1257) — a parameterized grid over
// (unlSize, nUnlPercent, score) covering every boundary score and
// the no-/half-/full-negUNL cases. This is rippled's parity bar for
// findAllCandidates and catches off-by-one drift in the canAdd cap
// or the strict-inequality watermark thresholds.
func TestFindAllCandidates_RippledCombination1(t *testing.T) {
	unlSizes := []int{34, 35, 80}
	nUnlPercents := []int{0, 50, 100}
	scores := []uint32{
		0,
		LowWaterMark - 1,
		LowWaterMark,
		LowWaterMark + 1,
		HighWaterMark - 1,
		HighWaterMark,
		HighWaterMark + 1,
		MinLocalValsToVote,
	}

	v := NewVoter(nodeID(0xA0))

	for _, us := range unlSizes {
		for _, np := range nUnlPercents {
			for _, score := range scores {
				name := fmt.Sprintf("us=%d/np=%d/score=%d", us, np, score)
				t.Run(name, func(t *testing.T) {
					unl, negUNL, scoreTable := buildCombinationFixture(us, np, score)
					require.Equal(t, us, len(unl))
					require.Equal(t, us*np/100, len(negUNL))
					require.Equal(t, us, len(scoreTable))

					var toDisableExpect, toReEnableExpect int
					switch np {
					case 0:
						if score < LowWaterMark {
							toDisableExpect = us
						}
					case 50:
						if score > HighWaterMark {
							toReEnableExpect = us * np / 100
						}
					case 100:
						if score > HighWaterMark {
							toReEnableExpect = us
						}
					}

					c := v.findAllCandidates(unl, negUNL, scoreTable)
					assert.Equal(t, toDisableExpect, len(c.toDisable),
						"toDisable count mismatch")
					assert.Equal(t, toReEnableExpect, len(c.toReEnable),
						"toReEnable count mismatch")
				})
			}
		}
	}
}

// TestFindAllCandidates_RippledCombination2 mirrors rippled's
// testFindAllCandidatesCombination combination 2
// (NegativeUNL_test.cpp:1258-1334) — the first 16 nodes get pairs of
// scores from the boundary array, every node beyond gets
// MinLocalValsToVote (= scores.back()), and the negUNL is built per
// percent rule. The expected counts encode the cap-and-watermark
// arithmetic across (unlSize, nUnlPercent) without enumerating every
// score: a single failure here flags drift in the candidate-set
// composition that combination 1 cannot reach.
func TestFindAllCandidates_RippledCombination2(t *testing.T) {
	unlSizes := []int{34, 35, 80}
	nUnlPercents := []int{0, 50, 100}
	scores := []uint32{
		0,
		LowWaterMark - 1,
		LowWaterMark,
		LowWaterMark + 1,
		HighWaterMark - 1,
		HighWaterMark,
		HighWaterMark + 1,
		MinLocalValsToVote,
	}

	v := NewVoter(nodeID(0xA0))

	build := func(unlSize, nUnlPercent int) (
		map[consensus.NodeID][33]byte,
		map[consensus.NodeID]struct{},
		map[consensus.NodeID]uint32,
	) {
		nodeIDs := make([]consensus.NodeID, unlSize)
		keys := make([][33]byte, unlSize)
		for i := 0; i < unlSize; i++ {
			keys[i] = makeKeyN(i)
			nodeIDs[i] = keyToNodeID(keys[i])
		}
		unl := make(map[consensus.NodeID][33]byte, unlSize)
		for i, n := range nodeIDs {
			unl[n] = keys[i]
		}
		scoreTable := make(map[consensus.NodeID]uint32, unlSize)
		nIdx := 0
		for _, sc := range scores {
			scoreTable[nodeIDs[nIdx]] = sc
			nIdx++
			scoreTable[nodeIDs[nIdx]] = sc
			nIdx++
		}
		tail := scores[len(scores)-1]
		for ; nIdx < unlSize; nIdx++ {
			scoreTable[nodeIDs[nIdx]] = tail
		}
		negUNL := make(map[consensus.NodeID]struct{})
		switch nUnlPercent {
		case 100:
			for _, n := range nodeIDs {
				negUNL[n] = struct{}{}
			}
		case 50:
			for i := 1; i < unlSize; i += 2 {
				negUNL[nodeIDs[i]] = struct{}{}
			}
		}
		return unl, negUNL, scoreTable
	}

	for _, us := range unlSizes {
		for _, np := range nUnlPercents {
			name := fmt.Sprintf("us=%d/np=%d", us, np)
			t.Run(name, func(t *testing.T) {
				unl, negUNL, scoreTable := build(us, np)
				require.Equal(t, us, len(unl))
				require.Equal(t, us*np/100, len(negUNL))
				require.Equal(t, us, len(scoreTable))

				var toDisableExpect, toReEnableExpect int
				switch np {
				case 0:
					toDisableExpect = 4
				case 50:
					toReEnableExpect = len(negUNL) - 6
				case 100:
					toReEnableExpect = len(negUNL) - 12
				}

				c := v.findAllCandidates(unl, negUNL, scoreTable)
				assert.Equal(t, toDisableExpect, len(c.toDisable),
					"toDisable count mismatch")
				assert.Equal(t, toReEnableExpect, len(c.toReEnable),
					"toReEnable count mismatch")
			})
		}
	}
}

// TestBuildUNLModifyTx_PseudoTxWireFormat pins the UNLModify
// pseudo-tx wire shape against rippled's REQUIRED/OPTIONAL common
// field rules. This guards the byte-level output of
// buildUNLModifyTx, which now routes through pseudo.EncodePseudoTx
// — a behaviour change from the previous in-place serialization
// that omitted sfSigningPubKey and emitted sfFlags=0.
//
// rippled references:
//   - TxFormats.cpp:34, 44 — sfFlags is soeOPTIONAL,
//     sfSigningPubKey is soeREQUIRED
//   - STObject.cpp:162-168 — set(SOTemplate) writes
//     defaultObject for REQUIRED (empty VL for SigningPubKey) and
//     nonPresentObject for OPTIONAL (Flags)
//   - STObject.cpp:907-921 — STI_NOTPRESENT fields are filtered
//     out of the serialized blob
//   - NegativeUNLVote.cpp:110-140 — addTx assembles the pseudo-tx
//     without setting sfFlags
//
// Asserting the bytes here means a regression in the encoder
// (e.g., a future "always emit Flags" change) fails this test
// with a precise message rather than silently diverging the
// transaction ID and breaking flag-ledger SHAMap consensus on
// the negative-UNL pseudo-tx position.
func TestBuildUNLModifyTx_PseudoTxWireFormat(t *testing.T) {
	validator := makeKey(0x42)
	blob, err := buildUNLModifyTx(99999, validator, ToDisable)
	require.NoError(t, err)
	require.NotNil(t, blob)

	hexBlob := hex.EncodeToString(blob)
	// sfSigningPubKey (field id 0x73 = type 7 / fieldCode 3) must
	// appear as VL(0) — bytes "7300". For UNLModify the next
	// field in canonical sort order is sfUNLModifyValidator
	// (0x70 0x13), so SigningPubKey ends with "73007013" rather
	// than the "730081" pattern used by SetFee.
	assert.Contains(t, hexBlob, "73007013",
		"UNLModify blob must carry sfSigningPubKey VL(0) immediately before sfUNLModifyValidator")
	// sfFlags (UInt32 type marker 0x22 + 4 zero bytes) must not
	// appear; rippled marks sfFlags as soeOPTIONAL and
	// NegativeUNLVote::addTx never sets it. STObject::add filters
	// STI_NOTPRESENT optionals out of the serialized blob.
	assert.NotContains(t, hexBlob, "2200000000",
		"UNLModify blob must not carry sfFlags=0 (rippled omits soeOPTIONAL nonPresent fields)")

	stx, err := binarycodec.Decode(hexBlob)
	require.NoError(t, err, "UNLModify blob must round-trip through binarycodec.Decode")

	got, ok := stx["SigningPubKey"]
	assert.True(t, ok, "decoded UNLModify must include SigningPubKey")
	assert.Equal(t, "", got, "SigningPubKey must decode as empty")

	_, hasFlags := stx["Flags"]
	assert.False(t, hasFlags, "decoded UNLModify must not include Flags")
}
