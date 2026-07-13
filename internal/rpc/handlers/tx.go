package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TxMethod handles the tx RPC method
type TxMethod struct{ BaseHandler }

func (m *TxMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
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

	var rawParams map[string]json.RawMessage
	if len(params) > 0 && !isJSONNull(params) {
		if err := json.Unmarshal(params, &rawParams); err != nil {
			return nil, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}
	_, hasTransaction := rawParams["transaction"]
	_, hasCTID := rawParams["ctid"]
	if hasTransaction && hasCTID {
		return nil, types.RPCErrorInvalidParams("Invalid parameters.")
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// CTID lookup support
	if hasCTID {
		ctidLedgerSeq, ctidTxIndex, ctidNetworkID, err := parseCTID(request.CTID)
		if err != nil {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
		// The CTID embeds a network id; reject the request when it does not match
		// this node's network (Tx.cpp:313-321).
		if nodeNet := ctx.Services.Ledger.GetServerInfo().NetworkID; uint32(ctidNetworkID) != nodeNet {
			return nil, types.RPCErrorWrongNetwork(fmt.Sprintf(
				"Wrong network. You should submit this request to a node running on NetworkID: %d", ctidNetworkID))
		}
		return m.lookupByCTID(ctx, ctidLedgerSeq, ctidTxIndex, request.Binary)
	}

	if !hasTransaction {
		return nil, types.RPCErrorInvalidParams("Invalid parameters.")
	}

	// A search range is formed only when both min_ledger and max_ledger are
	// present (a partial range is ignored, so a present 0 is a real bound, not
	// "absent"); when both are given the range must be ordered and span at most
	// 1000 ledgers (Tx.cpp:330-344, doTxHelp:75-93).
	var ledgerRange *types.TransactionSearchRange
	if request.MinLedger != nil && request.MaxLedger != nil {
		minLedger, maxLedger := *request.MinLedger, *request.MaxLedger
		if maxLedger < minLedger {
			return nil, types.RPCErrorInvalidLgrRange()
		}
		if maxLedger-minLedger > 1000 {
			return nil, types.RPCErrorExcessiveLgrRange()
		}
		ledgerRange = &types.TransactionSearchRange{Min: minLedger, Max: maxLedger}
	}

	txHash, err := protocol.Hash256FromHex(request.Transaction)
	if err != nil {
		return nil, types.RPCErrorNotImpl()
	}

	var txInfo *types.TransactionInfo
	if searcher, ok := ctx.Services.Ledger.(types.TransactionSearcher); ok {
		searchResult, searchErr := searcher.SearchTransaction(ctx.Context, txHash, ledgerRange)
		if searchErr != nil {
			return nil, types.RPCErrorInternal("Failed to search transaction history")
		}
		if searchResult == nil || searchResult.Transaction == nil {
			rpcErr := types.RPCErrorTxnNotFound("Transaction not found")
			if searchResult != nil && searchResult.SearchedAll != nil {
				rpcErr.Extra = map[string]any{"searched_all": *searchResult.SearchedAll}
			}
			return nil, rpcErr
		}
		txInfo = searchResult.Transaction
	} else {
		txInfo, err = ctx.Services.Ledger.GetTransaction(txHash)
		if err != nil || txInfo == nil {
			return nil, types.RPCErrorTxnNotFound("Transaction not found")
		}
		if ledgerRange != nil && (txInfo.LedgerIndex < ledgerRange.Min || txInfo.LedgerIndex > ledgerRange.Max) {
			return nil, types.RPCErrorTxnNotFound("Transaction not found")
		}
	}
	storedTx, err := decodeTxBlob(txInfo.TxData)
	if err != nil {
		return nil, types.RPCErrorInternal("Failed to decode transaction data")
	}

	// Resolve close time from the containing ledger
	closeTimeSec := txInfo.CloseTime
	if closeTimeSec == 0 && txInfo.LedgerIndex > 0 {
		if ledger, err := ctx.Services.Ledger.GetLedgerBySequence(txInfo.LedgerIndex); err == nil {
			closeTimeSec = ledger.CloseTime()
		}
	}

	return m.buildResponse(ctx, storedTx, txInfo, strings.ToUpper(request.Transaction), closeTimeSec, request.Binary), nil
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
	transactionIndex, hasTransactionIndex := jsonUint32(storedTx.Meta["TransactionIndex"])
	var response map[string]any
	if ctx.ApiVersion > 1 {
		response = m.buildResponseV2(storedTx, txInfo, hashStr, closeTimeSec, binary, networkID, transactionIndex, hasTransactionIndex)
	} else {
		response = m.buildResponseV1(storedTx, txInfo, hashStr, closeTimeSec, binary, networkID, transactionIndex, hasTransactionIndex)
	}

	if hasTransactionIndex {
		if ctid, ok := encodeTxResponseCTID(txInfo.LedgerIndex, transactionIndex, networkID); ok {
			response["ctid"] = ctid
		}
	}
	return response
}

// buildResponseV1 builds the legacy (API v1) response with flat tx fields on root.
func (m *TxMethod) buildResponseV1(
	storedTx StoredTransaction,
	txInfo *types.TransactionInfo,
	hashStr string,
	closeTimeSec int64,
	binary bool,
	networkID uint32,
	transactionIndex uint32,
	hasTransactionIndex bool,
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
		// Spread transaction fields flat on root
		maps.Copy(response, storedTx.TxJSON)
		injectDeliverMax(response, 1)
		if storedTx.Meta != nil {
			enrichTransactionMeta(storedTx.Meta, storedTx.TxJSON)
			response["meta"] = storedTx.Meta
		}
	}

	response["hash"] = hashStr
	response["inLedger"] = txInfo.LedgerIndex
	response["ledger_index"] = txInfo.LedgerIndex
	response["validated"] = txInfo.Validated
	if hasTransactionIndex {
		if ctid, ok := encodeCTID(
			txInfo.LedgerIndex,
			transactionIndex,
			transactionNetworkID(storedTx.TxJSON, networkID),
		); ok {
			response["ctid"] = ctid
		}
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
	transactionIndex uint32,
	hasTransactionIndex bool,
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
		injectDeliverMax(txJSON, 2)
		// date and ledger_index go inside tx_json for v2
		txJSON["ledger_index"] = txInfo.LedgerIndex
		if closeTimeSec > 0 {
			txJSON["date"] = closeTimeSec
		}
		if hasTransactionIndex {
			if ctid, ok := encodeCTID(
				txInfo.LedgerIndex,
				transactionIndex,
				transactionNetworkID(storedTx.TxJSON, networkID),
			); ok {
				txJSON["ctid"] = ctid
			}
		}
		response["tx_json"] = txJSON

		if storedTx.Meta != nil {
			enrichTransactionMeta(storedTx.Meta, storedTx.TxJSON)
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
			response["close_time_iso"] = protocol.FormatCloseTimeISO(protocol.FromRippleTime(uint32(closeTimeSec)))
		}
	}

	return response
}

// lookupByCTID looks up a transaction using a CTID (Compact Transaction ID)
func (m *TxMethod) lookupByCTID(ctx *types.RPCContext, ledgerSeq uint32, txIndex uint16, binary bool) (any, *types.RPCError) {
	ledger, err := ctx.Services.Ledger.GetLedgerBySequence(ledgerSeq)
	if err != nil {
		return nil, types.RPCErrorTxnNotFound("Transaction not found (ledger not available)")
	}

	// SHAMap iteration order differs from the metadata's transaction order.
	var foundHash [32]byte
	var foundData []byte
	var found bool

	ledger.ForEachTransaction(func(txHash [32]byte, txData []byte) bool {
		storedTx, decodeErr := decodeTxBlob(txData)
		if decodeErr != nil {
			return true
		}
		transactionIndex, ok := jsonUint32(storedTx.Meta["TransactionIndex"])
		if ok && transactionIndex == uint32(txIndex) {
			foundHash = txHash
			foundData = make([]byte, len(txData))
			copy(foundData, txData)
			found = true
			return false // stop iteration
		}
		return true
	})

	if !found {
		return nil, types.RPCErrorTxnNotFound("Transaction not found at specified index")
	}

	hashStr := protocol.Hash256Hex(foundHash)
	validated := ledger.IsValidated()
	closeTimeSec := ledger.CloseTime()
	ledgerHashStr := protocol.Hash256Hex(ledger.Hash())

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

	if decodeErr != nil {
		if binary {
			key := "tx"
			if ctx.ApiVersion > 1 {
				key = "tx_blob"
			}
			response[key] = strings.ToUpper(hex.EncodeToString(foundData))
		} else if ctx.ApiVersion > 1 {
			delete(response, "tx_json")
		}
	}
	return response
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
