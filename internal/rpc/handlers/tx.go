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

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/txprojection"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TxMethod handles the tx RPC method
type TxMethod struct{ baseHandler }

func (m *TxMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	// notEnabled takes precedence over any parameter validation, matching
	// rippled's useTxTables() gate as the first statement of doTxJson.
	if err := requireTxTables(ctx.Services); err != nil {
		return nil, err
	}

	var fields map[string]json.RawMessage
	if params != nil {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
	}
	transactionRaw, hasTransaction := fields["transaction"]
	ctidRaw, hasCTID := fields["ctid"]
	if hasTransaction && hasCTID {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}

	var transaction, ctid string
	var valid bool
	switch {
	case hasTransaction:
		transaction, valid = jsonCppStringRaw(transactionRaw)
		if !valid {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
	case hasCTID:
		ctid, valid = jsonCppStringRaw(ctidRaw)
		if !valid {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
	default:
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}

	var txHash [32]byte
	var ctidLedgerSeq uint32
	var ctidTxIndex uint16
	if hasCTID {
		var ctidNetworkID uint16
		var err error
		ctidLedgerSeq, ctidTxIndex, ctidNetworkID, err = parseCTID(ctid)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		if nodeNet := ctx.Services.Ledger.GetServerInfo().NetworkID; uint32(ctidNetworkID) != nodeNet {
			return nil, types.RpcErrorWrongNetwork(fmt.Sprintf(
				"Wrong network. You should submit this request to a node running on NetworkID: %d", ctidNetworkID))
		}
	} else {
		txHashBytes, err := hex.DecodeString(transaction)
		if err != nil || len(txHashBytes) != 32 {
			return nil, types.RpcErrorNotImpl()
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
				return nil, types.RpcErrorInvalidLgrRange()
			}
			if maxLedger-minLedger > 1000 {
				return nil, types.RpcErrorExcessiveLgrRange()
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
		if errors.Is(err, svcerr.ErrTxnDataCorrupt) {
			return nil, rpcDBDeserializationError("tx: transaction lookup failed", err)
		}
		return nil, rpcInternalError("tx: transaction lookup failed", err)
	}
	if err != nil || txInfo == nil {
		return nil, txNotFoundForSearch(hasLedgerRange, searched)
	}
	decode := decodeTxBlobForTx
	if txInfo.LedgerIndex == 0 {
		decode = decodeOpenTxBlob
	}
	storedTx, err := decode(txInfo.TxData)
	if err != nil {
		return nil, rpcDBDeserializationError("tx: transaction deserialization failed", err)
	}

	// Resolve close time from the containing ledger
	closeTimeSec := txInfo.CloseTime
	if closeTimeSec == 0 && txInfo.LedgerIndex > 0 {
		if ledger, err := ctx.Services.Ledger.GetLedgerBySequence(txInfo.LedgerIndex); err == nil {
			closeTimeSec = ledger.CloseTime()
		}
	}

	return m.buildResponse(ctx, storedTx, txInfo, strings.ToUpper(transaction), closeTimeSec, binaryMode), nil
}

func txNotFoundForSearch(hasRange bool, searched types.TxSearchResult) *types.RpcError {
	err := types.RpcErrorTxnNotFound("Transaction not found.")
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
	ctx *types.RpcContext,
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
		maps.Copy(response, txprojection.ProjectJSON(storedTx.TxJSON, "", 1))
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
	if ctid, ok := transactionJSONCTID(storedTx, txInfo, networkID); ok {
		response["ctid"] = ctid
	}
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
		txJSON = txprojection.ProjectJSON(storedTx.TxJSON, "", 2)
		// date and ledger_index go inside tx_json for v2
		if txInfo.LedgerIndex > 0 {
			txJSON["ledger_index"] = txInfo.LedgerIndex
			if closeTimeSec > 0 {
				txJSON["date"] = closeTimeSec
			}
			if ctid, ok := transactionJSONCTID(storedTx, txInfo, networkID); ok {
				txJSON["ctid"] = ctid
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
			response["close_time_iso"] = protocol.FormatCloseTimeISO(protocol.FromRippleTime(uint32(closeTimeSec)))
		}
	}

	return response
}

// lookupByCTID looks up a transaction using a CTID (Compact Transaction ID)
func (m *TxMethod) lookupByCTID(ctx *types.RpcContext, ledgerSeq uint32, txIndex uint16, binary bool) (any, *types.RpcError) {
	ledger, err := ctx.Services.Ledger.GetLedgerBySequence(ledgerSeq)
	if err != nil {
		return nil, types.RpcErrorTxnNotFound("Transaction not found.")
	}

	// SHAMap iteration order differs from the metadata's transaction order.
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
		return nil, types.RpcErrorTxnNotFound("Transaction not found.")
	}

	hashStr := protocol.Hash256Hex(foundHash)
	validated := ledger.IsValidated()
	closeTimeSec := ledger.CloseTime()
	ledgerHashStr := protocol.Hash256Hex(ledger.Hash())

	storedTx, decodeErr := decodeTxBlobForTx(foundData)
	if decodeErr != nil {
		return nil, rpcDBDeserializationError("tx: CTID transaction deserialization failed", decodeErr)
	}

	return m.ctidResponse(ctx, storedTx, hashStr, ledgerSeq, txIndex, closeTimeSec, validated, ledgerHashStr, binary), nil
}

func (m *TxMethod) ctidResponse(
	ctx *types.RpcContext,
	storedTx StoredTransaction,
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

	networkID := uint16(ctx.Services.Ledger.GetServerInfo().NetworkID)
	response := m.buildResponse(ctx, storedTx, txInfo, hashStr, closeTimeSec, binary)

	if binary {
		// buildResponseV1's "inLedger" and "date" fields have no CTID
		// equivalent.
		delete(response, "inLedger")
		delete(response, "date")
		return response
	}

	if ctx.ApiVersion > 1 {
		if txJSON, ok := response["tx_json"].(map[string]any); ok {
			// buildResponseV2 omits the ctid for ledger 0; the CTID lookup
			// still reports it.
			if ctid, ok := encodeCTID(ledgerSeq, uint32(txIndex), uint32(networkID)); ok {
				txJSON["ctid"] = ctid
			}
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
	return encodeTxResponseCTID(txInfo.LedgerIndex, transactionIndex, networkID)
}

func transactionJSONCTID(storedTx StoredTransaction, txInfo *types.TransactionInfo, networkID uint32) (string, bool) {
	if txInfo.LedgerIndex == 0 {
		return "", false
	}
	transactionIndex, ok := jsonUint32(storedTx.Meta["TransactionIndex"])
	if !ok {
		return "", false
	}
	return encodeCTID(txInfo.LedgerIndex, transactionIndex, transactionNetworkID(storedTx.TxJSON, networkID))
}

// StoredTransaction represents a transaction stored in the ledger
type StoredTransaction struct {
	TxJSON map[string]any `json:"tx_json"`
	Meta   map[string]any `json:"meta"`
}

func (m *TxMethod) RequiredRole() types.Role {
	return types.RoleUser
}

func (m *TxMethod) RequiredCondition() types.Condition {
	return types.NeedsNetworkConnection
}
