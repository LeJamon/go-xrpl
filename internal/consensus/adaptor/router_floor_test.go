package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouter_RequestLedger_Floor_DeclinesBelowBoundary verifies that the
// ledger_request acquisition path refuses to fetch a ledger below the
// online-delete floor — mirroring rippled's LedgerMaster::shouldAcquire, which
// does not acquire a missing ledger below minimumOnline. No fetch is issued.
func TestRouter_RequestLedger_Floor_DeclinesBelowBoundary(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	r.SetMinimumOnlineFloor(stubFloor(100))

	// Register a peer so the only reason a fetch wouldn't fire is the floor.
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), closed.Hash()))

	var target [32]byte
	target[0] = 0x42

	snap, started, _ := r.RequestLedger(target, 50) // seq 50 < floor 100
	assert.False(t, started, "acquisition below the floor must not start")
	assert.Nil(t, snap)
	assert.Empty(t, rs.legacyCalls(), "no base fetch may be issued below the floor")
	assert.Empty(t, rs.replayCalls(), "no replay-delta fetch may be issued below the floor")
	assert.Nil(t, r.fetchTracker.Find(target), "no acquisition may be registered below the floor")
}

func TestRouter_InternalAcquisitionEntrypointsDeclineKnownSeqBelowFloor(t *testing.T) {
	r, _, sender, _ := makeRouter(t)
	r.SetMinimumOnlineFloor(stubFloor(100))
	target := [32]byte{0x43}

	assert.False(t, r.startLedgerAcquisition(99, target, 7))
	r.startLedgerAcquisitionLegacy(99, target, 7)
	assert.Empty(t, sender.legacyCalls())
	assert.Empty(t, sender.replayCalls())
	assert.Nil(t, r.fetchTracker.Find(target))
}

// TestRouter_RequestLedger_Floor_AllowsAtOrAboveBoundary verifies a request at
// or above the floor proceeds, and that a nil floor leaves the path unchanged.
func TestRouter_RequestLedger_Floor_AllowsAtOrAboveBoundary(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	r.SetMinimumOnlineFloor(stubFloor(50))
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), closed.Hash()))

	var target [32]byte
	target[0] = 0x42

	_, started, _ := r.RequestLedger(target, 50) // seq 50 == floor 50: allowed
	assert.True(t, started, "acquisition at the floor must proceed")
	require.Len(t, rs.legacyCalls(), 1, "a base fetch must be issued at the floor")
	assert.Equal(t, target, rs.legacyCalls()[0].hash)
}

// TestRouter_RequestLedger_NilFloor_Unchanged verifies the acquisition path is
// unrestricted when no floor is installed (online_delete off / standalone).
func TestRouter_RequestLedger_NilFloor_Unchanged(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), closed.Hash()))

	var target [32]byte
	target[0] = 0x42

	_, started, _ := r.RequestLedger(target, 1) // a very low seq, no floor → allowed
	assert.True(t, started, "with no floor any sequence is acquirable")
	require.Len(t, rs.legacyCalls(), 1)
}

func TestRouter_HashOnlyHeaderBelowFloorTerminatesConsensusTarget(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	engine := &mockEngine{}
	r.engine = engine
	r.SetMinimumOnlineFloor(stubFloor(100))
	rootHash, rootData, _ := buildSelfHealSourceState(t)
	headerData := header.AddRaw(header.LedgerHeader{LedgerIndex: 50, AccountHash: rootHash}, false)
	target := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	r.consensusRecovery = consensusRecovery{targetHash: target, stepHash: target}
	acquisition := inbound.New(target, 0, 7, serveTestLogger(), r.acquisitionOpts()...)
	r.fetchTracker.Track(acquisition)

	result := processAcquisitionWork(context.Background(), acquisition, []acquisitionWorkEvent{{
		kind: acquisitionWorkData,
		data: &message.LedgerData{
			InfoType: message.LedgerInfoBase,
			Nodes:    []message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}},
		},
		peerID: 7,
	}})
	require.True(t, result.remove)
	require.True(t, result.policyFailure)
	require.Empty(t, result.badData, "a valid but locally stale header must not blame its peer")
	r.handleAcquisitionWorkResult(result)

	assert.Nil(t, r.fetchTracker.Find(target))
	assert.Equal(t, consensusRecovery{}, r.consensusRecovery)
	assert.Equal(t, []consensus.LedgerID{consensus.LedgerID(target)}, engine.getAcquireFailed())
	assert.True(t, r.catchupRetryBlocked(target, time.Now()))
	r.armConsensusCatchup()
	assert.Nil(t, r.fetchTracker.Find(target), "terminal stale target must not be re-armed")
}

func TestRouter_HashOnlyHeaderAtFloorIsAdmitted(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	r.SetMinimumOnlineFloor(stubFloor(100))
	rootHash, rootData, _ := buildSelfHealSourceState(t)
	headerData := header.AddRaw(header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash}, false)
	target := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	acquisition := inbound.New(target, 0, 7, serveTestLogger(), r.acquisitionOpts()...)

	require.NoError(t, acquisition.GotBase([]message.LedgerNode{
		{NodeData: headerData},
		{NodeData: rootData},
	}))
	assert.Equal(t, uint32(100), acquisition.Seq())
}

// TestRouter_HandleGetLedger_Floor_DeclinesBelowBoundary drives the legacy
// mtGET_LEDGER serve path and verifies the router declines to serve a ledger
// below the floor (no response frame is emitted), while serving one at/above it.
func TestRouter_HandleGetLedger_Floor_DeclinesBelowBoundary(t *testing.T) {
	engine := &mockEngine{}
	adaptor, rs := newTxSetWireAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, adaptor, inbox)

	l := adaptor.LedgerService().GetClosedLedger()
	require.NotNil(t, l)
	hash := l.Hash()

	// Floor above the served ledger's sequence: it is below the boundary.
	router.SetMinimumOnlineFloor(stubFloor(l.Sequence() + 1))

	ctx := t.Context()
	go router.Run(ctx)

	req := &message.GetLedger{
		InfoType:   message.LedgerInfoBase,
		LedgerHash: hash[:],
		LedgerSeq:  l.Sequence(),
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	}

	// Give the router a beat to process; assert it stays silent.
	require.Never(t, func() bool {
		return len(rs.sentTo(7)) > 0
	}, 200*time.Millisecond, 20*time.Millisecond,
		"router must not serve a ledger below the online-delete floor")
}

// TestRouter_HandleGetLedger_Floor_ServesAtOrAboveBoundary verifies the same
// serve path responds normally when the ledger is at or above the floor.
func TestRouter_HandleGetLedger_Floor_ServesAtOrAboveBoundary(t *testing.T) {
	engine := &mockEngine{}
	adaptor, rs := newTxSetWireAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, adaptor, inbox)

	l := adaptor.LedgerService().GetClosedLedger()
	require.NotNil(t, l)
	hash := l.Hash()

	// Floor at the served ledger's own sequence: not below, must serve.
	router.SetMinimumOnlineFloor(stubFloor(l.Sequence()))

	ctx := t.Context()
	go router.Run(ctx)

	req := &message.GetLedger{
		InfoType:   message.LedgerInfoBase,
		LedgerHash: hash[:],
		LedgerSeq:  l.Sequence(),
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	}

	require.Eventually(t, func() bool {
		return len(rs.sentTo(7)) > 0
	}, time.Second, 10*time.Millisecond,
		"router must serve a ledger at or above the floor")
}

func newFetchDepthServeRouter(t *testing.T) (*Router, *querytypeRecorder, *service.Service) {
	t.Helper()
	cfg := service.DefaultConfig()
	cfg.FetchDepth = 2
	svc, err := service.New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	for range 2 {
		_, err := svc.AcceptLedger(t.Context())
		require.NoError(t, err)
	}

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	sender := &querytypeRecorder{recordingSender: recordingSender{peerSupportsReplay: true}}
	a := New(Config{
		LedgerService: svc,
		Sender:        sender,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	return NewRouter(&mockEngine{}, a, nil), sender, svc
}

func TestRouter_HandleGetLedger_RespectsConfiguredFetchDepth(t *testing.T) {
	tests := []struct {
		name          string
		make          func(belowHash []byte, belowSeq, atSeq uint32) *message.GetLedger
		serves        bool
		wantBadReason string
		wantCookie    bool
	}{
		{
			name: "sequence below floor",
			make: func(_ []byte, belowSeq, _ uint32) *message.GetLedger {
				return &message.GetLedger{InfoType: message.LedgerInfoBase, LedgerSeq: belowSeq}
			},
		},
		{
			name: "hash below floor",
			make: func(belowHash []byte, _, _ uint32) *message.GetLedger {
				return &message.GetLedger{InfoType: message.LedgerInfoBase, LedgerHash: belowHash}
			},
		},
		{
			name: "explicit zero sequence does not select closed ledger",
			make: func(_ []byte, _, _ uint32) *message.GetLedger {
				return &message.GetLedger{
					InfoType:     message.LedgerInfoBase,
					LType:        message.LedgerTypeClosed,
					LedgerSeqSet: true,
				}
			},
		},
		{
			name: "sequence at floor",
			make: func(_ []byte, _, atSeq uint32) *message.GetLedger {
				return &message.GetLedger{InfoType: message.LedgerInfoBase, LedgerSeq: atSeq}
			},
			serves: true,
		},
		{
			name: "hash and matching sequence below floor",
			make: func(belowHash []byte, belowSeq, _ uint32) *message.GetLedger {
				return &message.GetLedger{InfoType: message.LedgerInfoBase, LedgerHash: belowHash, LedgerSeq: belowSeq}
			},
			serves: true,
		},
		{
			name: "matching request echoes explicit zero cookie",
			make: func(belowHash []byte, belowSeq, _ uint32) *message.GetLedger {
				return &message.GetLedger{
					InfoType:         message.LedgerInfoBase,
					LedgerHash:       belowHash,
					LedgerSeq:        belowSeq,
					RequestCookieSet: true,
				}
			},
			serves:     true,
			wantCookie: true,
		},
		{
			name: "direct hash and mismatched sequence",
			make: func(belowHash []byte, _, atSeq uint32) *message.GetLedger {
				return &message.GetLedger{InfoType: message.LedgerInfoBase, LedgerHash: belowHash, LedgerSeq: atSeq}
			},
			wantBadReason: "get-ledger-sequence-mismatch",
		},
		{
			name: "explicit zero cookie suppresses mismatch charge",
			make: func(belowHash []byte, _, atSeq uint32) *message.GetLedger {
				return &message.GetLedger{
					InfoType:         message.LedgerInfoBase,
					LedgerHash:       belowHash,
					LedgerSeq:        atSeq,
					RequestCookieSet: true,
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router, sender, svc := newFetchDepthServeRouter(t)
			earliest := svc.EarliestFetch()
			require.Greater(t, earliest, uint32(1))
			below, err := svc.GetLedgerBySequence(earliest - 1)
			require.NoError(t, err)
			belowHash := below.Hash()

			req := tc.make(belowHash[:], earliest-1, earliest)
			router.handleMessage(&peermanagement.InboundMessage{
				PeerID:  7,
				Type:    uint16(message.TypeGetLedger),
				Payload: encodePayload(t, req),
			})

			badData, sent := sender.snapshot()
			if tc.serves {
				assert.NotEmpty(t, sent)
			} else {
				assert.Empty(t, sent)
			}
			if tc.wantBadReason != "" {
				require.Len(t, badData, 1)
				assert.Equal(t, tc.wantBadReason, badData[0].reason)
			} else {
				assert.Empty(t, badData)
			}
			if tc.wantCookie {
				require.NotEmpty(t, sent)
				_, decoded := decodeFrame(t, sent[0].frame)
				response := decoded.(*message.LedgerData)
				assert.True(t, response.HasRequestCookie())
				assert.Zero(t, response.RequestCookie)
			}
		})
	}
}
