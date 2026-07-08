// Pin for the Change-family sequence gate: rippled Change::preflight rejects a
// pseudo-transaction that carries sfPreviousTxnID (a deserializable optional
// common field) with temBAD_SEQUENCE, in the same gate as a non-zero Sequence.
// Reference: rippled Change.cpp:70-75.
package pseudotx_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/stretchr/testify/require"
)

// TestPseudoPreflight_PreviousTxnIDRejected pins finding pseudo-PreviousTxnID: an
// otherwise-valid pseudo-tx whose decoded field set includes PreviousTxnID is
// temBAD_SEQUENCE, even with Sequence == 0.
func TestPseudoPreflight_PreviousTxnIDRejected(t *testing.T) {
	engine, _ := closedEngine(t, amendment.AllSupportedRules())
	tx := newAmendmentTx()
	// Mark PreviousTxnID as present in the decoded field set, mirroring a binary
	// pseudo-tx that carries the (optional, deserializable) sfPreviousTxnID.
	tx.Common.SetPresentFields(map[string]bool{"PreviousTxnID": true})

	result := engine.ApplyPseudo(tx)
	require.False(t, result.Applied)
	require.Equal(t, "temBAD_SEQUENCE", result.Result.String())
}
