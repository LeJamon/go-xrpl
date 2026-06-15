package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// TransactionEntryMethod handles the transaction_entry RPC method.
// Retrieves a transaction from a specific ledger version.
// Unlike the 'tx' method which searches across the ledger range,
// this method requires a specific ledger to search in.
// Reference: rippled TransactionEntry.cpp
type TransactionEntryMethod struct{ BaseHandler }

func (m *TransactionEntryMethod) RequiredRole() types.Role { return types.RoleUser }

func (m *TransactionEntryMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	var request struct {
		TxHash string `json:"tx_hash"`
		types.LedgerSpecifier
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if request.TxHash == "" {
		return nil, types.RpcErrorFieldNotFoundTransaction()
	}

	// Resolve the target ledger through the shared lookup (rippled
	// RPC::lookupLedger). transaction_entry does not search the open ledger, so
	// an open (current) target is refused with notYetImplemented before the tx
	// is looked up (TransactionEntry.cpp:50-56, "We don't work on ledger
	// current").
	targetLedger, _, lerr := LookupLedger(ctx, request.LedgerSpecifier)
	if lerr != nil {
		return nil, lerr
	}
	if !targetLedger.IsClosed() {
		return nil, types.RpcErrorNotYetImplemented()
	}

	// Parse the transaction hash
	txHashBytes, err := hex.DecodeString(request.TxHash)
	if err != nil || len(txHashBytes) != 32 {
		return nil, types.RpcErrorInvalidParams("Invalid tx_hash")
	}

	var txHash [32]byte
	copy(txHash[:], txHashBytes)

	// Look up the transaction and verify it is in the requested ledger.
	txInfo, err := ctx.Services.Ledger.GetTransaction(txHash)
	if err != nil || txInfo == nil {
		return nil, types.RpcErrorTransactionNotFound("Transaction not found.")
	}
	targetSeq := targetLedger.Sequence()
	if txInfo.LedgerIndex != targetSeq {
		return nil, types.RpcErrorTransactionNotFound(fmt.Sprintf("Transaction not found in ledger %d", targetSeq))
	}

	// Parse the stored transaction data (VL-encoded binary or JSON)
	storedTx, err := decodeTxBlob(txInfo.TxData)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to parse transaction data")
	}

	ledgerHash := txInfo.LedgerHash
	if ledgerHash == "" {
		h := targetLedger.Hash()
		ledgerHash = fmt.Sprintf("%X", h)
	}

	// Inject DeliveredAmount for Payment transactions
	if storedTx.Meta != nil {
		InjectDeliveredAmount(storedTx.TxJSON, storedTx.Meta)
	}

	response := map[string]any{
		"tx_json": storedTx.TxJSON,
	}

	// Metadata key: "meta" for v2+, "metadata" for v1
	if ctx.ApiVersion > 1 {
		response["meta"] = storedTx.Meta
	} else {
		response["metadata"] = storedTx.Meta
	}

	if ctx.ApiVersion > 1 {
		// v2: hash at root, conditional ledger_hash/ledger_index/close_time_iso
		response["hash"] = strings.ToUpper(request.TxHash)
		response["validated"] = txInfo.Validated

		if ledgerHash != "" {
			response["ledger_hash"] = ledgerHash
		}
		if txInfo.Validated {
			response["ledger_index"] = txInfo.LedgerIndex
			closeTimeSec := targetLedger.CloseTime()
			if closeTimeSec > 0 {
				closeTime := rippleEpochTime.Add(secondsToDuration(closeTimeSec))
				response["close_time_iso"] = closeTime.UTC().Format("2006-01-02T15:04:05Z")
			}
		}
	} else {
		// v1: always include ledger_index, ledger_hash, and validated
		response["ledger_index"] = txInfo.LedgerIndex
		response["ledger_hash"] = ledgerHash
		response["validated"] = txInfo.Validated
	}

	return response, nil
}
