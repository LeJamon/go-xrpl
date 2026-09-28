package adaptor

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	peerproto "github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/require"
	pb "google.golang.org/protobuf/proto"
)

func TestGetLedgerAdmissionRejectsInvalidRequestsBeforeQueue(t *testing.T) {
	r, recorder := makeRouterWithRelayRecorder(t)
	r.lifecycleState = routerLifecycleRunning
	r.serveJobs = make(chan *peermanagement.InboundMessage, 8)

	tests := []struct {
		name   string
		req    *message.GetLedger
		reason string
	}{
		{
			name:   "empty non-base node list",
			req:    &message.GetLedger{InfoType: message.LedgerInfoAsNode, LedgerHash: bytes.Repeat([]byte{1}, 32)},
			reason: "get-ledger-invalid-nodeids",
		},
		{
			name:   "missing locator",
			req:    &message.GetLedger{InfoType: message.LedgerInfoBase},
			reason: "get-ledger-invalid-request",
		},
		{
			name:   "invalid hash size",
			req:    &message.GetLedger{InfoType: message.LedgerInfoBase, LedgerHash: []byte{1}},
			reason: "get-ledger-invalid-hash",
		},
		{
			name:   "base query depth",
			req:    &message.GetLedger{InfoType: message.LedgerInfoBase, LType: message.LedgerTypeClosed, QueryDepthSet: true},
			reason: "get-ledger-bad-querydepth",
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msg := &peermanagement.InboundMessage{
				PeerID:  peermanagement.PeerID(i + 1),
				Type:    message.TypeGetLedger,
				Payload: encodePayload(t, test.req),
			}
			require.False(t, r.handleMessage(msg))
			require.Empty(t, r.serveJobs, "invalid requests must not enter the serve queue")
			calls := recorder.badDataCalls()
			require.Len(t, calls, i+1)
			require.Equal(t, test.reason, calls[i].reason)
		})
	}
}

func TestGetLedgerWorkerValidatesOnlyTheSoftNodeIDPrefix(t *testing.T) {
	newRequest := func() *message.GetLedger {
		ids := make([][]byte, txSetSoftMaxReplyNodes+1)
		for i := range ids {
			ids[i] = rootNodeID()
		}
		qt := message.QueryTypeIndirect
		return &message.GetLedger{
			InfoType:   message.LedgerInfoTsCandidate,
			LedgerHash: bytes.Repeat([]byte{3}, 32),
			NodeIDs:    ids,
			QueryType:  &qt,
		}
	}

	t.Run("malformed prefix is rejected", func(t *testing.T) {
		r, recorder := makeRouterWithRelayRecorder(t)
		req := newRequest()
		req.NodeIDs[0] = []byte{1}
		r.handleGetLedger(&peermanagement.InboundMessage{
			PeerID:  4,
			Type:    message.TypeGetLedger,
			Payload: encodePayload(t, req),
		})
		require.Equal(t, []badDataCall{{peerID: 4, reason: "get-ledger-invalid-nodeid"}}, recorder.badDataCalls())
		require.Empty(t, recorder.sentFrames())
	})

	t.Run("malformed tail is ignored and relay is truncated", func(t *testing.T) {
		r, recorder := makeRouterWithRelayRecorder(t)
		recorder.txsetPeer, recorder.txsetOK = 8, true
		req := newRequest()
		req.NodeIDs[len(req.NodeIDs)-1] = []byte{1}
		r.handleGetLedger(&peermanagement.InboundMessage{
			PeerID:  5,
			Type:    message.TypeGetLedger,
			Payload: encodePayload(t, req),
		})

		require.Empty(t, recorder.badDataCalls())
		frames := recorder.sentFrames()
		require.Len(t, frames, 1)
		_, decoded := decodeFrame(t, frames[0].frame)
		forwarded := decoded.(*message.GetLedger)
		require.Len(t, forwarded.NodeIDs, txSetSoftMaxReplyNodes)
		require.Equal(t, uint64(5), forwarded.RequestCookie)
	})
}

func TestGetLedgerAdmissionProductionIngress(t *testing.T) {
	journal := &chargeLogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(journal))
	t.Cleanup(func() { slog.SetDefault(previous) })

	connections := make(chan manifestOverlayConnection, 1)
	source := startRunningManifestOverlay(t, peermanagement.WithCompression(false))
	source.overlay.SetPeerConnectCallback(func(peerID peermanagement.PeerID) {
		connections <- manifestOverlayConnection{overlay: source, peerID: peerID}
	})
	client := startRunningManifestOverlay(t,
		peermanagement.WithCompression(false),
		peermanagement.WithMaxOutbound(1),
		peermanagement.WithFixedPeers(source.overlay.ListenAddr()),
	)
	var connection manifestOverlayConnection
	select {
	case connection = <-connections:
	case <-time.After(5 * time.Second):
		t.Fatal("ledger request source did not connect")
	}

	r, recorder := makeRouterWithRelayRecorder(t)
	r.lifecycleState = routerLifecycleRunning
	r.serveJobs = make(chan *peermanagement.InboundMessage, 2)
	t.Cleanup(func() { drainInboundMessages(r.serveJobs) })
	ledger := r.adaptor.LedgerService().GetClosedLedger()
	hash := ledger.Hash()
	sendWithCookie := func(infoType message.LedgerInfoType, count int, cookie *uint64) {
		t.Helper()
		ids := make([][]byte, count)
		for i := range ids {
			ids[i] = rootNodeID()
		}
		payload, err := pb.Marshal(&peerproto.TMGetLedger{
			Itype:         peerproto.TMLedgerInfoType(infoType).Enum(),
			LedgerHash:    hash[:],
			NodeIDs:       ids,
			RequestCookie: cookie,
		})
		require.NoError(t, err)
		frame, err := message.BuildWireMessage(message.TypeGetLedger, payload)
		require.NoError(t, err)
		require.NoError(t, connection.overlay.overlay.Send(connection.peerID, frame))
	}
	send := func(infoType message.LedgerInfoType, count int) {
		t.Helper()
		sendWithCookie(infoType, count, nil)
	}
	receive := func(infoType message.LedgerInfoType, count int) *peermanagement.InboundMessage {
		t.Helper()
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case inbound := <-client.overlay.Messages():
				if inbound.Type != message.TypeGetLedger {
					require.NoError(t, inbound.Close())
					continue
				}
				r.handleInboundMessage(inbound)
				require.Len(t, r.serveJobs, 1)
				job := <-r.serveJobs
				t.Cleanup(func() { require.NoError(t, job.Close()) })
				decoded, err := message.Decode(job.Type, job.Payload)
				require.NoError(t, err)
				request := decoded.(*message.GetLedger)
				require.Equal(t, infoType, request.InfoType)
				require.Len(t, request.NodeIDs, count, "oversized requests must never reach the serve queue")
				return job
			case <-timeout.C:
				t.Fatal("accepted ledger request did not reach the router")
			}
		}
	}
	receiveRejected := func() {
		t.Helper()
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case inbound := <-client.overlay.Messages():
				if inbound.Type != message.TypeGetLedger {
					require.NoError(t, inbound.Close())
					continue
				}
				r.handleInboundMessage(inbound)
				require.Empty(t, r.serveJobs)
				return
			case <-timeout.C:
				t.Fatal("rejected ledger request did not reach the router")
			}
		}
	}

	for _, infoType := range []message.LedgerInfoType{message.LedgerInfoTxNode, message.LedgerInfoAsNode, message.LedgerInfoTsCandidate} {
		before := len(journal.snapshot())
		send(infoType, 12_289)
		send(infoType, 12_288)
		job := receive(infoType, 12_288)
		require.NoError(t, job.Close())
		var invalid, trivial int
		for _, charge := range journal.snapshot()[before:] {
			if charge.context == "wire-invalid" {
				require.Equal(t, resource.FeeInvalidData().String(), charge.fee)
				invalid++
			}
			if charge.context == message.TypeGetLedger.String() {
				require.Equal(t, resource.FeeTrivialPeer().String(), charge.fee)
				trivial++
			}
		}
		require.Equal(t, 1, invalid)
		require.Equal(t, 1, trivial, "only the admitted request receives a message-close charge")
		require.Empty(t, recorder.sentFrames())
		require.Zero(t, r.DroppedServeJobs())
	}

	before := len(journal.snapshot())
	send(message.LedgerInfoAsNode, 0)
	receiveRejected()
	var admissionInvalid, admissionTrivial int
	for _, charge := range journal.snapshot()[before:] {
		if !strings.Contains(charge.context, message.TypeGetLedger.String()) {
			continue
		}
		switch charge.fee {
		case resource.FeeInvalidData().String():
			admissionInvalid++
		case resource.FeeTrivialPeer().String():
			admissionTrivial++
		}
	}
	require.Equal(t, 1, admissionInvalid, "admission must select one invalid fee")
	require.Zero(t, admissionTrivial, "selected admission fee must replace the trivial close fee")

	before = len(journal.snapshot())
	send(message.LedgerInfoTsCandidate, txSetSoftMaxReplyNodes+1)
	oversized := receive(message.LedgerInfoTsCandidate, txSetSoftMaxReplyNodes+1)
	r.handleGetLedger(oversized)
	require.NoError(t, oversized.Close())
	var moderate, trivial int
	for _, charge := range journal.snapshot()[before:] {
		if !strings.Contains(charge.context, "get-ledger-") && !strings.Contains(charge.context, message.TypeGetLedger.String()) {
			continue
		}
		switch charge.fee {
		case resource.FeeModerateBurdenPeer().String():
			moderate++
		case resource.FeeTrivialPeer().String():
			trivial++
		}
	}
	require.Equal(t, 2, moderate, "oversized uncookied requests receive independent worker fees")
	require.Equal(t, 1, trivial, "worker fees remain additive to the final message charge")

	before = len(journal.snapshot())
	zeroCookie := uint64(0)
	sendWithCookie(message.LedgerInfoTsCandidate, txSetSoftMaxReplyNodes+1, &zeroCookie)
	cookied := receive(message.LedgerInfoTsCandidate, txSetSoftMaxReplyNodes+1)
	r.handleGetLedger(cookied)
	require.NoError(t, cookied.Close())
	moderate, trivial = 0, 0
	for _, charge := range journal.snapshot()[before:] {
		if !strings.Contains(charge.context, "get-ledger-") && !strings.Contains(charge.context, message.TypeGetLedger.String()) {
			continue
		}
		switch charge.fee {
		case resource.FeeModerateBurdenPeer().String():
			moderate++
		case resource.FeeTrivialPeer().String():
			trivial++
		}
	}
	require.Equal(t, 1, moderate, "an explicit zero cookie suppresses only the normal worker fee")
	require.Equal(t, 1, trivial)

	send(message.LedgerInfoBase, 12_289)
	base := receive(message.LedgerInfoBase, 12_289)
	r.handleGetLedger(base)
	require.NoError(t, base.Close())
	require.Len(t, recorder.sentFrames(), 1)
	_, decoded := decodeFrame(t, recorder.sentFrames()[0].frame)
	reply := decoded.(*message.LedgerData)
	require.Equal(t, message.LedgerInfoBase, reply.InfoType)
	require.Equal(t, hash[:], reply.LedgerHash)
	require.NotEmpty(t, reply.Nodes)
	require.Empty(t, recorder.badDataCalls())
}
