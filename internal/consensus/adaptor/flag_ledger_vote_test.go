package adaptor

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/amendmentvote"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestAdaptorWithConfig is newTestAdaptor with FeeVote and
// AmendmentVote config knobs exposed.
func newTestAdaptorWithConfig(t *testing.T, fee FeeVoteStance, amendmentVote []string) *Adaptor {
	t.Helper()
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	return New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
		FeeVote:       fee,
		AmendmentVote: amendmentVote,
	})
}

// TestGenerateFlagLedgerPseudoTxs_BelowQuorumNoBlobs pins the
// quorum gate at RCLConsensus.cpp:361: with the trusted set
// non-empty (quorum >= 1) and zero validations passed in, the
// producer falls through to nil before any fee or amendment work.
func TestGenerateFlagLedgerPseudoTxs_BelowQuorumNoBlobs(t *testing.T) {
	a := newTestAdaptor(t)
	prev := a.ledgerService.GetClosedLedger()
	require.NotNil(t, prev)
	wrapped := WrapLedger(prev)

	require.Equal(t, 1, a.GetQuorum(), "single-validator UNL must yield quorum=1")

	blobs := a.GenerateFlagLedgerPseudoTxs(wrapped, nil)
	assert.Nil(t, blobs,
		"len(filtered)=0 < quorum=1 → no pseudo-txs (RCLConsensus.cpp:361)")
}

// TestGenerateFlagLedgerPseudoTxs_FeeVoteSeedsSetFee pins the
// fee-vote wiring: with a configured FeeVote target that differs
// from the parent ledger's FeeSettings, the constructor seed for
// target wins and a SetFee pseudo-tx is emitted.
//
// Genesis FeeSettings carries the standalone defaults; a target
// 10× the base fee is well outside that, so the algorithm picks
// target. We pass a self-validation of the parent so the post-
// quorum-gate code runs (RCLConsensus.cpp:361 requires
// validations.size() >= quorum, which is 1 in this single-validator
// fixture).
func TestGenerateFlagLedgerPseudoTxs_FeeVoteSeedsSetFee(t *testing.T) {
	a := newTestAdaptorWithConfig(t, FeeVoteStance{
		BaseFee:          100, // genesis is 10
		ReserveBase:      50_000_000,
		ReserveIncrement: 5_000_000,
	}, nil)
	prev := a.ledgerService.GetClosedLedger()
	require.NotNil(t, prev)
	wrapped := WrapLedger(prev)

	// Self-validation that votes for the target fees. Both legacy
	// and post-XRPFees field sets are populated so the test is
	// agnostic to whichever wire format the parent ledger's
	// amendment state selects.
	val := &consensus.Validation{
		LedgerID:              wrapped.ParentID(),
		LedgerSeq:             wrapped.Seq() - 1,
		NodeID:                a.identity.NodeID,
		SignTime:              time.Now(),
		BaseFee:               100,
		ReserveBase:           50_000_000,
		ReserveIncrement:      5_000_000,
		BaseFeeDrops:          100,
		ReserveBaseDrops:      50_000_000,
		ReserveIncrementDrops: 5_000_000,
	}

	blobs := a.GenerateFlagLedgerPseudoTxs(wrapped, []*consensus.Validation{val})
	require.Len(t, blobs, 1, "fee target differs from current → one SetFee blob")

	stx := decodeTx(t, blobs[0])
	assert.Equal(t, "SetFee", stx["TransactionType"],
		"emitted blob must be a SetFee pseudo-tx")
	assert.EqualValues(t, prev.Sequence()+1, asUint(stx["LedgerSequence"]),
		"LedgerSequence carries upcoming seq = parent + 1")
}

// TestExcludeNegativeUNL_DropsBannedValidators is a pure-helper
// test of the negUNL filter at RCLConsensus.cpp:358: validations
// signed by validators in the negUNL set are dropped before the
// quorum count and before vote tallying.
func TestExcludeNegativeUNL_DropsBannedValidators(t *testing.T) {
	good := consensus.NodeID{0x01}
	banned := consensus.NodeID{0x02}
	keep := consensus.NodeID{0x03}
	vals := []*consensus.Validation{
		{NodeID: good},
		{NodeID: banned},
		{NodeID: keep},
	}

	out := excludeNegativeUNL(vals, []consensus.NodeID{banned})
	require.Len(t, out, 2, "one validator on negUNL → two validations remain")
	assert.Equal(t, good, out[0].NodeID)
	assert.Equal(t, keep, out[1].NodeID)
}

// TestExcludeNegativeUNL_EmptyNegUNLPassesThrough verifies the
// no-allocation fast path: an empty negUNL returns the input slice
// header unchanged.
func TestExcludeNegativeUNL_EmptyNegUNLPassesThrough(t *testing.T) {
	vals := []*consensus.Validation{{NodeID: consensus.NodeID{0x01}}}
	out := excludeNegativeUNL(vals, nil)
	assert.Equal(t, vals, out, "empty negUNL → unchanged input")
}

// TestGenerateFlagLedgerPseudoTxs_AmendmentVoteSeedsGotMajority
// pins the amendment-vote wiring: with a configured AmendmentVote
// stance and one trusted validator (ourselves) voting for it, the
// algorithm emits an EnableAmendment with tfGotMajority.
//
// Genesis enables every SupportedYes/VoteDefaultYes amendment in
// the registry, so any real amendment name is already in the
// Enabled set and the producer correctly skips it. To exercise
// the not-yet-enabled branch we synthesize a pretend amendment
// hash that the ledger has never seen and inject it into the
// local stance directly.
func TestGenerateFlagLedgerPseudoTxs_AmendmentVoteSeedsGotMajority(t *testing.T) {
	a := newTestAdaptorWithConfig(t, FeeVoteStance{}, nil)
	var synthetic [32]byte
	for i := range synthetic {
		synthetic[i] = 0xC1
	}
	a.amendmentStances = map[[32]byte]amendmentvote.Stance{synthetic: amendmentvote.VoteUp}

	prev := a.ledgerService.GetClosedLedger()
	require.NotNil(t, prev)
	wrapped := WrapLedger(prev)

	val := &consensus.Validation{
		LedgerID:   wrapped.ID(),
		LedgerSeq:  wrapped.Seq(),
		NodeID:     a.identity.NodeID,
		SignTime:   time.Now(),
		Amendments: [][32]byte{synthetic},
	}

	blobs := a.GenerateFlagLedgerPseudoTxs(wrapped, []*consensus.Validation{val})

	var found bool
	for _, blob := range blobs {
		stx := decodeTx(t, blob)
		if stx["TransactionType"] != "EnableAmendment" {
			continue
		}
		if stringFold(stx["Amendment"]) != hex.EncodeToString(synthetic[:]) {
			continue
		}
		if asUint(stx["Flags"]) == 0x00010000 {
			found = true
			break
		}
	}
	assert.True(t, found,
		"expected EnableAmendment(synthetic, GotMajority) when our single trusted validator votes for it")
}

// TestAmendmentStances_SeededFromRegistry verifies the constructor
// walks amendment.AllFeatures() and seeds the stance map from each
// feature's registered VoteBehavior, mirroring rippled's
// AmendmentTable.cpp:556-580 — DefaultYes amendments default to
// VoteUp, Obsolete amendments default to VoteObsolete, DefaultNo
// amendments are absent from the map (lookup returns VoteAbstain).
// Without this, an unconfigured Go validator silently abstains on
// every amendment a rippled validator would auto-upvote.
func TestAmendmentStances_SeededFromRegistry(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: newTestLedgerService(t),
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})

	upvoted, obsolete, defaultNoSeen := 0, 0, 0
	for _, f := range amendment.AllFeatures() {
		stance, present := a.amendmentStances[f.ID]
		switch {
		case f.Vote == amendment.VoteObsolete:
			require.True(t, present, "obsolete amendment %q must seed VoteObsolete", f.Name)
			assert.Equal(t, amendmentvote.VoteObsolete, stance,
				"obsolete amendment %q must be VoteObsolete", f.Name)
			obsolete++
		case f.Supported == amendment.SupportedYes && f.Vote == amendment.VoteDefaultYes && !f.Retired:
			require.True(t, present, "default-yes amendment %q must seed VoteUp", f.Name)
			assert.Equal(t, amendmentvote.VoteUp, stance,
				"default-yes supported non-retired amendment %q must be VoteUp", f.Name)
			upvoted++
		case f.Vote == amendment.VoteDefaultNo:
			assert.False(t, present,
				"default-no amendment %q must NOT be in stances (lookup returns VoteAbstain)", f.Name)
			defaultNoSeen++
		}
	}
	assert.Greater(t, upvoted, 0, "registry must contain at least one default-yes amendment")
	assert.Greater(t, obsolete, 0, "registry must contain at least one obsolete amendment")
	assert.Greater(t, defaultNoSeen, 0, "registry must contain at least one default-no amendment")
}

// TestAmendmentStances_ConfigOverridesUpvote verifies an operator
// can promote a default-no amendment to VoteUp via
// Config.AmendmentVote — the same role rippled's [amendments]
// stanza plays at AmendmentTable.cpp:584-598.
func TestAmendmentStances_ConfigOverridesUpvote(t *testing.T) {
	// Pick a default-no, supported, non-retired feature from the
	// registry. fixDirectoryLimit fits this profile in the current
	// registry; if that ever flips we'll fail loudly here rather
	// than silently testing the wrong branch.
	target := amendment.GetFeatureByName("fixDirectoryLimit")
	require.NotNil(t, target)
	require.Equal(t, amendment.VoteDefaultNo, target.Vote,
		"test premise: target must be VoteDefaultNo (registry changed?)")

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: newTestLedgerService(t),
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
		AmendmentVote: []string{target.Name},
	})

	stance, present := a.amendmentStances[target.ID]
	require.True(t, present, "operator-listed amendment must enter stances")
	assert.Equal(t, amendmentvote.VoteUp, stance,
		"Config.AmendmentVote must override default-no to VoteUp")
}

// TestAmendmentStances_ConfigCannotOverrideObsolete pins rippled's
// AmendmentTable.cpp:728-733 contract: persistVote refuses to flip
// an obsolete amendment's vote. Listing such an amendment in
// Config.AmendmentVote must NOT promote it to VoteUp.
func TestAmendmentStances_ConfigCannotOverrideObsolete(t *testing.T) {
	target := amendment.GetFeatureByName("NonFungibleTokensV1")
	require.NotNil(t, target)
	require.Equal(t, amendment.VoteObsolete, target.Vote,
		"test premise: target must be VoteObsolete (registry changed?)")

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: newTestLedgerService(t),
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
		AmendmentVote: []string{target.Name},
	})

	stance, present := a.amendmentStances[target.ID]
	require.True(t, present, "obsolete amendment seeded by registry walk")
	assert.Equal(t, amendmentvote.VoteObsolete, stance,
		"Config.AmendmentVote must NOT promote obsolete amendments to VoteUp")
}

// TestFeeVote_EmptyConfigUsesRippledDefaults pins Item 1: with no
// FeeVote stanza in the config, adaptor.New() must seed each field
// from rippled's FeeSetup defaults (Config.h:65-78) so an
// unconfigured validator votes toward those defaults rather than
// no-change.
func TestFeeVote_EmptyConfigUsesRippledDefaults(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: newTestLedgerService(t),
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	assert.EqualValues(t, 10, a.feeVote.BaseFee,
		"empty BaseFee → rippled FeeSetup default reference_fee=10")
	assert.EqualValues(t, 10_000_000, a.feeVote.ReserveBase,
		"empty ReserveBase → rippled FeeSetup default account_reserve=10*DROPS_PER_XRP")
	assert.EqualValues(t, 2_000_000, a.feeVote.ReserveIncrement,
		"empty ReserveIncrement → rippled FeeSetup default owner_reserve=2*DROPS_PER_XRP")
}

// TestFeeVote_PartialConfigKeepsExplicitFields verifies the
// per-field substitution: an operator who sets BaseFee but leaves
// reserves zero must keep their explicit BaseFee and inherit the
// rippled defaults for the unset reserve fields.
func TestFeeVote_PartialConfigKeepsExplicitFields(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: newTestLedgerService(t),
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
		FeeVote:       FeeVoteStance{BaseFee: 25}, // reserves left zero
	})
	assert.EqualValues(t, 25, a.feeVote.BaseFee, "explicit BaseFee preserved")
	assert.EqualValues(t, 10_000_000, a.feeVote.ReserveBase,
		"unset ReserveBase → rippled default")
	assert.EqualValues(t, 2_000_000, a.feeVote.ReserveIncrement,
		"unset ReserveIncrement → rippled default")
}

// TestParseAmendmentsSLEBytes_FailsClosedOnGarbage pins Item 2:
// the bytes-half of readAmendmentsSLE returns ok=false on parse
// error so GenerateFlagLedgerPseudoTxs can suppress the round.
// Mirrors rippled's RCLConsensus::onClose, which has no try/catch
// around amendmentTable.doVoting — a malformed Amendments SLE
// propagates the exception and prevents pseudo-tx emission. The
// alternative ("no amendments enabled") would let GotMajority /
// Enable fire spuriously on every tracked amendment.
func TestParseAmendmentsSLEBytes_FailsClosedOnGarbage(t *testing.T) {
	corrupt := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	enabled, majorities, ok := parseAmendmentsSLEBytes(corrupt)
	assert.False(t, ok, "garbage SLE bytes must return ok=false")
	assert.Nil(t, enabled, "ok=false must zero enabled to prevent partial-state misuse")
	assert.Nil(t, majorities, "ok=false must zero majorities to prevent partial-state misuse")
}

// TestParseAmendmentsSLEBytes_EmptyIsBootstrap pins the
// genesis-bootstrap path: an empty SLE (no entry installed) is a
// successful read of empty state, NOT corruption — returns
// ok=true with empty maps so producers can run normally.
func TestParseAmendmentsSLEBytes_EmptyIsBootstrap(t *testing.T) {
	enabled, majorities, ok := parseAmendmentsSLEBytes(nil)
	assert.True(t, ok, "empty SLE is bootstrap state, not corruption")
	assert.Empty(t, enabled)
	assert.Empty(t, majorities)
}

// TestRunAmendmentVote_UsesParentCloseTime pins the rippled-parity
// time choice at AmendmentTable.h:157: doVoting is invoked with
// lastClosedLedger->parentCloseTime() — the close time of the
// ledger whose validations are being tallied — not the close time
// of the flag ledger itself. Pairing the parent-validations with
// prev's own close time would drift the 24h trusted-vote cache
// expiry and the majority-window enable check by one round.
//
// Build a synthetic prev with ParentCloseTime far enough below
// CloseTime that the cache timeout is unambiguous, run the
// producer, and verify the recorded timeout is parent-anchored.
func TestRunAmendmentVote_UsesParentCloseTime(t *testing.T) {
	a := newTestAdaptorWithConfig(t, FeeVoteStance{}, nil)

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)

	parentClose := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	flagClose := parentClose.Add(time.Hour)
	prev := ledger.NewFromHeader(header.LedgerHeader{
		LedgerIndex:     256,
		ParentCloseTime: parentClose,
		CloseTime:       flagClose,
	}, stateMap, txMap, drops.Fees{})

	val := &consensus.Validation{
		NodeID:     a.identity.NodeID,
		Amendments: [][32]byte{{0xA1}},
	}
	a.runAmendmentVote(prev, prev.Sequence()+1, []*consensus.Validation{val}, nil, nil)

	entry, ok := a.trustedVotes.recordedVotes[a.identity.NodeID]
	require.True(t, ok, "self-validation must register in trusted-vote cache")
	require.True(t, entry.hasTimeout(),
		"recordVotes must seat the timeout for a fresh validation")

	want := parentClose.Add(trustedVotesTimeout)
	assert.True(t, entry.timeout.Equal(want),
		"timeout must be ParentCloseTime+24h (got %s, want %s); "+
			"if this drifts to flagClose+24h the producer regressed to "+
			"prev.CloseTime, breaking AmendmentTable.h:157 parity",
		entry.timeout, want)
}

// TestGenerateFlagLedgerPseudoTxs_NoLedgerService is the defensive
// path: an adaptor with no ledger service returns nil rather than
// panicking.
func TestGenerateFlagLedgerPseudoTxs_NoLedgerService(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		Identity:   identity,
		Validators: []consensus.NodeID{identity.NodeID},
	})
	prev := newTestLedgerService(t).GetClosedLedger()
	wrapped := WrapLedger(prev)
	blobs := a.GenerateFlagLedgerPseudoTxs(wrapped, nil)
	assert.Nil(t, blobs, "no ledger service → no pseudo-txs (no panic)")
}

func decodeTx(t *testing.T, blob []byte) map[string]any {
	t.Helper()
	out, err := binarycodec.Decode(hex.EncodeToString(blob))
	require.NoError(t, err, "pseudo-tx blob must round-trip through binarycodec.Decode")
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
	s, ok := v.(string)
	if !ok {
		return ""
	}
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
