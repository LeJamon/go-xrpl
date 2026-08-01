package feevote

import (
	"encoding/hex"
	"math"
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr(x drops.XRPAmount) *drops.XRPAmount { return &x }

func TestDoVoting_NoChangeNoTx(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := current // unchanged → no votes for anything outside current

	blob, err := DoVoting(1024, current, target, nil, true)
	require.NoError(t, err)
	assert.Nil(t, blob, "no change → no SetFee tx")
}

func TestDoVoting_TargetSeededAsInitialVote(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{BaseFee: 12, ReserveBase: 15_000_000, ReserveIncrement: 3_000_000}

	// No additional votes — only the constructor's seed.
	blob, err := DoVoting(1024, current, target, nil, true)
	require.NoError(t, err)
	require.NotNil(t, blob, "constructor seeds target → consensus picks target")

	stx := decodeTx(t, blob)
	assert.Equal(t, "12", stx["BaseFeeDrops"])
	assert.Equal(t, "15000000", stx["ReserveBaseDrops"])
	assert.Equal(t, "3000000", stx["ReserveIncrementDrops"])
}

func TestDoVoting_VoteOutsideWindowIgnored(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{BaseFee: 12, ReserveBase: 11_000_000, ReserveIncrement: 2_500_000}

	// Three votes way outside [10,12] for BaseFee. They should be
	// dropped at getVotes time even though they outnumber the seed.
	votes := []Vote{
		{BaseFee: ptr(99)},
		{BaseFee: ptr(99)},
		{BaseFee: ptr(99)},
	}

	blob, err := DoVoting(1024, current, target, votes, true)
	require.NoError(t, err)
	require.NotNil(t, blob)
	stx := decodeTx(t, blob)
	assert.Equal(t, "12", stx["BaseFeeDrops"],
		"votes outside [current, target] window must not be picked")
}

func TestDoVoting_NegativeVoteOutsideWindow(t *testing.T) {
	current := Stance{BaseFee: 10}
	target := Stance{BaseFee: 20}
	votes := []Vote{{BaseFee: ptr(-15)}}

	blob, err := DoVoting(1024, current, target, votes, true)
	require.NoError(t, err)
	require.NotNil(t, blob)
	assert.Equal(t, "20", decodeTx(t, blob)["BaseFeeDrops"])
}

func TestDoVoting_DescendingWindow(t *testing.T) {
	current := Stance{BaseFee: 20}
	target := Stance{BaseFee: 10}
	votes := []Vote{
		{BaseFee: ptr(15)},
		{BaseFee: ptr(15)},
	}

	blob, err := DoVoting(1024, current, target, votes, true)
	require.NoError(t, err)
	require.NotNil(t, blob)
	assert.Equal(t, "15", decodeTx(t, blob)["BaseFeeDrops"])
}

func TestDoVoting_NoVoteCountsAsCurrent(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{BaseFee: 12, ReserveBase: 11_000_000, ReserveIncrement: 2_500_000}

	// Three validators agree on target for the two reserves but
	// diverge on BaseFee: two explicitly hold at current=10 and one
	// abstains (BaseFee nil → noVote → counted as current). Net for
	// BaseFee: voteMap[10]=3, voteMap[12]=1 → current wins, no
	// change. The reserves still flip to target because every vote
	// either explicitly chooses target or is the constructor seed.
	votes := []Vote{
		{BaseFee: ptr(10), ReserveBase: ptr(drops.XRPAmount(target.ReserveBase)), ReserveIncrement: ptr(drops.XRPAmount(target.ReserveIncrement))},
		{BaseFee: ptr(10), ReserveBase: ptr(drops.XRPAmount(target.ReserveBase)), ReserveIncrement: ptr(drops.XRPAmount(target.ReserveIncrement))},
		{ /* BaseFee abstain */ ReserveBase: ptr(drops.XRPAmount(target.ReserveBase)), ReserveIncrement: ptr(drops.XRPAmount(target.ReserveIncrement))},
	}

	blob, err := DoVoting(1024, current, target, votes, true)
	require.NoError(t, err)
	require.NotNil(t, blob)
	stx := decodeTx(t, blob)
	assert.Equal(t, "10", stx["BaseFeeDrops"], "BaseFee held at current by noVote majority")
	assert.Equal(t, "11000000", stx["ReserveBaseDrops"])
	assert.Equal(t, "2500000", stx["ReserveIncrementDrops"])
}

func TestDoVoting_OutOfRangeIsNoVote(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{BaseFee: 12, ReserveBase: 11_000_000, ReserveIncrement: 2_500_000}

	overflow := drops.MaxDrops + 1
	// Three votes overflowing on BaseFee → counted as 3× current
	// for that field. Beats the single seed for target → BaseFee
	// held at current. Reserves are voted explicitly so they still
	// flip; without that this DoVoting call would have nothing
	// changed and emit no tx.
	votes := []Vote{
		{BaseFee: &overflow, ReserveBase: ptr(drops.XRPAmount(target.ReserveBase)), ReserveIncrement: ptr(drops.XRPAmount(target.ReserveIncrement))},
		{BaseFee: &overflow, ReserveBase: ptr(drops.XRPAmount(target.ReserveBase)), ReserveIncrement: ptr(drops.XRPAmount(target.ReserveIncrement))},
		{BaseFee: &overflow, ReserveBase: ptr(drops.XRPAmount(target.ReserveBase)), ReserveIncrement: ptr(drops.XRPAmount(target.ReserveIncrement))},
	}
	blob, err := DoVoting(1024, current, target, votes, true)
	require.NoError(t, err)
	require.NotNil(t, blob)
	stx := decodeTx(t, blob)
	assert.Equal(t, "10", stx["BaseFeeDrops"],
		"overflow values must be treated as noVote, not picked")
}

func TestApplyVote_SignedBoundaries(t *testing.T) {
	v := newVotableValue(10, 20)
	minLegal := -drops.MaxDrops
	maxLegal := drops.MaxDrops
	belowMin := minLegal - 1
	aboveMax := maxLegal + 1

	applyVote(v, &minLegal)
	applyVote(v, &maxLegal)
	applyVote(v, &belowMin)
	applyVote(v, &aboveMax)

	assert.Equal(t, 1, v.votes[minLegal])
	assert.Equal(t, 1, v.votes[maxLegal])
	assert.Equal(t, 2, v.noVotes)
}

func TestDoVoting_PreXRPFeesWireFormat(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{BaseFee: 16, ReserveBase: 12_000_000, ReserveIncrement: 3_000_000}

	blob, err := DoVoting(1024, current, target, nil, false /* pre-XRPFees */)
	require.NoError(t, err)
	require.NotNil(t, blob)

	stx := decodeTx(t, blob)
	// sfBaseFee is uint64 — the codec returns it as a lowercase hex
	// string without leading zeros (matching rippled's
	// STUInt64::getJson, std::to_chars base 16). 16 decimal = 0x10.
	assert.Equal(t, "10", baseFeeHex(t, stx["BaseFee"]),
		"sfBaseFee must encode the uint64 value 16 (=0x10) in legacy hex form")
	assert.EqualValues(t, 12_000_000, asUint(stx["ReserveBase"]))
	assert.EqualValues(t, 3_000_000, asUint(stx["ReserveIncrement"]))
	assert.EqualValues(t, referenceFeeUnitsDeprecated, asUint(stx["ReferenceFeeUnits"]),
		"pre-XRPFees SetFee MUST stamp sfReferenceFeeUnits = FEE_UNITS_DEPRECATED")
	// Modern fields must be absent.
	_, hasModern := stx["BaseFeeDrops"]
	assert.False(t, hasModern, "pre-XRPFees must not carry sfBaseFeeDrops")
}

func TestDoVoting_TxCarriesAllThreeOnPartialChange(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{
		BaseFee:          12, // changed
		ReserveBase:      current.ReserveBase,
		ReserveIncrement: current.ReserveIncrement,
	}

	blob, err := DoVoting(1024, current, target, nil, true)
	require.NoError(t, err)
	require.NotNil(t, blob)
	stx := decodeTx(t, blob)

	// All three fields present even though only BaseFee changed.
	assert.Equal(t, "12", stx["BaseFeeDrops"])
	assert.Equal(t, strconv.FormatUint(current.ReserveBase, 10), stx["ReserveBaseDrops"])
	assert.Equal(t, strconv.FormatUint(current.ReserveIncrement, 10), stx["ReserveIncrementDrops"])
}

func TestDoVoting_LedgerSequenceIsUpcoming(t *testing.T) {
	current := Stance{BaseFee: 10}
	target := Stance{BaseFee: 12}

	blob, err := DoVoting(99999, current, target, nil, true)
	require.NoError(t, err)
	require.NotNil(t, blob)
	stx := decodeTx(t, blob)
	assert.EqualValues(t, 99999, asUint(stx["LedgerSequence"]))
}

func TestVotableValue_PicksHighestCountWithinWindow(t *testing.T) {
	v := newVotableValue(10, 14) // window = [10, 14]
	v.addVote(11)
	v.addVote(11)
	v.addVote(13)
	chosen, changed := v.getVotes()
	assert.True(t, changed)
	assert.EqualValues(t, 11, chosen, "11 has 2 votes, beats 13 (1) and seed-target 14 (1)")
}

func TestVotableValue_TieBreakLowestKeyWins(t *testing.T) {
	for i := range 64 {
		v := newVotableValue(10, 14) // window = [10, 14], seeds voteMap[14]=1
		v.addVote(11)
		v.addVote(11)
		v.addVote(13)
		v.addVote(13)
		// voteMap = {11:2, 13:2, 14:1}. Both 11 and 13 are in
		// window and tied at the max count. Ascending iteration
		// with strict-greater picks the first to reach the max → 11.
		chosen, changed := v.getVotes()
		assert.True(t, changed)
		assert.EqualValues(t, 11, chosen,
			"iter %d: tie at count=2 between 11 and 13 → lowest in-window key (11) wins, not %d", i, chosen)
	}
}

func TestVotableValue_SignedTieBreak(t *testing.T) {
	v := newVotableValue(10, 20)
	v.addVote(-15)
	v.addVote(-15)
	v.addVote(-15)
	v.addVote(12)
	v.addVote(12)
	v.addVote(18)
	v.addVote(18)

	chosen, changed := v.getVotes()
	assert.True(t, changed)
	assert.EqualValues(t, 12, chosen)
}

func TestBuildSetFeeTx_EmitsEmptySigningPubKey(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{BaseFee: 12, ReserveBase: 11_000_000, ReserveIncrement: 2_500_000}

	for _, xrpFees := range []bool{false, true} {
		blob, err := DoVoting(1024, current, target, nil, xrpFees)
		require.NoError(t, err)
		require.NotNil(t, blob)

		// Empty sfSigningPubKey serializes as 0x73 0x00 followed by
		// the next-larger field tag in canonical sort order. After
		// sfSigningPubKey (code 0x73) the next present common field
		// in a pseudo-tx is sfAccount (code 0x81). Asserting the
		// 3-byte sequence "730081" pins both the empty VL byte and
		// its position in the sort order.
		assert.Contains(t, hex.EncodeToString(blob), "730081",
			"xrpFeesEnabled=%v: blob must include sfSigningPubKey VL(0) followed by sfAccount", xrpFees)

		stx := decodeTx(t, blob)
		got, ok := stx["SigningPubKey"]
		assert.True(t, ok, "xrpFeesEnabled=%v: decoded tx must include SigningPubKey", xrpFees)
		assert.Equal(t, "", got, "xrpFeesEnabled=%v: SigningPubKey must decode as empty", xrpFees)
	}
}

func TestBuildSetFeeTx_OmitsFlags(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{BaseFee: 12, ReserveBase: 11_000_000, ReserveIncrement: 2_500_000}

	for _, xrpFees := range []bool{false, true} {
		blob, err := DoVoting(1024, current, target, nil, xrpFees)
		require.NoError(t, err)
		require.NotNil(t, blob)

		hexBlob := hex.EncodeToString(blob)
		assert.NotContains(t, hexBlob, "2200000000",
			"xrpFeesEnabled=%v: blob must not carry sfFlags=0 (rippled omits soeOPTIONAL nonPresent fields)", xrpFees)

		stx := decodeTx(t, blob)
		_, hasFlags := stx["Flags"]
		assert.False(t, hasFlags,
			"xrpFeesEnabled=%v: decoded tx must not include Flags", xrpFees)
	}
}

func TestBuildSetFeeTx_PreXRPFeesReserveOverflowFallsBackToCurrent(t *testing.T) {
	overflow := uint64(1) << 33 // > UINT32_MAX
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{
		BaseFee:          11,
		ReserveBase:      overflow,
		ReserveIncrement: overflow,
	}

	blob, err := DoVoting(1024, current, target, nil, false /* pre-XRPFees */)
	require.NoError(t, err)
	require.NotNil(t, blob, "BaseFee changed → tx emitted")

	stx := decodeTx(t, blob)
	assert.EqualValues(t, current.ReserveBase, asUint(stx["ReserveBase"]),
		"chosen ReserveBase > UINT32_MAX → fall back to current, not truncate")
	assert.EqualValues(t, current.ReserveIncrement, asUint(stx["ReserveIncrement"]),
		"chosen ReserveIncrement > UINT32_MAX → fall back to current")
}

func TestBuildSetFeeTx_PreXRPFeesRejectsOverflowingFallback(t *testing.T) {
	current := Stance{ReserveBase: math.MaxUint32 + 1}
	chosen := Stance{BaseFee: 11, ReserveBase: math.MaxUint32 + 2}

	blob, err := buildSetFeeTx(1024, current, chosen, false)
	require.ErrorContains(t, err, "reserve base")
	assert.Nil(t, blob)
}

func decodeTx(t *testing.T, blob []byte) map[string]any {
	t.Helper()
	out, err := binarycodec.Decode(hex.EncodeToString(blob))
	require.NoError(t, err, "serialized SetFee must round-trip through binarycodec.Decode")
	return out
}

// baseFeeHex normalizes the codec-decoded sfBaseFee value to a
// stable hex representation for comparison. The codec may return
// uint64 fields as hex strings under some paths.
func baseFeeHex(t *testing.T, v any) string {
	t.Helper()
	switch s := v.(type) {
	case string:
		return s
	default:
		t.Fatalf("sfBaseFee unexpected type %T: %v", v, v)
		return ""
	}
}

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
		x, err := strconv.ParseUint(n, 10, 64)
		if err == nil {
			return x
		}
		// Fallback: treat as hex.
		x, _ = strconv.ParseUint(n, 16, 64)
		return x
	}
	return 0
}
