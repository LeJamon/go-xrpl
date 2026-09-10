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

func TestRouter_LedgerDataSynchronousInvaliditySelectsOnePeerFee(t *testing.T) {
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
		t.Fatal("ledger-data source did not connect")
	}

	r, _ := makeRouterWithRelayRecorder(t)
	send := func(data *message.LedgerData) {
		t.Helper()
		frame, err := message.EncodeFrame(data)
		require.NoError(t, err)
		require.NoError(t, connection.overlay.overlay.Send(connection.peerID, frame))

		select {
		case inbound := <-client.overlay.LedgerDataMessages():
			r.handleInboundMessage(inbound)
		case <-time.After(5 * time.Second):
			t.Fatal("ledger-data frame did not reach the processing lane")
		}
	}

	assertOneInvalidDataCharge := func(before int, reason string) {
		t.Helper()
		charges := journal.snapshot()[before:]
		var invalid, trivial []chargeLog
		ledgerDataContext := message.TypeLedgerData.String()
		for _, charge := range charges {
			if charge.context != ledgerDataContext && !strings.HasPrefix(charge.context, ledgerDataContext+" ") {
				continue
			}
			switch charge.fee {
			case resource.FeeInvalidData().String():
				invalid = append(invalid, charge)
			case resource.FeeTrivialPeer().String():
				trivial = append(trivial, charge)
			}
		}
		require.Len(t, invalid, 1, "synchronous ledger-data rejection must select one invalid-data fee")
		require.Contains(t, invalid[0].context, reason)
		require.Empty(t, trivial, "selected invalid-data fee must replace the ledger-data message's trivial close fee")
	}

	before := len(journal.snapshot())
	send(&message.LedgerData{
		LedgerHash: []byte{1},
		InfoType:   message.LedgerInfoBase,
	})
	assertOneInvalidDataCharge(before, "ledger-data-hash")

	depth := uint32(0)
	before = len(journal.snapshot())
	send(&message.LedgerData{
		LedgerHash:       make([]byte, 32),
		InfoType:         message.LedgerInfoAsNode,
		Nodes:            []message.LedgerNode{{NodeData: []byte{0xFF}, Depth: &depth}},
		RequestCookie:    99,
		RequestCookieSet: true,
	})
	assertOneInvalidDataCharge(before, "ledger-data-node")
}
