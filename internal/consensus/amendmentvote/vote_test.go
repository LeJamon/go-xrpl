package amendmentvote

import (
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeAmendment produces a deterministic 32-byte amendment hash
// for a given byte tag. The hash itself doesn't need to match any
// real amendment — the algorithm only treats it as an opaque key.
func makeAmendment(tag byte) Amendment {
	var a Amendment
	for i := range a {
		a[i] = tag
	}
	return a
}

// baseTime is a stable reference point well past the XRPL epoch so
// MajorityTimeout arithmetic stays in a well-defined range.
var baseTime = time.Unix(1_700_000_000, 0).UTC()

func TestThreshold_PreFix(t *testing.T) {
	// 5 validators × 204/256 = 3.98… → integer division → 3.
	assert.Equal(t, 3, Threshold(5, false))
	// 100 validators × 204/256 = 79.6… → 79.
	assert.Equal(t, 79, Threshold(100, false))
	// 0 validators clamps to 1.
	assert.Equal(t, 1, Threshold(0, false))
}

func TestThreshold_PostFix(t *testing.T) {
	// 5 × 80/100 = 4 → 4.
	assert.Equal(t, 4, Threshold(5, true))
	// 100 × 80/100 = 80.
	assert.Equal(t, 80, Threshold(100, true))
	// Clamps to 1 on tiny sets.
	assert.Equal(t, 1, Threshold(1, true))
}

func TestPasses_StrictVsLax(t *testing.T) {
	// trustedValidations=10, strict (post-fix). threshold = 8.
	// Lax: votes >= 8 passes. Strict: votes > 8 passes.
	assert.True(t, passes(8, 8, 10, false), "lax: votes==threshold passes")
	assert.False(t, passes(8, 8, 10, true), "strict: votes==threshold fails")
	assert.True(t, passes(9, 8, 10, true), "strict: votes>threshold passes")
}

func TestPasses_SingleValidatorAlwaysLax(t *testing.T) {
	// trustedValidations==1: even strict mode degrades to >=, else
	// the gate is unreachable. AmendmentTable.cpp:372-374.
	assert.True(t, passes(1, 1, 1, true),
		"with 1 validator and strict mode, votes==threshold MUST pass")
}

func TestDecide_GotMajority(t *testing.T) {
	a := makeAmendment(0xAA)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 9}, // > threshold 8 (strict)
		Stances:            map[Amendment]Stance{a: VoteUp},
		StrictMajority:     true,
	}
	got := Decide(in)
	require.Len(t, got, 1)
	assert.Equal(t, a, got[0].Amendment)
	assert.Equal(t, TfGotMajority, got[0].Flags)
}

func TestDecide_LostMajority(t *testing.T) {
	a := makeAmendment(0xBB)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 4},
		Majority:           map[Amendment]time.Time{a: baseTime.Add(-time.Hour)},
		Stances:            map[Amendment]Stance{a: VoteAbstain},
		StrictMajority:     true,
	}
	got := Decide(in)
	require.Len(t, got, 1)
	assert.Equal(t, a, got[0].Amendment)
	assert.Equal(t, TfLostMajority, got[0].Flags)
}

func TestDecide_EnableWhenWindowHeld(t *testing.T) {
	// Ledger has majority for >= MajorityTimeout, validators still
	// agree (above threshold), local stance is up → enable.
	a := makeAmendment(0xCC)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 9},
		Majority:           map[Amendment]time.Time{a: baseTime.Add(-15 * 24 * time.Hour)},
		Stances:            map[Amendment]Stance{a: VoteUp},
		StrictMajority:     true,
	}
	got := Decide(in)
	require.Len(t, got, 1)
	assert.Equal(t, a, got[0].Amendment)
	assert.Equal(t, uint32(0), got[0].Flags, "enable pseudo-tx carries no flags")
}

func TestDecide_EnableSkippedWhenWindowNotHeldYet(t *testing.T) {
	a := makeAmendment(0xCC)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 9},
		Majority:           map[Amendment]time.Time{a: baseTime.Add(-1 * 24 * time.Hour)},
		Stances:            map[Amendment]Stance{a: VoteUp},
		StrictMajority:     true,
	}
	got := Decide(in)
	assert.Empty(t, got, "must wait until majority has held for MajorityTimeout")
}

func TestDecide_AlreadyEnabledSkipped(t *testing.T) {
	a := makeAmendment(0xDD)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 10},
		Majority:           map[Amendment]time.Time{a: baseTime.Add(-30 * 24 * time.Hour)},
		Stances:            map[Amendment]Stance{a: VoteUp},
		Enabled:            map[Amendment]bool{a: true},
		StrictMajority:     true,
	}
	got := Decide(in)
	assert.Empty(t, got, "already-enabled amendments must be skipped")
}

func TestDecide_ObsoleteStanceNeverVotes(t *testing.T) {
	a := makeAmendment(0xEE)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 10},
		Stances:            map[Amendment]Stance{a: VoteObsolete},
		StrictMajority:     true,
	}
	got := Decide(in)
	assert.Empty(t, got, "obsolete amendments must never produce gotMajority/enable")
}

func TestDecide_AbstainStanceNeverVotes(t *testing.T) {
	// Abstain must NOT propose gotMajority — only VoteUp does. The
	// LostMajority case for abstain is covered by
	// TestDecide_LostMajorityFiresEvenForAbstain.
	a := makeAmendment(0xEF)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 10},
		Stances:            map[Amendment]Stance{a: VoteAbstain},
		StrictMajority:     true,
	}
	got := Decide(in)
	assert.Empty(t, got, "abstain stance does not propose gotMajority")
}

func TestDecide_LostMajorityFiresEvenForAbstain(t *testing.T) {
	// LostMajority does NOT gate on local stance — the ledger
	// needs the timer cleared either way. AmendmentTable.cpp:910-915.
	a := makeAmendment(0xF0)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 0},
		Majority:           map[Amendment]time.Time{a: baseTime.Add(-time.Hour)},
		Stances:            map[Amendment]Stance{a: VoteAbstain},
		StrictMajority:     true,
	}
	got := Decide(in)
	require.Len(t, got, 1)
	assert.Equal(t, TfLostMajority, got[0].Flags)
}

func TestDecide_DeterministicOrder(t *testing.T) {
	// Multiple amendments → output sorted by hash so the resulting
	// tx-set hash is deterministic across map-iteration runs.
	hi := makeAmendment(0xFF)
	mid := makeAmendment(0x80)
	lo := makeAmendment(0x10)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{hi: 9, mid: 9, lo: 9},
		Stances:            map[Amendment]Stance{hi: VoteUp, mid: VoteUp, lo: VoteUp},
		StrictMajority:     true,
	}
	got := Decide(in)
	require.Len(t, got, 3)
	assert.Equal(t, lo, got[0].Amendment)
	assert.Equal(t, mid, got[1].Amendment)
	assert.Equal(t, hi, got[2].Amendment)
}

func TestDoVoting_SerializesEnableAmendmentTx(t *testing.T) {
	a := makeAmendment(0x42)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 9},
		Stances:            map[Amendment]Stance{a: VoteUp},
		StrictMajority:     true,
	}
	blobs, err := DoVoting(in)
	require.NoError(t, err)
	require.Len(t, blobs, 1)

	stx := decodeTx(t, blobs[0])
	assert.Equal(t, hex.EncodeToString(a[:]), stringFold(stx["Amendment"]))
	assert.EqualValues(t, 1024, asUint(stx["LedgerSequence"]))
	assert.EqualValues(t, TfGotMajority, asUint(stx["Flags"]),
		"GotMajority pseudo-tx must carry sfFlags = tfGotMajority")
}

func TestDoVoting_EnableTxOmitsFlags(t *testing.T) {
	// AmendmentTable.h:172-174: rippled writes sfFlags only when
	// non-zero. Our serialized enable pseudo-tx must have no Flags
	// field at all on the wire.
	a := makeAmendment(0x43)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 9},
		Majority:           map[Amendment]time.Time{a: baseTime.Add(-15 * 24 * time.Hour)},
		Stances:            map[Amendment]Stance{a: VoteUp},
		StrictMajority:     true,
	}
	blobs, err := DoVoting(in)
	require.NoError(t, err)
	require.Len(t, blobs, 1)

	stx := decodeTx(t, blobs[0])
	_, hasFlags := stx["Flags"]
	assert.False(t, hasFlags, "enable pseudo-tx (Flags=0) must omit sfFlags on the wire")
}

// runRoundFor is a helper that mirrors rippled's
// AmendmentTable_test.cpp doRound() at the algorithm level. It
// runs Decide once, then mutates `enabled` and `majority` based on
// the emitted Decisions: a GotMajority pseudo-tx records the
// closeTime as the majoritySince timestamp; an Enable pseudo-tx
// (Flags==0) marks the amendment enabled and clears its majority
// entry; a LostMajority pseudo-tx clears the majority entry. This
// matches rippled's `Change::applyEnableAmendment` side-effects on
// the parent ledger between rounds (AmendmentTable.cpp:902-924
// enumerated against ChangeImpl.cpp).
func runRoundFor(
	upcomingSeq uint32,
	closeTime time.Time,
	majorityTimeout time.Duration,
	trusted int,
	votes map[Amendment]int,
	stances map[Amendment]Stance,
	strict bool,
	enabled map[Amendment]bool,
	majority map[Amendment]time.Time,
) []Decision {
	decisions := Decide(Inputs{
		UpcomingSeq:        upcomingSeq,
		CloseTime:          closeTime,
		MajorityTimeout:    majorityTimeout,
		TrustedValidations: trusted,
		Votes:              votes,
		Enabled:            enabled,
		Majority:           majority,
		Stances:            stances,
		StrictMajority:     strict,
	})
	for _, d := range decisions {
		switch d.Flags {
		case TfGotMajority:
			majority[d.Amendment] = closeTime
		case TfLostMajority:
			delete(majority, d.Amendment)
		case 0:
			enabled[d.Amendment] = true
			delete(majority, d.Amendment)
		}
	}
	return decisions
}

// TestDecide_DetectMajoritySweep is the algorithm-level analog of
// rippled's testDetectMajority (AmendmentTable_test.cpp:837-902).
// 16 validators, post-fix strict, 2-week MajorityTimeout, sweep
// validator support i = 0..17 across consecutive weekly rounds.
//
// Expected state machine, mirroring the C++ assertions:
//
//   - i < 13 (< 80%): no GotMajority, no enable, no majority entry
//   - 13 <= i < 15: GotMajority crossed; majority entry held
//   - i == 15: timeout window elapsed → enable fires; majority cleared
//   - i > 15: amendment enabled; no further pseudo-tx
func TestDecide_DetectMajoritySweep(t *testing.T) {
	const validators = 16
	const majorityTimeout = 2 * 7 * 24 * time.Hour
	a := makeAmendment(0xA1)

	stances := map[Amendment]Stance{a: VoteUp}
	enabled := map[Amendment]bool{}
	majority := map[Amendment]time.Time{}

	for i := 0; i <= 17; i++ {
		votes := map[Amendment]int{}
		// Match rippled's "if i>0 && i<17" block: at i==0 nobody
		// votes; at i==17 we stop voting (the amendment is already
		// enabled by then so the producer should be silent).
		if i > 0 && i < 17 {
			votes[a] = i
		}
		closeTime := baseTime.Add(time.Duration(i) * 7 * 24 * time.Hour)

		runRoundFor(
			uint32(1024+i), closeTime, majorityTimeout,
			validators, votes, stances, true,
			enabled, majority,
		)

		switch {
		case i < 13:
			assert.False(t, enabled[a], "i=%d: amendment must not be enabled yet", i)
			assert.NotContains(t, majority, a, "i=%d: no majority recorded", i)
		case i < 15:
			assert.False(t, enabled[a], "i=%d: still pre-enable", i)
			assert.Contains(t, majority, a, "i=%d: majority recorded but timer not elapsed", i)
		case i == 15:
			assert.True(t, enabled[a], "i=%d: enable fires after 2-week window", i)
			assert.NotContains(t, majority, a, "i=%d: enable clears majority entry", i)
		default:
			assert.True(t, enabled[a], "i=%d: amendment stays enabled", i)
			assert.NotContains(t, majority, a, "i=%d: enabled amendment has no majority entry", i)
		}
	}
}

// TestDecide_LostMajoritySweep is the algorithm-level analog of
// rippled's testLostMajority (AmendmentTable_test.cpp:906-979).
// 16 validators, post-fix strict, 8-week MajorityTimeout (so the
// enable doesn't fire and we observe lost-majority cleanly).
//
// Round 1: full support → GotMajority; majority recorded.
// Rounds 2..7: gradually reduce support (16-i votes).
// At i=4 (12/16 = 75% < 80%) LostMajority fires; majority cleared.
func TestDecide_LostMajoritySweep(t *testing.T) {
	const validators = 16
	const majorityTimeout = 8 * 7 * 24 * time.Hour
	a := makeAmendment(0xA2)

	stances := map[Amendment]Stance{a: VoteUp}
	enabled := map[Amendment]bool{}
	majority := map[Amendment]time.Time{}

	// Round 1: establish majority.
	runRoundFor(
		1024,
		baseTime.Add(7*24*time.Hour),
		majorityTimeout,
		validators,
		map[Amendment]int{a: validators},
		stances, true,
		enabled, majority,
	)
	require.False(t, enabled[a], "round 1: enable must not fire (8-week timeout)")
	require.Contains(t, majority, a, "round 1: majority must be recorded")

	for i := 1; i < 8; i++ {
		closeTime := baseTime.Add(time.Duration(i+1) * 7 * 24 * time.Hour)
		runRoundFor(
			uint32(1024+i), closeTime, majorityTimeout,
			validators,
			map[Amendment]int{a: validators - i},
			stances, true,
			enabled, majority,
		)

		if i < 4 {
			assert.False(t, enabled[a], "i=%d: not yet enabled", i)
			assert.Contains(t, majority, a, "i=%d: majority still held (>80%%)", i)
		} else {
			assert.False(t, enabled[a], "i=%d: still not enabled (lost majority)", i)
			assert.NotContains(t, majority, a,
				"i=%d: LostMajority must clear the majority entry", i)
		}
	}
}

// TestDecide_VoteEnableMultiWeek mirrors rippled's testVoteEnable
// (AmendmentTable_test.cpp:757-833) at the algorithm level. With
// 10 validators, 2-week MajorityTimeout, post-fix strict, and a
// single test amendment we vote VoteUp for:
//
//   - Week 1: nobody else has voted → no decision
//   - Week 2: full support → GotMajority + majority recorded
//   - Week 5 (3 weeks past majority, > 2-week timeout): enable
//     fires; majority cleared; enabled set populated
//   - Week 6: amendment enabled — producer must stay silent
//
// This pins the multi-week ordering: GotMajority precedes Enable
// by at least MajorityTimeout, and an enabled amendment never
// re-emits.
func TestDecide_VoteEnableMultiWeek(t *testing.T) {
	const validators = 10
	const majorityTimeout = 2 * 7 * 24 * time.Hour
	a := makeAmendment(0xA3)

	stances := map[Amendment]Stance{a: VoteUp}
	enabled := map[Amendment]bool{}
	majority := map[Amendment]time.Time{}

	// Week 1: validators don't yet have any opinion (votes empty).
	d1 := runRoundFor(
		1024, baseTime.Add(1*7*24*time.Hour), majorityTimeout,
		validators, map[Amendment]int{},
		stances, true, enabled, majority,
	)
	assert.Empty(t, d1, "week 1: no votes yet → no decision")
	assert.False(t, enabled[a])
	assert.NotContains(t, majority, a)

	// Week 2: all 10 vote → GotMajority.
	d2 := runRoundFor(
		1025, baseTime.Add(2*7*24*time.Hour), majorityTimeout,
		validators, map[Amendment]int{a: validators},
		stances, true, enabled, majority,
	)
	require.Len(t, d2, 1)
	assert.Equal(t, TfGotMajority, d2[0].Flags,
		"week 2: full support crosses threshold → GotMajority")
	assert.Contains(t, majority, a, "week 2: majority recorded")
	assert.False(t, enabled[a])

	// Week 5: 3 weeks past majority (> 2-week timeout) → enable.
	d5 := runRoundFor(
		1028, baseTime.Add(5*7*24*time.Hour), majorityTimeout,
		validators, map[Amendment]int{a: validators},
		stances, true, enabled, majority,
	)
	require.Len(t, d5, 1)
	assert.EqualValues(t, 0, d5[0].Flags,
		"week 5: timeout elapsed → enable pseudo-tx (Flags==0)")
	assert.True(t, enabled[a], "week 5: enable mutates the enabled set")
	assert.NotContains(t, majority, a, "week 5: enable clears majority entry")

	// Week 6: amendment is enabled → producer silent.
	d6 := runRoundFor(
		1029, baseTime.Add(6*7*24*time.Hour), majorityTimeout,
		validators, map[Amendment]int{a: validators},
		stances, true, enabled, majority,
	)
	assert.Empty(t, d6, "week 6: enabled amendment must produce no further pseudo-tx")
}

func decodeTx(t *testing.T, blob []byte) map[string]any {
	t.Helper()
	out, err := binarycodec.Decode(hex.EncodeToString(blob))
	require.NoError(t, err, "EnableAmendment must round-trip through binarycodec.Decode")
	return out
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
		x, _ = strconv.ParseUint(n, 16, 64)
		return x
	}
	return 0
}

func stringFold(v any) string {
	switch s := v.(type) {
	case string:
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
