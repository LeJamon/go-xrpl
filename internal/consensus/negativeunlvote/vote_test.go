package negativeunlvote

import (
	"encoding/hex"
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
	return consensus.NodeID(makeKey(tag))
}

// fullScoreTable returns scoreTable[nid] = HighWaterMark+1 for
// every key in keys, plus the local node at MinLocalValsToVote+1
// so DoVoting clears the local-participation gate by default. The
// local-node assignment runs LAST so it overrides any HighWaterMark
// score the loop wrote when myKey is also in keys.
func fullScoreTable(myID consensus.NodeID, keys [][33]byte) map[consensus.NodeID]uint32 {
	st := map[consensus.NodeID]uint32{}
	for _, k := range keys {
		st[keyToNodeID(k)] = HighWaterMark + 1
	}
	st[myID] = MinLocalValsToVote + 1
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
