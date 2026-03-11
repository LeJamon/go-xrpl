package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountTxMethod handles the account_tx RPC method
type AccountTxMethod struct{}

func (m *AccountTxMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		LedgerIndexMin int32  `json:"ledger_index_min,omitempty"`
		LedgerIndexMax int32  `json:"ledger_index_max,omitempty"`
		LedgerHash     string `json:"ledger_hash,omitempty"`
		LedgerIndex    string `json:"ledger_index,omitempty"`
		Binary         bool   `json:"binary,omitempty"`
		Forward        bool   `json:"forward,omitempty"`
		types.PaginationParams
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if request.Account == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: account")
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Parse marker if provided
	var marker *types.AccountTxMarker
	if request.Marker != nil {
		if markerMap, ok := request.Marker.(map[string]interface{}); ok {
			marker = &types.AccountTxMarker{}
			if ledger, ok := markerMap["ledger"].(float64); ok {
				marker.LedgerSeq = uint32(ledger)
			}
			if seq, ok := markerMap["seq"].(float64); ok {
				marker.TxnSeq = uint32(seq)
			}
		}
	}

	result, err := types.Services.Ledger.GetAccountTransactions(
		request.Account,
		int64(request.LedgerIndexMin),
		int64(request.LedgerIndexMax),
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

	// Build transactions array
	transactions := make([]map[string]interface{}, len(result.Transactions))
	for i, tx := range result.Transactions {
		txEntry := map[string]interface{}{
			"validated": true,
		}

		// Add transaction hash
		txEntry["hash"] = strings.ToUpper(hex.EncodeToString(tx.Hash[:]))

		if request.Binary {
			// Binary mode: return hex blobs
			txEntry["tx_blob"] = strings.ToUpper(hex.EncodeToString(tx.TxBlob))
			txEntry["meta"] = strings.ToUpper(hex.EncodeToString(tx.Meta))
			txEntry["ledger_index"] = tx.LedgerIndex
		} else {
			// JSON mode: decode tx_blob and meta to JSON objects
			txBlobHex := hex.EncodeToString(tx.TxBlob)
			txJSON, err := binarycodec.Decode(txBlobHex)
			if err != nil {
				// Fallback to hex if decode fails
				txEntry["tx_blob"] = strings.ToUpper(txBlobHex)
			} else {
				// Add ledger_index and hash to tx_json
				txJSON["ledger_index"] = tx.LedgerIndex
				txJSON["hash"] = strings.ToUpper(hex.EncodeToString(tx.Hash[:]))

				// Inject DeliveredAmount for Payment transactions
				InjectDeliveredAmount(txJSON, nil)

				txEntry["tx"] = txJSON
			}

			// Decode metadata
			metaHex := hex.EncodeToString(tx.Meta)
			metaJSON, err := binarycodec.Decode(metaHex)
			if err != nil {
				txEntry["meta"] = strings.ToUpper(metaHex)
			} else {
				// Inject DeliveredAmount into metadata if this is a Payment
				if txJSON != nil {
					InjectDeliveredAmount(txJSON, metaJSON)
				}
				txEntry["meta"] = metaJSON
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


func (m *AccountTxMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *AccountTxMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *AccountTxMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
