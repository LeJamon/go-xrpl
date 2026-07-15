package rpc

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
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

	// PublishServerStatus publishes a server status event to server stream subscribers
	PublishServerStatus(event *ServerStatusEvent)

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

// PublishTransaction broadcasts a transaction event to subscribers
func (p *Publisher) PublishTransaction(event *TransactionEvent, affectedAccounts []string) {
	if event == nil || p.manager == nil {
		return
	}

	v1, err := marshalTransactionEvent(event, types.ApiVersion1)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal TransactionEvent", "err", err)
		return
	}
	v2, err := marshalTransactionEvent(event, types.ApiVersion2)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal TransactionEvent", "err", err)
		return
	}

	p.manager.BroadcastToStreamVersioned(types.SubTransactions, v1, v2)
	p.manager.BroadcastToStreamVersioned(types.SubTransactionsProposed, v1, v2)

	if len(affectedAccounts) > 0 {
		p.manager.BroadcastToAcceptedAccountsVersioned(v1, v2, affectedAccounts)
	}
}

func marshalTransactionEvent(event *TransactionEvent, apiVersion int) ([]byte, error) {
	txJSON, err := handlers.ProjectTransactionRaw(event.Transaction, event.Hash, apiVersion)
	if err != nil {
		return nil, err
	}

	projected := *event
	if apiVersion > 1 {
		projected.Transaction = nil
		projected.TxJson = txJSON
	} else {
		projected.Transaction = txJSON
		projected.TxJson = nil
		projected.Hash = ""
	}
	return json.Marshal(&projected)
}

// PublishValidation broadcasts a validation event to validation stream subscribers
func (p *Publisher) PublishValidation(event *ValidationEvent) {
	if event == nil || p.manager == nil {
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal ValidationEvent", "err", err)
		return
	}

	p.manager.BroadcastToStream(types.SubValidations, data, nil)
}

// PublishServerStatus broadcasts a server status event to server stream subscribers
func (p *Publisher) PublishServerStatus(event *ServerStatusEvent) {
	if event == nil || p.manager == nil {
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal ServerStatusEvent", "err", err)
		return
	}

	p.manager.BroadcastToStream(types.SubServer, data, nil)
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

// PublishProposedTransaction broadcasts a proposed transaction to accounts_proposed subscribers
func (p *Publisher) PublishProposedTransaction(event *ProposedTransactionEvent, accounts []string) {
	if event == nil || p.manager == nil {
		return
	}

	v1, err := marshalProposedTransactionEvent(event, types.ApiVersion1)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal ProposedTransactionEvent", "err", err)
		return
	}
	v2, err := marshalProposedTransactionEvent(event, types.ApiVersion2)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal ProposedTransactionEvent", "err", err)
		return
	}

	p.manager.BroadcastToStreamVersioned(types.SubTransactionsProposed, v1, v2)
	if len(accounts) > 0 {
		p.manager.BroadcastToAccountsProposedVersioned(v1, v2, accounts)
	}
}

func marshalProposedTransactionEvent(event *ProposedTransactionEvent, apiVersion int) ([]byte, error) {
	txJSON, err := handlers.ProjectTransactionRaw(event.Transaction, event.Hash, apiVersion)
	if err != nil {
		return nil, err
	}

	projected := *event
	if apiVersion > 1 {
		projected.Transaction = nil
		projected.TxJson = txJSON
	} else {
		projected.Transaction = txJSON
		projected.TxJson = nil
		projected.Hash = ""
	}
	return json.Marshal(&projected)
}

// PublishOrderBookChange broadcasts an order book change to book subscribers
func (p *Publisher) PublishOrderBookChange(event *TransactionEvent, books []types.OrderBookSpec) {
	if event == nil || p.manager == nil || len(books) == 0 {
		return
	}

	v1, err := marshalTransactionEvent(event, types.ApiVersion1)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal order-book TransactionEvent", "err", err)
		return
	}
	v2, err := marshalTransactionEvent(event, types.ApiVersion2)
	if err != nil {
		xrpllog.Named(xrpllog.PartitionRPC).Error("Failed to marshal order-book TransactionEvent", "err", err)
		return
	}

	p.manager.BroadcastToOrderBooksVersioned(v1, v2, books)
}

// GetSubscriberCount returns the number of active subscribers for a stream type
func (p *Publisher) GetSubscriberCount(streamType types.SubscriptionType) int {
	if p.manager == nil {
		return 0
	}
	return p.manager.GetSubscriberCount(streamType)
}

// NoOpPublisher is a publisher that does nothing (for testing or when subscriptions are disabled)
type NoOpPublisher struct{}

func NewNoOpPublisher() *NoOpPublisher {
	return &NoOpPublisher{}
}

func (p *NoOpPublisher) PublishLedgerClosed(event *LedgerCloseEvent)                   {}
func (p *NoOpPublisher) PublishTransaction(event *TransactionEvent, accounts []string) {}
func (p *NoOpPublisher) PublishValidation(event *ValidationEvent)                      {}
func (p *NoOpPublisher) PublishServerStatus(event *ServerStatusEvent)                  {}
func (p *NoOpPublisher) PublishConsensusPhase(phase string)                            {}
func (p *NoOpPublisher) PublishManifest(event *ManifestEvent)                          {}
func (p *NoOpPublisher) PublishPeerStatus(event *PeerStatusEvent)                      {}
func (p *NoOpPublisher) PublishProposedTransaction(event *ProposedTransactionEvent, accounts []string) {
}
func (p *NoOpPublisher) PublishOrderBookChange(event *TransactionEvent, books []types.OrderBookSpec) {
}
func (p *NoOpPublisher) GetSubscriberCount(streamType types.SubscriptionType) int { return 0 }

// Ensure implementations satisfy the interface
var _ EventPublisher = (*Publisher)(nil)
var _ EventPublisher = (*NoOpPublisher)(nil)
