package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// TxMethod handles the tx RPC method
type TxMethod struct{}

func (m *TxMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	var request struct {
		types.TransactionParam
		Binary    bool    `json:"binary,omitempty"`
		MinLedger *uint32 `json:"min_ledger,omitempty"`
		MaxLedger *uint32 `json:"max_ledger,omitempty"`
		CTID      string  `json:"ctid,omitempty"`
	}

	// notEnabled takes precedence over any parameter validation, matching
	// rippled's useTxTables() gate as the first statement of doTxJson.
	if err := RequireTxTables(ctx.Services); err != nil {
		return nil, err
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// Specifying both transaction and ctid is ambiguous and rejected,
	// matching rippled doTxJson.
	if request.Transaction != "" && request.CTID != "" {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}

	// CTID lookup support
	if request.CTID != "" && request.Transaction == "" {
		ctidLedgerSeq, ctidTxIndex, ctidNetworkID, err := parseCTID(request.CTID)
		if err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid ctid: %v", err))
		}
		// The CTID embeds a network id; reject the request when it does not match
		// this node's network (Tx.cpp:313-321).
		if nodeNet := ctx.Services.Ledger.GetServerInfo().NetworkID; uint32(ctidNetworkID) != nodeNet {
			return nil, types.RpcErrorWrongNetwork(fmt.Sprintf(
				"Wrong network. You should submit this request to a node running on NetworkID: %d", ctidNetworkID))
		}
		return m.lookupByCTID(ctx, ctidLedgerSeq, ctidTxIndex, request.Binary)
	}

	if request.Transaction == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: transaction")
	}

	// A search range is formed only when both min_ledger and max_ledger are
	// present (a partial range is ignored, so a present 0 is a real bound, not
	// "absent"); when both are given the range must be ordered and span at most
	// 1000 ledgers (Tx.cpp:330-344, doTxHelp:75-93).
	if request.MinLedger != nil && request.MaxLedger != nil {
		minLedger, maxLedger := *request.MinLedger, *request.MaxLedger
		if maxLedger < minLedger {
			return nil, types.RpcErrorInvalidLgrRange()
		}
		if maxLedger-minLedger > 1000 {
			return nil, types.RpcErrorExcessiveLgrRange()
		}
	}

	// Parse the transaction hash
	txHashBytes, err := hex.DecodeString(request.Transaction)
	if err != nil || len(txHashBytes) != 32 {
		return nil, types.RpcErrorNotImpl()
	}

	var txHash [32]byte
	copy(txHash[:], txHashBytes)

	// Look up the transaction
	txInfo, err := ctx.Services.Ledger.GetTransaction(txHash)
	if err != nil {
		return nil, types.RpcErrorTxnNotFound("Transaction not found")
	}
	storedTx, err := decodeTxBlob(txInfo.TxData)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to decode transaction data")
	}

	// Resolve close time from the containing ledger
	var closeTimeSec int64
	if txInfo.LedgerIndex > 0 {
		if ledger, err := ctx.Services.Ledger.GetLedgerBySequence(txInfo.LedgerIndex); err == nil {
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
) map[string]any {
	if ctx.ApiVersion > 1 {
		netID := uint16(ctx.Services.Ledger.GetServerInfo().NetworkID)
		return m.buildResponseV2(storedTx, txInfo, hashStr, closeTimeSec, binary, netID)
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
) map[string]any {
	response := map[string]any{}

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
		maps.Copy(response, storedTx.TxJSON)
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
	networkID uint16,
) map[string]any {
	response := map[string]any{}

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
		txJSON := make(map[string]any, len(storedTx.TxJSON)+3)
		maps.Copy(txJSON, storedTx.TxJSON)
		// date and ledger_index go inside tx_json for v2
		txJSON["ledger_index"] = txInfo.LedgerIndex
		if closeTimeSec > 0 {
			txJSON["date"] = closeTimeSec
		}
		if txInfo.LedgerIndex > 0 && txInfo.TxIndex <= 0xFFFF && txInfo.LedgerIndex < 0x0FFFFFFF {
			txJSON["ctid"] = encodeCTIDWithNetworkID(txInfo.LedgerIndex, uint16(txInfo.TxIndex), networkID)
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
func (m *TxMethod) lookupByCTID(ctx *types.RpcContext, ledgerSeq uint32, txIndex uint16, binary bool) (any, *types.RpcError) {
	ledger, err := ctx.Services.Ledger.GetLedgerBySequence(ledgerSeq)
	if err != nil {
		return nil, types.RpcErrorTxnNotFound("Transaction not found (ledger not available)")
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
		return nil, types.RpcErrorTxnNotFound("Transaction not found at specified index")
	}

	hashStr := strings.ToUpper(hex.EncodeToString(foundHash[:]))
	validated := ledger.IsValidated()
	closeTimeSec := ledger.CloseTime()
	ledgerHashStr := fmt.Sprintf("%X", ledger.Hash())

	storedTx, decodeErr := decodeTxBlob(foundData)

	return m.ctidResponse(ctx, storedTx, decodeErr, foundData, hashStr, ledgerSeq, txIndex, closeTimeSec, validated, ledgerHashStr, binary), nil
}

// ctidResponse shapes a CTID-lookup response by reusing buildResponse and then
// applying the CTID-specific deltas: the root "ctid" (v1) / in-tx_json "ctid"
// (v2) is grafted by buildResponse already; here we keep the root ledger fields
// unconditionally present (a fetched closed ledger is always available even if
// not yet validated), drop the v1 binary "inLedger"/"date" that the CTID format
// omits, and preserve the raw-hex tx_blob fallback used when the stored blob
// cannot be decoded.
func (m *TxMethod) ctidResponse(
	ctx *types.RpcContext,
	storedTx StoredTransaction,
	decodeErr error,
	foundData []byte,
	hashStr string,
	ledgerSeq uint32,
	txIndex uint16,
	closeTimeSec int64,
	validated bool,
	ledgerHashStr string,
	binary bool,
) map[string]any {
	txInfo := &types.TransactionInfo{
		LedgerIndex: ledgerSeq,
		LedgerHash:  ledgerHashStr,
		Validated:   validated,
		TxIndex:     uint32(txIndex),
	}

	tx := storedTx
	if decodeErr != nil {
		tx = StoredTransaction{}
	}
	networkID := uint16(ctx.Services.Ledger.GetServerInfo().NetworkID)
	response := m.buildResponse(ctx, tx, txInfo, hashStr, closeTimeSec, binary)

	// The CTID format reports the containing ledger unconditionally, whereas
	// buildResponseV2 gates these on validated. ledger_hash is always set by
	// buildResponse; ledger_index and close_time_iso may be missing for an
	// unvalidated ledger.
	response["ledger_index"] = ledgerSeq
	if closeTimeSec > 0 {
		closeTime := rippleEpochTime.Add(secondsToDuration(closeTimeSec))
		response["close_time_iso"] = closeTime.UTC().Format("2006-01-02T15:04:05Z")
	}

	if binary {
		// buildResponseV1's "inLedger"/"date" and an empty Encode(nil) tx_blob
		// have no CTID equivalent; on a decode failure CTID emits the raw blob.
		delete(response, "inLedger")
		delete(response, "date")
		if decodeErr != nil {
			response["tx_blob"] = strings.ToUpper(hex.EncodeToString(foundData))
		}
		return response
	}

	if ctx.ApiVersion > 1 {
		if decodeErr != nil {
			// On a decode failure the CTID format emits no tx_json at all,
			// whereas buildResponseV2 always wraps one.
			delete(response, "tx_json")
			return response
		}
		if txJSON, ok := response["tx_json"].(map[string]any); ok {
			// buildResponseV2 omits the ctid for ledger 0; the CTID lookup
			// still reports it.
			if ledgerSeq < 0x0FFFFFFF {
				txJSON["ctid"] = encodeCTIDWithNetworkID(ledgerSeq, txIndex, networkID)
			}
		}
		return response
	}

	// API v1 reports the CTID at the root; buildResponseV1 does not add it.
	response["ctid"] = encodeCTIDWithNetworkID(ledgerSeq, txIndex, networkID)
	return response
}

// parseCTID decodes a CTID hex string to ledger sequence and tx index.
// CTID format (64 bits): [63:60]=0xC marker, [59:32]=ledger_seq (28 bits),
// [31:16]=tx_index (16 bits), [15:0]=network_id (16 bits).
func parseCTID(ctid string) (ledgerSeq uint32, txIndex uint16, networkID uint16, err error) {
	if len(ctid) != 16 {
		return 0, 0, 0, fmt.Errorf("CTID must be 16 hex characters")
	}
	ctidBytes, decErr := hex.DecodeString(ctid)
	if decErr != nil || len(ctidBytes) != 8 {
		return 0, 0, 0, fmt.Errorf("invalid CTID hex")
	}

	// Validate marker nibble (high 4 bits should be 0xC)
	if ctidBytes[0]>>4 != 0xC {
		return 0, 0, 0, fmt.Errorf("invalid CTID marker")
	}

	val := uint64(0)
	for _, b := range ctidBytes {
		val = (val << 8) | uint64(b)
	}

	// Extract components per CTID spec
	ledgerSeq = uint32((val >> 32) & 0x0FFFFFFF)
	txIndex = uint16((val >> 16) & 0xFFFF)
	networkID = uint16(val & 0xFFFF)

	return ledgerSeq, txIndex, networkID, nil
}

// secondsToDuration converts ripple epoch seconds to a time.Duration
func secondsToDuration(secs int64) time.Duration {
	return time.Duration(secs) * time.Second
}

// StoredTransaction represents a transaction stored in the ledger
type StoredTransaction struct {
	TxJSON map[string]any `json:"tx_json"`
	Meta   map[string]any `json:"meta"`
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
