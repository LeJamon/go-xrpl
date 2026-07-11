package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
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

// Adopting a peer's ledger during initial sync is an LCL jump and must be
// announced to peers.
func TestStatusChange_AdoptLedgerFromHeaderAnnouncesSwitch(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	sender := &scRecordingSender{}
	a := New(Config{LedgerService: svc, Sender: sender})

	cl := svc.GetClosedLedger()
	require.NotNil(t, cl)
	h := cl.Header()
	raw := header.AddRaw(h, true)

	require.NoError(t, a.AdoptLedgerFromHeader(raw))

	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Len(t, sender.scs, 1)
	sc := sender.scs[0]
	assert.Equal(t, message.NodeEventSwitchedLedger, sc.NewEvent)
	assert.Equal(t, h.LedgerIndex, sc.LedgerSeq)
	assert.Equal(t, h.Hash[:], sc.LedgerHash)
	assert.Equal(t, h.ParentHash[:], sc.LedgerHashPrevious)
}

func TestStatusChange_OmitsNewStatusAdvertisesValidatedRange(t *testing.T) {
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
