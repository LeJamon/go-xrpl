package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// TxHistoryMethod handles the tx_history RPC method
type TxHistoryMethod struct{}

func (m *TxHistoryMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Start uint32 `json:"start,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	result, err := ctx.Services.Ledger.GetTransactionHistory(request.Start)
	if err != nil {
		if err.Error() == "transaction history not available (no database configured)" {
			return nil, &types.RpcError{
				Code:    73,
				Message: "Transaction history not available. Database not configured.",
			}
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get transaction history: %v", err))
	}

	// Build transactions array with deserialized JSON
	txs := make([]interface{}, len(result.Transactions))
	for i, tx := range result.Transactions {
		hashStr := strings.ToUpper(hex.EncodeToString(tx.Hash[:]))
		txHex := hex.EncodeToString(tx.TxBlob)

		// Decode to full JSON
		decoded, err := binarycodec.Decode(txHex)
		if err != nil {
			// Fallback to hex blob
			txs[i] = map[string]interface{}{
				"hash":         hashStr,
				"ledger_index": tx.LedgerIndex,
				"tx_blob":      strings.ToUpper(txHex),
			}
			continue
		}

		decoded["hash"] = hashStr
		decoded["ledger_index"] = tx.LedgerIndex

		// Inject DeliverMax for Payment transactions
		if txType, ok := decoded["TransactionType"].(string); ok && txType == "Payment" {
			if amount, ok := decoded["Amount"]; ok {
				decoded["DeliverMax"] = amount
			}
		}

		txs[i] = decoded
	}

	response := map[string]interface{}{
		"index": result.Index,
		"txs":   txs,
	}

	return response, nil
}

func (m *TxHistoryMethod) RequiredRole() types.Role {
	return types.RoleUser
}

func (m *TxHistoryMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1}
}

func (m *TxHistoryMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
