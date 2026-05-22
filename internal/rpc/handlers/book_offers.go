package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// BookOffersMethod handles the book_offers RPC method
type BookOffersMethod struct{ BaseHandler }

func (m *BookOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// rippled BookOffers.cpp:45-49 calls RPC::lookupLedger before any of the
	// per-field validation in :51-189. We mirror that by parsing the request
	// envelope first, resolving the requested ledger when it points to a
	// specific seq/hash, and only then walking the field-level checks. A
	// bogus ledger_index combined with missing taker_pays must surface
	// lgrNotFound — not invalidParams (Book_test.cpp:1329-1336).
	var topLevel map[string]json.RawMessage
	if len(params) > 0 {
		if err := json.Unmarshal(params, &topLevel); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	var request struct {
		TakerGets json.RawMessage  `json:"taker_gets"`
		TakerPays json.RawMessage  `json:"taker_pays"`
		Taker     *json.RawMessage `json:"taker,omitempty"`
		Domain    *json.RawMessage `json:"domain,omitempty"`
		types.LedgerSpecifier
		types.PaginationParams
	}
	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	ledgerIndex, rpcErr := resolveBookOffersLedger(ctx, request.LedgerSpecifier)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// M1: per-field missing checks in pays-then-gets order, matching
	// BookOffers.cpp:51-55 and Book_test.cpp:1338-1357. isMember on the
	// raw envelope distinguishes "key absent" (missing) from "key present
	// with non-object payload" (handled by decodeBookSideShape below).
	if _, ok := topLevel["taker_pays"]; !ok {
		return nil, types.RpcErrorMissingField("taker_pays")
	}
	if _, ok := topLevel["taker_gets"]; !ok {
		return nil, types.RpcErrorMissingField("taker_gets")
	}

	// Validate the two sides in the same order rippled does
	// (BookOffers.cpp:60-162): shape both sides first, then currency
	// presence/string, then to_currency, then issuer rules — pay before
	// gets at each phase.
	paysFields, rpcErr := decodeBookSideShape(request.TakerPays, "taker_pays")
	if rpcErr != nil {
		return nil, rpcErr
	}
	getsFields, rpcErr := decodeBookSideShape(request.TakerGets, "taker_gets")
	if rpcErr != nil {
		return nil, rpcErr
	}

	payCurrency, rpcErr := requireCurrencyShape(paysFields, "taker_pays")
	if rpcErr != nil {
		return nil, rpcErr
	}
	getCurrency, rpcErr := requireCurrencyShape(getsFields, "taker_gets")
	if rpcErr != nil {
		return nil, rpcErr
	}

	if rpcErr := validateCurrencyCode(payCurrency, "taker_pays.currency", types.RpcErrorSrcCurMalformed); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := validateCurrencyCode(getCurrency, "taker_gets.currency", types.RpcErrorDstAmtMalformed); rpcErr != nil {
		return nil, rpcErr
	}

	takerPays, rpcErr := resolveBookSideIssuer(paysFields, payCurrency, "taker_pays", types.RpcErrorSrcIsrMalformed)
	if rpcErr != nil {
		return nil, rpcErr
	}
	takerGets, rpcErr := resolveBookSideIssuer(getsFields, getCurrency, "taker_gets", types.RpcErrorDstIsrMalformed)
	if rpcErr != nil {
		return nil, rpcErr
	}

	taker, rpcErr := parseTaker(request.Taker)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if rpcErr := validateDomain(request.Domain); rpcErr != nil {
		return nil, rpcErr
	}

	// M5: reject `proof`/`marker` until they're honoured. Accepting them
	// silently would let a paginated client treat a single-page response
	// as the complete book (rippled BookOffers.cpp:201-214 threads both
	// into NetworkOps::getBookPage).
	if rpcErr := rejectUnsupportedPagination(topLevel); rpcErr != nil {
		return nil, rpcErr
	}

	if sameMarket(takerGets, takerPays) {
		return nil, types.RpcErrorBadMarket()
	}

	// Clamp the limit using rippled's bookOffers range {0, 60, 100}.
	// When the user omits "limit" (zero value), ClampLimit returns the default (60).
	limit := ClampLimit(request.Limit, LimitBookOffers, ctx.Unlimited)
	result, err := ctx.Services.Ledger.GetBookOffers(ctx.Context, takerGets, takerPays, taker, ledgerIndex, limit)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get book offers: %v", err))
	}

	response := map[string]interface{}{
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"offers":       result.Offers,
		"validated":    result.Validated,
	}

	// Echo the effective (clamped) limit when the user specified one.
	if request.Limit > 0 {
		response["limit"] = limit
	}

	return response, nil
}

// resolveBookOffersLedger mirrors rippled BookOffers.cpp:45-49 (RPC::lookupLedger).
// For the keyword specifiers (validated/current/closed/"") we defer to the
// service layer which always has those handles; for an explicit ledger_hash
// or numeric ledger_index we pre-resolve so a bogus value returns
// lgrNotFound / lgrIdxMalformed before any field-level validation runs.
//
// Returns the canonical ledgerIndex string the downstream service layer
// expects ("current", "closed", "validated", or a decimal seq).
func resolveBookOffersLedger(ctx *types.RpcContext, spec types.LedgerSpecifier) (string, *types.RpcError) {
	if spec.LedgerHash != "" {
		raw, err := hex.DecodeString(spec.LedgerHash)
		if err != nil || len(raw) != 32 {
			return "", &types.RpcError{
				Code:        types.RpcINVALID_PARAMS,
				ErrorString: "invalidParams",
				Type:        "invalidParams",
				Message:     "ledgerHashMalformed",
			}
		}
		var h [32]byte
		copy(h[:], raw)
		l, lerr := ctx.Services.Ledger.GetLedgerByHash(h)
		if lerr != nil || l == nil {
			return "", types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return strconv.FormatUint(uint64(l.Sequence()), 10), nil
	}

	li := spec.LedgerIndex.String()
	switch li {
	case "", "current":
		return "current", nil
	case "closed":
		return "closed", nil
	case "validated":
		return "validated", nil
	}

	seq, perr := strconv.ParseUint(li, 10, 32)
	if perr != nil {
		return "", &types.RpcError{
			Code:        types.RpcINVALID_PARAMS,
			ErrorString: "invalidParams",
			Type:        "invalidParams",
			Message:     "ledgerIndexMalformed",
		}
	}
	l, lerr := ctx.Services.Ledger.GetLedgerBySequence(uint32(seq))
	if lerr != nil || l == nil {
		return "", types.RpcErrorLgrNotFound("ledgerNotFound")
	}
	return strconv.FormatUint(seq, 10), nil
}

// rejectUnsupportedPagination implements M5 of the b2651ca conformance review:
// rippled's BookOffers.cpp:201-214 threads `proof` and `marker` into
// NetworkOps::getBookPage. goxrpld doesn't honour either yet, so accepting
// them silently would let a paginated client mistake a partial page for the
// complete book. Until the service grows resume-from-marker support, refuse
// both with notSupported (code 75) rather than dropping them on the floor.
func rejectUnsupportedPagination(top map[string]json.RawMessage) *types.RpcError {
	if raw, ok := top["proof"]; ok {
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil && b {
			return types.RpcErrorNotSupported("Proof requests are not yet supported by book_offers.")
		}
	}
	if raw, ok := top["marker"]; ok && !isJSONNull(raw) {
		return types.RpcErrorNotSupported("Marker-based pagination is not yet supported by book_offers.")
	}
	return nil
}

// isJSONNull reports whether the raw JSON value is the literal `null`,
// optionally surrounded by whitespace.
func isJSONNull(raw json.RawMessage) bool {
	i := 0
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\n', '\r':
			i++
			continue
		}
		break
	}
	if i+4 > len(raw) {
		return false
	}
	return raw[i] == 'n' && raw[i+1] == 'u' && raw[i+2] == 'l' && raw[i+3] == 'l'
}

// decodeBookSideShape enforces rippled BookOffers.cpp:60-64 (isObjectOrNull):
// the side must be a JSON object or null. JSON null decodes to a nil map,
// which is then treated as "no fields present" by the downstream checks.
func decodeBookSideShape(data json.RawMessage, field string) (map[string]json.RawMessage, *types.RpcError) {
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &asMap); err != nil {
		return nil, types.RpcErrorObjectField(field)
	}
	return asMap, nil
}

// requireCurrencyShape mirrors BookOffers.cpp:66-76: the side must have a
// currency member, and that member must be a JSON string.
func requireCurrencyShape(fields map[string]json.RawMessage, field string) (string, *types.RpcError) {
	raw, ok := fields["currency"]
	if !ok {
		return "", types.RpcErrorMissingField(field + ".currency")
	}
	if !isJSONString(raw) {
		return "", types.RpcErrorExpectedField(field+".currency", "string")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", types.RpcErrorExpectedField(field+".currency", "string")
	}
	return s, nil
}

// resolveBookSideIssuer mirrors BookOffers.cpp:98-162: parse the optional
// issuer field, reject noAccount() (ACCOUNT_ONE), then enforce the XRP /
// non-XRP cross-rules between currency and issuer. The issuer side is
// treated as XRP when either the field is absent or its decoded AccountID
// is the all-zero xrpAccount() sentinel.
func resolveBookSideIssuer(
	fields map[string]json.RawMessage,
	currency, field string,
	issuerErr func(string) *types.RpcError,
) (types.Amount, *types.RpcError) {
	isXRP := currency == "" || currency == "XRP"

	var issuer string
	issuerIsXRP := true
	if raw, ok := fields["issuer"]; ok {
		if !isJSONString(raw) {
			return types.Amount{}, types.RpcErrorExpectedField(field+".issuer", "string")
		}
		if err := json.Unmarshal(raw, &issuer); err != nil {
			return types.Amount{}, types.RpcErrorExpectedField(field+".issuer", "string")
		}
		_, accID, err := addresscodec.DecodeClassicAddressToAccountID(issuer)
		if err != nil {
			return types.Amount{}, issuerErr("Invalid field '" + field + ".issuer', bad issuer.")
		}
		if bytes.Equal(accID, accountOneID[:]) {
			return types.Amount{}, issuerErr("Invalid field '" + field + ".issuer', bad issuer account one.")
		}
		var zero [20]byte
		issuerIsXRP = bytes.Equal(accID, zero[:])
	}

	if isXRP && !issuerIsXRP {
		return types.Amount{}, issuerErr("Unneeded field '" + field + ".issuer' for XRP currency specification.")
	}
	if !isXRP && issuerIsXRP {
		return types.Amount{}, issuerErr("Invalid field '" + field + ".issuer', expected non-XRP issuer.")
	}

	return types.Amount{Currency: currency, Issuer: issuer}, nil
}

// isJSONString reports whether the raw JSON value is a string literal,
// looking past leading whitespace. Used to mirror rippled's per-field
// isString() checks (BookOffers.cpp:69, :102, :135, :167).
func isJSONString(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '"':
			return true
		default:
			return false
		}
	}
	return false
}

// accountOneID is rippled's noAccount() sentinel — base58 "rrrrrrrrrrrrrrrrrrrrBZbvji",
// the canonical "ACCOUNT_ONE" id that to_issuer accepts but the handler rejects.
var accountOneID = func() [20]byte {
	var id [20]byte
	_, raw, _ := addresscodec.DecodeClassicAddressToAccountID(state.AccountOneAddress)
	copy(id[:], raw)
	return id
}()

// validateCurrencyCode mirrors rippled's to_currency rules: empty or "XRP"
// (native), exactly 3 ISO characters, or exactly 40 hex characters. The
// per-side error constructor + field name produce rippled's full message
// text ("Invalid field 'taker_pays.currency', bad currency.").
// Reference: rippled UintTypes.cpp to_currency() and BookOffers.cpp:80-95.
func validateCurrencyCode(currency, field string, errFn func(string) *types.RpcError) *types.RpcError {
	if currency == "" || currency == "XRP" {
		return nil
	}
	if len(currency) == 3 {
		return nil
	}
	if len(currency) == 40 {
		if _, err := hex.DecodeString(currency); err == nil {
			return nil
		}
	}
	return errFn("Invalid field '" + field + "', bad currency.")
}

// parseTaker mirrors BookOffers.cpp:164-173: the parameter must be a string
// when present, and an empty / unparsable string is invalid_field_error.
func parseTaker(raw *json.RawMessage) (string, *types.RpcError) {
	if raw == nil {
		return "", nil
	}
	if !isJSONString(*raw) {
		return "", types.RpcErrorExpectedField("taker", "string")
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err != nil {
		return "", types.RpcErrorExpectedField("taker", "string")
	}
	if _, _, err := addresscodec.DecodeClassicAddressToAccountID(s); err != nil {
		return "", types.RpcErrorInvalidField("taker")
	}
	return s, nil
}

// validateDomain mirrors BookOffers.cpp:175-189: when present, the domain
// parameter must be a 64-character hex string (uint256). Unparseable values
// return rpcDOMAIN_MALFORMED.
//
// M4 of the b2651ca review: rippled then threads the parsed domain into
// NetworkOps::getBookPage (BookOffers.cpp:207-214) to select a
// PermissionedDEX-scoped order book. goxrpld doesn't yet route the domain
// through GetBookOffers, so a syntactically valid domain would silently
// return the non-domain book — wrong liquidity. Until the
// PermissionedDEX-aware keylet.BookDir derivation lands, reject any present
// non-zero domain with notSupported. An all-zero domain is treated as
// "no domain" (matches rippled's open-market semantics) and accepted.
func validateDomain(raw *json.RawMessage) *types.RpcError {
	if raw == nil {
		return nil
	}
	if !isJSONString(*raw) {
		return types.RpcErrorDomainMalformed("Unable to parse domain.")
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err != nil {
		return types.RpcErrorDomainMalformed("Unable to parse domain.")
	}
	if len(s) != 64 {
		return types.RpcErrorDomainMalformed("Unable to parse domain.")
	}
	decoded, err := hex.DecodeString(s)
	if err != nil {
		return types.RpcErrorDomainMalformed("Unable to parse domain.")
	}
	var zero [32]byte
	if !bytes.Equal(decoded, zero[:]) {
		return types.RpcErrorNotSupported("PermissionedDEX-scoped book_offers are not yet supported.")
	}
	return nil
}

// sameMarket reports whether the two sides describe identical issues, in
// which case rippled returns rpcBAD_MARKET (BookOffers.cpp:191-195).
func sameMarket(a, b types.Amount) bool {
	aXRP := a.Currency == "" || a.Currency == "XRP"
	bXRP := b.Currency == "" || b.Currency == "XRP"
	if aXRP && bXRP {
		return true
	}
	if aXRP != bXRP {
		return false
	}
	return a.Currency == b.Currency && a.Issuer == b.Issuer
}
