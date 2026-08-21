package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/stretchr/testify/require"
)

type recordingFeeHistorian struct {
	*stubHistorian
	mu    sync.Mutex
	calls []feeLookup
}

type feeLookup struct {
	id  consensus.LedgerID
	seq uint32
}

func (h *recordingFeeHistorian) GetTrustedFullValidations(id consensus.LedgerID, seq uint32) []*consensus.Validation {
	h.mu.Lock()
	h.calls = append(h.calls, feeLookup{id: id, seq: seq})
	h.mu.Unlock()
	return h.stubHistorian.GetTrustedFullValidations(id, seq)
}

func (h *recordingFeeHistorian) RecheckFullyValidated(id consensus.LedgerID, seq uint32) ([]*consensus.Validation, int, bool) {
	validations := h.GetTrustedFullValidations(id, seq)
	return validations, 1, len(validations) >= 1
}

func (h *recordingFeeHistorian) lookupCalls() []feeLookup {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]feeLookup(nil), h.calls...)
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
				{LoadFee: 320, LedgerSeq: 1, Full: true},
				{LoadFee: 0, LedgerSeq: 1, Full: true}, // omitted → substituted with LoadBase=256
				{LoadFee: 500, LedgerSeq: 1, Full: true},
			},
		},
	})

	a.refreshRemoteFee(1, id, consensus.LedgerID{})

	// Sorted set: {256, 320, 500}; middle = 320.
	if got := ft.RemoteFee(); got != 320 {
		t.Fatalf("RemoteFee = %d; want 320", got)
	}
}

func TestRefreshRemoteFee_ExcludesPartialValidations(t *testing.T) {
	a := newTestAdaptor(t)
	ft := a.ledgerService.FeeTrack()

	id := consensus.LedgerID{0xAA}
	a.SetValidationHistorian(&stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			id: {
				{LoadFee: 320, LedgerSeq: 1, Full: true},
				{LoadFee: 10, LedgerSeq: 1, Full: false}, // partial → dropped
				{LoadFee: 400, LedgerSeq: 1, Full: true},
			},
		},
	})

	a.refreshRemoteFee(1, id, consensus.LedgerID{})

	// Full-only set: {320, 400}; median index 1 = 400. Had the partial
	// leaked, {10, 320, 400} would give median 320 — so 400 proves the
	// Full filter applied.
	if got := ft.RemoteFee(); got != 400 {
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
	before := ft.RemoteFee()
	a.refreshRemoteFee(1, consensus.LedgerID{0xBB}, consensus.LedgerID{})
	if got := ft.RemoteFee(); got != before {
		t.Fatalf("RemoteFee changed without historian: before=%d after=%d", before, got)
	}
}

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
	if got := ft.RemoteFee(); got != feetrack.LoadBase {
		t.Fatalf("RemoteFee = %d on empty validations; want LoadBase %d", got, feetrack.LoadBase)
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

	l := svc.GetClosedLedger()
	if l == nil {
		t.Fatal("closed ledger must exist in the test service")
	}
	id := consensus.LedgerID(l.Hash())
	parentID := consensus.LedgerID(l.ParentHash())

	a.SetValidationHistorian(&stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			id:       {{LoadFee: 320, LedgerSeq: 1, Full: true}},
			parentID: {{LoadFee: 500, LedgerSeq: 0, Full: true}, {LoadFee: 500, LedgerSeq: 0, Full: true}},
		},
	})

	a.refreshRemoteFee(1, id, parentID)

	// Union {320, 500, 500}; median index 1 = 500. The current ledger
	// alone would yield 320, so 500 proves the parent was folded in.
	if got := ft.RemoteFee(); got != 500 {
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
				{LedgerSeq: 0, Full: true},
				explicitZero,
			},
		},
	}

	require.Equal(t, []uint32{feetrack.LoadBase, 0}, collectValidationFees(historian, id, 0, feetrack.LoadBase))
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

	targetID := consensus.LedgerID(header.Hash)
	parentID := consensus.LedgerID(header.ParentHash)
	historian := &recordingFeeHistorian{stubHistorian: &stubHistorian{
		byLedger: map[consensus.LedgerID][]*consensus.Validation{
			targetID: {{LoadFee: 320, LedgerSeq: header.LedgerIndex, SignTime: closeTime, Full: true}},
			parentID: {{LoadFee: 500, LedgerSeq: header.LedgerIndex - 1, Full: true}, {LoadFee: 500, LedgerSeq: header.LedgerIndex - 1, Full: true}},
		},
	}}
	a.SetValidationHistorian(historian)
	svc.SetOnValidatedLedger(func(seq uint32, hash, parentHash [32]byte) {
		svc.GetValidatedLedgerIndex()
		a.refreshRemoteFee(seq, consensus.LedgerID(hash), consensus.LedgerID(parentHash))
	})

	ft.SetRemoteFee(777)
	a.OnLedgerFullyValidated(targetID, header.LedgerIndex)
	require.Equal(t, uint32(777), ft.RemoteFee())
	require.Equal(t, []feeLookup{{id: targetID, seq: header.LedgerIndex}}, historian.lookupCalls())

	done := make(chan error, 1)
	go func() {
		done <- svc.SwitchToPreferredLedger(candidate)
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("preferred-ledger switch blocked while notifying the validated-ledger callback")
	}
	signTime, result := a.recheckFullyValidated(header.LedgerIndex, header.Hash)
	require.Equal(t, validationRecheckAccepted, result)
	svc.SetValidatedLedgerAt(header.LedgerIndex, header.Hash, signTime)

	require.Equal(t, header.LedgerIndex, svc.GetValidatedLedgerIndex())
	require.Equal(t, uint32(500), ft.RemoteFee())
	wantLookups := []feeLookup{{id: targetID, seq: header.LedgerIndex}, {id: targetID, seq: header.LedgerIndex}, {id: targetID, seq: header.LedgerIndex}, {id: parentID, seq: header.LedgerIndex - 1}}
	require.Equal(t, wantLookups, historian.lookupCalls())

	a.OnLedgerFullyValidated(targetID, header.LedgerIndex)
	require.Equal(t, wantLookups, historian.lookupCalls())
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
			targetID: {{LoadFee: 320, LedgerSeq: expected.Sequence(), SignTime: closeTime, Full: true}},
			parentID: {{LoadFee: 500, LedgerSeq: expected.Sequence() - 1, Full: true}, {LoadFee: 500, LedgerSeq: expected.Sequence() - 1, Full: true}},
		},
	}}
	a.SetValidationHistorian(historian)

	ft.SetRemoteFee(777)
	a.OnLedgerFullyValidated(targetID, expected.Sequence())
	require.Equal(t, uint32(777), ft.RemoteFee())
	require.Equal(t, []feeLookup{{id: targetID, seq: expected.Sequence()}}, historian.lookupCalls())

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
	signTime, result := a.recheckFullyValidated(expected.Sequence(), expected.Hash())
	require.Equal(t, validationRecheckAccepted, result)
	svc.SetValidatedLedgerAt(expected.Sequence(), expected.Hash(), signTime)

	require.Equal(t, expected.Hash(), svc.GetClosedLedger().Hash())
	require.Equal(t, expected.Sequence(), svc.GetValidatedLedgerIndex())
	require.Equal(t, uint32(500), ft.RemoteFee())
	require.Equal(t, []feeLookup{{id: targetID, seq: expected.Sequence()}, {id: targetID, seq: expected.Sequence()}, {id: targetID, seq: expected.Sequence()}, {id: parentID, seq: expected.Sequence() - 1}}, historian.lookupCalls())
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
	if got := a.GetLoadFee(); got != ft.LocalFee() {
		t.Fatalf("local-dominated GetLoadFee = %d; want %d", got, ft.LocalFee())
	}
}
