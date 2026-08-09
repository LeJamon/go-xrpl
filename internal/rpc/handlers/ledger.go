package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	ledgerheader "github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/rpc/txprojection"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/protocol"
)

// LedgerMethod handles the ledger RPC method.
type LedgerMethod struct{ baseHandler }

func (m *LedgerMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	if boolErr := validateLedgerBooleanOptions(params); boolErr != nil {
		return nil, boolErr
	}
	ledgerSpec, hasLedgerSelector, selectorErr := parseLedgerSpecifier(params)
	if selectorErr != nil {
		return nil, selectorErr
	}
	var request struct {
		types.LedgerSpecifier
		Accounts     bool            `json:"accounts,omitempty"`
		Full         bool            `json:"full,omitempty"`
		Transactions bool            `json:"transactions,omitempty"`
		Expand       bool            `json:"expand,omitempty"`
		OwnerFunds   bool            `json:"owner_funds,omitempty"`
		Binary       bool            `json:"binary,omitempty"`
		Queue        bool            `json:"queue,omitempty"`
		Type         json.RawMessage `json:"type,omitempty"`
	}

	if err := parseParams(params, &request); err != nil {
		return nil, err
	}
	request.LedgerSpecifier = ledgerSpec

	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	if !hasLedgerSelector {
		response, rpcErr := ledgerDefaultResponse(ctx)
		if rpcErr != nil {
			return nil, rpcErr
		}
		addLedgerTypeWarning(response, params)
		return response, nil
	}
	dumpQueue := request.Queue && hasLedgerSelector

	// Resolve the target before the permission gate, matching LedgerHandler::check.
	targetLedger, validated, lerr := lookupLedger(ctx, request.LedgerSpecifier)
	if lerr != nil {
		return nil, lerr
	}

	if request.Full || request.Accounts {
		if !ctx.Role.IsUnlimited() {
			return nil, rpcerrors.RpcErrorNoPermission("ledger")
		}
		if request.Binary {
			setLoadMedium(ctx)
		} else {
			setLoadHeavy(ctx)
		}
	}
	if dumpQueue && targetLedger.IsClosed() {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	if request.Full {
		request.Transactions = true
		request.Expand = true
		request.Accounts = true
	}

	ledgerInfo, ledgerInfoErr := buildLedgerJSON(targetLedger, request.Binary, request.Full, ctx.ApiVersion)
	if ledgerInfoErr != nil {
		return nil, rpcInternalError("ledger: map root lookup failed", ledgerInfoErr)
	}
	ledgerHash := FormatLedgerHash(targetLedger.Hash())

	closeTimeSec := targetLedger.CloseTime()
	closeTimeISO := protocol.FormatCloseTimeISO(protocol.FromRippleTime(uint32(max(closeTimeSec, 0))))
	syntheticContext := SyntheticMetadataContext{
		LedgerSequence: targetLedger.Sequence(),
		CloseTime:      closeTimeSec,
	}

	var ownerFundsAnnotator *ledgerOwnerFundsAnnotator
	if request.OwnerFunds && request.Expand {
		ownerFundsAnnotator = &ledgerOwnerFundsAnnotator{ctx: ctx, ledger: targetLedger}
	}

	if request.Transactions {
		var txList []any
		apiVersion := ctx.ApiVersion
		var decodeErr error
		var ownerFundsErr error
		visit := func(txHashKey [32]byte, txData []byte) bool {
			hashStr := strings.ToUpper(hex.EncodeToString(txHashKey[:]))
			if request.Expand {
				txEntry, err := expandTransaction(txData, hashStr, request.Binary, apiVersion, syntheticContext)
				if err != nil {
					decodeErr = err
					return false
				}
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
				if ownerFundsAnnotator != nil {
					storedTx, err := decodeTxBlob(txData)
					if err != nil {
						decodeErr = err
						return false
					}
					continueEnumeration, err := ownerFundsAnnotator.annotate(txEntry, storedTx.TxJSON)
					if err != nil {
						ownerFundsErr = err
						return false
					}
					if !continueEnumeration {
						return false
					}
				}
				txList = append(txList, txEntry)
			} else {
				txList = append(txList, hashStr)
			}
			return true
		}
		var iterErr error
		if contextual, ok := targetLedger.(interface {
			ForEachTransactionContext(context.Context, func([32]byte, []byte) bool) error
		}); ok {
			iterErr = contextual.ForEachTransactionContext(ctx.Context, visit)
		} else {
			iterErr = targetLedger.ForEachTransaction(visit)
		}
		if iterErr != nil {
			return nil, rpcInternalError("ledger: transaction iteration failed", iterErr)
		}
		if ownerFundsErr != nil {
			return nil, rpcInternalError("ledger: transaction owner_funds failed", ownerFundsErr)
		}
		if decodeErr != nil {
			// LedgerToJson treats a malformed stored transaction as a corrupt
			// leaf, logs it, and returns the entries decoded before that leaf.
			// Keep that partial-success behavior instead of turning the entire
			// ledger response into a database-deserialization RPC error.
			logRpcError("ledger: transaction decoding failed", decodeErr)
		}
		if txList == nil {
			txList = []any{}
		}
		ledgerInfo["transactions"] = txList
	}

	// accounts (LedgerFill::dumpState) dumps the full state tree into the
	// ledger object under accountState (LedgerToJson.cpp fillJsonState).
	if request.Accounts {
		accountState, err := dumpAccountState(ctx, targetLedger, request.Binary, request.Expand)
		if err != nil {
			return nil, rpcInternalError("ledger: state dump failed", err)
		}
		ledgerInfo["accountState"] = accountState
	}

	response := map[string]any{
		"ledger":    ledgerInfo,
		"validated": validated,
	}
	if targetLedger.IsClosed() {
		response["ledger_hash"] = ledgerHash
		response["ledger_index"] = targetLedger.Sequence()
	} else {
		response["ledger_current_index"] = targetLedger.Sequence()
	}

	if dumpQueue {
		queueData, queueInternalError, queueOwnerFundsErr := buildLedgerQueueData(
			ctx,
			request.Binary,
			request.Expand,
			ownerFundsAnnotator,
		)
		if queueOwnerFundsErr != nil {
			return nil, rpcInternalError("ledger: queue owner_funds failed", queueOwnerFundsErr)
		}
		if len(queueData) > 0 {
			response["queue_data"] = queueData
		}
		if queueInternalError {
			return nil, rpcInternalInvariantError("ledger: queue owner_funds failed for MPT OfferCreate").WithExtra(response)
		}
	}
	addLedgerTypeWarning(response, params)

	return response, nil
}

func buildLedgerJSON(l types.LedgerReader, binaryMode, full bool, apiVersion int) (map[string]any, error) {
	if binaryMode {
		if !l.IsClosed() {
			return map[string]any{"closed": false}, nil
		}
		txHash, stateHash := ledgerMapHashes(l)
		return map[string]any{
			"closed": true,
			"ledger_data": strings.ToUpper(hex.EncodeToString(ledgerheader.AddRaw(ledgerheader.LedgerHeader{
				LedgerIndex:         l.Sequence(),
				ParentCloseTime:     protocol.FromRippleTime(uint32(max(l.ParentCloseTime(), 0))),
				ParentHash:          l.ParentHash(),
				TxHash:              txHash,
				AccountHash:         stateHash,
				Drops:               l.TotalDrops(),
				CloseFlags:          l.CloseFlags(),
				CloseTimeResolution: uint8(l.CloseTimeResolution()),
				CloseTime:           protocol.FromRippleTime(uint32(max(l.CloseTime(), 0))),
			}, false))),
		}, nil
	}

	parentHash := l.ParentHash()
	result := map[string]any{
		"parent_hash": strings.ToUpper(hex.EncodeToString(parentHash[:])),
	}
	if apiVersion > 1 {
		result["ledger_index"] = l.Sequence()
	} else {
		result["ledger_index"] = strconv.FormatUint(uint64(l.Sequence()), 10)
	}

	if l.IsClosed() {
		result["closed"] = true
	} else if !full {
		result["closed"] = false
		return result, nil
	}

	txHash, stateHash := ledgerMapHashes(l)
	result["ledger_hash"] = FormatLedgerHash(l.Hash())
	result["transaction_hash"] = FormatLedgerHash(txHash)
	result["account_hash"] = FormatLedgerHash(stateHash)
	result["total_coins"] = strconv.FormatUint(l.TotalDrops(), 10)
	result["close_flags"] = l.CloseFlags()
	result["parent_close_time"] = l.ParentCloseTime()
	result["close_time"] = l.CloseTime()
	result["close_time_resolution"] = l.CloseTimeResolution()
	if l.CloseTime() != 0 {
		closeTime := protocol.FromRippleTime(uint32(max(l.CloseTime(), 0)))
		result["close_time_human"] = protocol.FormatCloseTimeHuman(closeTime)
		result["close_time_iso"] = protocol.FormatCloseTimeISO(closeTime)
		if l.CloseFlags()&ledgerheader.LCFNoConsensusTime != 0 {
			result["close_time_estimated"] = true
		}
	}
	return result, nil
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
		ID: types.WarningFieldsDeprecated,
		Message: "Some fields from your request are deprecated. Please check the documentation at " +
			"https://xrpl.org/docs/references/http-websocket-apis/ and update your request. " +
			"Field `type` is deprecated.",
	}}
}

func validateLedgerBooleanOptions(params json.RawMessage) *rpcerrors.RpcError {
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
			return rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
		if _, ok := value.(bool); !ok {
			return rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
	}
	return nil
}

func ledgerRequestHasSelector(params json.RawMessage) (bool, *rpcerrors.RpcError) {
	if params == nil {
		return false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return false, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	present := make([]string, 0, 1)
	for _, name := range []string{"ledger", "ledger_hash", "ledger_index"} {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		present = append(present, name)
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return false, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
	}
	if len(present) > 1 {
		if _, hasLegacy := fields["ledger"]; hasLegacy {
			return false, rpcerrors.RpcErrorInvalidParams("Exactly one of 'ledger', 'ledger_hash', or 'ledger_index' can be specified.")
		}
		return false, rpcerrors.RpcErrorInvalidParams("Exactly one of 'ledger_hash' or 'ledger_index' can be specified.")
	}
	if len(present) == 0 {
		return false, nil
	}

	name := present[0]
	var value any
	if err := json.Unmarshal(fields[name], &value); err != nil {
		return false, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	if name == "ledger_hash" {
		hash, ok := value.(string)
		if !ok || len(hash) != 64 {
			return false, rpcerrors.RpcErrorInvalidParams("Invalid field 'ledger_hash', not hex string.")
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return false, rpcerrors.RpcErrorInvalidParams("Invalid field 'ledger_hash', not hex string.")
		}
		return true, nil
	}
	if _, ok := value.(string); ok {
		return true, nil
	}
	if _, ok := value.(float64); ok {
		rawNumber := strings.TrimSpace(string(fields[name]))
		if strings.ContainsAny(rawNumber, ".eE") {
			return false, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid field '%s', not string or number.", name))
		}
		return true, nil
	}
	return false, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid field '%s', not string or number.", name))
}

func ledgerDefaultResponse(ctx *types.RpcContext) (map[string]any, *rpcerrors.RpcError) {
	closed, err := ctx.Services.Ledger().GetLedgerBySequence(ctx.Services.Ledger().GetClosedLedgerIndex())
	if err != nil || closed == nil {
		return nil, rpcerrors.RpcErrorLgrNotFound("ledgerNotFound")
	}
	open, err := ctx.Services.Ledger().GetLedgerBySequence(ctx.Services.Ledger().GetCurrentLedgerIndex())
	if err != nil || open == nil {
		return nil, rpcerrors.RpcErrorLgrNotFound("ledgerNotFound")
	}
	return map[string]any{
		"closed": buildLedgerSummaryJSON(closed, true, ctx.ApiVersion),
		"open":   buildLedgerSummaryJSON(open, false, ctx.ApiVersion),
	}, nil
}

// ownerFundsLedgerView resolves the state view for the target ledger so
// owner_funds can be computed against that ledger rather than current state.
// Missing capabilities, lookup failures, and nil results are operational
// errors because silently omitting owner_funds would produce a partial reply.
func ownerFundsLedgerView(ctx *types.RpcContext, l types.LedgerReader) (types.LedgerStateView, error) {
	src, ok := ctx.Services.Ledger().(types.LedgerViewSource)
	if !ok {
		return nil, errors.New("ledger service does not expose state views")
	}
	if l.IsClosed() {
		view, _, err := src.GetLedgerViewByHash(l.Hash())
		if err != nil {
			return nil, err
		}
		if view == nil {
			return nil, errors.New("ledger view lookup returned nil")
		}
		return view, nil
	}
	view, _, err := src.GetLedgerViewBySeq(l.Sequence())
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errors.New("ledger view lookup returned nil")
	}
	return view, nil
}

type ledgerOwnerFundsAnnotator struct {
	ctx          *types.RpcContext
	ledger       types.LedgerReader
	view         types.LedgerStateView
	reservesRead bool
	reserveBase  uint64
	reserveInc   uint64
}

func (a *ledgerOwnerFundsAnnotator) annotate(txEntry, txJSON map[string]any) (bool, error) {
	if ledgerOwnerFundsUnsupportedMPT(txJSON) {
		return false, nil
	}
	applicable, needsReserves := TransactionOwnerFundsRequirements(txJSON)
	if !applicable {
		return true, nil
	}
	if a.view == nil {
		view, err := ownerFundsLedgerView(a.ctx, a.ledger)
		if err != nil {
			return false, fmt.Errorf("owner_funds view lookup: %w", err)
		}
		a.view = view
	}
	if needsReserves && !a.reservesRead {
		_, fallbackBase, fallbackInc := a.ctx.Services.Ledger().GetCurrentFees()
		reserveBase, reserveInc, err := reserveSettingsFromLedger(a.view, fallbackBase, fallbackInc)
		if err != nil {
			return false, fmt.Errorf("owner_funds reserve lookup: %w", err)
		}
		a.reserveBase = reserveBase
		a.reserveInc = reserveInc
		a.reservesRead = true
	}
	return annotateOwnerFunds(txEntry, txJSON, a.view, a.reserveBase, a.reserveInc)
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
) (bool, error) {
	if ledgerOwnerFundsUnsupportedMPT(txJSON) {
		return false, nil
	}

	funds, ok, err := TransactionOwnerFunds(txJSON, view, reserveBase, reserveInc)
	if err != nil {
		return false, err
	}
	if ok {
		txEntry["owner_funds"] = funds
	}
	return true, nil
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
// keys otherwise.
func dumpAccountState(ctx *types.RpcContext, l types.LedgerReader, binary, expanded bool) ([]any, error) {
	state := make([]any, 0)
	appendItem := func(index string, data []byte) error {
		upperIndex := strings.ToUpper(index)
		switch {
		case binary:
			state = append(state, map[string]any{
				"hash":    upperIndex,
				"tx_blob": strings.ToUpper(hex.EncodeToString(data)),
			})
		case expanded:
			decoded, err := binarycodec.Decode(hex.EncodeToString(data))
			if err != nil {
				return fmt.Errorf("decode ledger entry %s: %w", upperIndex, err)
			}
			decoded["index"] = upperIndex
			addLedgerEntryJSONFields(decoded, upperIndex)
			state = append(state, decoded)
		default:
			state = append(state, upperIndex)
		}
		return nil
	}
	if source, ok := l.(types.ContextLedgerStateSource); ok {
		var itemErr error
		err := source.ForEachLedgerStateContext(ctx.Context, func(key [32]byte, data []byte) bool {
			itemErr = appendItem(hex.EncodeToString(key[:]), data)
			return itemErr == nil
		})
		if err != nil {
			return nil, err
		}
		if itemErr != nil {
			return nil, itemErr
		}
		return state, nil
	}

	ledgerIndex := "current"
	if l.IsClosed() {
		hash := l.Hash()
		ledgerIndex = hex.EncodeToString(hash[:])
	}
	marker := ""
	limit := limitLedgerData.Default
	if binary {
		limit = limitLedgerDataBinary.Default
	}
	for {
		result, err := ctx.Services.Ledger().GetLedgerData(ctx.Context, ledgerIndex, limit, marker)
		if err != nil {
			return nil, err
		}
		if result == nil {
			break
		}
		for _, item := range result.State {
			if err := appendItem(item.Index, item.Data); err != nil {
				return nil, err
			}
		}
		if result.Marker == "" {
			break
		}
		marker = result.Marker
	}
	return state, nil
}

// buildLedgerQueueData assembles the top-level queue_data array for the
// ledger method from the live TxQ, mirroring rippled fillJsonQueue
// (LedgerToJson.cpp:286-316). Each entry carries the per-tx fee/spend/auth
// fields plus the account, retry/preflight bookkeeping and the transaction
// body (tx for API v1, merged tx_json for v2+). Returns nil when the queue is
// empty or unwired.
func buildLedgerQueueData(
	ctx *types.RpcContext,
	binary, expanded bool,
	ownerFundsAnnotator *ledgerOwnerFundsAnnotator,
) ([]any, bool, error) {
	if ctx.Services == nil || ctx.Services.QueueAllTxs() == nil {
		return nil, false, nil
	}
	txs := ctx.Services.QueueAllTxs()()
	if len(txs) == 0 {
		return nil, false, nil
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
		if expanded && ownerFundsAnnotator != nil {
			body, ok := txBody.(map[string]any)
			if ok {
				continueEnumeration, err := ownerFundsAnnotator.annotate(body, qtx.TxJSON)
				if err != nil {
					return nil, false, err
				}
				if !continueEnumeration {
					queueData = append(queueData, entry)
					return queueData, true, nil
				}
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
	return queueData, false, nil
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
		txJSON := txprojection.ProjectJSON(qtx.TxJSON, "", apiVersion)
		return map[string]any{"tx_json": txJSON, "hash": hashStr, "validated": false}
	}
	return txprojection.ProjectJSON(qtx.TxJSON, hashStr, apiVersion)
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
) (map[string]any, error) {
	storedTx, err := decodeTxBlob(txData)
	if err != nil {
		return nil, err
	}
	if storedTx.TxJSON != nil {
		return expandStoredTransaction(storedTx, hashStr, binary, apiVersion, ctx)
	}
	return nil, fmt.Errorf("stored transaction has no transaction JSON")
}

// expandStoredTransaction formats a JSON-stored transaction for the response.
func expandStoredTransaction(
	storedTx StoredTransaction,
	hashStr string,
	binary bool,
	apiVersion int,
	ctx SyntheticMetadataContext,
) (map[string]any, error) {
	txEntry := map[string]any{}

	if binary {
		// Encode tx_json back to binary hex
		txBlob, err := binarycodec.Encode(storedTx.TxJSON)
		if err == nil {
			txEntry["tx_blob"] = txBlob
		} else {
			return nil, fmt.Errorf("encode transaction: %w", err)
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
			} else {
				return nil, fmt.Errorf("encode transaction metadata: %w", err)
			}
		}
		return txEntry, nil
	}

	if apiVersion > 1 {
		txEntry["tx_json"] = txprojection.ProjectJSON(storedTx.TxJSON, "", apiVersion)
		txEntry["hash"] = hashStr
		if storedTx.Meta != nil {
			injectExpandedLedgerDeliveredAmount(storedTx.TxJSON, storedTx.Meta, ctx)
			injectMPTokenIssuanceID(storedTx.TxJSON, storedTx.Meta)
			txEntry["meta"] = storedTx.Meta
		}
	} else {
		maps.Copy(txEntry, txprojection.ProjectJSON(storedTx.TxJSON, hashStr, apiVersion))
		if storedTx.Meta != nil {
			injectExpandedLedgerDeliveredAmount(storedTx.TxJSON, storedTx.Meta, ctx)
			injectMPTokenIssuanceID(storedTx.TxJSON, storedTx.Meta)
			txEntry["metaData"] = storedTx.Meta
		}
	}
	return txEntry, nil
}

func injectExpandedLedgerDeliveredAmount(txJSON, meta map[string]any, ctx SyntheticMetadataContext) {
	txType, _ := txJSON["TransactionType"].(string)
	if txType == "Payment" || txType == "CheckCash" {
		InjectDeliveredAmount(txJSON, meta, ctx)
	}
}
