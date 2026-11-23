package adaptor

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type chargeLog struct {
	fee     string
	context string
}

type chargeLogHandler struct {
	mu      sync.Mutex
	charges []chargeLog
}

func (h *chargeLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug-8
}

func (h *chargeLogHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "resource charge" {
		return nil
	}
	var charge chargeLog
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "fee":
			charge.fee = attr.Value.String()
		case "context":
			charge.context = attr.Value.String()
		}
		return true
	})
	h.mu.Lock()
	h.charges = append(h.charges, charge)
	h.mu.Unlock()
	return nil
}

func (h *chargeLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *chargeLogHandler) WithGroup(string) slog.Handler { return h }

func (h *chargeLogHandler) snapshot() []chargeLog {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]chargeLog(nil), h.charges...)
}

func TestRouter_InboundFramesSelectOnePeerFee(t *testing.T) {
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
	case <-time.After(progressTimeout):
		t.Fatal("ledger-data source did not connect")
	}

	r, _ := makeRouterWithRelayRecorder(t)
	manifestRouter, _, _ := routerWithCache(t, nil, 0, 0)
	send := func(data message.Message) {
		t.Helper()
		frame, err := message.EncodeFrame(data)
		require.NoError(t, err)
		require.NoError(t, connection.overlay.overlay.Send(connection.peerID, frame))

		lane := client.overlay.LedgerDataMessages()
		if data.Type() == message.TypeManifests {
			lane = client.overlay.ManifestMessages()
		}
		select {
		case inbound := <-lane:
			if data.Type() == message.TypeManifests {
				manifestRouter.processManifestJob(inbound)
			} else {
				r.handleInboundMessage(inbound)
			}
		case <-time.After(progressTimeout):
			t.Fatalf("%s did not reach the processing lane", data.Type())
		}
	}

	assertOneCharge := func(before int, msgType message.MessageType, expected resource.Charge, reason string) {
		t.Helper()
		var charges []chargeLog
		for _, charge := range journal.snapshot()[before:] {
			if charge.context == msgType.String() || strings.HasPrefix(charge.context, msgType.String()+" ") {
				charges = append(charges, charge)
			}
		}
		require.Len(t, charges, 1)
		require.Equal(t, expected.String(), charges[0].fee)
		require.Equal(t, strings.TrimSpace(msgType.String()+" "+reason), charges[0].context)
	}

	before := len(journal.snapshot())
	send(&message.LedgerData{
		LedgerHash: []byte{1},
		InfoType:   message.LedgerInfoBase,
	})
	assertOneCharge(before, message.TypeLedgerData, resource.FeeInvalidData(), "ledger-data-hash")

	depth := uint32(0)
	before = len(journal.snapshot())
	send(&message.LedgerData{
		LedgerHash:       make([]byte, 32),
		InfoType:         message.LedgerInfoAsNode,
		Nodes:            []message.LedgerNode{{NodeData: []byte{0xFF}, Depth: &depth}},
		RequestCookie:    99,
		RequestCookieSet: true,
	})
	assertOneCharge(before, message.TypeLedgerData, resource.FeeInvalidData(), "ledger-data-node")

	before = len(journal.snapshot())
	send(&message.ProofPathResponse{
		Key: make([]byte, 32), LedgerHash: make([]byte, 32),
		MapType: message.LedgerMapAccountState, Error: message.ReplyErrorNoNode,
	})
	assertOneCharge(before, message.TypeProofPathResponse, resource.FeeInvalidData(), "proof_path_response")

	before = len(journal.snapshot())
	send(&message.ProofPathResponse{
		Key: make([]byte, 32), LedgerHash: make([]byte, 32), MapType: message.LedgerMapAccountState,
	})
	assertOneCharge(before, message.TypeProofPathResponse, resource.FeeMalformedData(), "proof_path_response malformed")

	closed := makeClosedLedgerWithTxs(t, nil)
	lookup := newFakeLookup()
	lookup.add(closed)
	hash := closed.Hash()
	key := keylet.LedgerHashes().Key
	wireHeader, path, err := newLedgerProviderForTest(lookup).GetProofPath(hash[:], key[:], message.LedgerMapAccountState)
	require.NoError(t, err)
	before = len(journal.snapshot())
	send(&message.ProofPathResponse{
		Key: key[:], LedgerHash: hash[:], MapType: message.LedgerMapAccountState,
		LedgerHeader: wireHeader, Path: path,
	})
	assertOneCharge(before, message.TypeProofPathResponse, resource.FeeTrivialPeer(), "")

	before = len(journal.snapshot())
	send(&message.Manifests{})
	assertOneCharge(before, message.TypeManifests, resource.FeeUselessData(), "empty")

	entry := message.Manifest{STObject: buildWireManifest(t, 1, 0x26, 0x27)}
	before = len(journal.snapshot())
	send(&message.Manifests{List: []message.Manifest{entry}})
	assertOneCharge(before, message.TypeManifests, resource.FeeTrivialPeer(), "")
}
