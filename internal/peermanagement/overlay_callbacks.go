package peermanagement

import (
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

func (o *Overlay) SetLedgerHintProvider(fn func() (LedgerHints, bool)) {
	o.providersMu.Lock()
	o.ledgerHintProvider = fn
	o.providersMu.Unlock()
}

func (o *Overlay) ledgerHintProviderSnapshot() func() (LedgerHints, bool) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.ledgerHintProvider
}

// SetValidLedgerProvider wires the validated-ledger source used by
// handleStatusChange. ok=false suppresses tracking updates.
func (o *Overlay) SetValidLedgerProvider(fn func() (seq uint32, age time.Duration, ok bool)) {
	o.providersMu.Lock()
	o.validLedgerProvider = fn
	o.providersMu.Unlock()
}

func (o *Overlay) validLedgerProviderSnapshot() func() (seq uint32, age time.Duration, ok bool) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.validLedgerProvider
}

// PeerStatusUpdate captures the post-decode TMStatusChange fields the RPC
// layer needs to materialize a peer_status WebSocket event.
type PeerStatusUpdate struct {
	Status         string
	Action         string
	LedgerHash     string
	LedgerIndex    *uint32
	Date           *uint32
	LedgerIndexMin *uint32
	LedgerIndexMax *uint32
}

// SetPeerStatusPublisher wires a sink for peer_status events. Passing nil
// disconnects the sink.
func (o *Overlay) SetPeerStatusPublisher(fn func(PeerStatusUpdate)) {
	o.providersMu.Lock()
	o.peerStatusPublisher = fn
	o.providersMu.Unlock()
}

func (o *Overlay) peerStatusPublisherSnapshot() func(PeerStatusUpdate) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.peerStatusPublisher
}

func peerStatusUpperName(s message.NodeStatus) string {
	switch s {
	case message.NodeStatusConnecting:
		return "CONNECTING"
	case message.NodeStatusConnected:
		return "CONNECTED"
	case message.NodeStatusMonitoring:
		return "MONITORING"
	case message.NodeStatusValidating:
		return "VALIDATING"
	case message.NodeStatusShutting:
		return "SHUTTING"
	default:
		return ""
	}
}

func peerStatusActionName(e message.NodeEvent) string {
	switch e {
	case message.NodeEventClosingLedger:
		return "CLOSING_LEDGER"
	case message.NodeEventAcceptedLedger:
		return "ACCEPTED_LEDGER"
	case message.NodeEventSwitchedLedger:
		return "SWITCHED_LEDGER"
	default:
		return ""
	}
}
