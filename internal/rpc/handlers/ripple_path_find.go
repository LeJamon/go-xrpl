package handlers

import (
	"encoding/json"
	"sort"

	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
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
type RipplePathFindMethod struct{ BaseHandler }

func (m *RipplePathFindMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if rpcErr := RequirePathSearch(ctx); rpcErr != nil {
		return nil, rpcErr
	}
	setLoadHeavy(ctx)

	probe := map[string]json.RawMessage{}
	if params != nil {
		if err := json.Unmarshal(params, &probe); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}
	ledgerSpec, hasLedgerSelector, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	if rpcErr := RequireLedgerService(ctx.Services); rpcErr != nil {
		return nil, rpcErr
	}
	var view types.LedgerStateView
	var meta *pathFindLedgerMeta
	var rpcErr *types.RpcError
	standalone := ctx != nil && ctx.Services != nil && ctx.Services.Ledger != nil &&
		ctx.Services.Ledger.GetServerInfo().Standalone
	usesLookup := hasLedgerSelector || standalone
	if usesLookup {
		view, meta, rpcErr = resolvePathFindLedger(ctx, ledgerSpec, true)
		if rpcErr != nil {
			return nil, rpcErr
		}
		release, rpcErr := AcquirePathfind(ctx)
		if rpcErr != nil {
			return nil, rpcErr
		}
		defer release()
	} else {
		if types.ValidatedLedgerStale(ctx.Services.Ledger.GetServerInfo()) {
			if ctx.ApiVersion == types.ApiVersion1 {
				return nil, types.RpcErrorNoNetwork("")
			}
			return nil, types.RpcErrorNotSynced("")
		}
		view, meta, rpcErr = resolvePathFindLedger(ctx, types.LedgerSpecifier{}, false)
		if rpcErr != nil {
			return nil, rpcErr
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

	dstAmount, err := state.AmountFromJSON(rawDstAmount)
	if err != nil || !pathfinder.IsValidAsset(dstAmount) {
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
		amt, smErr := state.AmountFromJSON(rawSendMax)
		if smErr != nil || !pathfinder.IsValidAsset(amt) || (amt.Signum() <= 0 && amt.Value() != "-1") {
			return nil, types.RpcErrorSendMaxMalformed("SendMax amount malformed.")
		}
		sendMax = &amt
	}

	srcCurrencies, rpcErr := parseSourceCurrencies(probe, srcAccount, sendMax)
	if rpcErr != nil {
		return nil, rpcErr
	}

	var domainID *[32]byte
	if rawDomain, ok := probe["domain"]; ok {
		var parsed bool
		domainID, parsed = types.ParsePathFindDomain(rawDomain)
		if !parsed {
			return nil, types.RpcErrorDomainMalformed("Domain is malformed.")
		}
	}
	if view == nil {
		return nil, types.NewRpcError(types.RpcNO_CURRENT, "noCurrent", "noCurrent", "Current ledger is unavailable.")
	}

	// Existence checks. Reference: rippled PathRequest::isValid.
	if exists, err := view.Exists(keylet.Account(srcAccount)); err != nil {
		return nil, rpcInternalError("ripple_path_find: source account lookup failed", err)
	} else if !exists {
		return nil, types.RpcErrorSrcActNotFound("Source account not found.")
	}
	if exists, err := view.Exists(keylet.Account(dstAccount)); err != nil {
		return nil, rpcInternalError("ripple_path_find: destination account lookup failed", err)
	} else if !exists {
		// Only XRP can be sent to a non-existent account, and the payment
		// must meet the account reserve.
		if !dstAmount.IsNative() {
			return nil, types.RpcErrorActNotFound("Account not found.")
		}
		if !convertAll {
			feeData, feeErr := view.Read(keylet.Fees())
			if feeErr != nil {
				return nil, rpcInternalError("ripple_path_find: fee settings lookup failed", feeErr)
			}
			fees, feeErr := state.ParseFeeSettings(feeData)
			if feeErr != nil {
				return nil, rpcInternalError("ripple_path_find: fee settings parsing failed", feeErr)
			}
			reserveBase := fees.GetReserveBase()
			if dstAmount.Drops() < int64(reserveBase) {
				return nil, types.RpcErrorDstAmtMalformed("Destination amount/currency/issuer is malformed.")
			}
		}
	}

	// Run pathfinding at the production search level (rippled PATH_SEARCH).
	if !usesLookup {
		release, rpcErr := WaitPathfind(ctx)
		if rpcErr != nil {
			return nil, rpcErr
		}
		defer release()
	}
	pr := pathfinder.NewPathRequest(srcAccount, dstAccount, dstAmount, sendMax, srcCurrencies, convertAll)
	pr.SetDomainID(domainID)
	result := pr.Execute(view)
	if result.SourceCurrencyOverflow {
		return nil, rpcInternalInvariantError("ripple_path_find: source currency limit exceeded")
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
		return nil, rpcInternalError("ripple_path_find: response marshaling failed", mErr)
	}
	var flat map[string]any
	if uErr := json.Unmarshal(encoded, &flat); uErr != nil {
		return nil, rpcInternalError("ripple_path_find: response normalization failed", uErr)
	}
	return flat, nil
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

	var sendMaxIssue payment.Issue
	if sendMax != nil {
		sendMaxIssue = payment.GetIssue(*sendMax)
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
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, types.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}
		rawCurrency, hasCurrency := fields["currency"]
		rawMPT, hasMPT := fields["mpt_issuance_id"]
		_, hasIssuer := fields["issuer"]
		if hasCurrency == hasMPT {
			return nil, types.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}
		if hasMPT && hasIssuer {
			return nil, types.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
		}
		if hasMPT {
			var mptID string
			if err := json.Unmarshal(rawMPT, &mptID); err != nil {
				return nil, types.RpcErrorSrcCurMalformed("Source currency is malformed.")
			}
			id, ok := pathfinder.ParseSourceMPTID(mptID)
			if !ok {
				return nil, types.RpcErrorSrcCurMalformed("Source currency is malformed.")
			}
			issue := payment.NewMPTIssue(id)
			if sendMax != nil {
				if !issue.Equal(sendMaxIssue) {
					continue
				}
			}
			add(issue)
			continue
		}
		var currencyValue string
		if err := json.Unmarshal(rawCurrency, &currencyValue); err != nil || !keylet.IsValidCurrencyCode(currencyValue) {
			return nil, types.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}
		currency := canonCurrency(currencyValue)
		isXRPCur := currency == "XRP"

		var issuerID [20]byte
		if rawIssuer, hasIssuer := fields["issuer"]; hasIssuer {
			var issuerStr string
			if err := json.Unmarshal(rawIssuer, &issuerStr); err != nil {
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
			if sendMaxIssue.IsMPT || currency != canonCurrency(sendMaxIssue.Currency) {
				continue
			}
			// If neither issuer is the source and they are not equal, the
			// source issuer is illegal.
			if issuerID != srcAccount && sendMaxIssue.Issuer != srcAccount && issuerID != sendMaxIssue.Issuer {
				return nil, types.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
			}
			// If both are the source, use the source; otherwise use the one
			// that's not the source.
			if issuerID != srcAccount {
				add(payment.Issue{Currency: currency, Issuer: issuerID})
			} else if sendMaxIssue.Issuer != srcAccount {
				add(payment.Issue{Currency: currency, Issuer: sendMaxIssue.Issuer})
			} else {
				add(payment.Issue{Currency: currency, Issuer: srcAccount})
			}
			if !isXRPCur {
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

type selectedPathFindLedger struct {
	view   types.LedgerStateView
	reader types.LedgerReader
}

func (l selectedPathFindLedger) Sequence() uint32  { return l.reader.Sequence() }
func (l selectedPathFindLedger) Hash() [32]byte    { return l.reader.Hash() }
func (l selectedPathFindLedger) IsValidated() bool { return l.reader.IsValidated() }

// resolvePathFindLedger selects the ledger to run pathfinding on. With no
// selector the closed ledger is used and no metadata is reported.
func resolvePathFindLedger(
	ctx *types.RpcContext,
	spec types.LedgerSpecifier,
	hasSelector bool,
) (types.LedgerStateView, *pathFindLedgerMeta, *types.RpcError) {
	if !hasSelector {
		view, err := ctx.Services.Ledger.GetClosedLedgerView()
		if err != nil {
			return nil, nil, types.NewRpcError(types.RpcNO_CURRENT, "noCurrent", "noCurrent", "Current ledger is unavailable.")
		}
		return view, nil, nil
	}
	selection, rpcErr := parseLedgerSelectorInput(spec, ledgerselector.Current())
	if rpcErr != nil {
		return nil, nil, rpcErr
	}

	source, ok := ctx.Services.Ledger.(types.LedgerViewSource)
	if !ok {
		return nil, nil, types.RpcErrorLgrNotFound("ledgerNotFound")
	}
	bySequence := func(sequence uint32) (selectedPathFindLedger, bool, error) {
		view, reader, err := source.GetLedgerViewBySeq(sequence)
		selected := selectedPathFindLedger{view: view, reader: reader}
		return selected, view != nil && reader != nil, err
	}
	byHash := func(hash [32]byte) (selectedPathFindLedger, bool, error) {
		view, reader, err := source.GetLedgerViewByHash(hash)
		selected := selectedPathFindLedger{view: view, reader: reader}
		return selected, view != nil && reader != nil, err
	}
	validated := func() (selectedPathFindLedger, bool, error) {
		sequence := ctx.Services.Ledger.GetValidatedLedgerIndex()
		if sequence == 0 {
			return selectedPathFindLedger{}, false, nil
		}
		return bySequence(sequence)
	}
	resolved, err := ledgerselector.Resolve(selection, ledgerselector.Callbacks[selectedPathFindLedger]{
		Current: func() (selectedPathFindLedger, bool, error) {
			return bySequence(ctx.Services.Ledger.GetCurrentLedgerIndex())
		},
		Closed: func() (selectedPathFindLedger, bool, error) {
			return bySequence(ctx.Services.Ledger.GetClosedLedgerIndex())
		},
		Validated:  validated,
		BySequence: bySequence,
		ByHash:     byHash,
	})
	if err != nil {
		return nil, nil, types.RpcErrorLgrNotFound("ledgerNotFound")
	}
	validatedResult := resolved.Validated
	if selection.Kind() == ledgerselector.KindCurrent {
		validatedResult = false
	} else if selection.Kind() == ledgerselector.KindValidated {
		validatedResult = true
	}
	return resolved.Value.view, &pathFindLedgerMeta{
		current:   !resolved.Value.reader.IsClosed(),
		seq:       resolved.Sequence,
		hash:      resolved.Hash,
		validated: validatedResult,
	}, nil
}

// formatAmountJSON formats an Amount for JSON output, matching rippled's
// STAmount::getJson(JsonOptions::none) behavior.
// XRP amounts are serialized as a string of drops.
// IOU amounts are serialized as {"currency": ..., "issuer": ..., "value": ...}.
func formatAmountJSON(amt state.Amount) any {
	if amt.IsNative() {
		return amt.Value()
	}
	if amt.IsMPT() {
		return map[string]string{
			"mpt_issuance_id": amt.MPTIssuanceID(),
			"value":           amt.Value(),
		}
	}
	return map[string]string{
		"currency": amt.Currency,
		"issuer":   amt.Issuer,
		"value":    amt.Value(),
	}
}
