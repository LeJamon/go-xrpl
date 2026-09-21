package adaptor

import (
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	peerproto "github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/require"
	pb "google.golang.org/protobuf/proto"
)

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
	send := func(infoType message.LedgerInfoType, count int) {
		t.Helper()
		ids := make([][]byte, count)
		for i := range ids {
			ids[i] = rootNodeID()
		}
		payload, err := pb.Marshal(&peerproto.TMGetLedger{
			Itype:      peerproto.TMLedgerInfoType(infoType).Enum(),
			LedgerHash: hash[:],
			NodeIDs:    ids,
		})
		require.NoError(t, err)
		frame, err := message.BuildWireMessage(message.TypeGetLedger, payload)
		require.NoError(t, err)
		require.NoError(t, connection.overlay.overlay.Send(connection.peerID, frame))
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
