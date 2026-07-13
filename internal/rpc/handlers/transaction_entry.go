package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

// TransactionEntryMethod handles the transaction_entry RPC method.
// Retrieves a transaction from a specific ledger version.
// Unlike the 'tx' method which searches across the ledger range,
// this method requires a specific ledger to search in.
// Reference: rippled TransactionEntry.cpp
type TransactionEntryMethod struct{ BaseHandler }

func (m *TransactionEntryMethod) RequiredRole() types.Role { return types.RoleUser }

func (m *TransactionEntryMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	targetLedger, validated, lerr := LookupLedger(ctx, parsedLedgerSpec)
	if lerr != nil {
		return nil, lerr
	}
	fields := make(map[string]json.RawMessage)
	if len(params) != 0 {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
	}
	lookupExtra := make(map[string]any)
	if targetLedger.IsClosed() {
		lookupExtra["ledger_hash"] = FormatLedgerHash(targetLedger.Hash())
		lookupExtra["ledger_index"] = targetLedger.Sequence()
	} else {
		lookupExtra["ledger_current_index"] = targetLedger.Sequence()
	}
	lookupExtra["validated"] = validated
	txHashRaw, hasTxHash := fields["tx_hash"]
	if !hasTxHash {
		return nil, types.RPCErrorFieldNotFoundTransaction().WithExtra(lookupExtra)
	}
	if !targetLedger.IsClosed() {
		return nil, types.RPCErrorNotYetImplemented().WithExtra(lookupExtra)
	}

	var txHashString string
	_ = json.Unmarshal(txHashRaw, &txHashString)
	var txHash [32]byte
	if txHashString != "0" {
		txHashBytes, err := hex.DecodeString(txHashString)
		if err != nil || len(txHashBytes) != 32 {
			return nil, types.RPCErrorMalformedRequestBare().WithExtra(lookupExtra)
		}
		copy(txHash[:], txHashBytes)
	}

	var txInfo *types.TransactionInfo
	var lookupErr error
	if source, ok := targetLedger.(types.LedgerTransactionSource); ok {
		txData, found, readErr := source.GetLedgerTransaction(txHash)
		if readErr != nil {
			return nil, types.RPCErrorInternal("Failed to read transaction data")
		}
		if found {
			txIndex, hasIndex := txcore.TransactionIndexFromTxWithMetaBlob(txData)
			if !hasIndex {
				txIndex = ^uint32(0)
			}
			txInfo = &types.TransactionInfo{
				TxData:      txData,
				LedgerIndex: targetLedger.Sequence(),
				LedgerHash:  FormatLedgerHash(targetLedger.Hash()),
				Validated:   validated,
				TxIndex:     txIndex,
			}
		}
	} else {
		txInfo, lookupErr = ctx.Services.Ledger.GetTransaction(txHash)
	}
	if lookupErr != nil || txInfo == nil {
		return nil, types.RPCErrorTransactionNotFound("Transaction not found.").WithExtra(lookupExtra)
	}
	targetSeq := targetLedger.Sequence()
	if txInfo.LedgerIndex != targetSeq {
		return nil, types.RPCErrorTransactionNotFound(fmt.Sprintf("Transaction not found in ledger %d", targetSeq)).WithExtra(lookupExtra)
	}

	// Parse the stored transaction data (VL-encoded binary or JSON)
	storedTx, err := decodeTxBlob(txInfo.TxData)
	if err != nil {
		return nil, types.RPCErrorInternal("Failed to parse transaction data")
	}

	ledgerHash := FormatLedgerHash(targetLedger.Hash())

	response := maps.Clone(lookupExtra)
	response["tx_json"] = projectTransactionJSON(storedTx.TxJSON, strings.ToUpper(txHashString), ctx.ApiVersion)

	// Metadata key: "meta" for v2+, "metadata" for v1
	if ctx.ApiVersion > 1 {
		response["meta"] = storedTx.Meta
	} else {
		response["metadata"] = storedTx.Meta
	}

	if ctx.ApiVersion > 1 {
		// v2: hash at root, conditional ledger_hash/ledger_index/close_time_iso
		response["hash"] = strings.ToUpper(txHashString)
		response["validated"] = validated

		if ledgerHash != "" {
			response["ledger_hash"] = ledgerHash
		}
		if validated {
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
