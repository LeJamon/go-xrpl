package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// TxHistoryMethod handles the tx_history RPC method
type TxHistoryMethod struct{ baseHandler }

func (m *TxHistoryMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	var request struct {
		Start uint32 `json:"start,omitempty"`
	}

	// notEnabled takes precedence over any parameter validation, matching
	// rippled's useTxTables() gate as the first statement of doTxHistory.
	if err := requireTxTables(ctx.Services); err != nil {
		return nil, err
	}
	setLoadMedium(ctx)

	if err := parseParams(params, &request); err != nil {
		return nil, err
	}

	result, err := ctx.Services.Ledger().GetTransactionHistory(ctx.Context, request.Start)
	if err != nil {
		if errors.Is(err, svcerr.ErrTxHistoryUnavailable) {
			return nil, rpcerrors.RpcErrorNotEnabled("")
		}
		return nil, rpcInternalError("tx_history: transaction query failed", err)
	}

	// Build transactions array with deserialized JSON
	txs := make([]any, len(result.Transactions))
	for i, tx := range result.Transactions {
		hashStr := strings.ToUpper(hex.EncodeToString(tx.Hash[:]))
		// Decode to full JSON
		decoded, err := decodeBinaryObject(tx.TxBlob)
		if err != nil {
			return nil, rpcInternalError("tx_history: transaction decoding failed", err)
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

	response := map[string]any{
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
