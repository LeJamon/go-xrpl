package rpc

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/keylet"

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

	if p.manager.HasMPTIssuanceSubscribers() {
		if ids := affectedMPTIssuances(event.Meta); len(ids) > 0 {
			p.manager.BroadcastToMPTIssuancesVersioned(v1, v2, ids)
		}
	}
}

// affectedMPTIssuances lists the MPT issuances a transaction's metadata
// touches: the ID derived from each MPTokenIssuance node's Sequence and
// Issuer, plus every MPTokenIssuanceID field and MPT amount among the node's
// final (or, for a created node, new) fields.
func affectedMPTIssuances(metaJSON json.RawMessage) []string {
	var meta struct {
		AffectedNodes []map[string]struct {
			LedgerEntryType string                     `json:"LedgerEntryType"`
			NewFields       map[string]json.RawMessage `json:"NewFields"`
			FinalFields     map[string]json.RawMessage `json:"FinalFields"`
		} `json:"AffectedNodes"`
	}
	if len(metaJSON) == 0 || json.Unmarshal(metaJSON, &meta) != nil {
		return nil
	}
	seen := make(map[string]struct{})
	add := func(id string) {
		if len(id) == 48 {
			seen[strings.ToUpper(id)] = struct{}{}
		}
	}
	for _, wrapper := range meta.AffectedNodes {
		for kind, node := range wrapper {
			fields := node.FinalFields
			if kind == "CreatedNode" {
				fields = node.NewFields
			}
			if fields == nil {
				continue
			}
			if node.LedgerEntryType == "MPTokenIssuance" {
				var sequence uint32
				var issuer string
				if json.Unmarshal(fields["Sequence"], &sequence) == nil && json.Unmarshal(fields["Issuer"], &issuer) == nil {
					if _, accountID, err := addresscodec.DecodeClassicAddressToAccountID(issuer); err == nil && len(accountID) == 20 {
						id := keylet.MakeMPTID(sequence, [20]byte(accountID))
						add(hex.EncodeToString(id[:]))
					}
				}
			}
			for name, raw := range fields {
				if name == "MPTokenIssuanceID" {
					var id string
					if json.Unmarshal(raw, &id) == nil {
						add(id)
					}
					continue
				}
				var amount struct {
					MPTIssuanceID *string `json:"mpt_issuance_id"`
					Value         *string `json:"value"`
				}
				if json.Unmarshal(raw, &amount) == nil && amount.MPTIssuanceID != nil && amount.Value != nil {
					add(*amount.MPTIssuanceID)
				}
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
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
