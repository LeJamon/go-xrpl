package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// rippleEpochTime is 2000-01-01T00:00:00Z
var rippleEpochTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// LedgerMethod handles the ledger RPC method.
type LedgerMethod struct{ BaseHandler }

func (m *LedgerMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
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

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// full and accounts dump every state node; rippled gates both behind an
	// unlimited (admin / identified) role else rpcNO_PERMISSION
	// (LedgerHandler.cpp:66-72). full also implies expand + transactions +
	// accounts (LedgerToJson.cpp isFull/isExpanded).
	if request.Full || request.Accounts {
		if !ctx.Unlimited {
			return nil, types.RpcErrorNoPermission("ledger")
		}
	}
	if request.Full {
		request.Transactions = true
		request.Expand = true
		request.Accounts = true
	}

	// Resolve the target ledger through the shared lookup (rippled
	// RPC::lookupLedger), which defaults to the current ledger and emits the
	// rippled-faithful ledgerHashMalformed / ledgerIndexMalformed /
	// ledgerNotFound errors.
	targetLedger, validated, lerr := LookupLedger(ctx, request.LedgerSpecifier)
	if lerr != nil {
		return nil, lerr
	}

	// Build ledger info (shared with ledger_request).
	ledgerInfo := ledgerInfoJSON(targetLedger)
	ledgerHash := ledgerInfo["ledger_hash"].(string)

	closeTimeSec := targetLedger.CloseTime()
	closeTimeISO := rippleEpochTime.Add(time.Duration(closeTimeSec) * time.Second).UTC().Format(time.RFC3339)

	_, reserveBase, reserveInc := ctx.Services.Ledger.GetCurrentFees()

	if request.Transactions {
		var txList []any
		apiVersion := ctx.ApiVersion
		// owner_funds only annotates expanded (non-binary) OfferCreate txs.
		var ownerFundsView types.LedgerStateView
		if request.OwnerFunds && request.Expand && !request.Binary {
			ownerFundsView = ownerFundsLedgerView(ctx, targetLedger)
		}
		targetLedger.ForEachTransaction(func(txHashKey [32]byte, txData []byte) bool {
			hashStr := strings.ToUpper(hex.EncodeToString(txHashKey[:]))
			if request.Expand {
				txEntry := expandTransaction(txData, hashStr, request.Binary, apiVersion)
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
					annotateOwnerFunds(txEntry, apiVersion, ownerFundsView, reserveBase, reserveInc)
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
		"ledger":       ledgerInfo,
		"ledger_hash":  ledgerHash,
		"ledger_index": targetLedger.Sequence(),
		"validated":    validated,
	}

	response["reserve_base_drops"] = fmt.Sprintf("%d", reserveBase)
	response["reserve_inc_drops"] = fmt.Sprintf("%d", reserveInc)

	if request.Queue {
		if queueData := buildLedgerQueueData(ctx, request.Binary, request.Expand); len(queueData) > 0 {
			response["queue_data"] = queueData
		}
	}

	return response, nil
}

// ownerFundsLedgerView resolves the state view for the target ledger so
// owner_funds can be computed against it, mirroring rippled's accountFunds
// call against fill.ledger (LedgerToJson.cpp:216-221). Returns nil when the
// service can't supply a view for that ledger (mocks, unsupported selectors),
// in which case the annotation is simply omitted.
func ownerFundsLedgerView(ctx *types.RpcContext, l types.LedgerReader) types.LedgerStateView {
	src, ok := ctx.Services.Ledger.(types.LedgerViewSource)
	if !ok {
		return nil
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
func annotateOwnerFunds(txEntry map[string]any, apiVersion int, view types.LedgerStateView, reserveBase, reserveInc uint64) {
	txFields := txEntry
	if apiVersion > 1 {
		inner, ok := txEntry["tx_json"].(map[string]any)
		if !ok {
			return
		}
		txFields = inner
	}

	if txFields["TransactionType"] != "OfferCreate" {
		return
	}
	account, _ := txFields["Account"].(string)
	if account == "" {
		return
	}
	amount, ok := parseLedgerAmount(txFields["TakerGets"])
	if !ok {
		return
	}

	// Self-funded offers (issuer == account) carry no owner_funds.
	if !amount.IsNative() && amount.Issuer == account {
		return
	}

	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil || len(idBytes) != 20 {
		return
	}
	var accountID [20]byte
	copy(accountID[:], idBytes)

	funds := tx.AccountFunds(view, accountID, amount, false, reserveBase, reserveInc)
	txEntry["owner_funds"] = funds.Value()
}

// parseLedgerAmount converts a transaction-JSON amount value (an XRP drops
// string or an issued-currency object) into a state.Amount via the codec's
// own unmarshaler, so XRP and IOU shapes are handled identically to the rest
// of the stack.
func parseLedgerAmount(raw any) (state.Amount, bool) {
	if raw == nil {
		return state.Amount{}, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return state.Amount{}, false
	}
	var amount state.Amount
	if err := json.Unmarshal(encoded, &amount); err != nil {
		return state.Amount{}, false
	}
	return amount, true
}

// dumpAccountState walks the full state tree and returns the accountState
// array rippled emits for accounts:true (LedgerToJson.cpp fillJsonState):
// expanded SLE JSON in JSON mode, {hash, tx_blob} in binary mode, or bare
// keys otherwise. The walk paginates GetLedgerData to cover every node.
func dumpAccountState(ctx *types.RpcContext, l types.LedgerReader, binary, expanded bool) []any {
	ledgerIndex := strconv.FormatUint(uint64(l.Sequence()), 10)
	state := make([]any, 0)
	marker := ""
	for {
		result, err := ctx.Services.Ledger.GetLedgerData(ctx.Context, ledgerIndex, 0, marker)
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
				if decoded, derr := binarycodec.Decode(hex.EncodeToString(item.Data)); derr == nil {
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
func buildLedgerQueueData(ctx *types.RpcContext, binary, expanded bool) []any {
	if ctx.Services == nil || ctx.Services.QueueAllTxs == nil {
		return nil
	}
	txs := ctx.Services.QueueAllTxs()
	if len(txs) == 0 {
		return nil
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
		if apiVersion > 1 {
			for k, v := range txBody {
				entry[k] = v
			}
		} else {
			entry["tx"] = txBody
		}

		queueData = append(queueData, entry)
	}
	return queueData
}

// buildQueueTxBody renders the queued transaction body the way
// fillJsonQueue's nested fillJsonTx call does (LedgerToJson.cpp:311): a hash
// or tx_blob in non-expanded / binary modes, otherwise the flattened tx
// fields with the hash injected.
func buildQueueTxBody(qtx types.QueuedTxInfo, binary, expanded bool, apiVersion int) map[string]any {
	hashStr := strings.ToUpper(hex.EncodeToString(qtx.TxID[:]))
	if !expanded {
		return map[string]any{"hash": hashStr}
	}
	if binary {
		body := map[string]any{"hash": hashStr}
		if blob, err := binarycodec.Encode(qtx.TxJSON); err == nil {
			body["tx_blob"] = blob
		}
		return body
	}
	if apiVersion > 1 {
		return map[string]any{"tx_json": qtx.TxJSON, "hash": hashStr}
	}
	body := make(map[string]any, len(qtx.TxJSON)+1)
	maps.Copy(body, qtx.TxJSON)
	body["hash"] = hashStr
	return body
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
func expandTransaction(txData []byte, hashStr string, binary bool, apiVersion int) map[string]any {
	storedTx, err := decodeTxBlob(txData)
	if err == nil && storedTx.TxJSON != nil {
		return expandStoredTransaction(storedTx, hashStr, binary, apiVersion)
	}

	// Cannot decode: return raw blob
	txEntry := map[string]any{}
	txEntry["tx_blob"] = strings.ToUpper(hex.EncodeToString(txData))
	txEntry["hash"] = hashStr
	return txEntry
}

// expandStoredTransaction formats a JSON-stored transaction for the response.
func expandStoredTransaction(storedTx StoredTransaction, hashStr string, binary bool, apiVersion int) map[string]any {
	txEntry := map[string]any{}

	if binary {
		// Encode tx_json back to binary hex
		txBlob, err := binarycodec.Encode(storedTx.TxJSON)
		if err == nil {
			txEntry["tx_blob"] = txBlob
		}
		txEntry["hash"] = hashStr
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
		// API v2+: use tx_json and meta keys
		txEntry["tx_json"] = storedTx.TxJSON
		txEntry["hash"] = hashStr
		if storedTx.Meta != nil {
			InjectDeliveredAmount(storedTx.TxJSON, storedTx.Meta)
			txEntry["meta"] = storedTx.Meta
		}
	} else {
		// API v1: flatten tx fields at top level, metadata under "metaData"
		maps.Copy(txEntry, storedTx.TxJSON)
		txEntry["hash"] = hashStr
		if storedTx.Meta != nil {
			InjectDeliveredAmount(storedTx.TxJSON, storedTx.Meta)
			txEntry["metaData"] = storedTx.Meta
		}
	}
	return txEntry
}
