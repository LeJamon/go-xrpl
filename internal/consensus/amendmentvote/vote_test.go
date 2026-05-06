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
	// Ledger has majority, validators fell off — emit lostMajority
	// regardless of local stance.
	a := makeAmendment(0xBB)
	in := Inputs{
		UpcomingSeq:        1024,
		CloseTime:          baseTime,
		MajorityTimeout:    14 * 24 * time.Hour,
		TrustedValidations: 10,
		Votes:              map[Amendment]int{a: 4}, // below threshold
		Majority:           map[Amendment]time.Time{a: baseTime.Add(-time.Hour)},
		Stances:            map[Amendment]Stance{a: VoteAbstain}, // not voting yes locally
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
		Enabled:            map[Amendment]bool{a: true}, // already on
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
		Votes:              map[Amendment]int{a: 10}, // strong validator support
		Stances:            map[Amendment]Stance{a: VoteObsolete},
		StrictMajority:     true,
	}
	got := Decide(in)
	assert.Empty(t, got, "obsolete amendments must never produce gotMajority/enable")
}

func TestDecide_AbstainStanceNeverVotes(t *testing.T) {
	// VoteAbstain must produce no gotMajority — only VoteUp does.
	// (Abstain still allows lostMajority, see other test.)
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
