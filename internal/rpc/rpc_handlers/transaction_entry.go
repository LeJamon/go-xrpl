package rpc_handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// TransactionEntryMethod handles the transaction_entry RPC method.
// Retrieves a transaction from a specific ledger version.
// Unlike the 'tx' method which searches across the ledger range,
// this method requires a specific ledger to search in.
// Reference: rippled TransactionEntry.cpp
type TransactionEntryMethod struct{}

func (m *TransactionEntryMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	var request struct {
		TxHash      string `json:"tx_hash"`
		LedgerHash  string `json:"ledger_hash,omitempty"`
		LedgerIndex any    `json:"ledger_index,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, rpc_types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if request.TxHash == "" {
		return nil, rpc_types.RpcErrorInvalidParams("Missing required parameter: tx_hash")
	}

	if rpc_types.Services == nil || rpc_types.Services.Ledger == nil {
		return nil, rpc_types.RpcErrorInternal("Ledger service not available")
	}

	// Parse the transaction hash
	txHashBytes, err := hex.DecodeString(request.TxHash)
	if err != nil || len(txHashBytes) != 32 {
		return nil, rpc_types.RpcErrorInvalidParams("Invalid tx_hash")
	}

	var txHash [32]byte
	copy(txHash[:], txHashBytes)

	// Look up the transaction
	txInfo, err := rpc_types.Services.Ledger.GetTransaction(txHash)
	if err != nil || txInfo == nil {
		return nil, &rpc_types.RpcError{
			Code:        -1,
			ErrorString: "txnNotFound",
			Message:     "Transaction not found.",
		}
	}

	// Resolve the target ledger and verify the transaction is in it
	targetSeq, rpcErr := m.resolveTargetLedger(request.LedgerHash, request.LedgerIndex)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Verify the transaction is in the requested ledger
	if txInfo.LedgerIndex != targetSeq {
		return nil, &rpc_types.RpcError{
			Code:        -1,
			ErrorString: "txnNotFound",
			Message:     fmt.Sprintf("Transaction not found in ledger %d", targetSeq),
		}
	}

	// Parse the stored transaction data
	var storedTx StoredTransaction
	if err := json.Unmarshal(txInfo.TxData, &storedTx); err != nil {
		return nil, rpc_types.RpcErrorInternal("Failed to parse transaction data")
	}

	// Get ledger hash for response
	ledgerHash := txInfo.LedgerHash
	if ledgerHash == "" {
		ledger, err := rpc_types.Services.Ledger.GetLedgerBySequence(targetSeq)
		if err == nil && ledger != nil {
			h := ledger.Hash()
			ledgerHash = fmt.Sprintf("%X", h)
		}
	}

	response := map[string]interface{}{
		"ledger_index": txInfo.LedgerIndex,
		"ledger_hash":  ledgerHash,
		"metadata":     storedTx.Meta,
		"tx_json":      storedTx.TxJSON,
		"validated":    txInfo.Validated,
	}

	return response, nil
}

// resolveTargetLedger resolves the ledger sequence from the request params.
func (m *TransactionEntryMethod) resolveTargetLedger(ledgerHash string, ledgerIndex any) (uint32, *rpc_types.RpcError) {
	// If ledger_hash is provided, resolve by hash
	if ledgerHash != "" {
		hashBytes, err := hex.DecodeString(ledgerHash)
		if err != nil || len(hashBytes) != 32 {
			return 0, rpc_types.RpcErrorInvalidParams("Invalid ledger_hash")
		}
		var hash [32]byte
		copy(hash[:], hashBytes)
		ledger, err := rpc_types.Services.Ledger.GetLedgerByHash(hash)
		if err != nil || ledger == nil {
			return 0, rpc_types.RpcErrorLgrNotFound("Ledger not found")
		}
		return ledger.Sequence(), nil
	}

	// If ledger_index is provided
	if ledgerIndex != nil {
		switch v := ledgerIndex.(type) {
		case float64:
			return uint32(v), nil
		case string:
			switch v {
			case "validated":
				return rpc_types.Services.Ledger.GetValidatedLedgerIndex(), nil
			case "closed":
				return rpc_types.Services.Ledger.GetClosedLedgerIndex(), nil
			case "current":
				return rpc_types.Services.Ledger.GetCurrentLedgerIndex(), nil
			default:
				seq, err := strconv.ParseUint(v, 10, 32)
				if err != nil {
					return 0, rpc_types.RpcErrorInvalidParams("Invalid ledger_index: " + v)
				}
				return uint32(seq), nil
			}
		}
	}

	// Default to validated ledger
	return rpc_types.Services.Ledger.GetValidatedLedgerIndex(), nil
}

func (m *TransactionEntryMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleGuest
}

func (m *TransactionEntryMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
