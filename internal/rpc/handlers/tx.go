package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

// TxMethod handles the tx RPC method
type TxMethod struct{}

func (m *TxMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	// notEnabled takes precedence over any parameter validation, matching
	// rippled's useTxTables() gate as the first statement of doTxJson.
	if err := RequireTxTables(ctx.Services); err != nil {
		return nil, err
	}

	var fields map[string]json.RawMessage
	if params != nil {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
	}
	transactionRaw, hasTransaction := fields["transaction"]
	ctidRaw, hasCTID := fields["ctid"]
	if hasTransaction && hasCTID {
		return nil, types.RPCErrorInvalidParams("Invalid parameters.")
	}

	var transaction, ctid string
	var valid bool
	switch {
	case hasTransaction:
		transaction, valid = jsonCppStringRaw(transactionRaw)
		if !valid {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
	case hasCTID:
		ctid, valid = jsonCppStringRaw(ctidRaw)
		if !valid {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
	default:
		return nil, types.RPCErrorInvalidParams("Invalid parameters.")
	}

	var txHash [32]byte
	var ctidLedgerSeq uint32
	var ctidTxIndex uint16
	if hasCTID {
		var ctidNetworkID uint16
		var err error
		ctidLedgerSeq, ctidTxIndex, ctidNetworkID, err = parseCTID(ctid)
		if err != nil {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
		if nodeNet := ctx.Services.Ledger.GetServerInfo().NetworkID; uint32(ctidNetworkID) != nodeNet {
			return nil, types.RPCErrorWrongNetwork(fmt.Sprintf(
				"Wrong network. You should submit this request to a node running on NetworkID: %d", ctidNetworkID))
		}
	} else {
		txHashBytes, err := hex.DecodeString(transaction)
		if err != nil || len(txHashBytes) != 32 {
			return nil, types.RPCErrorNotImpl()
		}
		copy(txHash[:], txHashBytes)
	}

	binaryMode := false
	if binaryRaw, ok := fields["binary"]; ok {
		binaryMode = jsonCppBoolRaw(binaryRaw)
	}

	var rangeMin, rangeMax uint32
	hasLedgerRange := false
	if minRaw, hasMin := fields["min_ledger"]; hasMin {
		if maxRaw, hasMax := fields["max_ledger"]; hasMax {
			minLedger, minOK := txUint32Raw(minRaw)
			maxLedger, maxOK := txUint32Raw(maxRaw)
			if !minOK || !maxOK || maxLedger < minLedger {
				return nil, types.RPCErrorInvalidLgrRange()
			}
			if maxLedger-minLedger > 1000 {
				return nil, types.RPCErrorExcessiveLgrRange()
			}
			rangeMin, rangeMax, hasLedgerRange = minLedger, maxLedger, true
		}
	}

	if hasCTID {
		return m.lookupByCTID(ctx, ctidLedgerSeq, ctidTxIndex, binaryMode)
	}

	var txInfo *types.TransactionInfo
	var searched types.TxSearchResult
	var err error
	if hasLedgerRange {
		if ranged, ok := ctx.Services.Ledger.(types.RangedTransactionLookup); ok {
			txInfo, searched, err = ranged.GetTransactionWithRange(ctx.Context, txHash, rangeMin, rangeMax)
		} else {
			txInfo, err = ctx.Services.Ledger.GetTransaction(txHash)
		}
	} else {
		txInfo, err = ctx.Services.Ledger.GetTransaction(txHash)
	}
	if err != nil && !errors.Is(err, svcerr.ErrTxnNotFound) {
		return nil, types.RPCErrorInternal("Internal error.")
	}
	if err != nil || txInfo == nil {
		return nil, txNotFoundForSearch(hasLedgerRange, searched)
	}
	storedTx, err := decodeTxBlob(txInfo.TxData)
	if err != nil {
		return nil, types.RPCErrorInternal("Failed to decode transaction data")
	}

	// Resolve close time from the containing ledger
	var closeTimeSec int64
	if txInfo.LedgerIndex > 0 {
		if ledger, err := ctx.Services.Ledger.GetLedgerBySequence(txInfo.LedgerIndex); err == nil {
			closeTimeSec = ledger.CloseTime()
		}
	}

	return m.buildResponse(ctx, storedTx, txInfo, strings.ToUpper(transaction), closeTimeSec, binaryMode), nil
}

func txNotFoundForSearch(hasRange bool, searched types.TxSearchResult) *types.RPCError {
	err := types.RPCErrorTxnNotFound("Transaction not found.")
	if !hasRange || searched == types.TxSearchUnknown {
		return err
	}
	return err.WithExtra(map[string]any{
		"searched_all": searched == types.TxSearchAll,
	})
}

func txUint32Raw(raw json.RawMessage) (uint32, bool) {
	value, err := decodeRawJSONValue(raw)
	if err != nil {
		return 0, false
	}
	switch value := value.(type) {
	case nil:
		return 0, true
	case bool:
		if value {
			return 1, true
		}
		return 0, true
	case json.Number:
		number, err := value.Float64()
		if err != nil || number < 0 || number > math.MaxUint32 {
			return 0, false
		}
		return uint32(number), true
	case string:
		number, err := strconv.ParseUint(value, 10, 32)
		return uint32(number), err == nil
	default:
		return 0, false
	}
}

// buildResponse constructs the tx response, choosing v1 or v2 format based on ctx.ApiVersion.
func (m *TxMethod) buildResponse(
	ctx *types.RPCContext,
	storedTx StoredTransaction,
	txInfo *types.TransactionInfo,
	hashStr string,
	closeTimeSec int64,
	binary bool,
) map[string]any {
	networkID := ctx.Services.Ledger.GetServerInfo().NetworkID
	if ctx.ApiVersion > 1 {
		return m.buildResponseV2(storedTx, txInfo, hashStr, closeTimeSec, binary, networkID)
	}
	return m.buildResponseV1(storedTx, txInfo, hashStr, closeTimeSec, binary, networkID)
}

// buildResponseV1 builds the legacy (API v1) response with flat tx fields on root.
func (m *TxMethod) buildResponseV1(
	storedTx StoredTransaction,
	txInfo *types.TransactionInfo,
	hashStr string,
	closeTimeSec int64,
	binary bool,
	networkID uint32,
) map[string]any {
	response := map[string]any{}

	if binary {
		txBlob, err := binarycodec.Encode(storedTx.TxJSON)
		if err == nil {
			response["tx"] = txBlob
		}
		if storedTx.Meta != nil {
			metaBlob, err := binarycodec.Encode(storedTx.Meta)
			if err == nil {
				response["meta"] = metaBlob
			}
		}
	} else {
		maps.Copy(response, projectTransactionJSON(storedTx.TxJSON, "", 1))
		if storedTx.Meta != nil {
			InjectSyntheticFields(storedTx.TxJSON, storedTx.Meta, SyntheticMetadataContext{
				LedgerSequence: txInfo.LedgerIndex,
				CloseTime:      closeTimeSec,
			})
			response["meta"] = storedTx.Meta
		}
	}

	response["hash"] = hashStr
	if txInfo.LedgerIndex > 0 {
		response["inLedger"] = txInfo.LedgerIndex
		response["ledger_index"] = txInfo.LedgerIndex
	}
	response["validated"] = txInfo.Validated
	if ctid, ok := txResultCTID(storedTx.Meta, txInfo, networkID); ok {
		response["ctid"] = ctid
	}

	if closeTimeSec > 0 {
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
	networkID uint32,
) map[string]any {
	response := map[string]any{}
	var txJSON map[string]any

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
		txJSON = projectTransactionJSON(storedTx.TxJSON, "", 2)
		// date and ledger_index go inside tx_json for v2
		if txInfo.LedgerIndex > 0 {
			txJSON["ledger_index"] = txInfo.LedgerIndex
			if closeTimeSec > 0 {
				txJSON["date"] = closeTimeSec
			}
		}
		response["tx_json"] = txJSON

		if storedTx.Meta != nil {
			InjectSyntheticFields(storedTx.TxJSON, storedTx.Meta, SyntheticMetadataContext{
				LedgerSequence: txInfo.LedgerIndex,
				CloseTime:      closeTimeSec,
			})
			response["meta"] = storedTx.Meta
		}
	}

	// Root-level fields
	response["hash"] = hashStr
	response["validated"] = txInfo.Validated
	if ctid, ok := txResultCTID(storedTx.Meta, txInfo, networkID); ok {
		response["ctid"] = ctid
	}

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
func (m *TxMethod) lookupByCTID(ctx *types.RPCContext, ledgerSeq uint32, txIndex uint16, binary bool) (any, *types.RPCError) {
	ledger, err := ctx.Services.Ledger.GetLedgerBySequence(ledgerSeq)
	if err != nil {
		return nil, types.RPCErrorTxnNotFound("Transaction not found.")
	}

	var foundHash [32]byte
	var foundData []byte
	var found bool

	ledger.ForEachTransaction(func(txHash [32]byte, txData []byte) bool {
		metadataIndex, ok := txcore.TransactionIndexFromTxWithMetaBlob(txData)
		if ok && metadataIndex == uint32(txIndex) {
			foundHash = txHash
			foundData = make([]byte, len(txData))
			copy(foundData, txData)
			found = true
			return false
		}
		return true
	})

	if !found {
		return nil, types.RPCErrorTxnNotFound("Transaction not found.")
	}

	hashStr := strings.ToUpper(hex.EncodeToString(foundHash[:]))
	validated := ledger.IsValidated()
	closeTimeSec := ledger.CloseTime()
	ledgerHashStr := fmt.Sprintf("%X", ledger.Hash())

	storedTx, decodeErr := decodeTxBlob(foundData)

	return m.ctidResponse(ctx, storedTx, decodeErr, foundData, hashStr, ledgerSeq, txIndex, closeTimeSec, validated, ledgerHashStr, binary), nil
}

func (m *TxMethod) ctidResponse(
	ctx *types.RPCContext,
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
	response := m.buildResponse(ctx, tx, txInfo, hashStr, closeTimeSec, binary)

	if binary {
		if decodeErr != nil {
			delete(response, "tx")
			delete(response, "tx_blob")
			key := "tx"
			if ctx.ApiVersion > 1 {
				key = "tx_blob"
			}
			response[key] = strings.ToUpper(hex.EncodeToString(foundData))
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
		return response
	}

	return response
}

func txResultCTID(meta map[string]any, txInfo *types.TransactionInfo, networkID uint32) (string, bool) {
	if txInfo.LedgerIndex == 0 || txInfo.LedgerIndex >= maxCTIDLedgerSequence ||
		networkID >= maxCTIDComponent {
		return "", false
	}
	transactionIndex, ok := jsonUint32(meta["TransactionIndex"])
	if !ok {
		return "", false
	}
	return EncodeCTID(txInfo.LedgerIndex, transactionIndex, networkID)
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
