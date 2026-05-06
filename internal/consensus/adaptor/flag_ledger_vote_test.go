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

// TestGenerateFlagLedgerPseudoTxs_NoStanceNoBlobs verifies the
// quiet path: a freshly-started node with no fee or amendment
// vote stance produces no pseudo-txs at flag-ledger close.
func TestGenerateFlagLedgerPseudoTxs_NoStanceNoBlobs(t *testing.T) {
	a := newTestAdaptor(t)
	prev := a.ledgerService.GetClosedLedger()
	require.NotNil(t, prev)
	wrapped := WrapLedger(prev)

	blobs := a.GenerateFlagLedgerPseudoTxs(wrapped, nil)
	assert.Nil(t, blobs,
		"no fee target, no amendment stance, no validations → no pseudo-txs")
}

// TestGenerateFlagLedgerPseudoTxs_FeeVoteSeedsSetFee pins the
// fee-vote wiring: with a configured FeeVote target that differs
// from the parent ledger's FeeSettings, the constructor seed for
// target wins and a SetFee pseudo-tx is emitted.
//
// Genesis FeeSettings carries the standalone defaults; a target
// 10× the base fee is well outside that, so the algorithm picks
// target.
func TestGenerateFlagLedgerPseudoTxs_FeeVoteSeedsSetFee(t *testing.T) {
	a := newTestAdaptorWithConfig(t, FeeVoteStance{
		BaseFee:          100, // genesis is 10
		ReserveBase:      50_000_000,
		ReserveIncrement: 5_000_000,
	}, nil)
	prev := a.ledgerService.GetClosedLedger()
	require.NotNil(t, prev)
	wrapped := WrapLedger(prev)

	blobs := a.GenerateFlagLedgerPseudoTxs(wrapped, nil)
	require.Len(t, blobs, 1, "fee target differs from current → one SetFee blob")

	stx := decodeTx(t, blobs[0])
	assert.Equal(t, "SetFee", stx["TransactionType"],
		"emitted blob must be a SetFee pseudo-tx")
	// LedgerSequence carries upcoming seq = parent + 1.
	assert.EqualValues(t, prev.Sequence()+1, asUint(stx["LedgerSequence"]))
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
