package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/txprojection"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TransactionEntryMethod handles the transaction_entry RPC method.
type TransactionEntryMethod struct{ baseHandler }

func (m *TransactionEntryMethod) RequiredRole() types.Role { return types.RoleUser }

func (m *TransactionEntryMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	targetLedger, validated, lerr := lookupLedger(ctx, parsedLedgerSpec)
	if lerr != nil {
		return nil, lerr
	}
	fields := make(map[string]json.RawMessage)
	if len(params) != 0 {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
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
		return nil, types.RpcErrorFieldNotFoundTransaction().WithExtra(lookupExtra)
	}
	if !targetLedger.IsClosed() {
		return nil, types.RpcErrorNotYetImplemented().WithExtra(lookupExtra)
	}

	var txHashString string
	_ = json.Unmarshal(txHashRaw, &txHashString)
	var txHash [32]byte
	if txHashString != "0" {
		txHashBytes, err := hex.DecodeString(txHashString)
		if err != nil || len(txHashBytes) != 32 {
			return nil, types.RpcErrorMalformedRequestBare().WithExtra(lookupExtra)
		}
		copy(txHash[:], txHashBytes)
	}

	var txInfo *types.TransactionInfo
	var lookupErr error
	var txData []byte
	var found bool
	var readErr error
	hasSource := false
	if source, ok := targetLedger.(types.ContextLedgerTransactionSource); ok {
		hasSource = true
		txData, found, readErr = source.GetLedgerTransactionContext(ctx.Context, txHash)
	} else if source, ok := targetLedger.(types.LedgerTransactionSource); ok {
		hasSource = true
		txData, found, readErr = source.GetLedgerTransaction(txHash)
	}
	if readErr != nil {
		return nil, rpcInternalError("transaction_entry: transaction read failed", readErr)
	}
	if hasSource {
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
		return nil, types.RpcErrorTransactionNotFound("Transaction not found.").WithExtra(lookupExtra)
	}
	targetSeq := targetLedger.Sequence()
	if txInfo.LedgerIndex != targetSeq {
		return nil, types.RpcErrorTransactionNotFound(fmt.Sprintf("Transaction not found in ledger %d", targetSeq)).WithExtra(lookupExtra)
	}

	// Parse the stored transaction data (VL-encoded binary or JSON)
	storedTx, err := decodeTxBlobForTransactionEntry(txInfo.TxData)
	if err != nil {
		return nil, rpcInternalError("transaction_entry: transaction decoding failed", err)
	}

	ledgerHash := FormatLedgerHash(targetLedger.Hash())
	hashString := strings.ToUpper(txHashString)

	response := map[string]any{
		"tx_json": txprojection.ProjectJSON(storedTx.TxJSON, hashString, ctx.ApiVersion),
	}

	if storedTx.Meta != nil {
		if ctx.ApiVersion > 1 {
			response["meta"] = storedTx.Meta
		} else {
			response["metadata"] = storedTx.Meta
		}
	}

	if ctx.ApiVersion > 1 {
		response["hash"] = hashString
		response["validated"] = validated

		if ledgerHash != "" {
			response["ledger_hash"] = ledgerHash
		}
		if validated {
			response["ledger_index"] = txInfo.LedgerIndex
			closeTimeSec := targetLedger.CloseTime()
			if closeTimeSec > 0 {
				response["close_time_iso"] = protocol.FormatCloseTimeISO(protocol.FromRippleTime(uint32(closeTimeSec)))
			}
		}
	} else {
		response["ledger_index"] = txInfo.LedgerIndex
		response["ledger_hash"] = ledgerHash
		response["validated"] = txInfo.Validated
	}

	return response, nil
}
