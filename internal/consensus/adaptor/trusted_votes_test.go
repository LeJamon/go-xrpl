package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trustedVotesEpoch is a stable reference past the XRPL epoch so
// timeout arithmetic stays in well-defined territory.
var trustedVotesEpoch = time.Unix(1_700_000_000, 0).UTC()

func makeNodeID(b byte) consensus.NodeID {
	var id consensus.NodeID
	id[0] = b
	return id
}

func makeAmendmentTV(b byte) [32]byte {
	var a [32]byte
	for i := range a {
		a[i] = b
	}
	return a
}

// TestTrustedVotes_RecordsTrustedValidatorVotes verifies the basic
// happy path: a validation from a trusted validator contributes
// its sfAmendments to GetVotes and bumps available by one.
func TestTrustedVotes_RecordsTrustedValidatorVotes(t *testing.T) {
	v1 := makeNodeID(1)
	v2 := makeNodeID(2)
	a := makeAmendmentTV(0xAA)
	b := makeAmendmentTV(0xBB)

	tv := NewTrustedVotes()
	tv.TrustChanged([]consensus.NodeID{v1, v2})

	tv.RecordVotes(trustedVotesEpoch, []*consensus.Validation{
		{NodeID: v1, Amendments: [][32]byte{a, b}},
		{NodeID: v2, Amendments: [][32]byte{a}},
	})

	available, votes := tv.GetVotes()
	assert.Equal(t, 2, available, "both trusted validators voted → available=2")
	assert.Equal(t, 2, votes[a], "amendment a got two upVotes")
	assert.Equal(t, 1, votes[b], "amendment b got one upVote")
}

// TestTrustedVotes_IgnoresUntrustedValidations verifies that a
// validation from a non-trusted NodeID is silently dropped — its
// votes do not appear in GetVotes and it does not affect available.
func TestTrustedVotes_IgnoresUntrustedValidations(t *testing.T) {
	trusted := makeNodeID(1)
	untrusted := makeNodeID(99)
	a := makeAmendmentTV(0xAA)

	tv := NewTrustedVotes()
	tv.TrustChanged([]consensus.NodeID{trusted})

	tv.RecordVotes(trustedVotesEpoch, []*consensus.Validation{
		{NodeID: untrusted, Amendments: [][32]byte{a}},
	})

	available, votes := tv.GetVotes()
	assert.Equal(t, 0, available, "no trusted validations → available=0")
	assert.Empty(t, votes, "untrusted votes must not be tallied")
}

// TestTrustedVotes_24hTimeoutClears advances closeTime past the
// 24h timeout and verifies that the cached vote is fully evicted —
// timeout unseated, upVotes cleared, available drops to zero.
func TestTrustedVotes_24hTimeoutClears(t *testing.T) {
	v1 := makeNodeID(1)
	a := makeAmendmentTV(0xAA)

	tv := NewTrustedVotes()
	tv.TrustChanged([]consensus.NodeID{v1})
	tv.RecordVotes(trustedVotesEpoch, []*consensus.Validation{
		{NodeID: v1, Amendments: [][32]byte{a}},
	})

	// Advance 25h with no fresh validations → entry expires.
	tv.RecordVotes(trustedVotesEpoch.Add(25*time.Hour), nil)

	available, votes := tv.GetVotes()
	assert.Equal(t, 0, available, "timeout expired → available=0")
	assert.Empty(t, votes, "expired entry contributes no votes")
}

// TestTrustedVotes_BelowTimeoutPreservesVote verifies the
// flap-dampening core: a validator silent for less than 24h still
// contributes its last seen vote.
func TestTrustedVotes_BelowTimeoutPreservesVote(t *testing.T) {
	v1 := makeNodeID(1)
	a := makeAmendmentTV(0xAA)

	tv := NewTrustedVotes()
	tv.TrustChanged([]consensus.NodeID{v1})
	tv.RecordVotes(trustedVotesEpoch, []*consensus.Validation{
		{NodeID: v1, Amendments: [][32]byte{a}},
	})

	// 23h later, no fresh validations: entry still alive.
	tv.RecordVotes(trustedVotesEpoch.Add(23*time.Hour), nil)

	available, votes := tv.GetVotes()
	assert.Equal(t, 1, available, "within timeout → available preserved")
	assert.Equal(t, 1, votes[a], "within timeout → vote preserved")
}

// TestTrustedVotes_NewVotesReplacePrevious verifies that a fresh
// validation replaces the cached upVotes wholesale (not append).
func TestTrustedVotes_NewVotesReplacePrevious(t *testing.T) {
	v1 := makeNodeID(1)
	a := makeAmendmentTV(0xAA)
	b := makeAmendmentTV(0xBB)

	tv := NewTrustedVotes()
	tv.TrustChanged([]consensus.NodeID{v1})

	tv.RecordVotes(trustedVotesEpoch, []*consensus.Validation{
		{NodeID: v1, Amendments: [][32]byte{a}},
	})
	tv.RecordVotes(trustedVotesEpoch.Add(time.Hour), []*consensus.Validation{
		{NodeID: v1, Amendments: [][32]byte{b}},
	})

	_, votes := tv.GetVotes()
	assert.Zero(t, votes[a], "replaced vote must NOT contribute to a anymore")
	assert.Equal(t, 1, votes[b], "current vote contributes to b")
}

// TestTrustedVotes_EmptyAmendmentsClearsUpvotes mirrors rippled's
// "validator has no amendment votes" branch at
// AmendmentTable.cpp:206-211: a validation with empty sfAmendments
// resets the cached upVotes to nothing while still refreshing the
// timeout.
func TestTrustedVotes_EmptyAmendmentsClearsUpvotes(t *testing.T) {
	v1 := makeNodeID(1)
	a := makeAmendmentTV(0xAA)

	tv := NewTrustedVotes()
	tv.TrustChanged([]consensus.NodeID{v1})

	tv.RecordVotes(trustedVotesEpoch, []*consensus.Validation{
		{NodeID: v1, Amendments: [][32]byte{a}},
	})
	tv.RecordVotes(trustedVotesEpoch.Add(time.Hour), []*consensus.Validation{
		{NodeID: v1, Amendments: nil}, // empty sfAmendments
	})

	available, votes := tv.GetVotes()
	assert.Equal(t, 1, available, "validator still alive → available=1")
	assert.Empty(t, votes, "empty sfAmendments → no votes contributed")
}

// TestTrustedVotes_TrustChangedPreservesExisting mirrors
// AmendmentTable.cpp:130-143: validators retained across a UNL
// change keep their cached vote; new validators get a fresh
// empty entry.
func TestTrustedVotes_TrustChangedPreservesExisting(t *testing.T) {
	v1 := makeNodeID(1)
	v2 := makeNodeID(2)
	v3 := makeNodeID(3)
	a := makeAmendmentTV(0xAA)

	tv := NewTrustedVotes()
	tv.TrustChanged([]consensus.NodeID{v1, v2})
	tv.RecordVotes(trustedVotesEpoch, []*consensus.Validation{
		{NodeID: v1, Amendments: [][32]byte{a}},
	})

	// Replace v2 with v3; v1 retained.
	tv.TrustChanged([]consensus.NodeID{v1, v3})

	available, votes := tv.GetVotes()
	assert.Equal(t, 1, available, "v1 still alive (v3 has no validations yet)")
	assert.Equal(t, 1, votes[a], "v1's preserved vote still contributes")
}

// TestTrustedVotes_TrustChangedDropsRemoved verifies that votes
// from validators no longer trusted are evicted entirely. Mirrors
// the swap at AmendmentTable.cpp:146.
func TestTrustedVotes_TrustChangedDropsRemoved(t *testing.T) {
	v1 := makeNodeID(1)
	v2 := makeNodeID(2)
	a := makeAmendmentTV(0xAA)

	tv := NewTrustedVotes()
	tv.TrustChanged([]consensus.NodeID{v1})
	tv.RecordVotes(trustedVotesEpoch, []*consensus.Validation{
		{NodeID: v1, Amendments: [][32]byte{a}},
	})

	// v1 removed from UNL, v2 added.
	tv.TrustChanged([]consensus.NodeID{v2})

	available, votes := tv.GetVotes()
	assert.Equal(t, 0, available, "v2 has no votes; v1's entry was dropped")
	assert.Zero(t, votes[a], "removed validator's votes must not contribute")
}

// TestTrustedVotes_AvailableCountReflectsTimeoutSet exercises the
// available counter through the full lifecycle: fresh, recording,
// and post-expiry.
func TestTrustedVotes_AvailableCountReflectsTimeoutSet(t *testing.T) {
	v1 := makeNodeID(1)
	v2 := makeNodeID(2)
	v3 := makeNodeID(3)
	a := makeAmendmentTV(0xAA)

	tv := NewTrustedVotes()
	tv.TrustChanged([]consensus.NodeID{v1, v2, v3})

	available, _ := tv.GetVotes()
	assert.Equal(t, 0, available, "fresh entries have unseated timeout → available=0")

	tv.RecordVotes(trustedVotesEpoch, []*consensus.Validation{
		{NodeID: v1, Amendments: [][32]byte{a}},
	})
	available, _ = tv.GetVotes()
	assert.Equal(t, 1, available, "one validation → available=1")

	// Advance past timeout with no fresh validations.
	tv.RecordVotes(trustedVotesEpoch.Add(25*time.Hour), nil)
	available, _ = tv.GetVotes()
	assert.Equal(t, 0, available, "post-expiry → available=0")
}

// TestTrustedVotes_Integration_FlapsAtFlagLedger pins the flap-
// dampening behavior the cache exists for. Two scenarios with 11
// validators and a single test amendment:
//
//   - fast flap (validator drops every 23h): all votes contribute,
//     11/11 stays above 80%.
//   - slow flap (validator drops every 25h): the flapping
//     validator's vote expires before its next validation, so the
//     tally drops to 10/11 some rounds and below the strict 80%
//     gate (8.8 → strictly > 8 fails when votes drop to ≤ 8).
//
// We exercise GetVotes directly rather than running Decide; the
// algorithm-level threshold check is exercised in the
// amendmentvote package tests. What this test pins is the cache's
// effect on the available denominator and per-amendment votes
// across multi-round time advancement.
func TestTrustedVotes_Integration_FlapsAtFlagLedger(t *testing.T) {
	const total = 11
	flapper := makeNodeID(0)
	stable := make([]consensus.NodeID, 0, total-1)
	for i := 1; i < total; i++ {
		stable = append(stable, makeNodeID(byte(i)))
	}
	all := append([]consensus.NodeID{flapper}, stable...)
	amend := makeAmendmentTV(0xAA)

	build := func(nodes []consensus.NodeID) []*consensus.Validation {
		out := make([]*consensus.Validation, 0, len(nodes))
		for _, id := range nodes {
			out = append(out, &consensus.Validation{
				NodeID:     id,
				Amendments: [][32]byte{amend},
			})
		}
		return out
	}

	t.Run("fast flap stays above threshold", func(t *testing.T) {
		tv := NewTrustedVotes()
		tv.TrustChanged(all)

		// Round 0: everyone votes.
		tv.RecordVotes(trustedVotesEpoch, build(all))

		// Round 1 (23h later): flapper silent, but still within
		// timeout → its vote still contributes.
		tv.RecordVotes(trustedVotesEpoch.Add(23*time.Hour), build(stable))
		available, votes := tv.GetVotes()
		assert.Equal(t, total, available,
			"23h flap: all entries still within timeout")
		assert.Equal(t, total, votes[amend],
			"23h flap: flapper's last vote still contributes")
	})

	t.Run("slow flap loses the flapper's vote", func(t *testing.T) {
		tv := NewTrustedVotes()
		tv.TrustChanged(all)

		// Round 0: everyone votes.
		tv.RecordVotes(trustedVotesEpoch, build(all))

		// Round 1 (25h later): flapper silent and past timeout →
		// its entry is cleared.
		tv.RecordVotes(trustedVotesEpoch.Add(25*time.Hour), build(stable))
		available, votes := tv.GetVotes()
		require.Equal(t, total-1, available,
			"25h flap: flapper expired, only stable validators count")
		assert.Equal(t, total-1, votes[amend],
			"25h flap: flapper's vote no longer contributes")
	})
}
