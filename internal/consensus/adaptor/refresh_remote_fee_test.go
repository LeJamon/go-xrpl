package adaptor

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/feetrack"
)

// TestRefreshRemoteFee_MedianOverTrustedValidations pins the
// LedgerMaster.cpp:977-1006 port: collect LoadFee from trusted
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
				{LoadFee: 320},
				{LoadFee: 0}, // omitted → substituted with LoadBase=256
				{LoadFee: 500},
			},
		},
	})

	a.refreshRemoteFee(id)

	// Sorted set: {256, 320, 500}; middle = 320.
	if got := ft.GetRemoteFee(); got != 320 {
		t.Fatalf("RemoteFee = %d; want 320", got)
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
