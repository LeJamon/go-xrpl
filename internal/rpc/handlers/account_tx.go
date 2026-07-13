package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

// AccountTxMethod handles account_tx: it pages through the transactions that
// affected the account over a validated-ledger range, oldest- or newest-first.
type AccountTxMethod struct{ BaseHandler }

func (m *AccountTxMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	// notEnabled takes precedence over any parameter validation, matching
	// rippled's useTxTables() gate as the first statement of doAccountTxJson.
	if err := RequireTxTables(ctx.Services); err != nil {
		return nil, err
	}

	var rawParams map[string]json.RawMessage
	if len(params) != 0 {
		if err := json.Unmarshal(params, &rawParams); err != nil {
			return nil, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}
	var binary, forward bool
	if ctx.ApiVersion > types.ApiVersion1 {
		var boolErr *types.RPCError
		binary, boolErr = parseAccountTxBool(rawParams, "binary", ctx.ApiVersion)
		if boolErr != nil {
			return nil, boolErr
		}
		forward, boolErr = parseAccountTxBool(rawParams, "forward", ctx.ApiVersion)
		if boolErr != nil {
			return nil, boolErr
		}
	}

	// Explicit zero is invalid; non-admin limits are clamped to [10, 400].
	limit, limitErr := ReadLimitField(params, LimitAccountTx, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	if ctx.ApiVersion <= types.ApiVersion1 {
		var boolErr *types.RPCError
		binary, boolErr = parseAccountTxBool(rawParams, "binary", ctx.ApiVersion)
		if boolErr != nil {
			return nil, boolErr
		}
		forward, boolErr = parseAccountTxBool(rawParams, "forward", ctx.ApiVersion)
		if boolErr != nil {
			return nil, boolErr
		}
	}

	var request types.AccountParam
	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}
	if err := ValidateAccount(request.Account); err != nil {
		return nil, err
	}

	var requestedMin, requestedMax int64
	var singleSelection *ledgerselector.Selector
	rawMin, hasMin := rawParams["ledger_index_min"]
	rawMax, hasMax := rawParams["ledger_index_max"]
	hasMinMax := hasMin || hasMax
	_, hasLedgerHash := rawParams["ledger_hash"]
	_, hasLedgerIndex := rawParams["ledger_index"]
	hasLedgerSpec := hasLedgerHash || hasLedgerIndex

	// API v2: reject conflicting ledger parameters (ledger_index_min/max vs ledger_hash/ledger_index)
	if ctx.ApiVersion > 1 && hasMinMax && hasLedgerSpec {
		return nil, types.RPCErrorInvalidParams("invalidParams")
	}

	if hasMinMax {
		requestedMax = int64(^uint32(0))
		if hasMin {
			value, rangeErr := parseAccountTxRangeBound(rawMin, "ledger_index_min", 0)
			if rangeErr != nil {
				return nil, rangeErr
			}
			requestedMin = value
		}
		if hasMax {
			value, rangeErr := parseAccountTxRangeBound(rawMax, "ledger_index_max", ^uint32(0))
			if rangeErr != nil {
				return nil, rangeErr
			}
			requestedMax = value
		}
	} else if hasLedgerSpec {
		selection, selectionErr := parseAccountTxLedgerSelector(rawParams)
		if selectionErr != nil {
			return nil, selectionErr
		}
		singleSelection = &selection
	}

	// Parse marker only after all ledger arguments have been accepted.
	var marker *types.AccountTxMarker
	if rawMarker, ok := rawParams["marker"]; ok {
		parsed, markerErr := parseAccountTxMarker(rawMarker)
		if markerErr != nil {
			return nil, markerErr
		}
		marker = parsed
	}

	validatedMin, validatedMax, hasValidatedRange := accountTxValidatedRange(ctx.Services.Ledger)
	if !hasValidatedRange {
		if ctx.ApiVersion <= types.ApiVersion1 {
			return nil, types.RPCErrorLgrIdxsInvalid()
		}
		return nil, types.RPCErrorNotSynced("")
	}
	ledgerIndexMin := int64(validatedMin)
	ledgerIndexMax := int64(validatedMax)
	if hasMinMax {
		if ctx.ApiVersion > types.ApiVersion1 &&
			((requestedMax > int64(validatedMax) && requestedMax != int64(^uint32(0))) ||
				(requestedMin < int64(validatedMin) && requestedMin != 0)) {
			return nil, types.RPCErrorLgrIdxMalformed()
		}
		if requestedMin > ledgerIndexMin {
			ledgerIndexMin = requestedMin
		}
		if requestedMax < ledgerIndexMax {
			ledgerIndexMax = requestedMax
		}
		if ledgerIndexMax < ledgerIndexMin {
			if ctx.ApiVersion <= types.ApiVersion1 {
				return nil, types.RPCErrorLgrIdxsInvalid()
			}
			return nil, types.RPCErrorInvalidLgrRange()
		}
	} else if singleSelection != nil {
		resolved, resolveErr := resolveLedgerSelection(ctx, *singleSelection)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if !resolved.Validated || resolved.Sequence < validatedMin || resolved.Sequence > validatedMax {
			return nil, types.RPCErrorLgrNotValidated()
		}
		ledgerIndexMin = int64(resolved.Sequence)
		ledgerIndexMax = int64(resolved.Sequence)
	}

	result, err := ctx.Services.Ledger.GetAccountTransactions(
		ctx.Context,
		request.Account,
		ledgerIndexMin,
		ledgerIndexMax,
		limit,
		marker,
		forward,
	)
	if err != nil {
		if errors.Is(err, svcerr.ErrTxHistoryUnavailable) {
			return nil, types.RPCErrorNotEnabled("")
		}
		return nil, mapAccountQueryErr(err, fmt.Sprintf("Failed to get account transactions: %v", err))
	}

	// Cache for ledger lookups by sequence, to avoid repeated lookups
	// for transactions in the same ledger.
	type ledgerCacheEntry struct {
		hash         [32]byte
		closeTimeSec int64
		found        bool
	}
	ledgerCache := make(map[uint32]*ledgerCacheEntry)

	lookupLedger := func(seq uint32) *ledgerCacheEntry {
		if entry, ok := ledgerCache[seq]; ok {
			return entry
		}
		entry := &ledgerCacheEntry{}
		if source, ok := ctx.Services.Ledger.(types.LedgerContextReader); ok {
			ledgerContext, lookupErr := source.GetLedgerContext(ctx.Context, seq)
			if lookupErr == nil && ledgerContext != nil {
				entry.hash = ledgerContext.Hash
				entry.closeTimeSec = ledgerContext.CloseTime
				entry.found = true
				ledgerCache[seq] = entry
				return entry
			}
		}
		ledger, lookupErr := ctx.Services.Ledger.GetLedgerBySequence(seq)
		if lookupErr == nil && ledger != nil {
			entry.hash = ledger.Hash()
			entry.closeTimeSec = ledger.CloseTime()
			entry.found = true
		}
		ledgerCache[seq] = entry
		return entry
	}

	serverInfo := ctx.Services.Ledger.GetServerInfo()
	networkID := serverInfo.NetworkID

	isV2 := ctx.ApiVersion > 1

	// Build transactions array
	transactions := make([]map[string]any, len(result.Transactions))
	for i, txn := range result.Transactions {
		txEntry := map[string]any{
			"validated": true,
		}

		txHashHex := strings.ToUpper(hex.EncodeToString(txn.Hash[:]))

		if binary {
			// Binary mode
			txEntry["tx_blob"] = strings.ToUpper(hex.EncodeToString(txn.TxBlob))
			if isV2 {
				// API v2: meta_blob
				txEntry["meta_blob"] = strings.ToUpper(hex.EncodeToString(txn.Meta))
			} else {
				// API v1: meta
				txEntry["meta"] = strings.ToUpper(hex.EncodeToString(txn.Meta))
			}
			txEntry["ledger_index"] = txn.LedgerIndex
		} else {
			// JSON mode: decode tx_blob and meta into JSON objects

			// Determine the tx JSON key based on API version
			txKey := "tx"
			if isV2 {
				txKey = "tx_json"
			}

			// Decode tx_blob into JSON
			txJSON, decErr := decodeBinaryObject(txn.TxBlob)
			if decErr != nil {
				// Fallback to hex if decode fails
				txEntry["tx_blob"] = strings.ToUpper(hex.EncodeToString(txn.TxBlob))
				txEntry["hash"] = txHashHex
				txEntry["ledger_index"] = txn.LedgerIndex
			} else {
				// Add date inside the tx JSON (both v1 and v2)
				ledgerInfo := lookupLedger(txn.LedgerIndex)
				if ledgerInfo.found && ledgerInfo.closeTimeSec > 0 {
					txJSON["date"] = ledgerInfo.closeTimeSec
				}

				// Inject DeliverMax for Payment transactions
				injectDeliverMax(txJSON, ctx.ApiVersion)

				if isV2 {
					// API v2 retains ledger_index inside tx_json; hash is emitted
					// at the transaction-entry root.
					txJSON["ledger_index"] = txn.LedgerIndex
				} else {
					txJSON["hash"] = txHashHex
					txJSON["inLedger"] = txn.LedgerIndex
					txJSON["ledger_index"] = txn.LedgerIndex
				}

				if ctid, ok := encodeCTID(
					txn.LedgerIndex,
					txn.TxnSeq,
					transactionNetworkID(txJSON, networkID),
				); ok {
					txJSON["ctid"] = ctid
				}

				txEntry[txKey] = txJSON
			}

			// Decode metadata
			metaJSON, metaErr := decodeBinaryObject(txn.Meta)
			if metaErr != nil {
				txEntry["meta"] = strings.ToUpper(hex.EncodeToString(txn.Meta))
			} else {
				if txJSONMap, ok := txEntry[txKey].(map[string]any); ok {
					enrichTransactionMeta(metaJSON, txJSONMap)
				}
				txEntry["meta"] = metaJSON
			}

			if isV2 {
				txEntry["hash"] = txHashHex
				txEntry["ledger_index"] = txn.LedgerIndex
				// API v2: add per-entry ledger_hash and close_time_iso
				ledgerInfo := lookupLedger(txn.LedgerIndex)
				if ledgerInfo.found {
					txEntry["ledger_hash"] = strings.ToUpper(hex.EncodeToString(ledgerInfo.hash[:]))
					if ledgerInfo.closeTimeSec > 0 {
						txEntry["close_time_iso"] = protocol.FormatCloseTimeISO(protocol.FromRippleTime(uint32(ledgerInfo.closeTimeSec)))
					}
				}
			}
		}

		transactions[i] = txEntry
	}

	response := map[string]any{
		"account":          result.Account,
		"ledger_index_min": result.LedgerMin,
		"ledger_index_max": result.LedgerMax,
		"limit":            result.Limit,
		"transactions":     transactions,
		"validated":        result.Validated,
	}

	if result.Marker != nil {
		response["marker"] = map[string]any{
			"ledger": result.Marker.LedgerSeq,
			"seq":    result.Marker.TxnSeq,
		}
	}

	return response, nil
}

func accountTxValidatedRange(service types.LedgerService) (uint32, uint32, bool) {
	maxSequence := service.GetValidatedLedgerIndex()
	if maxSequence == 0 {
		maxSequence = service.GetServerInfo().ValidatedLedgerSeq
	}
	if maxSequence == 0 {
		return 0, 0, false
	}

	minSequence := maxSequence
	for _, interval := range strings.Split(service.GetServerInfo().CompleteLedgers, ",") {
		parts := strings.Split(strings.TrimSpace(interval), "-")
		if len(parts) == 0 || len(parts) > 2 {
			continue
		}
		first, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil {
			continue
		}
		last := first
		if len(parts) == 2 {
			last, err = strconv.ParseUint(parts[1], 10, 32)
			if err != nil {
				continue
			}
		}
		if uint64(maxSequence) >= first && uint64(maxSequence) <= last {
			minSequence = uint32(first)
			break
		}
	}
	return minSequence, maxSequence, true
}

func parseAccountTxBool(raw map[string]json.RawMessage, field string, apiVersion int) (bool, *types.RPCError) {
	value, present := raw[field]
	if !present {
		return false, nil
	}
	if apiVersion > types.ApiVersion1 {
		var result bool
		if err := json.Unmarshal(value, &result); err != nil || isJSONNull(value) {
			return false, types.RPCErrorInvalidField(field)
		}
		return result, nil
	}

	var legacy any
	if err := json.Unmarshal(value, &legacy); err != nil {
		return false, types.RPCErrorInvalidField(field)
	}
	switch typed := legacy.(type) {
	case nil:
		return false, nil
	case bool:
		return typed, nil
	case string:
		return typed != "", nil
	case float64:
		return typed != 0, nil
	default:
		return false, types.RPCErrorInvalidField(field)
	}
}

func parseAccountTxRangeBound(raw json.RawMessage, field string, negativeDefault uint32) (int64, *types.RPCError) {
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid field '%s'.", field))
	}
	if value < 0 {
		return int64(negativeDefault), nil
	}
	if uint64(value) > uint64(^uint32(0)) {
		return 0, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid field '%s'.", field))
	}
	return value, nil
}

func parseAccountTxLedgerSelector(raw map[string]json.RawMessage) (ledgerselector.Selector, *types.RPCError) {
	if rawHash, ok := raw["ledger_hash"]; ok {
		var value string
		if isJSONNull(rawHash) || json.Unmarshal(rawHash, &value) != nil {
			return ledgerselector.Selector{}, types.RPCErrorInvalidParams("ledgerHashNotString")
		}
		selection, err := ledgerselector.ParseHash(value)
		if err != nil {
			return ledgerselector.Selector{}, types.RPCErrorInvalidParams("ledgerHashMalformed")
		}
		return selection, nil
	}

	rawIndex := raw["ledger_index"]
	var shortcut string
	if err := json.Unmarshal(rawIndex, &shortcut); err == nil {
		switch shortcut {
		case "", "current":
			return ledgerselector.Current(), nil
		case "closed":
			return ledgerselector.Closed(), nil
		case "validated":
			return ledgerselector.Validated(), nil
		default:
			return ledgerselector.Selector{}, types.RPCErrorInvalidParams("ledger_index string malformed")
		}
	}

	var sequence uint32
	if err := json.Unmarshal(rawIndex, &sequence); err != nil {
		return ledgerselector.Selector{}, types.RPCErrorInvalidParams("ledger_index string malformed")
	}
	return ledgerselector.FromSequence(sequence), nil
}

func parseAccountTxMarker(raw json.RawMessage) (*types.AccountTxMarker, *types.RPCError) {
	invalid := func() *types.RPCError {
		return types.RPCErrorInvalidParams("invalid marker. Provide ledger index via ledger field, and transaction sequence number via seq field")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, invalid()
	}
	rawLedger, hasLedger := fields["ledger"]
	rawSequence, hasSequence := fields["seq"]
	if !hasLedger || !hasSequence {
		return nil, invalid()
	}
	ledgerSequence, ok := accountTxMarkerUInt(rawLedger)
	if !ok {
		return nil, invalid()
	}
	transactionSequence, ok := accountTxMarkerUInt(rawSequence)
	if !ok {
		return nil, invalid()
	}
	return &types.AccountTxMarker{
		LedgerSeq: ledgerSequence,
		TxnSeq:    transactionSequence,
	}, nil
}

func accountTxMarkerUInt(raw json.RawMessage) (uint32, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	switch typed := value.(type) {
	case nil:
		return 0, true
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case float64:
		if typed < 0 || typed > math.MaxUint32 || math.Trunc(typed) != typed {
			return 0, false
		}
		return uint32(typed), true
	default:
		return 0, false
	}
}

// injectDeliverMax adds DeliverMax to Payment transaction JSON.
// For API v1: adds DeliverMax = Amount (keeps Amount).
// For API v2+: adds DeliverMax = Amount, then removes Amount.
// This matches rippled's RPC::insertDeliverMax in DeliverMax.cpp.
func injectDeliverMax(txJSON map[string]any, apiVersion int) {
	amount, hasAmount := txJSON["Amount"]
	if !hasAmount {
		return
	}
	txType, _ := txJSON["TransactionType"].(string)
	if txType != "Payment" {
		return
	}
	txJSON["DeliverMax"] = amount
	if apiVersion > 1 {
		delete(txJSON, "Amount")
	}
}
