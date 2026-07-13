package adaptor

import (
	"context"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scRecordingSender records broadcast status changes; any other
// NetworkSender method panics via the nil embedded interface.
type scRecordingSender struct {
	NetworkSender
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

	a.OnModeChange(consensus.ModeObserving, consensus.ModeWrongLedger)
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
	svc := newTestLedgerService(t)
	a := New(Config{LedgerService: svc, Sender: sender})

	for range 3 {
		_, err := svc.AcceptLedger(context.TODO())
		require.NoError(t, err)
	}
	_, maxSeq, ok := svc.AvailableLedgerRange()
	require.True(t, ok)
	require.Greater(t, maxSeq, uint32(2))
	// Only the tip is durably served: the advertised range must start at the
	// floor, proving it is not the old hard-coded genesis lower bound.
	svc.SetMinimumOnlineFunc(func() uint32 { return maxSeq })

	a.OnPhaseChange(consensus.PhaseOpen, consensus.PhaseEstablish)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Len(t, sender.scs, 1)
	sc := sender.scs[0]
	assert.Equal(t, message.NodeEventClosingLedger, sc.NewEvent)
	assert.Equal(t, message.NodeStatus(0), sc.NewStatus)
	require.NotNil(t, sc.FirstSeq)
	require.NotNil(t, sc.LastSeq)
	assert.Equal(t, maxSeq, *sc.FirstSeq)
	assert.Equal(t, maxSeq, *sc.LastSeq)
	assert.NotZero(t, sc.NetworkTime)
}
