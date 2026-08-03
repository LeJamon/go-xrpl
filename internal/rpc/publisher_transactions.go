package rpc

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/txprojection"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

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
	txJSON, err := txprojection.ProjectRaw(event.Transaction, event.Hash, apiVersion)
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
	txJSON, err := txprojection.ProjectRaw(event.Transaction, event.Hash, apiVersion)
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
