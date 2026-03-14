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

// TxMethod handles the tx RPC method
type TxMethod struct{}

func (m *TxMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.TransactionParam
		Binary    bool   `json:"binary,omitempty"`
		MinLedger uint32 `json:"min_ledger,omitempty"`
		MaxLedger uint32 `json:"max_ledger,omitempty"`
		CTID      string `json:"ctid,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// CTID lookup support
	if request.CTID != "" && request.Transaction == "" {
		ctidLedgerSeq, ctidTxIndex, err := parseCTID(request.CTID)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid ctid: " + err.Error())
		}
		return m.lookupByCTID(ctx, ctidLedgerSeq, ctidTxIndex, request.Binary)
	}

	if request.Transaction == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: transaction")
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
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
	var storedTx StoredTransaction
	if err := json.Unmarshal(txInfo.TxData, &storedTx); err != nil {
		return nil, types.RpcErrorInternal("Failed to parse transaction data")
	}

	// Resolve close time from the containing ledger
	var closeTimeSec int64
	if txInfo.LedgerIndex > 0 {
		if ledger, err := types.Services.Ledger.GetLedgerBySequence(txInfo.LedgerIndex); err == nil {
			closeTimeSec = ledger.CloseTime()
		}
	}

	return m.buildResponse(ctx, storedTx, txInfo, strings.ToUpper(request.Transaction), closeTimeSec, request.Binary), nil
}

// buildResponse constructs the tx response, choosing v1 or v2 format based on ctx.ApiVersion.
func (m *TxMethod) buildResponse(
	ctx *types.RpcContext,
	storedTx StoredTransaction,
	txInfo *types.TransactionInfo,
	hashStr string,
	closeTimeSec int64,
	binary bool,
) map[string]interface{} {
	if ctx.ApiVersion > 1 {
		return m.buildResponseV2(storedTx, txInfo, hashStr, closeTimeSec, binary)
	}
	return m.buildResponseV1(storedTx, txInfo, hashStr, closeTimeSec, binary)
}

// buildResponseV1 builds the legacy (API v1) response with flat tx fields on root.
func (m *TxMethod) buildResponseV1(
	storedTx StoredTransaction,
	txInfo *types.TransactionInfo,
	hashStr string,
	closeTimeSec int64,
	binary bool,
) map[string]interface{} {
	response := map[string]interface{}{}

	if binary {
		txBlob, err := binarycodec.Encode(storedTx.TxJSON)
		if err == nil {
			response["tx_blob"] = txBlob
		}
		if storedTx.Meta != nil {
			metaBlob, err := binarycodec.Encode(storedTx.Meta)
			if err == nil {
				response["meta"] = metaBlob
			}
		}
	} else {
		// Spread transaction fields flat on root
		for k, v := range storedTx.TxJSON {
			response[k] = v
		}
		if storedTx.Meta != nil {
			InjectDeliveredAmount(storedTx.TxJSON, storedTx.Meta)
			response["meta"] = storedTx.Meta
		}
	}

	response["hash"] = hashStr
	response["inLedger"] = txInfo.LedgerIndex
	response["ledger_index"] = txInfo.LedgerIndex
	response["ledger_hash"] = txInfo.LedgerHash
	response["validated"] = txInfo.Validated

	if closeTimeSec > 0 {
		closeTime := rippleEpochTime.Add(secondsToDuration(closeTimeSec))
		response["close_time_iso"] = closeTime.UTC().Format("2006-01-02T15:04:05Z")
		response["date"] = closeTimeSec
	}

	return response
}

// buildResponseV2 builds the API v2 response with tx_json wrapper and structured fields.
func (m *TxMethod) buildResponseV2(
	storedTx StoredTransaction,
	txInfo *types.TransactionInfo,
	hashStr string,
	closeTimeSec int64,
	binary bool,
) map[string]interface{} {
	response := map[string]interface{}{}

	if binary {
		txBlob, err := binarycodec.Encode(storedTx.TxJSON)
		if err == nil {
			response["tx_blob"] = txBlob
		}
		if storedTx.Meta != nil {
			metaBlob, err := binarycodec.Encode(storedTx.Meta)
			if err == nil {
				response["meta_blob"] = metaBlob
			}
		}
	} else {
		// Wrap transaction fields in tx_json
		txJSON := make(map[string]interface{}, len(storedTx.TxJSON)+3)
		for k, v := range storedTx.TxJSON {
			txJSON[k] = v
		}
		// date and ledger_index go inside tx_json for v2
		txJSON["ledger_index"] = txInfo.LedgerIndex
		if closeTimeSec > 0 {
			txJSON["date"] = closeTimeSec
		}
		// Add CTID inside tx_json
		if txInfo.LedgerIndex > 0 && txInfo.TxIndex <= 0xFFFF && txInfo.LedgerIndex < 0x0FFFFFFF {
			txJSON["ctid"] = encodeCTID(txInfo.LedgerIndex, uint16(txInfo.TxIndex))
		}
		response["tx_json"] = txJSON

		if storedTx.Meta != nil {
			InjectDeliveredAmount(storedTx.TxJSON, storedTx.Meta)
			response["meta"] = storedTx.Meta
		}
	}

	// Root-level fields
	response["hash"] = hashStr
	response["validated"] = txInfo.Validated

	if txInfo.LedgerHash != "" {
		response["ledger_hash"] = txInfo.LedgerHash
	}
	// ledger_index and close_time_iso only at root for validated txs
	if txInfo.Validated {
		response["ledger_index"] = txInfo.LedgerIndex
		if closeTimeSec > 0 {
			closeTime := rippleEpochTime.Add(secondsToDuration(closeTimeSec))
			response["close_time_iso"] = closeTime.UTC().Format("2006-01-02T15:04:05Z")
		}
	}

	return response
}

// lookupByCTID looks up a transaction using a CTID (Compact Transaction ID)
func (m *TxMethod) lookupByCTID(ctx *types.RpcContext, ledgerSeq uint32, txIndex uint16, binary bool) (interface{}, *types.RpcError) {
	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	ledger, err := types.Services.Ledger.GetLedgerBySequence(ledgerSeq)
	if err != nil {
		return nil, &types.RpcError{
			Code:        -1,
			ErrorString: "txnNotFound",
			Message:     "Transaction not found (ledger not available)",
		}
	}

	// Iterate transactions to find the one at the given index
	var foundHash [32]byte
	var foundData []byte
	var currentIdx uint16
	var found bool

	ledger.ForEachTransaction(func(txHash [32]byte, txData []byte) bool {
		if currentIdx == txIndex {
			foundHash = txHash
			foundData = make([]byte, len(txData))
			copy(foundData, txData)
			found = true
			return false // stop iteration
		}
		currentIdx++
		return true
	})

	if !found {
		return nil, &types.RpcError{
			Code:        -1,
			ErrorString: "txnNotFound",
			Message:     "Transaction not found at specified index",
		}
	}

	hashStr := strings.ToUpper(hex.EncodeToString(foundHash[:]))
	validated := ledger.IsValidated()
	closeTimeSec := ledger.CloseTime()
	ledgerHash := ledger.Hash()
	ledgerHashStr := strings.ToUpper(fmt.Sprintf("%x", ledgerHash))

	if binary {
		response := map[string]interface{}{}
		response["tx_blob"] = strings.ToUpper(hex.EncodeToString(foundData))
		if ctx.ApiVersion > 1 {
			response["meta_blob"] = "" // no separate metadata in raw CTID lookup binary path
		}
		response["hash"] = hashStr
		response["ledger_index"] = ledgerSeq
		response["ledger_hash"] = ledgerHashStr
		response["validated"] = validated
		if closeTimeSec > 0 {
			closeTime := rippleEpochTime.Add(secondsToDuration(closeTimeSec))
			response["close_time_iso"] = closeTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		return response, nil
	}

	// Decode the raw transaction blob to JSON
	txHex := hex.EncodeToString(foundData)
	decoded, decodeErr := binarycodec.Decode(txHex)

	if ctx.ApiVersion > 1 {
		response := map[string]interface{}{}
		if decodeErr == nil {
			txJSON := decoded
			if closeTimeSec > 0 {
				txJSON["date"] = closeTimeSec
			}
			if ledgerSeq < 0x0FFFFFFF && txIndex <= 0xFFFF {
				txJSON["ctid"] = encodeCTID(ledgerSeq, txIndex)
			}
			response["tx_json"] = txJSON
		}
		response["hash"] = hashStr
		response["ledger_index"] = ledgerSeq
		response["ledger_hash"] = ledgerHashStr
		response["validated"] = validated
		if closeTimeSec > 0 {
			closeTime := rippleEpochTime.Add(secondsToDuration(closeTimeSec))
			response["close_time_iso"] = closeTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		return response, nil
	}

	// API v1 format: flat fields on root
	response := map[string]interface{}{
		"hash":         hashStr,
		"ledger_index": ledgerSeq,
		"inLedger":     ledgerSeq,
		"validated":    validated,
		"ledger_hash":  ledgerHashStr,
	}
	if decodeErr == nil {
		for k, v := range decoded {
			response[k] = v
		}
	}
	if closeTimeSec > 0 {
		closeTime := rippleEpochTime.Add(secondsToDuration(closeTimeSec))
		response["close_time_iso"] = closeTime.UTC().Format("2006-01-02T15:04:05Z")
		response["date"] = closeTimeSec
	}
	// Add CTID to v1 response at root level
	response["ctid"] = encodeCTID(ledgerSeq, txIndex)

	return response, nil
}

// parseCTID decodes a CTID hex string to ledger sequence and tx index.
// CTID format (64 bits): [63:60]=0xC marker, [59:32]=ledger_seq (28 bits),
// [31:16]=tx_index (16 bits), [15:0]=network_id (16 bits).
func parseCTID(ctid string) (uint32, uint16, error) {
	if len(ctid) != 16 {
		return 0, 0, fmt.Errorf("CTID must be 16 hex characters")
	}
	ctidBytes, err := hex.DecodeString(ctid)
	if err != nil || len(ctidBytes) != 8 {
		return 0, 0, fmt.Errorf("invalid CTID hex")
	}

	// Validate marker nibble (high 4 bits should be 0xC)
	if ctidBytes[0]>>4 != 0xC {
		return 0, 0, fmt.Errorf("invalid CTID marker")
	}

	val := uint64(0)
	for _, b := range ctidBytes {
		val = (val << 8) | uint64(b)
	}

	// Extract components per CTID spec
	ledgerSeq := uint32((val >> 32) & 0x0FFFFFFF)
	txIndex := uint16((val >> 16) & 0xFFFF)
	// networkID := uint16(val & 0xFFFF) // ignored for now

	return ledgerSeq, txIndex, nil
}

// encodeCTID encodes ledger sequence and tx index into a CTID hex string.
// Uses network_id = 0.
// CTID format (64 bits): [63:60]=0xC marker, [59:32]=ledger_seq (28 bits),
// [31:16]=tx_index (16 bits), [15:0]=network_id (16 bits).
func encodeCTID(ledgerSeq uint32, txIndex uint16) string {
	val := uint64(0xC)<<60 |
		uint64(ledgerSeq)<<32 |
		uint64(txIndex)<<16 |
		uint64(0) // network_id = 0
	return fmt.Sprintf("%016X", val)
}

// secondsToDuration converts ripple epoch seconds to a time.Duration
func secondsToDuration(secs int64) time.Duration {
	return time.Duration(secs) * time.Second
}

// StoredTransaction represents a transaction stored in the ledger
type StoredTransaction struct {
	TxJSON map[string]interface{} `json:"tx_json"`
	Meta   map[string]interface{} `json:"meta"`
}

func (m *TxMethod) RequiredRole() types.Role {
	return types.RoleUser
}

func (m *TxMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *TxMethod) RequiredCondition() types.Condition {
	return types.NeedsNetworkConnection
}
