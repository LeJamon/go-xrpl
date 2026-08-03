package rpc

import (
	"encoding/json"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// EventPublisher publishes events to WebSocket subscribers
// This interface allows the ledger service and other components to publish
// events without directly depending on the WebSocket/subscription implementation
type EventPublisher interface {
	// PublishLedgerClosed publishes a ledger close event to all ledger stream subscribers
	PublishLedgerClosed(event *LedgerCloseEvent)

	// PublishTransaction publishes a transaction event to transaction stream subscribers
	// If affectedAccounts is provided, the event is also sent to account subscribers
	PublishTransaction(event *TransactionEvent, affectedAccounts []string)

	// PublishValidation publishes a validation event to validation stream subscribers
	PublishValidation(event *ValidationEvent)

	// PublishServerStatus publishes a server status event and reports whether
	// at least one server-stream subscriber was targeted.
	PublishServerStatus(event *ServerStatusEvent) bool

	// PublishConsensusPhase publishes a consensus phase change to consensus stream subscribers
	PublishConsensusPhase(phase string)

	// PublishManifest publishes a manifest event to manifest stream subscribers
	PublishManifest(event *ManifestEvent)

	// PublishPeerStatus publishes a peer status event to peer_status stream subscribers
	PublishPeerStatus(event *PeerStatusEvent)

	// PublishProposedTransaction publishes a proposed transaction to accounts_proposed subscribers
	PublishProposedTransaction(event *ProposedTransactionEvent, accounts []string)

	// PublishOrderBookChange publishes an order book change to book subscribers
	PublishOrderBookChange(event *TransactionEvent, books []types.OrderBookSpec)

	// GetSubscriberCount returns the number of active subscribers for a stream type
	GetSubscriberCount(streamType types.SubscriptionType) int
}

// Publisher implements EventPublisher using subscription.Manager
type Publisher struct {
	manager *subscription.Manager
}

// NewPublisher creates a new Publisher with the given subscription manager
func NewPublisher(manager *subscription.Manager) *Publisher {
	return &Publisher{
		manager: manager,
	}
}

// PublishLedgerClosed broadcasts a ledger close event to all ledger stream subscribers
func (p *Publisher) PublishLedgerClosed(event *LedgerCloseEvent) {
	if event == nil || p.manager == nil {
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal LedgerCloseEvent", "err", err)
		return
	}

	p.manager.BroadcastToStream(types.SubLedger, data, nil)
}

// PublishValidation broadcasts a validation event to validation stream subscribers
func (p *Publisher) PublishValidation(event *ValidationEvent) {
	if event == nil || p.manager == nil {
		return
	}

	v1, err := marshalValidationEvent(event, types.ApiVersion1)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal ValidationEvent", "err", err)
		return
	}
	v2, err := marshalValidationEvent(event, types.ApiVersion2)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal ValidationEvent", "err", err)
		return
	}

	p.manager.BroadcastToStreamVersioned(types.SubValidations, v1, v2)
}

func marshalValidationEvent(event *ValidationEvent, apiVersion int) ([]byte, error) {
	if apiVersion != types.ApiVersion1 {
		return json.Marshal(event)
	}
	type validationEvent ValidationEvent
	return json.Marshal(struct {
		*validationEvent
		LedgerIndex string `json:"ledger_index"`
	}{
		validationEvent: (*validationEvent)(event),
		LedgerIndex:     strconv.FormatUint(uint64(event.LedgerIndex), 10),
	})
}

// PublishServerStatus broadcasts a server status event to server stream subscribers.
func (p *Publisher) PublishServerStatus(event *ServerStatusEvent) bool {
	if event == nil || p.manager == nil {
		return false
	}

	data, err := json.Marshal(event)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal ServerStatusEvent", "err", err)
		return false
	}

	return p.manager.BroadcastToStream(types.SubServer, data, nil) != 0
}

// PublishConsensusPhase broadcasts a consensus phase change event
func (p *Publisher) PublishConsensusPhase(phase string) {
	if p.manager == nil {
		return
	}

	event := NewConsensusEvent(phase)
	data, err := json.Marshal(event)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal ConsensusEvent", "err", err)
		return
	}

	p.manager.BroadcastToStream(types.SubConsensus, data, nil)
}

// PublishManifest broadcasts a manifest event to manifest stream subscribers
func (p *Publisher) PublishManifest(event *ManifestEvent) {
	if event == nil || p.manager == nil {
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal ManifestEvent", "err", err)
		return
	}

	p.manager.BroadcastToStream(types.SubManifests, data, nil)
}

// PublishPeerStatus broadcasts a peer status event to peer_status stream subscribers
func (p *Publisher) PublishPeerStatus(event *PeerStatusEvent) {
	if event == nil || p.manager == nil {
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal PeerStatusEvent", "err", err)
		return
	}

	p.manager.BroadcastToStream(types.SubPeerStatus, data, nil)
}

// GetSubscriberCount returns the number of active subscribers for a stream type
func (p *Publisher) GetSubscriberCount(streamType types.SubscriptionType) int {
	if p.manager == nil {
		return 0
	}
	return p.manager.GetSubscriberCount(streamType)
}

// Ensure implementations satisfy the interface
var _ EventPublisher = (*Publisher)(nil)
