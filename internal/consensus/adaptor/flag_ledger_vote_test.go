package adaptor

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestAdaptorWithConfig is newTestAdaptor with the operator
// config knobs (FeeVote, AmendmentVote) exposed for the
// flag-ledger-vote tests. Reuses the same standalone service +
// validator seed.
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
	// LedgerSequence carries upcoming seq = parent + 1.
	assert.EqualValues(t, prev.Sequence()+1, asUint(stx["LedgerSequence"]))
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
	a.amendmentVoteIDs = [][32]byte{synthetic}

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

// TestGenerateFlagLedgerPseudoTxs_NoLedgerService is the defensive
// path: an adaptor with no ledger service returns nil rather than
// panicking. Tests that construct adaptors directly (without going
// through Components) hit this path.
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
