package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	ledgerheader "github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/stretchr/testify/require"
)

type recordingFeeHistorian struct {
	*stubHistorian
	mu    sync.Mutex
	calls []consensus.LedgerID
}

func (h *recordingFeeHistorian) GetTrustedValidations(id consensus.LedgerID) []*consensus.Validation {
	h.mu.Lock()
	h.calls = append(h.calls, id)
	h.mu.Unlock()
	return h.stubHistorian.GetTrustedValidations(id)
}

func (h *recordingFeeHistorian) lookupCalls() []consensus.LedgerID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]consensus.LedgerID(nil), h.calls...)
}

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

	a.refreshRemoteFee(1, id, consensus.LedgerID{})

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

	a.refreshRemoteFee(1, id, consensus.LedgerID{})

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
	a.refreshRemoteFee(1, consensus.LedgerID{0xBB}, consensus.LedgerID{})
	if got := ft.GetRemoteFee(); got != before {
		t.Fatalf("RemoteFee changed without historian: before=%d after=%d", before, got)
	}
}

// TestRefreshRemoteFee_EmptyValidations resets the remote fee to LoadBase
// through the normal validated-ledger promotion path when neither ledger has a
// fee sample.
func TestRefreshRemoteFee_EmptyValidations(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService
	ft := svc.FeeTrack()
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	seq, err := svc.AcceptConsensusResult(context.Background(), parent, nil, nil, time.Unix(1_700_000_000, 0), true)
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	require.Equal(t, seq, closed.Sequence())

	ft.SetRemoteFee(777)
	a.SetValidationHistorian(&stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{},
	})
	a.OnLedgerFullyValidated(consensus.LedgerID(closed.Hash()), seq)
	if got := ft.GetRemoteFee(); got != ft.GetLoadBase() {
		t.Fatalf("RemoteFee = %d on empty validations; want LoadBase %d", got, ft.GetLoadBase())
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

	a.refreshRemoteFee(1, id, parentID)

	// Union {320, 500, 500}; median index 1 = 500. The current ledger
	// alone would yield 320, so 500 proves the parent was folded in.
	if got := ft.GetRemoteFee(); got != 500 {
		t.Fatalf("RemoteFee = %d; want 500 (parent validations folded in)", got)
	}
}

func TestCollectValidationFees_DistinguishesExplicitZeroFromAbsent(t *testing.T) {
	id := consensus.LedgerID{0xDD}
	explicitZero := &consensus.Validation{Full: true}
	explicitZero.SetLoadFee(0)
	historian := &stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			id: {
				{Full: true},
				explicitZero,
			},
		},
	}

	require.Equal(t, []uint32{feetrack.LoadBase, 0}, collectValidationFees(historian, id, feetrack.LoadBase))
}

func TestRefreshRemoteFee_ValidationBeforePeerAdoption(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService
	ft := svc.FeeTrack()
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	candidate, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, candidate.Close(closeTime, 0))
	header := candidate.Header()
	stateMap, err := candidate.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := candidate.TxMapSnapshot()
	require.NoError(t, err)

	targetID := consensus.LedgerID(header.Hash)
	parentID := consensus.LedgerID(header.ParentHash)
	historian := &recordingFeeHistorian{stubHistorian: &stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			targetID: {{LoadFee: 320, Full: true}},
			parentID: {{LoadFee: 500, Full: true}, {LoadFee: 500, Full: true}},
		},
	}}
	a.SetValidationHistorian(historian)
	svc.SetOnValidatedLedger(func(seq uint32, hash, parentHash [32]byte) {
		svc.GetValidatedLedgerIndex()
		a.refreshRemoteFee(seq, consensus.LedgerID(hash), consensus.LedgerID(parentHash))
	})

	ft.SetRemoteFee(777)
	a.OnLedgerFullyValidated(targetID, header.LedgerIndex)
	require.Equal(t, uint32(777), ft.GetRemoteFee())
	require.Empty(t, historian.lookupCalls())

	done := make(chan error, 1)
	go func() {
		done <- svc.AdoptLedgerWithState(context.Background(), &header, stateMap, txMap)
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("peer adoption blocked while notifying the validated-ledger callback")
	}

	require.Equal(t, header.LedgerIndex, svc.GetValidatedLedgerIndex())
	require.Equal(t, uint32(500), ft.GetRemoteFee())
	require.Equal(t, []consensus.LedgerID{targetID, parentID}, historian.lookupCalls())

	a.OnLedgerFullyValidated(targetID, header.LedgerIndex)
	require.Equal(t, []consensus.LedgerID{targetID, parentID}, historian.lookupCalls())
}

func TestRefreshRemoteFee_ValidationBeforeHeaderAdoption(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	ft := svc.FeeTrack()
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	candidate, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, candidate.Close(closeTime, 0))
	header := candidate.Header()
	targetID := consensus.LedgerID(header.Hash)
	parentID := consensus.LedgerID(header.ParentHash)
	historian := &recordingFeeHistorian{stubHistorian: &stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			targetID: {{LoadFee: 320, Full: true}},
			parentID: {{LoadFee: 500, Full: true}, {LoadFee: 500, Full: true}},
		},
	}}
	a.SetValidationHistorian(historian)

	ft.SetRemoteFee(777)
	a.OnLedgerFullyValidated(targetID, header.LedgerIndex)
	require.Equal(t, uint32(777), ft.GetRemoteFee())
	require.Empty(t, historian.lookupCalls())

	done := make(chan error, 1)
	go func() {
		done <- a.AdoptLedgerFromHeader(ledgerheader.AddRaw(header, true))
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("header adoption blocked while notifying the validated-ledger callback")
	}

	require.Equal(t, header.LedgerIndex, svc.GetValidatedLedgerIndex())
	require.Equal(t, uint32(500), ft.GetRemoteFee())
	require.Equal(t, []consensus.LedgerID{targetID, parentID}, historian.lookupCalls())
}

func TestRefreshRemoteFee_ValidationBeforeHeaderReAdoption(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})

	_, first, _, _ := buildSuccessorAgainstParent(t, svc.GetClosedLedger())
	firstHeader := first.Header()
	require.NoError(t, svc.AdoptLedgerHeader(&firstHeader))

	_, second, _, _ := buildSuccessorAgainstParent(t, svc.GetClosedLedger())
	secondHeader := second.Header()
	targetID := consensus.LedgerID(secondHeader.Hash)
	parentID := consensus.LedgerID(secondHeader.ParentHash)
	historian := &recordingFeeHistorian{stubHistorian: &stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			targetID: {{LoadFee: 320, Full: true}},
			parentID: {{LoadFee: 500, Full: true}, {LoadFee: 500, Full: true}},
		},
	}}
	a.SetValidationHistorian(historian)

	ft := svc.FeeTrack()
	ft.SetRemoteFee(777)
	a.OnLedgerFullyValidated(targetID, secondHeader.LedgerIndex)
	require.Equal(t, uint32(777), ft.GetRemoteFee())
	require.Empty(t, historian.lookupCalls())

	done := make(chan error, 1)
	go func() {
		done <- svc.ReAdoptLedgerHeader(&secondHeader)
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("header re-adoption blocked while notifying the validated-ledger callback")
	}

	require.Equal(t, secondHeader.LedgerIndex, svc.GetValidatedLedgerIndex())
	require.Equal(t, uint32(500), ft.GetRemoteFee())
	require.Equal(t, []consensus.LedgerID{targetID, parentID}, historian.lookupCalls())
}

func TestRefreshRemoteFee_ValidationBeforeConsensusClose(t *testing.T) {
	a := newTestAdaptor(t)
	svc := a.ledgerService
	ft := svc.FeeTrack()
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	closeTime := time.Unix(1_700_000_000, 0)

	probe := newTestLedgerService(t)
	_, err := probe.AcceptConsensusResult(context.Background(), parent, nil, nil, closeTime, true)
	require.NoError(t, err)
	expected := probe.GetClosedLedger()
	require.NotNil(t, expected)

	targetID := consensus.LedgerID(expected.Hash())
	parentID := consensus.LedgerID(expected.ParentHash())
	historian := &recordingFeeHistorian{stubHistorian: &stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			targetID: {{LoadFee: 320, Full: true}},
			parentID: {{LoadFee: 500, Full: true}, {LoadFee: 500, Full: true}},
		},
	}}
	a.SetValidationHistorian(historian)

	ft.SetRemoteFee(777)
	a.OnLedgerFullyValidated(targetID, expected.Sequence())
	require.Equal(t, uint32(777), ft.GetRemoteFee())
	require.Empty(t, historian.lookupCalls())

	done := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(context.Background(), parent, nil, nil, closeTime, true)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("consensus close blocked while notifying the validated-ledger callback")
	}

	require.Equal(t, expected.Hash(), svc.GetClosedLedger().Hash())
	require.Equal(t, expected.Sequence(), svc.GetValidatedLedgerIndex())
	require.Equal(t, uint32(500), ft.GetRemoteFee())
	require.Equal(t, []consensus.LedgerID{targetID, parentID}, historian.lookupCalls())
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
