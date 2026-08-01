package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scRecordingSender records broadcast status changes; any other
// consensusNetwork method panics via the nil embedded interface.
type scRecordingSender struct {
	consensusNetwork
	mu  sync.Mutex
	scs []*message.StatusChange
}

func (s *scRecordingSender) BroadcastStatusChange(sc *message.StatusChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scs = append(s.scs, sc)
	return nil
}

func (s *scRecordingSender) events() []message.NodeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]message.NodeEvent, len(s.scs))
	for i, sc := range s.scs {
		out[i] = sc.NewEvent
	}
	return out
}

// Issue #1207: while the engine builds on the wrong LCL, phase-driven status
// changes must carry LOST_SYNC instead of CLOSING/ACCEPTED_LEDGER, and
// entering wrongLedger itself announces LOST_SYNC (the engine pins without
// running rounds, so no phase-driven status would go out).
func TestStatusChange_WrongLedgerSubstitutesLostSync(t *testing.T) {
	sender := &scRecordingSender{}
	svc := newTestLedgerService(t)
	a := New(Config{LedgerService: svc, Sender: sender})

	_, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)

	a.SetOperatingMode(consensus.OpModeFull)
	a.OnModeChange(consensus.ModeObserving, consensus.ModeWrongLedger)
	require.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())
	a.OnPhaseChange(consensus.PhaseOpen, consensus.PhaseEstablish)
	a.OnPhaseChange(consensus.PhaseEstablish, consensus.PhaseAccepted)

	require.Equal(t, []message.NodeEvent{
		message.NodeEventLostSync, // wrongLedger entry
		message.NodeEventLostSync, // substituted for CLOSING_LEDGER
		message.NodeEventLostSync, // substituted for ACCEPTED_LEDGER
	}, sender.events())

	// Recovery restores the normal events.
	a.OnModeChange(consensus.ModeWrongLedger, consensus.ModeObserving)
	a.OnPhaseChange(consensus.PhaseOpen, consensus.PhaseEstablish)
	a.OnPhaseChange(consensus.PhaseEstablish, consensus.PhaseAccepted)

	events := sender.events()
	require.Len(t, events, 5)
	assert.Equal(t, message.NodeEventClosingLedger, events[3])
	assert.Equal(t, message.NodeEventAcceptedLedger, events[4])
}

func TestStatusChange_WrongLedgerDemotionPolicy(t *testing.T) {
	tests := []struct {
		name    string
		initial consensus.OperatingMode
		newMode consensus.Mode
		want    consensus.OperatingMode
	}{
		{name: "full", initial: consensus.OpModeFull, newMode: consensus.ModeWrongLedger, want: consensus.OpModeConnected},
		{name: "tracking", initial: consensus.OpModeTracking, newMode: consensus.ModeWrongLedger, want: consensus.OpModeConnected},
		{name: "connected", initial: consensus.OpModeConnected, newMode: consensus.ModeWrongLedger, want: consensus.OpModeConnected},
		{name: "syncing", initial: consensus.OpModeSyncing, newMode: consensus.ModeWrongLedger, want: consensus.OpModeSyncing},
		{name: "disconnected", initial: consensus.OpModeDisconnected, newMode: consensus.ModeWrongLedger, want: consensus.OpModeDisconnected},
		{name: "recovery does not promote", initial: consensus.OpModeFull, newMode: consensus.ModeObserving, want: consensus.OpModeFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestLedgerService(t)
			a := New(Config{LedgerService: svc, Sender: &scRecordingSender{}})
			a.SetOperatingMode(tt.initial)

			a.OnModeChange(consensus.ModeObserving, tt.newMode)

			require.Equal(t, tt.want, a.GetOperatingMode())
		})
	}
}

func TestStatusChange_WrongLedgerBlocksStaleOperatingModePromotion(t *testing.T) {
	svc := newTestLedgerService(t)
	a := New(Config{LedgerService: svc, Sender: &scRecordingSender{}})
	a.SetOperatingMode(consensus.OpModeFull)
	a.OnModeChange(consensus.ModeProposing, consensus.ModeWrongLedger)

	a.SetOperatingMode(consensus.OpModeTracking)
	require.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())
	a.SetOperatingMode(consensus.OpModeFull)
	require.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())

	a.OnModeChange(consensus.ModeWrongLedger, consensus.ModeObserving)
	a.SetOperatingMode(consensus.OpModeSyncing)
	a.OnModeChange(consensus.ModeObserving, consensus.ModeWrongLedger)
	a.SetOperatingMode(consensus.OpModeFull)
	require.Equal(t, consensus.OpModeSyncing, a.GetOperatingMode())

	a.OnModeChange(consensus.ModeWrongLedger, consensus.ModeSwitchedLedger)
	a.SetOperatingMode(consensus.OpModeTracking)
	require.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
}

func TestStatusChange_WrongLedgerDoesNotOverwriteConcurrentDisconnect(t *testing.T) {
	svc := newTestLedgerService(t)
	a := New(Config{LedgerService: svc, Sender: &scRecordingSender{}})
	a.SetOperatingMode(consensus.OpModeFull)

	a.mu.Lock()
	locked := true
	defer func() {
		if locked {
			a.mu.Unlock()
		}
	}()
	done := make(chan struct{})
	go func() {
		a.OnModeChange(consensus.ModeProposing, consensus.ModeWrongLedger)
		close(done)
	}()
	require.Eventually(t, func() bool {
		return consensus.Mode(a.consensusMode.Load()) == consensus.ModeWrongLedger
	}, time.Second, time.Millisecond)

	a.setOperatingModeLocked(consensus.OpModeDisconnected)
	a.mu.Unlock()
	locked = false

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("OnModeChange did not complete")
	}
	require.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())
}

// The SWITCHED_LEDGER broadcast carries the adopted ledger's identity and,
// like rippled's switchLastClosedLedger message, no status or validated-range
// fields.
func TestStatusChange_OnLedgerSwitchedPayload(t *testing.T) {
	sender := &scRecordingSender{}
	svc := newTestLedgerService(t)
	a := New(Config{LedgerService: svc, Sender: sender})

	l := stubLedger{id: consensus.LedgerID{0xAA}, seq: 42, parentID: consensus.LedgerID{0xBB}}
	a.OnLedgerSwitched(l)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Len(t, sender.scs, 1)
	sc := sender.scs[0]
	assert.Equal(t, message.NodeEventSwitchedLedger, sc.NewEvent)
	assert.Equal(t, uint32(42), sc.LedgerSeq)
	assert.Equal(t, l.id[:], sc.LedgerHash)
	assert.Equal(t, l.parentID[:], sc.LedgerHashPrevious)
	assert.Equal(t, message.NodeStatus(0), sc.NewStatus)
	assert.Nil(t, sc.FirstSeq)
	assert.Nil(t, sc.LastSeq)
	assert.NotZero(t, sc.NetworkTime)
}

// A phase-driven status change omits new_status (peers inherit their last
// record, as rippled's notify() sends none) and advertises the range we
// durably serve — clamped up to the online-delete floor, not genesis..tip.
func TestStatusChange_OmitsNewStatusAdvertisesServedRange(t *testing.T) {
	sender := &scRecordingSender{}
	svc := adg_newNonStandaloneService(t)
	a := New(Config{LedgerService: svc, Sender: sender})

	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	closedSeq, err := svc.AcceptConsensusResult(context.TODO(), parent, nil, nil, time.Now(), true)
	require.NoError(t, err)
	require.Greater(t, closedSeq, svc.GetValidatedLedgerIndex())

	a.OnPhaseChange(consensus.PhaseOpen, consensus.PhaseEstablish)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Len(t, sender.scs, 1)
	sc := sender.scs[0]
	assert.Equal(t, message.NodeEventClosingLedger, sc.NewEvent)
	assert.Equal(t, message.NodeStatus(0), sc.NewStatus)
	assert.Equal(t, closedSeq, sc.LedgerSeq)
	require.NotNil(t, sc.FirstSeq)
	require.NotNil(t, sc.LastSeq)
	assert.Equal(t, svc.GetValidatedLedgerIndex(), *sc.LastSeq)
	assert.Less(t, *sc.LastSeq, sc.LedgerSeq)
	assert.NotZero(t, sc.NetworkTime)
}
