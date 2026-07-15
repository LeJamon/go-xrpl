package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

// AccountTxMethod handles account_tx: it pages through the transactions that
// affected the account over a validated-ledger range, oldest- or newest-first.
type AccountTxMethod struct{ BaseHandler }

func (m *AccountTxMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	if err := RequireTxTables(ctx.Services); err != nil {
		return nil, err
	}
	if err := validateJsonCppIntegerRange(params); err != nil {
		return nil, err
	}

	fields := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(params)) != 0 {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	if ctx.ApiVersion > 1 {
		if raw, ok := fields["binary"]; ok {
			if _, isBool := accountTxBoolValue(raw); !isBool {
				return nil, types.RPCErrorInvalidField("binary")
			}
		}
		if raw, ok := fields["forward"]; ok {
			if _, isBool := accountTxBoolValue(raw); !isBool {
				return nil, types.RPCErrorInvalidField("forward")
			}
		}
	}

	limit, limitErr := ReadLimitField(params, LimitAccountTx, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}

	binary := accountTxBool(fields["binary"])
	forward := accountTxBool(fields["forward"])

	accountRaw, ok := fields["account"]
	if !ok {
		return nil, types.RPCErrorMissingField("account")
	}
	accountValue, decodeErr := decodeAccountTxValue(accountRaw)
	if decodeErr != nil {
		return nil, types.RPCErrorInvalidField("account")
	}
	account, ok := accountValue.(string)
	if !ok {
		return nil, types.RPCErrorInvalidField("account")
	}
	if !types.IsValidClassicAddress(account) {
		return nil, types.RPCErrorActMalformed("Account malformed.")
	}

	ledgerSelection, ledgerErr := parseAccountTxLedgerSelection(ctx, fields)
	if ledgerErr != nil {
		return nil, ledgerErr
	}

	var marker *types.AccountTxMarker
	if raw, ok := fields["marker"]; ok {
		var markerErr *types.RPCError
		marker, markerErr = parseAccountTxMarker(raw)
		if markerErr != nil {
			return nil, markerErr
		}
	}

	ledgerIndexMin, ledgerIndexMax, ledgerErr := resolveAccountTxLedgerSelection(ctx, ledgerSelection)
	if ledgerErr != nil {
		return nil, ledgerErr
	}

	result, err := ctx.Services.Ledger.GetAccountTransactions(
		ctx.Context,
		account,
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
			var sourceTxJSON map[string]any
			ledgerInfo := lookupLedger(txn.LedgerIndex)

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
				sourceTxJSON = maps.Clone(txJSON)
				ctidNetworkID := networkID
				if override, ok := jsonUint32(sourceTxJSON["NetworkID"]); ok {
					ctidNetworkID = override
				}
				// Add date inside the tx JSON (both v1 and v2)
				if ledgerInfo.found && ledgerInfo.closeTimeSec > 0 {
					txJSON["date"] = ledgerInfo.closeTimeSec
				}

				// Inject DeliverMax for Payment transactions
				injectDeliverMax(txJSON, ctx.ApiVersion)

				if !isV2 {
					txJSON["hash"] = txHashHex
				}
				if txn.LedgerIndex > 0 {
					txJSON["ledger_index"] = txn.LedgerIndex
					if !isV2 {
						txJSON["inLedger"] = txn.LedgerIndex
					}
					if ctid, ok := EncodeCTID(txn.LedgerIndex, txn.TxnSeq, ctidNetworkID); ok {
						txJSON["ctid"] = ctid
					}
				}

				txEntry[txKey] = txJSON
			}

			// Decode metadata
			metaJSON, metaErr := decodeBinaryObject(txn.Meta)
			if metaErr != nil {
				txEntry["meta"] = strings.ToUpper(hex.EncodeToString(txn.Meta))
			} else {
				if sourceTxJSON != nil {
					InjectSyntheticFields(sourceTxJSON, metaJSON, SyntheticMetadataContext{
						LedgerSequence: txn.LedgerIndex,
						CloseTime:      ledgerInfo.closeTimeSec,
					})
				}
				txEntry["meta"] = metaJSON
			}

			if isV2 {
				txEntry["hash"] = txHashHex
				txEntry["ledger_index"] = txn.LedgerIndex
				// API v2: add per-entry ledger_hash and close_time_iso
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

func decodeAccountTxValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func accountTxBoolValue(raw json.RawMessage) (bool, bool) {
	value, err := decodeAccountTxValue(raw)
	if err != nil {
		return false, false
	}
	b, ok := value.(bool)
	return b, ok
}

func accountTxBool(raw json.RawMessage) bool {
	value, err := decodeAccountTxValue(raw)
	if err != nil {
		return false
	}
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case json.Number:
		n, err := value.Float64()
		return err == nil && n != 0
	case string:
		return value != ""
	case []any:
		return len(value) != 0
	case map[string]any:
		return len(value) != 0
	default:
		return false
	}
}

type accountTxLedgerSelection struct {
	hasRange bool
	min      uint32
	max      uint32
	spec     json.RawMessage
}

func parseAccountTxLedgerSelection(ctx *types.RPCContext, fields map[string]json.RawMessage) (accountTxLedgerSelection, *types.RPCError) {
	minRaw, hasMin := fields["ledger_index_min"]
	maxRaw, hasMax := fields["ledger_index_max"]
	_, hasHash := fields["ledger_hash"]
	_, hasIndex := fields["ledger_index"]

	if ctx.ApiVersion > 1 && (hasMin || hasMax) && (hasHash || hasIndex) {
		return accountTxLedgerSelection{}, types.RPCErrorInvalidParams("invalidParams")
	}
	if hasMin || hasMax {
		min := uint32(0)
		max := uint32(math.MaxUint32)
		var err *types.RPCError
		if hasMin {
			min, err = accountTxRangeBound(minRaw, 0, "ledger_index_min")
			if err != nil {
				return accountTxLedgerSelection{}, err
			}
		}
		if hasMax {
			max, err = accountTxRangeBound(maxRaw, math.MaxUint32, "ledger_index_max")
			if err != nil {
				return accountTxLedgerSelection{}, err
			}
		}
		return accountTxLedgerSelection{hasRange: true, min: min, max: max}, nil
	}

	if hashRaw, ok := fields["ledger_hash"]; ok {
		value, err := decodeAccountTxValue(hashRaw)
		hash, isString := value.(string)
		if err != nil || !isString {
			return accountTxLedgerSelection{}, types.RPCErrorInvalidParams("ledgerHashNotString")
		}
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 {
			return accountTxLedgerSelection{}, types.RPCErrorInvalidParams("ledgerHashMalformed")
		}
		spec, _ := json.Marshal(map[string]json.RawMessage{"ledger_hash": hashRaw})
		return accountTxLedgerSelection{spec: spec}, nil
	} else if indexRaw, ok := fields["ledger_index"]; ok {
		index, err := accountTxLedgerIndex(indexRaw)
		if err != nil {
			return accountTxLedgerSelection{}, err
		}
		spec, _ := json.Marshal(map[string]any{"ledger_index": index})
		return accountTxLedgerSelection{spec: spec}, nil
	} else {
		return accountTxLedgerSelection{}, nil
	}
}

func resolveAccountTxLedgerSelection(ctx *types.RPCContext, selection accountTxLedgerSelection) (int64, int64, *types.RPCError) {
	validatedMin, validatedMax, ok := accountTxValidatedRange(ctx.Services.Ledger.GetServerInfo().CompleteLedgers)
	if !ok {
		if ctx.ApiVersion == types.ApiVersion1 {
			return 0, 0, types.NewRPCError(types.RpcLGR_IDXS_INVALID, "lgrIdxsInvalid", "lgrIdxsInvalid", "Ledger indexes invalid.")
		}
		return 0, 0, types.NewRPCError(types.RpcNOT_SYNCED, "notSynced", "notSynced", "Not synced to the network.")
	}

	if selection.hasRange {
		if ctx.ApiVersion > 1 &&
			((selection.max > validatedMax && selection.max != math.MaxUint32) ||
				(selection.min < validatedMin && selection.min != 0)) {
			return 0, 0, types.NewRPCError(types.RpcLGR_IDX_MALFORMED, "lgrIdxMalformed", "lgrIdxMalformed", "Ledger index malformed.")
		}
		min, max := validatedMin, validatedMax
		if selection.min > min {
			min = selection.min
		}
		if selection.max < max {
			max = selection.max
		}
		if max < min {
			if ctx.ApiVersion == types.ApiVersion1 {
				return 0, 0, types.NewRPCError(types.RpcLGR_IDXS_INVALID, "lgrIdxsInvalid", "lgrIdxsInvalid", "Ledger indexes invalid.")
			}
			return 0, 0, types.RPCErrorInvalidLgrRange()
		}
		return int64(min), int64(max), nil
	}

	if selection.spec == nil {
		return int64(validatedMin), int64(validatedMax), nil
	}
	ledger, validated, err := LookupLedger(ctx, selection.spec)
	if err != nil {
		return 0, 0, err
	}
	if !validated || !ledger.IsValidated() || ledger.Sequence() < validatedMin || ledger.Sequence() > validatedMax {
		return 0, 0, types.NewRPCError(types.RpcLGR_NOT_VALIDATED, "lgrNotValidated", "lgrNotValidated", "Ledger not validated.")
	}
	return int64(ledger.Sequence()), int64(ledger.Sequence()), nil
}

func accountTxValidatedRange(complete string) (uint32, uint32, bool) {
	parts := strings.Split(complete, ",")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return 0, 0, false
	}
	bounds := strings.Split(strings.TrimSpace(parts[len(parts)-1]), "-")
	minParsed, minErr := strconv.ParseUint(bounds[0], 10, 32)
	maxParsed := minParsed
	var maxErr error
	if len(bounds) > 1 {
		maxParsed, maxErr = strconv.ParseUint(bounds[len(bounds)-1], 10, 32)
	}
	min, max := uint32(minParsed), uint32(maxParsed)
	minOK, maxOK := minErr == nil, maxErr == nil
	return min, max, minOK && maxOK && min <= max
}

func accountTxRangeBound(raw json.RawMessage, negativeDefault uint32, field string) (uint32, *types.RPCError) {
	value, err := decodeAccountTxValue(raw)
	if err != nil {
		return 0, types.RPCErrorInvalidParams("Invalid field '" + field + "'.")
	}
	var number float64
	switch value := value.(type) {
	case nil:
		return 0, nil
	case bool:
		if value {
			return 1, nil
		}
		return 0, nil
	case json.Number:
		number, err = value.Float64()
	case string:
		parsed, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			err = parseErr
		} else {
			number = float64(parsed)
		}
	default:
		err = errors.New("not numeric")
	}
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, types.RPCErrorInvalidParams("Invalid field '" + field + "'.")
	}
	if number < 0 {
		return negativeDefault, nil
	}
	if number > math.MaxUint32 {
		return 0, types.RPCErrorInvalidParams("Invalid field '" + field + "'.")
	}
	return uint32(number), nil
}

func accountTxLedgerIndex(raw json.RawMessage) (types.LedgerIndex, *types.RPCError) {
	value, err := decodeAccountTxValue(raw)
	if err != nil {
		return "", types.RPCErrorInvalidParams("ledger_index string malformed")
	}
	switch value := value.(type) {
	case nil:
		return "current", nil
	case json.Number:
		number, err := value.Float64()
		if err != nil || number < 0 || number > math.MaxUint32 || math.IsNaN(number) || math.IsInf(number, 0) {
			return "", types.RPCErrorInvalidParams("ledgerIndexMalformed")
		}
		return types.LedgerIndex(strconv.FormatUint(uint64(uint32(number)), 10)), nil
	case string:
		switch value {
		case "", "current":
			return "current", nil
		case "closed", "validated":
			return types.LedgerIndex(value), nil
		default:
			return "", types.RPCErrorInvalidParams("ledger_index string malformed")
		}
	default:
		return "", types.RPCErrorInvalidParams("ledger_index string malformed")
	}
}

func parseAccountTxMarker(raw json.RawMessage) (*types.AccountTxMarker, *types.RPCError) {
	invalid := func() *types.RPCError {
		return types.RPCErrorInvalidParams("invalid marker. Provide ledger index via ledger field, and transaction sequence number via seq field")
	}
	value, err := decodeAccountTxValue(raw)
	if err != nil {
		return nil, invalid()
	}
	markerMap, ok := value.(map[string]any)
	if !ok {
		return nil, invalid()
	}
	ledgerValue, hasLedger := markerMap["ledger"]
	seqValue, hasSeq := markerMap["seq"]
	if !hasLedger || !hasSeq {
		return nil, invalid()
	}
	ledger, ok := accountTxUint32(ledgerValue)
	if !ok {
		return nil, invalid()
	}
	seq, ok := accountTxUint32(seqValue)
	if !ok {
		return nil, invalid()
	}
	return &types.AccountTxMarker{LedgerSeq: ledger, TxnSeq: seq}, nil
}

func accountTxUint32(value any) (uint32, bool) {
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
		if err != nil || number < 0 || number > math.MaxUint32 || math.Round(number) != number {
			return 0, false
		}
		return uint32(number), true
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
