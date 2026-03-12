package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountTxMethod handles the account_tx RPC method
type AccountTxMethod struct{ BaseHandler }

func (m *AccountTxMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		LedgerIndexMin *json.RawMessage `json:"ledger_index_min,omitempty"`
		LedgerIndexMax *json.RawMessage `json:"ledger_index_max,omitempty"`
		LedgerHash     string           `json:"ledger_hash,omitempty"`
		LedgerIndex    string           `json:"ledger_index,omitempty"`
		Binary         bool             `json:"binary,omitempty"`
		Forward        bool             `json:"forward,omitempty"`
		types.PaginationParams
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if err := ValidateAccount(request.Account); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Parse ledger_index_min/max as int32
	var ledgerIndexMin, ledgerIndexMax int32
	if request.LedgerIndexMin != nil {
		var v int32
		if err := json.Unmarshal(*request.LedgerIndexMin, &v); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid field 'ledger_index_min'.")
		}
		ledgerIndexMin = v
	}
	if request.LedgerIndexMax != nil {
		var v int32
		if err := json.Unmarshal(*request.LedgerIndexMax, &v); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid field 'ledger_index_max'.")
		}
		ledgerIndexMax = v
	}

	hasMinMax := request.LedgerIndexMin != nil || request.LedgerIndexMax != nil
	hasLedgerSpec := request.LedgerHash != "" || request.LedgerIndex != ""

	// API v2: reject conflicting ledger parameters (ledger_index_min/max vs ledger_hash/ledger_index)
	if ctx.ApiVersion > 1 && hasMinMax && hasLedgerSpec {
		return nil, types.RpcErrorInvalidParams("invalidParams")
	}

	// Parse marker if provided
	var marker *types.AccountTxMarker
	if request.Marker != nil {
		if markerMap, ok := request.Marker.(map[string]interface{}); ok {
			marker = &types.AccountTxMarker{}
			if ledger, ok := markerMap["ledger"]; ok {
				switch v := ledger.(type) {
				case float64:
					marker.LedgerSeq = uint32(v)
				case json.Number:
					n, _ := v.Int64()
					marker.LedgerSeq = uint32(n)
				default:
					return nil, types.RpcErrorInvalidParams("invalid marker. Provide ledger index via ledger field, and transaction sequence number via seq field")
				}
			}
			if seq, ok := markerMap["seq"]; ok {
				switch v := seq.(type) {
				case float64:
					marker.TxnSeq = uint32(v)
				case json.Number:
					n, _ := v.Int64()
					marker.TxnSeq = uint32(n)
				default:
					return nil, types.RpcErrorInvalidParams("invalid marker. Provide ledger index via ledger field, and transaction sequence number via seq field")
				}
			}
		} else {
			return nil, types.RpcErrorInvalidParams("invalid marker. Provide ledger index via ledger field, and transaction sequence number via seq field")
		}
	}

	result, err := types.Services.Ledger.GetAccountTransactions(
		request.Account,
		int64(ledgerIndexMin),
		int64(ledgerIndexMax),
		request.Limit,
		marker,
		request.Forward,
	)
	if err != nil {
		if err.Error() == "transaction history not available (no database configured)" {
			return nil, &types.RpcError{
				Code:    73,
				Message: "Transaction history not available. Database not configured.",
			}
		}
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    19,
				Message: "Account not found.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get account transactions: " + err.Error())
	}

	// Cache for ledger lookups by sequence, to avoid repeated lookups
	// for transactions in the same ledger.
	type ledgerCacheEntry struct {
		hash         [32]byte
		closeTimeSec int64
		found        bool
	}
	ledgerCache := make(map[uint32]*ledgerCacheEntry)

	lookupLedger := func(seq uint32) *ledgerCacheEntry {
		if entry, ok := ledgerCache[seq]; ok {
			return entry
		}
		entry := &ledgerCacheEntry{}
		ledger, lookupErr := types.Services.Ledger.GetLedgerBySequence(seq)
		if lookupErr == nil && ledger != nil {
			entry.hash = ledger.Hash()
			entry.closeTimeSec = ledger.CloseTime()
			entry.found = true
		}
		ledgerCache[seq] = entry
		return entry
	}

	// Get network_id for CTID encoding
	serverInfo := types.Services.Ledger.GetServerInfo()
	networkID := serverInfo.NetworkID

	isV2 := ctx.ApiVersion > 1

	// Build transactions array
	transactions := make([]map[string]interface{}, len(result.Transactions))
	for i, txn := range result.Transactions {
		txEntry := map[string]interface{}{
			"validated": true,
		}

		txHashHex := strings.ToUpper(hex.EncodeToString(txn.Hash[:]))

		if request.Binary {
			// Binary mode
			txEntry["tx_blob"] = strings.ToUpper(hex.EncodeToString(txn.TxBlob))
			if isV2 {
				// API v2: meta_blob
				txEntry["meta_blob"] = strings.ToUpper(hex.EncodeToString(txn.Meta))
			} else {
				// API v1: meta
				txEntry["meta"] = strings.ToUpper(hex.EncodeToString(txn.Meta))
			}
			txEntry["ledger_index"] = txn.LedgerIndex
		} else {
			// JSON mode: decode tx_blob and meta into JSON objects

			// Determine the tx JSON key based on API version
			txKey := "tx"
			if isV2 {
				txKey = "tx_json"
			}

			// Decode tx_blob into JSON
			txBlobHex := hex.EncodeToString(txn.TxBlob)
			txJSON, decErr := binarycodec.Decode(txBlobHex)
			if decErr != nil {
				// Fallback to hex if decode fails
				txEntry["tx_blob"] = strings.ToUpper(txBlobHex)
			} else {
				// Add date inside the tx JSON (both v1 and v2)
				ledgerInfo := lookupLedger(txn.LedgerIndex)
				if ledgerInfo.found && ledgerInfo.closeTimeSec > 0 {
					txJSON["date"] = ledgerInfo.closeTimeSec
				}

				// Inject DeliverMax for Payment transactions
				injectDeliverMax(txJSON, ctx.ApiVersion)

				if isV2 {
					// API v2: add fields inside tx_json
					txJSON["ledger_index"] = txn.LedgerIndex
					txJSON["hash"] = txHashHex

					// Add CTID inside tx_json (v2 only)
					if txn.LedgerIndex > 0 && txn.LedgerIndex < 0x0FFFFFFF {
						txJSON["ctid"] = encodeCTIDWithNetworkID(txn.LedgerIndex, uint16(txn.TxnSeq), uint16(networkID))
					}
				}

				txEntry[txKey] = txJSON
			}

			// Decode metadata
			metaHex := hex.EncodeToString(txn.Meta)
			metaJSON, metaErr := binarycodec.Decode(metaHex)
			if metaErr != nil {
				txEntry["meta"] = strings.ToUpper(metaHex)
			} else {
				// Inject DeliveredAmount into metadata if this is a Payment
				if txJSONMap, ok := txEntry[txKey].(map[string]interface{}); ok {
					InjectDeliveredAmount(txJSONMap, metaJSON)
				}
				txEntry["meta"] = metaJSON
			}

			// Add hash at entry level (both v1 and v2 — for v1 this is where
			// consumers find the hash when the tx_blob couldn't be decoded into
			// a full tx object; rippled puts it inside the 'tx' object but our
			// binary codec path doesn't always produce a decodable blob).
			txEntry["hash"] = txHashHex
			txEntry["ledger_index"] = txn.LedgerIndex

			if isV2 {
				// API v2: add per-entry ledger_hash and close_time_iso
				ledgerInfo := lookupLedger(txn.LedgerIndex)
				if ledgerInfo.found {
					txEntry["ledger_hash"] = strings.ToUpper(hex.EncodeToString(ledgerInfo.hash[:]))
					if ledgerInfo.closeTimeSec > 0 {
						closeTime := rippleEpochTime.Add(time.Duration(ledgerInfo.closeTimeSec) * time.Second)
						txEntry["close_time_iso"] = closeTime.UTC().Format("2006-01-02T15:04:05Z")
					}
				}
			}
		}

		transactions[i] = txEntry
	}

	response := map[string]interface{}{
		"account":          result.Account,
		"ledger_index_min": result.LedgerMin,
		"ledger_index_max": result.LedgerMax,
		"limit":            result.Limit,
		"transactions":     transactions,
		"validated":        result.Validated,
	}

	if result.Marker != nil {
		response["marker"] = map[string]interface{}{
			"ledger": result.Marker.LedgerSeq,
			"seq":    result.Marker.TxnSeq,
		}
	}

	return response, nil
}

// injectDeliverMax adds DeliverMax to Payment transaction JSON.
// For API v1: adds DeliverMax = Amount (keeps Amount).
// For API v2+: adds DeliverMax = Amount, then removes Amount.
// This matches rippled's RPC::insertDeliverMax in DeliverMax.cpp.
func injectDeliverMax(txJSON map[string]interface{}, apiVersion int) {
	amount, hasAmount := txJSON["Amount"]
	if !hasAmount {
		return
	}
	txType, _ := txJSON["TransactionType"].(string)
	if txType != "Payment" {
		return
	}
	txJSON["DeliverMax"] = amount
	if apiVersion > 1 {
		delete(txJSON, "Amount")
	}
}

// encodeCTIDWithNetworkID encodes ledger sequence, tx index, and network ID into a CTID hex string.
// CTID format (64 bits): [63:60]=0xC marker, [59:32]=ledger_seq (28 bits),
// [31:16]=tx_index (16 bits), [15:0]=network_id (16 bits).
func encodeCTIDWithNetworkID(ledgerSeq uint32, txIndex uint16, networkID uint16) string {
	val := uint64(0xC)<<60 |
		uint64(ledgerSeq)<<32 |
		uint64(txIndex)<<16 |
		uint64(networkID)
	return fmt.Sprintf("%016X", val)
}
