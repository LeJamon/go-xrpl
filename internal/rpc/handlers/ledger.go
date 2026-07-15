package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	ledgerheader "github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/protocol"
)

// LedgerMethod handles the ledger RPC method.
type LedgerMethod struct{ BaseHandler }

func (m *LedgerMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	if boolErr := validateLedgerBooleanOptions(params); boolErr != nil {
		return nil, boolErr
	}
	ledgerSpec, hasLedgerSelector, selectorErr := parseLedgerSpecifier(params)
	if selectorErr != nil {
		return nil, selectorErr
	}
	var request struct {
		types.LedgerSpecifier
		Accounts     bool `json:"accounts,omitempty"`
		Full         bool `json:"full,omitempty"`
		Transactions bool `json:"transactions,omitempty"`
		Expand       bool `json:"expand,omitempty"`
		OwnerFunds   bool `json:"owner_funds,omitempty"`
		Binary       bool `json:"binary,omitempty"`
		Queue        bool `json:"queue,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}
	request.LedgerSpecifier = ledgerSpec

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	if !hasLedgerSelector {
		closed, err := ctx.Services.Ledger.GetLedgerBySequence(ctx.Services.Ledger.GetClosedLedgerIndex())
		if err != nil || closed == nil {
			return nil, types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		open, err := ctx.Services.Ledger.GetLedgerBySequence(ctx.Services.Ledger.GetCurrentLedgerIndex())
		if err != nil || open == nil {
			return nil, types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		response := map[string]any{
			"closed": buildLedgerJSON(closed, false, false, ctx.ApiVersion),
			"open":   buildLedgerJSON(open, false, false, ctx.ApiVersion),
		}
		addLedgerTypeWarning(response, params)
		return response, nil
	}
	dumpQueue := request.Queue && hasLedgerSelector

	// Resolve the target before the permission gate, matching LedgerHandler::check.
	targetLedger, validated, lerr := LookupLedger(ctx, request.LedgerSpecifier)
	if lerr != nil {
		return nil, lerr
	}

	if request.Full || request.Accounts {
		if !ctx.Unlimited {
			return nil, types.RPCErrorNoPermission("ledger")
		}
	}
	if dumpQueue && targetLedger.IsClosed() {
		return nil, types.RPCErrorInvalidParams("Invalid parameters.")
	}
	if request.Full {
		request.Transactions = true
		request.Expand = true
		request.Accounts = true
	}

	ledgerInfo := buildLedgerJSON(targetLedger, request.Binary, request.Full, ctx.ApiVersion)
	ledgerHash := FormatLedgerHash(targetLedger.Hash())

	closeTimeSec := targetLedger.CloseTime()
	closeTimeISO := protocol.FormatCloseTimeISO(protocol.FromRippleTime(uint32(max(closeTimeSec, 0))))
	syntheticContext := SyntheticMetadataContext{
		LedgerSequence: targetLedger.Sequence(),
		CloseTime:      closeTimeSec,
	}

	_, reserveBase, reserveInc := ctx.Services.Ledger.GetCurrentFees()
	var ownerFundsView types.LedgerStateView
	ownerFundsReserveBase, ownerFundsReserveInc := reserveBase, reserveInc
	if request.OwnerFunds && request.Expand {
		ownerFundsView = ownerFundsLedgerView(ctx, targetLedger)
		if ownerFundsView != nil {
			ownerFundsReserveBase, ownerFundsReserveInc = reserveSettingsFromLedger(ownerFundsView, reserveBase, reserveInc)
		}
	}

	if request.Transactions {
		var txList []any
		apiVersion := ctx.ApiVersion
		targetLedger.ForEachTransaction(func(txHashKey [32]byte, txData []byte) bool {
			hashStr := strings.ToUpper(hex.EncodeToString(txHashKey[:]))
			if request.Expand {
				txEntry := expandTransaction(txData, hashStr, request.Binary, apiVersion, syntheticContext)
				// Add per-entry context fields for v2+
				if apiVersion > 1 && !request.Binary {
					if targetLedger.IsClosed() {
						txEntry["ledger_hash"] = ledgerHash
					}
					txEntry["validated"] = validated
					if validated {
						txEntry["ledger_index"] = targetLedger.Sequence()
						if closeTimeSec > 0 {
							txEntry["close_time_iso"] = closeTimeISO
						}
					}
				}
				if ownerFundsView != nil {
					storedTx, _ := decodeTxBlob(txData)
					if !annotateOwnerFunds(txEntry, storedTx.TxJSON, ownerFundsView, ownerFundsReserveBase, ownerFundsReserveInc) {
						return false
					}
				}
				txList = append(txList, txEntry)
			} else {
				txList = append(txList, hashStr)
			}
			return true
		})
		if txList == nil {
			txList = []any{}
		}
		ledgerInfo["transactions"] = txList
	}

	// accounts (LedgerFill::dumpState) dumps the full state tree into the
	// ledger object under accountState (LedgerToJson.cpp fillJsonState).
	if request.Accounts {
		ledgerInfo["accountState"] = dumpAccountState(ctx, targetLedger, request.Binary, request.Expand)
	}

	response := map[string]any{
		"ledger":    ledgerInfo,
		"validated": validated,
	}
	if !targetLedger.IsClosed() {
		response["ledger_current_index"] = targetLedger.Sequence()
	} else {
		response["ledger_hash"] = ledgerHash
		response["ledger_index"] = targetLedger.Sequence()
	}
	response["validated"] = validated

	if dumpQueue {
		queueData, queueInternalError := buildLedgerQueueData(
			ctx,
			request.Binary,
			request.Expand,
			request.OwnerFunds,
			ownerFundsView,
			ownerFundsReserveBase,
			ownerFundsReserveInc,
		)
		if len(queueData) > 0 {
			response["queue_data"] = queueData
		}
		if queueInternalError {
			return nil, types.RPCErrorInternal("ledger queue owner_funds failed for MPT OfferCreate").WithExtra(response)
		}
	}
	addLedgerTypeWarning(response, params)

	return response, nil
}

func buildLedgerJSON(l types.LedgerReader, binaryMode, full bool, apiVersion int) map[string]any {
	if binaryMode {
		if !l.IsClosed() {
			return map[string]any{"closed": false}
		}
		return map[string]any{
			"closed": true,
			"ledger_data": strings.ToUpper(hex.EncodeToString(ledgerheader.AddRaw(ledgerheader.LedgerHeader{
				LedgerIndex:         l.Sequence(),
				ParentCloseTime:     protocol.FromRippleTime(uint32(max(l.ParentCloseTime(), 0))),
				ParentHash:          l.ParentHash(),
				TxHash:              l.TxMapHash(),
				AccountHash:         l.StateMapHash(),
				Drops:               l.TotalDrops(),
				CloseFlags:          l.CloseFlags(),
				CloseTimeResolution: l.CloseTimeResolution(),
				CloseTime:           protocol.FromRippleTime(uint32(max(l.CloseTime(), 0))),
			}, false))),
		}
	}
	parentHash := l.ParentHash()
	ledger := map[string]any{
		"parent_hash": strings.ToUpper(hex.EncodeToString(parentHash[:])),
	}
	if apiVersion > 1 {
		ledger["ledger_index"] = l.Sequence()
	} else {
		ledger["ledger_index"] = strconv.FormatUint(uint64(l.Sequence()), 10)
	}

	if l.IsClosed() {
		ledger["closed"] = true
	} else if !full {
		ledger["closed"] = false
		return ledger
	}

	hash := l.Hash()
	txHash := l.TxMapHash()
	stateHash := l.StateMapHash()
	ledger["ledger_hash"] = strings.ToUpper(hex.EncodeToString(hash[:]))
	ledger["transaction_hash"] = strings.ToUpper(hex.EncodeToString(txHash[:]))
	ledger["account_hash"] = strings.ToUpper(hex.EncodeToString(stateHash[:]))
	ledger["total_coins"] = strconv.FormatUint(l.TotalDrops(), 10)
	ledger["close_flags"] = l.CloseFlags()
	ledger["parent_close_time"] = l.ParentCloseTime()
	ledger["close_time"] = l.CloseTime()
	ledger["close_time_resolution"] = l.CloseTimeResolution()

	if closeTime := l.CloseTime(); closeTime != 0 {
		utc := protocol.FromRippleTime(uint32(max(closeTime, 0)))
		ledger["close_time_human"] = utc.Format("2006-Jan-02 15:04:05.000000000 UTC")
		ledger["close_time_iso"] = protocol.FormatCloseTimeISO(utc)
		if l.CloseFlags()&1 != 0 {
			ledger["close_time_estimated"] = true
		}
	}
	return ledger
}

func addLedgerTypeWarning(response map[string]any, params json.RawMessage) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return
	}
	if _, ok := fields["type"]; !ok {
		return
	}
	response["warnings"] = []types.WarningObject{{
		ID: 2004,
		Message: "Some fields from your request are deprecated. Please check the documentation at " +
			"https://xrpl.org/docs/references/http-websocket-apis/ and update your request. " +
			"Field `type` is deprecated.",
	}}
}

func validateLedgerBooleanOptions(params json.RawMessage) *types.RPCError {
	if params == nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil
	}
	for _, name := range []string{"full", "transactions", "accounts", "expand", "binary", "owner_funds", "queue"} {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return types.RPCErrorInvalidParams("Invalid parameters.")
		}
		if _, ok := value.(bool); !ok {
			return types.RPCErrorInvalidParams("Invalid parameters.")
		}
	}
	return nil
}

func ledgerRequestHasSelector(params json.RawMessage) (bool, *types.RPCError) {
	if params == nil {
		return false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return false, types.RPCErrorInvalidParams("Invalid parameters.")
	}
	selectorNames := []string{"ledger", "ledger_hash", "ledger_index"}
	present := make([]string, 0, 1)
	for _, name := range selectorNames {
		if _, ok := fields[name]; ok {
			present = append(present, name)
		}
	}
	if len(present) > 1 {
		if _, hasLegacy := fields["ledger"]; hasLegacy {
			return false, types.RPCErrorInvalidParams("Exactly one of 'ledger', 'ledger_hash', or 'ledger_index' can be specified.")
		}
		return false, types.RPCErrorInvalidParams("Exactly one of 'ledger_hash' or 'ledger_index' can be specified.")
	}
	if len(present) == 0 {
		return false, nil
	}

	name := present[0]
	var value any
	if err := json.Unmarshal(fields[name], &value); err != nil {
		return false, types.RPCErrorInvalidParams("Invalid parameters.")
	}
	if name == "ledger_hash" {
		hash, ok := value.(string)
		if !ok || len(hash) != 64 {
			return false, types.RPCErrorInvalidParams("Invalid field 'ledger_hash', not hex string.")
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return false, types.RPCErrorInvalidParams("Invalid field 'ledger_hash', not hex string.")
		}
		return true, nil
	}
	if _, stringValue := value.(string); stringValue {
		return true, nil
	}
	if _, numberValue := value.(float64); numberValue {
		rawNumber := strings.TrimSpace(string(fields[name]))
		if strings.ContainsAny(rawNumber, ".eE") {
			return false, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid field '%s', not string or number.", name))
		}
		if strings.HasPrefix(rawNumber, "-") {
			return true, nil
		}
		return true, nil
	}
	return false, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid field '%s', not string or number.", name))
}

func ledgerDefaultResponse(ctx *types.RPCContext) (map[string]any, *types.RPCError) {
	closed, err := ctx.Services.Ledger.GetLedgerBySequence(ctx.Services.Ledger.GetClosedLedgerIndex())
	if err != nil || closed == nil {
		return nil, types.RPCErrorLgrNotFound("ledgerNotFound")
	}
	open, err := ctx.Services.Ledger.GetLedgerBySequence(ctx.Services.Ledger.GetCurrentLedgerIndex())
	if err != nil || open == nil {
		return nil, types.RPCErrorLgrNotFound("ledgerNotFound")
	}
	return map[string]any{
		"closed": map[string]any{"ledger": ledgerDataHeader(ledgerHeaderInfo(closed), false, ctx.ApiVersion)},
		"open":   map[string]any{"ledger": ledgerDataHeader(ledgerHeaderInfo(open), false, ctx.ApiVersion)},
	}, nil
}

func ledgerHeaderInfo(l types.LedgerReader) *types.LedgerHeaderInfo {
	closeTime := l.CloseTime()
	close := protocol.FromRippleTime(uint32(max(closeTime, 0)))
	return &types.LedgerHeaderInfo{
		AccountHash:         l.StateMapHash(),
		CloseFlags:          l.CloseFlags(),
		CloseTime:           closeTime,
		CloseTimeHuman:      close.UTC().Format("2006-Jan-02 15:04:05.000000000 UTC"),
		CloseTimeISO:        protocol.FormatCloseTimeISO(close),
		CloseTimeResolution: l.CloseTimeResolution(),
		Closed:              l.IsClosed(),
		LedgerHash:          l.Hash(),
		LedgerIndex:         l.Sequence(),
		ParentCloseTime:     l.ParentCloseTime(),
		ParentHash:          l.ParentHash(),
		TotalCoins:          l.TotalDrops(),
		TransactionHash:     l.TxMapHash(),
	}
}

// ownerFundsLedgerView resolves the state view for the target ledger so
// owner_funds can be computed against it, mirroring rippled's accountFunds
// call against fill.ledger (LedgerToJson.cpp:216-221). Returns nil when the
// service can't supply a view for that ledger (mocks, unsupported selectors),
// in which case the annotation is simply omitted.
func ownerFundsLedgerView(ctx *types.RPCContext, l types.LedgerReader) types.LedgerStateView {
	src, ok := ctx.Services.Ledger.(types.LedgerViewSource)
	if !ok {
		return nil
	}
	if l.IsClosed() {
		view, _, err := src.GetLedgerViewByHash(l.Hash())
		if err != nil {
			return nil
		}
		return view
	}
	view, _, err := src.GetLedgerViewBySeq(l.Sequence())
	if err != nil {
		return nil
	}
	return view
}

// annotateOwnerFunds adds owner_funds to an expanded OfferCreate tx entry
// when the offer is not self-funded, matching LedgerToJson.cpp:206-224. The
// value is the offer owner's available funds for the TakerGets asset computed
// with fhIGNORE_FREEZE (so freezes do not zero the reported funds).
func annotateOwnerFunds(
	txEntry map[string]any,
	txJSON map[string]any,
	view types.LedgerStateView,
	reserveBase, reserveInc uint64,
) bool {
	if ledgerOwnerFundsUnsupportedMPT(txJSON) {
		return false
	}

	if funds, ok := TransactionOwnerFunds(txJSON, view, reserveBase, reserveInc); ok {
		txEntry["owner_funds"] = funds
	}
	return true
}

func ledgerOwnerFundsUnsupportedMPT(txJSON map[string]any) bool {
	// Released rippled 3.2.0 stops expanded transaction enumeration when
	// owner_funds reaches a non-issuer MPT offer.
	if txJSON["TransactionType"] != "OfferCreate" {
		return false
	}
	amount, ok := parseTransactionAmount(txJSON["TakerGets"])
	if !ok || !amount.IsMPT() {
		return false
	}
	account, _ := txJSON["Account"].(string)
	_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil || len(accountBytes) != 20 {
		return false
	}
	issuanceID, err := mptutil.DecodeID(amount.MPTIssuanceID())
	if err != nil {
		return false
	}
	var accountID [20]byte
	copy(accountID[:], accountBytes)
	issuerID := mptutil.Issuer(issuanceID)
	return accountID != issuerID
}

// dumpAccountState walks the full state tree and returns the accountState
// array rippled emits for accounts:true (LedgerToJson.cpp fillJsonState):
// expanded SLE JSON in JSON mode, {hash, tx_blob} in binary mode, or bare
// keys otherwise. The walk paginates GetLedgerData to cover every node.
func dumpAccountState(ctx *types.RPCContext, l types.LedgerReader, binary, expanded bool) []any {
	ledgerIndex := strconv.FormatUint(uint64(l.Sequence()), 10)
	state := make([]any, 0)
	marker := ""
	limit := LimitLedgerData.Default
	if binary {
		limit = LimitLedgerDataBinary.Default
	}
	for {
		result, err := ctx.Services.Ledger.GetLedgerData(ctx.Context, ledgerIndex, limit, marker)
		if err != nil || result == nil {
			break
		}
		for _, item := range result.State {
			upperIndex := strings.ToUpper(item.Index)
			switch {
			case binary:
				state = append(state, map[string]any{
					"hash":    upperIndex,
					"tx_blob": strings.ToUpper(hex.EncodeToString(item.Data)),
				})
			case expanded:
				if decoded, derr := decodeBinaryObject(item.Data); derr == nil {
					decoded["index"] = upperIndex
					state = append(state, decoded)
				} else {
					state = append(state, upperIndex)
				}
			default:
				state = append(state, upperIndex)
			}
		}
		if result.Marker == "" {
			break
		}
		marker = result.Marker
	}
	return state
}

// buildLedgerQueueData assembles the top-level queue_data array for the
// ledger method from the live TxQ, mirroring rippled fillJsonQueue
// (LedgerToJson.cpp:286-316). Each entry carries the per-tx fee/spend/auth
// fields plus the account, retry/preflight bookkeeping and the transaction
// body (tx for API v1, merged tx_json for v2+). Returns nil when the queue is
// empty or unwired.
func buildLedgerQueueData(
	ctx *types.RPCContext,
	binary, expanded, ownerFunds bool,
	ownerFundsView types.LedgerStateView,
	reserveBase, reserveInc uint64,
) ([]any, bool) {
	if ctx.Services == nil || ctx.Services.QueueAllTxs == nil {
		return nil, false
	}
	txs := ctx.Services.QueueAllTxs()
	if len(txs) == 0 {
		return nil, false
	}

	apiVersion := ctx.ApiVersion
	queueData := make([]any, 0, len(txs))
	for _, qtx := range txs {
		account, encErr := addresscodec.EncodeAccountIDToClassicAddress(qtx.Account[:])
		if encErr != nil {
			continue
		}
		entry := map[string]any{
			"fee_level":         strconv.FormatUint(qtx.FeeLevel, 10),
			"fee":               strconv.FormatUint(qtx.Fee, 10),
			"max_spend_drops":   strconv.FormatUint(qtx.MaxSpendDrops, 10),
			"auth_change":       qtx.AuthChange,
			"account":           account,
			"retries_remaining": qtx.RetriesRemaining,
			"preflight_result":  qtx.PreflightResult,
		}
		if qtx.LastValid != 0 {
			entry["LastLedgerSequence"] = qtx.LastValid
		}
		if qtx.HasLastResult {
			entry["last_result"] = qtx.LastResult
		}

		txBody := buildQueueTxBody(qtx, binary, expanded, apiVersion)
		if ownerFunds && expanded && ownerFundsView != nil {
			body, ok := txBody.(map[string]any)
			if ok && !annotateOwnerFunds(body, qtx.TxJSON, ownerFundsView, reserveBase, reserveInc) {
				queueData = append(queueData, entry)
				return queueData, true
			}
		}
		if body, ok := txBody.(map[string]any); ok {
			if apiVersion > 1 {
				for k, v := range body {
					entry[k] = v
				}
			} else {
				entry["tx"] = body
			}
		} else if apiVersion > 1 {
			entry["hash"] = txBody
		} else {
			entry["tx"] = txBody
		}

		queueData = append(queueData, entry)
	}
	return queueData, false
}

// buildQueueTxBody renders the queued transaction body the way
// fillJsonQueue's nested fillJsonTx call does (LedgerToJson.cpp:311): a hash
// or tx_blob in non-expanded / binary modes, otherwise the flattened tx
// fields with the hash injected.
func buildQueueTxBody(qtx types.QueuedTxInfo, binary, expanded bool, apiVersion int) any {
	hashStr := strings.ToUpper(hex.EncodeToString(qtx.TxID[:]))
	if !expanded {
		return hashStr
	}
	if binary {
		body := map[string]any{}
		if blob, err := binarycodec.Encode(qtx.TxJSON); err == nil {
			body["tx_blob"] = blob
		}
		if apiVersion > 1 {
			body["hash"] = hashStr
		}
		return body
	}
	if apiVersion > 1 {
		txJSON := projectTransactionJSON(qtx.TxJSON, "", apiVersion)
		return map[string]any{"tx_json": txJSON, "hash": hashStr, "validated": false}
	}
	return projectTransactionJSON(qtx.TxJSON, hashStr, apiVersion)
}

// expandTransaction builds an expanded transaction object from raw txData.
// It handles VL-encoded binary blobs and JSON StoredTransaction format.
//
// The output format varies by API version:
//   - API v1: tx fields at top level + "metaData" for metadata
//   - API v2+: "tx_json" key + "meta" key + "hash"
//
// For binary mode, tx_blob and meta_blob/meta are returned as hex strings.
// Reference: rippled LedgerToJson.cpp fillJsonTx()
func expandTransaction(
	txData []byte,
	hashStr string,
	binary bool,
	apiVersion int,
	ctx SyntheticMetadataContext,
) map[string]any {
	storedTx, err := decodeTxBlob(txData)
	if err == nil && storedTx.TxJSON != nil {
		return expandStoredTransaction(storedTx, hashStr, binary, apiVersion, ctx)
	}

	// Cannot decode: return raw blob
	txEntry := map[string]any{}
	txEntry["tx_blob"] = strings.ToUpper(hex.EncodeToString(txData))
	if apiVersion > 1 || !binary {
		txEntry["hash"] = hashStr
	}
	return txEntry
}

// expandStoredTransaction formats a JSON-stored transaction for the response.
func expandStoredTransaction(
	storedTx StoredTransaction,
	hashStr string,
	binary bool,
	apiVersion int,
	ctx SyntheticMetadataContext,
) map[string]any {
	txEntry := map[string]any{}

	if binary {
		// Encode tx_json back to binary hex
		txBlob, err := binarycodec.Encode(storedTx.TxJSON)
		if err == nil {
			txEntry["tx_blob"] = txBlob
		}
		if apiVersion > 1 {
			txEntry["hash"] = hashStr
		}
		// Encode metadata to binary hex
		if storedTx.Meta != nil {
			metaBlob, err := binarycodec.Encode(storedTx.Meta)
			if err == nil {
				if apiVersion > 1 {
					txEntry["meta_blob"] = metaBlob
				} else {
					txEntry["meta"] = metaBlob
				}
			}
		}
		return txEntry
	}

	if apiVersion > 1 {
		txEntry["tx_json"] = projectTransactionJSON(storedTx.TxJSON, "", apiVersion)
		txEntry["hash"] = hashStr
		if storedTx.Meta != nil {
			injectExpandedLedgerDeliveredAmount(storedTx.TxJSON, storedTx.Meta, ctx)
			InjectMPTokenIssuanceID(storedTx.TxJSON, storedTx.Meta)
			txEntry["meta"] = storedTx.Meta
		}
	} else {
		maps.Copy(txEntry, projectTransactionJSON(storedTx.TxJSON, hashStr, apiVersion))
		if storedTx.Meta != nil {
			injectExpandedLedgerDeliveredAmount(storedTx.TxJSON, storedTx.Meta, ctx)
			InjectMPTokenIssuanceID(storedTx.TxJSON, storedTx.Meta)
			txEntry["metaData"] = storedTx.Meta
		}
	}
	return txEntry
}

func injectExpandedLedgerDeliveredAmount(txJSON, meta map[string]any, ctx SyntheticMetadataContext) {
	txType, _ := txJSON["TransactionType"].(string)
	if txType == "Payment" || txType == "CheckCash" {
		InjectDeliveredAmount(txJSON, meta, ctx)
	}
}
