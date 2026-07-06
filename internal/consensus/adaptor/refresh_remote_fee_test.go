package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
)

// TestRefreshRemoteFee_MedianOverTrustedValidations pins the
// LedgerMaster.cpp:977-1006 port: collect LoadFee from trusted FULL
// validations (substituting LoadBase for any that omitted the field),
// sort, and forward the median to FeeTrack.SetRemoteFee.
func TestRefreshRemoteFee_MedianOverTrustedValidations(t *testing.T) {
	a := newTestAdaptor(t)
	ft := a.ledgerService.FeeTrack()
	if ft == nil {
		t.Fatal("FeeTrack must be non-nil from service.New")
	}

	id := consensus.LedgerID{0xAA}
	a.SetValidationHistorian(&stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			id: {
				{LoadFee: 320, Full: true},
				{LoadFee: 0, Full: true}, // omitted → substituted with LoadBase=256
				{LoadFee: 500, Full: true},
			},
		},
	})

	a.refreshRemoteFee(id)

	// Sorted set: {256, 320, 500}; middle = 320.
	if got := ft.GetRemoteFee(); got != 320 {
		t.Fatalf("RemoteFee = %d; want 320", got)
	}
}

// TestRefreshRemoteFee_ExcludesPartialValidations pins the
// Validations::fees() Full-only filter: trusted PARTIAL validations must
// not leak into the median even though GetTrustedValidations returns them.
func TestRefreshRemoteFee_ExcludesPartialValidations(t *testing.T) {
	a := newTestAdaptor(t)
	ft := a.ledgerService.FeeTrack()

	id := consensus.LedgerID{0xAA}
	a.SetValidationHistorian(&stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			id: {
				{LoadFee: 320, Full: true},
				{LoadFee: 10, Full: false}, // partial → dropped
				{LoadFee: 400, Full: true},
			},
		},
	})

	a.refreshRemoteFee(id)

	// Full-only set: {320, 400}; median index 1 = 400. Had the partial
	// leaked, {10, 320, 400} would give median 320 — so 400 proves the
	// Full filter applied.
	if got := ft.GetRemoteFee(); got != 400 {
		t.Fatalf("RemoteFee = %d; want 400 (partial excluded)", got)
	}
}

// TestRefreshRemoteFee_NoHistorian no-ops without crashing when the
// historian isn't wired (early-startup, unit-test paths).
func TestRefreshRemoteFee_NoHistorian(t *testing.T) {
	a := newTestAdaptor(t)
	ft := a.ledgerService.FeeTrack()
	if ft == nil {
		t.Fatal("FeeTrack must be non-nil")
	}
	before := ft.GetRemoteFee()
	a.refreshRemoteFee(consensus.LedgerID{0xBB})
	if got := ft.GetRemoteFee(); got != before {
		t.Fatalf("RemoteFee changed without historian: before=%d after=%d", before, got)
	}
}

// TestRefreshRemoteFee_EmptyValidations leaves the remote fee untouched
// when the historian has no validations for the ledger — matches the
// "no signal → no change" pattern (rippled also short-circuits when
// fees is empty by falling through to base, but the median we'd compute
// over a single base-only sample is meaningless).
func TestRefreshRemoteFee_EmptyValidations(t *testing.T) {
	a := newTestAdaptor(t)
	ft := a.ledgerService.FeeTrack()
	ft.SetRemoteFee(777)
	a.SetValidationHistorian(&stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{},
	})
	a.refreshRemoteFee(consensus.LedgerID{0xCC})
	if got := ft.GetRemoteFee(); got != 777 {
		t.Fatalf("RemoteFee mutated on empty validations: got %d, want 777", got)
	}
}

// TestRefreshRemoteFee_FoldsParentLedgerValidations pins the
// LedgerMaster.cpp:978-984 parent concatenation: the median is taken
// over the union of the validated ledger's and its parent's trusted
// full validations, not the current ledger alone.
func TestRefreshRemoteFee_FoldsParentLedgerValidations(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService
	ft := svc.FeeTrack()

	// A real ledger so refreshRemoteFee can resolve its parent hash.
	l := svc.GetClosedLedger()
	if l == nil {
		t.Fatal("closed ledger must exist in the test service")
	}
	id := consensus.LedgerID(l.Hash())
	parentID := consensus.LedgerID(l.ParentHash())

	a.SetValidationHistorian(&stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			id:       {{LoadFee: 320, Full: true}},
			parentID: {{LoadFee: 500, Full: true}, {LoadFee: 500, Full: true}},
		},
	})

	a.refreshRemoteFee(id)

	// Union {320, 500, 500}; median index 1 = 500. The current ledger
	// alone would yield 320, so 500 proves the parent was folded in.
	if got := ft.GetRemoteFee(); got != 500 {
		t.Fatalf("RemoteFee = %d; want 500 (parent validations folded in)", got)
	}
}

// TestGetLoadFee_MaxLocalCluster pins RCLConsensus.cpp:872 port: the
// validation-side load fee takes max(local, cluster), and emits 0
// (= "omit") when the max collapses to LoadBase.
func TestGetLoadFee_MaxLocalCluster(t *testing.T) {
	a := newTestAdaptor(t)
	ft := a.ledgerService.FeeTrack()

	// Default state: both at LoadBase → omit (0).
	if got := a.GetLoadFee(); got != 0 {
		t.Fatalf("default GetLoadFee = %d; want 0", got)
	}

	// Cluster > local → returned value is the cluster fee.
	ft.SetClusterFee(feetrack.LoadBase * 3)
	if got := a.GetLoadFee(); got != feetrack.LoadBase*3 {
		t.Fatalf("cluster-dominated GetLoadFee = %d; want %d", got, feetrack.LoadBase*3)
	}

	// Local > cluster → returned value is the local fee.
	ft.SetClusterFee(feetrack.LoadBase)
	ft.RaiseLocalFee() // raise latch
	ft.RaiseLocalFee() // local = 320
	if got := a.GetLoadFee(); got != ft.GetLocalFee() {
		t.Fatalf("local-dominated GetLoadFee = %d; want %d", got, ft.GetLocalFee())
	}
}
