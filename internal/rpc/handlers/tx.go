package handlers

import (
	"encoding/hex"
	"encoding/json"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// TxMethod handles the tx RPC method
type TxMethod struct{}

func (m *TxMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.TransactionParam
		Binary    bool   `json:"binary,omitempty"`
		MinLedger uint32 `json:"min_ledger,omitempty"`
		MaxLedger uint32 `json:"max_ledger,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if request.Transaction == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: transaction")
	}

	// Check if ledger service is available
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Parse the transaction hash
	txHashBytes, err := hex.DecodeString(request.Transaction)
	if err != nil || len(txHashBytes) != 32 {
		return nil, types.RpcErrorInvalidParams("Invalid transaction hash")
	}

	var txHash [32]byte
	copy(txHash[:], txHashBytes)

	// Look up the transaction
	txInfo, err := types.Services.Ledger.GetTransaction(txHash)
	if err != nil {
		return nil, &types.RpcError{
			Code:        -1,
			ErrorString: "txnNotFound",
			Message:     "Transaction not found",
		}
	}

	// Parse the stored transaction data
	// The stored data includes both the transaction and its metadata
	var storedTx StoredTransaction
	if err := json.Unmarshal(txInfo.TxData, &storedTx); err != nil {
		return nil, types.RpcErrorInternal("Failed to parse transaction data")
	}

	// Build the response
	response := map[string]interface{}{}

	// If binary mode, return binary encoded transaction
	if request.Binary {
		// Encode transaction to binary
		txBlob, err := binarycodec.Encode(storedTx.TxJSON)
		if err == nil {
			response["tx_blob"] = txBlob
		}
		// Encode metadata to binary
		if storedTx.Meta != nil {
			metaBlob, err := binarycodec.Encode(storedTx.Meta)
			if err == nil {
				response["meta"] = metaBlob
			}
		}
	} else {
		// Return JSON format
		for k, v := range storedTx.TxJSON {
			response[k] = v
		}
		if storedTx.Meta != nil {
			response["meta"] = storedTx.Meta
		}
	}

	// Add ledger info
	response["hash"] = request.Transaction
	response["inLedger"] = txInfo.LedgerIndex
	response["ledger_index"] = txInfo.LedgerIndex
	response["ledger_hash"] = txInfo.LedgerHash
	response["validated"] = txInfo.Validated

	return response, nil
}

// StoredTransaction represents a transaction stored in the ledger
type StoredTransaction struct {
	TxJSON map[string]interface{} `json:"tx_json"`
	Meta   map[string]interface{} `json:"meta"`
}

func (m *TxMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *TxMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
