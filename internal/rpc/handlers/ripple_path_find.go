package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
	"github.com/LeJamon/go-xrpl/keylet"
)

// maxSrcCurrencies is the maximum number of explicit source_currencies
// entries (rippled RPC::Tuning::max_src_cur).
const maxSrcCurrencies = 18

// ripplePathFindResponse represents the ripple_path_find RPC response.
// Reference: rippled PathRequest::doUpdate() builds newStatus with these
// fields; the ledger fields are merged in by doRipplePathFind via
// RPC::lookupLedger when the caller selects an explicit ledger
// (RipplePathFind.cpp:160-174). Note rippled's final reply carries no
// destination_tag: PathRequest::isValid sets it on jvStatus, but doUpdate
// replaces jvStatus wholesale with newStatus, which never includes it.
type ripplePathFindResponse struct {
	Alternatives          []pathAlternativeJSON `json:"alternatives"`
	DestinationAccount    string                `json:"destination_account"`
	DestinationAmount     any                   `json:"destination_amount"`
	DestinationCurrencies []string              `json:"destination_currencies"`
	FullReply             bool                  `json:"full_reply"`
	ID                    json.RawMessage       `json:"id,omitempty"`
	SourceAccount         string                `json:"source_account"`
	LedgerCurrentIndex    uint32                `json:"ledger_current_index,omitempty"`
	LedgerHash            string                `json:"ledger_hash,omitempty"`
	LedgerIndex           uint32                `json:"ledger_index,omitempty"`
	Validated             *bool                 `json:"validated,omitempty"`
}

type pathAlternativeJSON struct {
	// DestinationAmount is only present for convert-all requests
	// (destination_amount: -1), reporting the maximum deliverable amount.
	DestinationAmount any                  `json:"destination_amount,omitempty"`
	PathsCanonical    []any                `json:"paths_canonical"`
	PathsComputed     [][]payment.PathStep `json:"paths_computed"`
	SourceAmount      any                  `json:"source_amount"`
}

// RipplePathFindMethod handles the ripple_path_find RPC method.
// Reference: rippled RipplePathFind.cpp + PathRequest::parseJson/isValid.
type RipplePathFindMethod struct{}

func (m *RipplePathFindMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	release, rpcErr := AcquirePathfind(ctx)
	if rpcErr != nil {
		return nil, rpcErr
	}
	defer release()

	probe := map[string]json.RawMessage{}
	if params != nil {
		if err := json.Unmarshal(params, &probe); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	// Field validation follows rippled PathRequest::parseJson order exactly.
	rawSrc, ok := probe["source_account"]
	if !ok {
		return nil, types.RpcErrorSrcActMissing("Source account not provided.")
	}
	rawDst, ok := probe["destination_account"]
	if !ok {
		return nil, types.RpcErrorDstActMissing("Destination account not provided.")
	}
	rawDstAmount, ok := probe["destination_amount"]
	if !ok {
		return nil, types.RpcErrorDstAmtMissing("Destination amount/currency/issuer is missing.")
	}

	srcAccount, ok := decodeAccountRaw(rawSrc)
	if !ok {
		return nil, types.RpcErrorSrcActMalformed("Source account is malformed.")
	}
	dstAccount, ok := decodeAccountRaw(rawDst)
	if !ok {
		return nil, types.RpcErrorDstActMalformed("Destination account is malformed.")
	}

	dstAmount, err := parsePathFindAmount(rawDstAmount)
	if err != nil {
		return nil, types.RpcErrorDstAmtMalformed("Destination amount/currency/issuer is malformed.")
	}

	// destination_amount of exactly -1 selects convert-all mode.
	// Reference: rippled PathRequest::parseJson convert_all_ check.
	convertAll := dstAmount.Value() == "-1"
	if !convertAll && dstAmount.Signum() <= 0 {
		return nil, types.RpcErrorDstAmtMalformed("Destination amount/currency/issuer is malformed.")
	}

	var sendMax *state.Amount
	if rawSendMax, hasSendMax := probe["send_max"]; hasSendMax {
		// send_max requires destination_amount to be -1.
		if !convertAll {
			return nil, types.RpcErrorDstAmtMalformed("Destination amount/currency/issuer is malformed.")
		}
		amt, smErr := parsePathFindAmount(rawSendMax)
		if smErr != nil || (amt.Signum() <= 0 && amt.Value() != "-1") {
			return nil, types.RpcErrorSendMaxMalformed("SendMax amount malformed.")
		}
		sendMax = &amt
	}

	srcCurrencies, rpcErr := parseSourceCurrencies(probe, srcAccount, sendMax)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Ledger selection: an explicit ledger_hash/ledger_index resolves a
	// specific ledger and merges its metadata into the response, mirroring
	// rippled's RPC::lookupLedger merge; otherwise the closed ledger is used
	// with no ledger fields in the reply.
	view, meta, rpcErr := resolvePathFindLedger(ctx, probe)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Existence checks. Reference: rippled PathRequest::isValid.
	if exists, _ := view.Exists(keylet.Account(srcAccount)); !exists {
		return nil, types.RpcErrorSrcActNotFound("Source account not found.")
	}
	if exists, _ := view.Exists(keylet.Account(dstAccount)); !exists {
		// Only XRP can be sent to a non-existent account, and the payment
		// must meet the account reserve.
		if !dstAmount.IsNative() {
			return nil, types.RpcErrorActNotFound("Account not found.")
		}
		if !convertAll {
			_, reserveBase, _ := ctx.Services.Ledger.GetCurrentFees()
			if dstAmount.Drops() < int64(reserveBase) {
				return nil, types.RpcErrorDstAmtMalformed("Destination amount/currency/issuer is malformed.")
			}
		}
	}

	// Run pathfinding at the production search level (rippled PATH_SEARCH).
	pr := pathfinder.NewPathRequest(srcAccount, dstAccount, dstAmount, sendMax, srcCurrencies, convertAll)
	result := pr.Execute(view)
	if result.SourceCurrencyOverflow {
		return nil, types.RpcErrorInternal("Internal error.")
	}

	response := ripplePathFindResponse{
		DestinationAccount:    state.EncodeAccountIDSafe(dstAccount),
		DestinationAmount:     formatAmountJSON(dstAmount),
		DestinationCurrencies: result.DestinationCurrencies,
		FullReply:             true, // legacy path always does a full reply (!fast)
		ID:                    probe["id"],
		SourceAccount:         state.EncodeAccountIDSafe(srcAccount),
	}

	if meta != nil {
		if meta.current {
			response.LedgerCurrentIndex = meta.seq
		} else {
			response.LedgerHash = FormatLedgerHash(meta.hash)
			response.LedgerIndex = meta.seq
		}
		response.Validated = &meta.validated
	}

	for _, alt := range result.Alternatives {
		pathsComputed := alt.PathsComputed
		if pathsComputed == nil {
			pathsComputed = [][]payment.PathStep{}
		}
		jAlt := pathAlternativeJSON{
			// paths_canonical is always an empty array for the legacy
			// ripple_path_find API (rippled PathRequest::findPaths).
			PathsCanonical: []any{},
			PathsComputed:  pathsComputed,
			SourceAmount:   formatAmountJSON(alt.SourceAmount),
		}
		if convertAll {
			jAlt.DestinationAmount = formatAmountJSON(alt.DestinationAmount)
		}
		response.Alternatives = append(response.Alternatives, jAlt)
	}

	if response.Alternatives == nil {
		response.Alternatives = []pathAlternativeJSON{}
	}
	if response.DestinationCurrencies == nil {
		response.DestinationCurrencies = []string{}
	}
	sort.Strings(response.DestinationCurrencies)

	// Return a map so the server envelope flattens the fields directly
	// into `result` like rippled; non-map results get wrapped under
	// `result.data`, which no XRPL client understands.
	encoded, mErr := json.Marshal(response)
	if mErr != nil {
		return nil, types.RpcErrorInternal("Internal error.")
	}
	var flat map[string]any
	if uErr := json.Unmarshal(encoded, &flat); uErr != nil {
		return nil, types.RpcErrorInternal("Internal error.")
	}
	return flat, nil
}

func (m *RipplePathFindMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *RipplePathFindMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *RipplePathFindMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

// decodeAccountRaw decodes a JSON string into an AccountID. Returns false
// for non-string values or malformed addresses.
func decodeAccountRaw(raw json.RawMessage) ([20]byte, bool) {
	var addr string
	if err := json.Unmarshal(raw, &addr); err != nil {
		return [20]byte{}, false
	}
	id, err := state.DecodeAccountID(addr)
	if err != nil {
		return [20]byte{}, false
	}
	return id, true
}

// parseSourceCurrencies validates the optional source_currencies array,
// following rippled PathRequest::parseJson: max 18 entries, mandatory
// currency, optional issuer, XRP may not carry an issuer, a missing issuer
// defaults to the source account, and entries are reconciled against
// send_max when present.
func parseSourceCurrencies(
	probe map[string]json.RawMessage,
	srcAccount [20]byte,
	sendMax *state.Amount,
) ([]payment.Issue, *types.RpcError) {
	rawSC, ok := probe["source_currencies"]
	if !ok {
		return nil, nil
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(rawSC, &entries); err != nil || len(entries) == 0 || len(entries) > maxSrcCurrencies {
		return nil, types.RpcErrorSrcCurMalformed("Source currency is malformed.")
	}

	var sendMaxCurrency string
	var sendMaxIssuer [20]byte
	if sendMax != nil {
		sendMaxCurrency = "XRP"
		if !sendMax.IsNative() {
			sendMaxCurrency = sendMax.Currency
			sendMaxIssuer, _ = state.DecodeAccountID(sendMax.Issuer)
		}
	}

	var srcCurrencies []payment.Issue
	seen := make(map[payment.Issue]bool)
	add := func(issue payment.Issue) {
		if !seen[issue] {
			seen[issue] = true
			srcCurrencies = append(srcCurrencies, issue)
		}
	}

	for _, raw := range entries {
		var sc struct {
			Currency *string         `json:"currency"`
			Issuer   json.RawMessage `json:"issuer"`
		}
		if err := json.Unmarshal(raw, &sc); err != nil || sc.Currency == nil || !isValidCurrencyCode(*sc.Currency) {
			return nil, types.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}
		currency := canonCurrency(*sc.Currency)
		isXRPCur := currency == "XRP"

		var issuerID [20]byte
		if sc.Issuer != nil && !isJSONNull(sc.Issuer) {
			var issuerStr string
			if err := json.Unmarshal(sc.Issuer, &issuerStr); err != nil {
				return nil, types.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
			}
			id, err := state.DecodeAccountID(issuerStr)
			if err != nil {
				return nil, types.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
			}
			issuerID = id
		}

		if isXRPCur {
			if issuerID != ([20]byte{}) {
				return nil, types.RpcErrorSrcCurMalformed("Source currency is malformed.")
			}
		} else if issuerID == ([20]byte{}) {
			issuerID = srcAccount
		}

		if sendMax != nil {
			// If the currencies don't match, ignore the source currency.
			if currency != canonCurrency(sendMaxCurrency) {
				continue
			}
			// If neither issuer is the source and they are not equal, the
			// source issuer is illegal.
			if issuerID != srcAccount && sendMaxIssuer != srcAccount && issuerID != sendMaxIssuer {
				return nil, types.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
			}
			// If both are the source, use the source; otherwise use the one
			// that's not the source.
			if issuerID != srcAccount {
				add(payment.Issue{Currency: currency, Issuer: issuerID})
			} else if sendMaxIssuer != srcAccount {
				add(payment.Issue{Currency: currency, Issuer: sendMaxIssuer})
			} else {
				add(payment.Issue{Currency: currency, Issuer: srcAccount})
			}
			continue
		}

		add(payment.Issue{Currency: currency, Issuer: issuerID})
	}

	return srcCurrencies, nil
}

// pathFindLedgerMeta carries the metadata of an explicitly selected ledger,
// merged into the response like rippled's RPC::lookupLedger result.
type pathFindLedgerMeta struct {
	current   bool
	seq       uint32
	hash      [32]byte
	validated bool
}

// resolvePathFindLedger selects the ledger to run pathfinding on. With no
// ledger_hash/ledger_index the closed ledger is used (rippled's pathfinding
// default) and no metadata is reported.
func resolvePathFindLedger(
	ctx *types.RpcContext,
	probe map[string]json.RawMessage,
) (types.LedgerStateView, *pathFindLedgerMeta, *types.RpcError) {
	if rawHash, ok := probe["ledger_hash"]; ok && !isJSONNull(rawHash) {
		var hashStr string
		if err := json.Unmarshal(rawHash, &hashStr); err != nil {
			return nil, nil, types.RpcErrorInvalidParams("ledgerHashMalformed")
		}
		rawBytes, err := hex.DecodeString(hashStr)
		if err != nil || len(rawBytes) != 32 {
			return nil, nil, types.RpcErrorInvalidParams("ledgerHashMalformed")
		}
		var h [32]byte
		copy(h[:], rawBytes)
		src, ok := ctx.Services.Ledger.(types.LedgerViewSource)
		if !ok {
			return nil, nil, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		view, reader, lerr := src.GetLedgerViewByHash(h)
		if lerr != nil || view == nil {
			return nil, nil, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return view, &pathFindLedgerMeta{
			seq:       reader.Sequence(),
			hash:      reader.Hash(),
			validated: reader.IsValidated(),
		}, nil
	}

	if rawIdx, ok := probe["ledger_index"]; ok && !isJSONNull(rawIdx) {
		var li types.LedgerIndex
		if err := json.Unmarshal(rawIdx, &li); err != nil {
			return nil, nil, types.RpcErrorInvalidParams("ledgerIndexMalformed")
		}
		selector := li.String()
		switch selector {
		case "", "current", "closed", "validated":
			view, err := ctx.Services.Ledger.GetClosedLedgerView()
			if err != nil {
				return nil, nil, types.NewRpcError(types.RpcNO_CURRENT, "noCurrent", "noCurrent", "Current ledger is unavailable.")
			}
			info := ctx.Services.Ledger.GetServerInfo()
			if selector == "current" {
				return view, &pathFindLedgerMeta{
					current: true,
					seq:     ctx.Services.Ledger.GetCurrentLedgerIndex(),
				}, nil
			}
			return view, &pathFindLedgerMeta{
				seq:       info.ClosedLedgerSeq,
				hash:      info.ClosedLedgerHash,
				validated: info.HaveValidated && info.ValidatedLedgerSeq == info.ClosedLedgerSeq,
			}, nil
		}
		seq, perr := strconv.ParseUint(selector, 10, 32)
		if perr != nil {
			return nil, nil, types.RpcErrorInvalidParams("ledgerIndexMalformed")
		}
		src, ok := ctx.Services.Ledger.(types.LedgerViewSource)
		if !ok {
			return nil, nil, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		view, reader, lerr := src.GetLedgerViewBySeq(uint32(seq))
		if lerr != nil || view == nil {
			return nil, nil, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return view, &pathFindLedgerMeta{
			seq:       reader.Sequence(),
			hash:      reader.Hash(),
			validated: reader.IsValidated(),
		}, nil
	}

	view, err := ctx.Services.Ledger.GetClosedLedgerView()
	if err != nil {
		return nil, nil, types.NewRpcError(types.RpcNO_CURRENT, "noCurrent", "noCurrent", "Current ledger is unavailable.")
	}
	return view, nil, nil
}

// formatAmountJSON formats an Amount for JSON output, matching rippled's
// STAmount::getJson(JsonOptions::none) behavior.
// XRP amounts are serialized as a string of drops.
// IOU amounts are serialized as {"currency": ..., "issuer": ..., "value": ...}.
func formatAmountJSON(amt state.Amount) any {
	if amt.IsNative() {
		return amt.Value()
	}
	return map[string]string{
		"currency": amt.Currency,
		"issuer":   amt.Issuer,
		"value":    amt.Value(),
	}
}

// parsePathFindAmount parses a JSON amount for path finding, mirroring
// rippled amountFromJsonNoThrow: a string is XRP drops; an object must be a
// non-XRP issued amount with a valid issuer (XRP may not be specified as an
// object).
func parsePathFindAmount(raw json.RawMessage) (state.Amount, error) {
	// Try as string first (XRP drops)
	var strVal string
	if err := json.Unmarshal(raw, &strVal); err == nil {
		drops, err := strconv.ParseInt(strVal, 10, 64)
		if err != nil {
			return state.Amount{}, fmt.Errorf("invalid XRP amount %q: %w", strVal, err)
		}
		return state.NewXRPAmountFromInt(drops), nil
	}

	// Try as IOU object
	var iou struct {
		Currency *string `json:"currency"`
		Issuer   string  `json:"issuer"`
		Value    string  `json:"value"`
	}
	if err := json.Unmarshal(raw, &iou); err != nil {
		return state.Amount{}, fmt.Errorf("amount must be string or {currency,issuer,value} object")
	}

	if iou.Currency == nil || !isValidCurrencyCode(*iou.Currency) {
		return state.Amount{}, fmt.Errorf("invalid currency")
	}
	if *iou.Currency == "" || *iou.Currency == "XRP" {
		return state.Amount{}, fmt.Errorf("XRP may not be specified as an object")
	}
	if _, err := state.DecodeAccountID(iou.Issuer); err != nil {
		return state.Amount{}, fmt.Errorf("invalid issuer")
	}
	if _, err := strconv.ParseFloat(iou.Value, 64); err != nil {
		return state.Amount{}, fmt.Errorf("invalid amount value %q", iou.Value)
	}

	return state.NewIssuedAmountFromDecimalString(iou.Value, *iou.Currency, iou.Issuer), nil
}
