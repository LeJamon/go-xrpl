package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TransactionEntryMethod handles the transaction_entry RPC method.
type TransactionEntryMethod struct{ BaseHandler }

func (m *TransactionEntryMethod) RequiredRole() types.Role { return types.RoleUser }

func (m *TransactionEntryMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	targetLedger, _, lerr := LookupLedger(ctx, params)
	if lerr != nil {
		return nil, lerr
	}

	var fields map[string]json.RawMessage
	if len(params) > 0 && !isJSONNull(params) {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
	}
	rawTxHash, hasTxHash := fields["tx_hash"]
	if !hasTxHash {
		return nil, types.RPCErrorFieldNotFoundTransaction()
	}
	if !targetLedger.IsClosed() {
		return nil, types.RPCErrorNotYetImplemented()
	}

	var txHashString string
	_ = json.Unmarshal(rawTxHash, &txHashString)

	txHashBytes, err := hex.DecodeString(txHashString)
	if err != nil || len(txHashBytes) != 32 {
		return nil, types.RPCErrorMalformedRequestBare()
	}

	var txHash [32]byte
	copy(txHash[:], txHashBytes)

	var txData []byte
	if err := targetLedger.ForEachTransaction(func(candidate [32]byte, data []byte) bool {
		if candidate != txHash {
			return true
		}
		txData = append([]byte(nil), data...)
		return false
	}); err != nil {
		return nil, types.RPCErrorInternal("Failed to read ledger transactions")
	}
	if txData == nil {
		return nil, types.RPCErrorTransactionNotFound("Transaction not found.")
	}

	storedTx, err := decodeTxBlob(txData)
	if err != nil {
		return nil, types.RPCErrorInternal("Failed to parse transaction data")
	}

	ledgerHash := fmt.Sprintf("%X", targetLedger.Hash())
	ledgerIndex := targetLedger.Sequence()
	validated := targetLedger.IsValidated()

	injectDeliverMax(storedTx.TxJSON, ctx.ApiVersion)

	response := map[string]any{
		"tx_json": storedTx.TxJSON,
	}

	if ctx.ApiVersion > 1 {
		response["meta"] = storedTx.Meta
	} else {
		response["metadata"] = storedTx.Meta
	}

	if ctx.ApiVersion > 1 {
		response["hash"] = strings.ToUpper(txHashString)
		response["validated"] = validated

		if ledgerHash != "" {
			response["ledger_hash"] = ledgerHash
		}
		if validated {
			response["ledger_index"] = ledgerIndex
			closeTimeSec := targetLedger.CloseTime()
			if closeTimeSec > 0 {
				response["close_time_iso"] = protocol.FormatCloseTimeISO(protocol.FromRippleTime(uint32(closeTimeSec)))
			}
		}
	} else {
		storedTx.TxJSON["hash"] = strings.ToUpper(txHashString)
		response["ledger_index"] = ledgerIndex
		response["ledger_hash"] = ledgerHash
		response["validated"] = validated
	}

	return response, nil
}
