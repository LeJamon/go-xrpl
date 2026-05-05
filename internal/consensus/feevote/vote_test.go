package feevote

import (
	"encoding/hex"
	"strconv"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ptr wraps a uint64 for the pointer-set Vote fields.
func ptr(x uint64) *uint64 { return &x }

// TestDoVoting_NoChangeNoTx pins the early-return path when the
// chosen value matches current for all three fields. Mirrors
// FeeVoteImpl.cpp:291 (the `||` gate) — if nothing changed, no
// SetFee tx is emitted.
func TestDoVoting_NoChangeNoTx(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := current // unchanged → no votes for anything outside current

	blob, err := DoVoting(1024, current, target, nil, true)
	require.NoError(t, err)
	assert.Nil(t, blob, "no change → no SetFee tx")
}

// TestDoVoting_TargetSeededAsInitialVote verifies the constructor
// quirk at FeeVoteImpl.cpp:44 — voteMap[target] is pre-incremented
// in the constructor, so a single trusted validator at target with
// no other votes is enough to pick target as consensus.
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

// TestDoVoting_VoteOutsideWindowIgnored pins the
// [min(current, target), max(current, target)] clamp at
// VotableValue::getVotes (FeeVoteImpl.cpp:74-83). A vote far
// outside that range cannot be picked, so a single in-window seed
// (target) wins.
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

// TestDoVoting_NoVoteCountsAsCurrent pins the noVote semantics at
// VotableValue::noVote (FeeVoteImpl.cpp:53-57). A field absent on
// a validator's vote increments voteMap[current], so abstainers on
// that specific field pull the consensus toward current — even
// while the validator still votes for target on the other fields.
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
		{BaseFee: ptr(10), ReserveBase: ptr(target.ReserveBase), ReserveIncrement: ptr(target.ReserveIncrement)},
		{BaseFee: ptr(10), ReserveBase: ptr(target.ReserveBase), ReserveIncrement: ptr(target.ReserveIncrement)},
		{ /* BaseFee abstain */ ReserveBase: ptr(target.ReserveBase), ReserveIncrement: ptr(target.ReserveIncrement)},
	}

	blob, err := DoVoting(1024, current, target, votes, true)
	require.NoError(t, err)
	require.NotNil(t, blob)
	stx := decodeTx(t, blob)
	assert.Equal(t, "10", stx["BaseFeeDrops"], "BaseFee held at current by noVote majority")
	assert.Equal(t, "11000000", stx["ReserveBaseDrops"])
	assert.Equal(t, "2500000", stx["ReserveIncrementDrops"])
}

// TestDoVoting_OutOfRangeIsNoVote pins the isLegalAmountSigned
// guard at applyVote — values exceeding MaxLegalDrops must be
// dropped silently (counted as a vote for current), not raise
// an error. Matches FeeVoteImpl.cpp:225-262.
func TestDoVoting_OutOfRangeIsNoVote(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{BaseFee: 12, ReserveBase: 11_000_000, ReserveIncrement: 2_500_000}

	overflow := MaxLegalDrops + 1
	// Three votes overflowing on BaseFee → counted as 3× current
	// for that field. Beats the single seed for target → BaseFee
	// held at current. Reserves are voted explicitly so they still
	// flip; without that this DoVoting call would have nothing
	// changed and emit no tx.
	votes := []Vote{
		{BaseFee: &overflow, ReserveBase: ptr(target.ReserveBase), ReserveIncrement: ptr(target.ReserveIncrement)},
		{BaseFee: &overflow, ReserveBase: ptr(target.ReserveBase), ReserveIncrement: ptr(target.ReserveIncrement)},
		{BaseFee: &overflow, ReserveBase: ptr(target.ReserveBase), ReserveIncrement: ptr(target.ReserveIncrement)},
	}
	blob, err := DoVoting(1024, current, target, votes, true)
	require.NoError(t, err)
	require.NotNil(t, blob)
	stx := decodeTx(t, blob)
	assert.Equal(t, "10", stx["BaseFeeDrops"],
		"overflow values must be treated as noVote, not picked")
}

// TestDoVoting_PreXRPFeesWireFormat exercises the legacy field
// shape: sfBaseFee (uint64 hex), sfReserveBase (uint32),
// sfReserveIncrement (uint32), sfReferenceFeeUnits required.
// Mirrors FeeVoteImpl.cpp:307-318.
func TestDoVoting_PreXRPFeesWireFormat(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}
	target := Stance{BaseFee: 16, ReserveBase: 12_000_000, ReserveIncrement: 3_000_000}

	blob, err := DoVoting(1024, current, target, nil, false /* pre-XRPFees */)
	require.NoError(t, err)
	require.NotNil(t, blob)

	stx := decodeTx(t, blob)
	// sfBaseFee is uint64 — the codec returns it as a 16-character
	// big-endian hex string. 16 decimal = 0x10 = "0000000000000010".
	assert.Equal(t, "0000000000000010", baseFeeHex(t, stx["BaseFee"]),
		"sfBaseFee must encode the uint64 value 16 (=0x10) in legacy hex form")
	assert.EqualValues(t, 12_000_000, asUint(stx["ReserveBase"]))
	assert.EqualValues(t, 3_000_000, asUint(stx["ReserveIncrement"]))
	assert.EqualValues(t, ReferenceFeeUnitsDeprecated, asUint(stx["ReferenceFeeUnits"]),
		"pre-XRPFees SetFee MUST stamp sfReferenceFeeUnits = FEE_UNITS_DEPRECATED")
	// Modern fields must be absent.
	_, hasModern := stx["BaseFeeDrops"]
	assert.False(t, hasModern, "pre-XRPFees must not carry sfBaseFeeDrops")
}

// TestDoVoting_TxCarriesAllThreeOnPartialChange pins the
// FeeVoteImpl.cpp:297-319 contract: when ANY field changes, the tx
// carries all three at their chosen values (which equal current
// for unchanged fields). Useful so the on-chain SetFee snapshot is
// always self-contained.
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

// TestDoVoting_LedgerSequenceIsUpcoming pins the seq plumbing —
// the SetFee tx carries the parent+1 seq (the upcoming flag
// ledger). Mirrors FeeVoteImpl.cpp:299.
func TestDoVoting_LedgerSequenceIsUpcoming(t *testing.T) {
	current := Stance{BaseFee: 10}
	target := Stance{BaseFee: 12}

	blob, err := DoVoting(99999, current, target, nil, true)
	require.NoError(t, err)
	require.NotNil(t, blob)
	stx := decodeTx(t, blob)
	assert.EqualValues(t, 99999, asUint(stx["LedgerSequence"]))
}

// TestVotableValue_PicksHighestCountWithinWindow exercises the
// inner getVotes loop directly: the most-voted in-window value
// wins.
func TestVotableValue_PicksHighestCountWithinWindow(t *testing.T) {
	v := newVotableValue(10, 14) // window = [10, 14]
	v.addVote(11)
	v.addVote(11)
	v.addVote(13)
	chosen, changed := v.getVotes()
	assert.True(t, changed)
	assert.EqualValues(t, 11, chosen, "11 has 2 votes, beats 13 (1) and seed-target 14 (1)")
}

// TestVotableValue_TieBreakLowestKeyWins pins the deterministic
// tie-break order: with two in-window values at equal vote counts,
// the lowest key wins. Mirrors rippled's std::map ascending-key
// iteration with strict `val > weight` at FeeVoteImpl.cpp:74-83.
//
// Without this guarantee, two goXRPL nodes given identical inputs
// could pick different values from Go's randomized map iteration —
// the resulting SetFee blobs would diverge across the network.
//
// The case is constructed so two distinct in-window keys (11 and
// 13) reach the same vote count (2), and that count strictly
// exceeds every other in-window key's count. Under ascending-key
// iteration with `val > weight`, the first key to reach the max
// wins (11); a `val >= weight` bug, descending iteration, or
// random map-order would let 13 win on at least some runs.
func TestVotableValue_TieBreakLowestKeyWins(t *testing.T) {
	for i := 0; i < 64; i++ {
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

// TestBuildSetFeeTx_EmitsEmptySigningPubKey pins the wire-format
// requirement that pseudo-tx blobs carry sfSigningPubKey as an
// empty VL (field code 0x73, length 0x00). rippled's STTx ctor at
// STTx.cpp:113-128 calls set(format->getSOTemplate()), inserting a
// default-constructed empty Blob for every REQUIRED common field
// (TxFormats.cpp:32-50); STObject::add at STObject.cpp:881-921
// then serializes the empty Blob. Omitting it changes the blob
// length and hence the txID — diverging consensus.
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

// TestBuildSetFeeTx_OmitsFlags pins the inverse of the
// SigningPubKey requirement: rippled declares sfFlags as
// soeOPTIONAL in the common-fields template (TxFormats.cpp:34) and
// FeeVoteImpl::doVoting (FeeVoteImpl.cpp:297-319) never sets it on
// the assembled STTx, so STObject::set(SOTemplate) at
// STObject.cpp:156-169 leaves it as STI_NOTPRESENT and
// STObject::add at STObject.cpp:907-921 filters it out of the
// serialized blob. The Go encoder must match — emitting Flags=0
// would prepend `2200000000` to the blob, shift every later field,
// and produce a different transaction ID. Validators that disagree
// on the txID cannot converge their SHAMaps on the flag-ledger
// pseudo-tx position, so consensus on the fee change fails.
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

// TestBuildSetFeeTx_PreXRPFeesReserveOverflowFallsBackToCurrent
// pins the dropsAs<uint32>(current) fallback at
// FeeVoteImpl.cpp:312-316: when the chosen pre-XRPFees ReserveBase
// or ReserveIncrement does not fit in uint32, rippled emits the
// CURRENT value, not a silent truncation of chosen.
//
// Triggered here by setting target above UINT32_MAX so getVotes
// picks an out-of-range value. The window
// [current, target] then spans up to 2^33, and the seeded
// voteMap[target]=1 wins because no other vote is in the window.
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
