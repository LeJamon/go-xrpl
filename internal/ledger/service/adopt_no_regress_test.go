package service

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdoptLedgerWithState_BackwardFillDoesNotRegressClosed pins the
// no-regress invariant on s.closedLedger: backward-chain adoptions
// (cascade-adopt of held parent seqs filling history) MUST install
// the entry into ledgerHistory[seq] but MUST NOT regress
// s.closedLedger.Sequence(). Without the gate, goxrpl's
// closed_ledger.seq oscillates downward each time a parent acquires,
// the ModeManager's Tracking → Full check (which compares ourSeq vs
// peerSeq) wedges on the regressing tip, and the engine never gets
// promoted to OpModeFull — the structural bootstrap deadlock that
// blocked the all-5-UNL soak rerun in the second-pass review.
//
// Properties pinned:
//  1. Forward adoption (seq > current) advances s.closedLedger.
//  2. Backward adoption (seq < current) installs into history but
//     leaves s.closedLedger pointing at the higher seq.
//  3. ledgerHistory[seq] still contains the backward-adopted entry
//     (history is fork-of-truth for by-seq lookups).
//  4. s.openLedger is rebuilt only on forward adoption — backward
//     fills must NOT clobber the engine's open view to stale state.
func TestAdoptLedgerWithState_BackwardFillDoesNotRegressClosed(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	mkHeader := func(seq uint32, salt byte) (*header.LedgerHeader, *shamap.SHAMap) {
		var h, parent [32]byte
		h[0] = salt
		h[1] = byte(seq)
		parent[0] = salt
		parent[1] = byte(seq - 1)
		stateMap := shamap.New(shamap.TypeState)
		return &header.LedgerHeader{
			LedgerIndex: seq,
			Hash:        h,
			ParentHash:  parent,
		}, stateMap
	}

	// Pre-condition: closed at genesis (seq 2 in goxrpl).
	startSeq := svc.GetClosedLedgerIndex()
	startOpen := svc.openLedger
	require.NotNil(t, startOpen, "Start must populate openLedger")

	// Forward adopt seq=78 — must advance.
	h78, sm78 := mkHeader(78, 0xA1)
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), h78, sm78, nil))
	assert.Equal(t, uint32(78), svc.GetClosedLedgerIndex(),
		"forward adopt must advance closedLedger seq")
	advancedOpen := svc.openLedger
	assert.NotEqual(t, startOpen, advancedOpen,
		"forward adopt must rebuild openLedger on the new closed reference")

	// Backward adopt seq=77 — must NOT regress.
	h77, sm77 := mkHeader(77, 0xA1)
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), h77, sm77, nil))
	assert.Equal(t, uint32(78), svc.GetClosedLedgerIndex(),
		"backward fill must NOT regress closedLedger from 78 to 77 — "+
			"this is the cascade-adopt deadlock the gate prevents")
	assert.Equal(t, advancedOpen, svc.openLedger,
		"backward fill must NOT rebuild openLedger to the older seq — "+
			"engine's open view would silently regress to stale state")

	// History assertion: backward-adopted entry IS in ledgerHistory.
	got77, err := svc.GetLedgerByHash(h77.Hash)
	require.NoError(t, err, "backward-adopted ledger must be queryable by hash")
	require.NotNil(t, got77)
	assert.Equal(t, uint32(77), got77.Sequence())

	// Backward adopt seq=50 — also must NOT regress.
	h50, sm50 := mkHeader(50, 0xA1)
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), h50, sm50, nil))
	assert.Equal(t, uint32(78), svc.GetClosedLedgerIndex(),
		"deeper backward fill must still leave closedLedger at the latest forward seq")

	// Forward adopt seq=80 — must advance again.
	h80, sm80 := mkHeader(80, 0xA1)
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), h80, sm80, nil))
	assert.Equal(t, uint32(80), svc.GetClosedLedgerIndex(),
		"a later forward adopt must still advance — gate is direction-aware, not write-once")

	_ = startSeq
}
