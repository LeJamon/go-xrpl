package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/service/svcerr"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// xrpAccountID is the zero AccountID returned by rippled's xrpAccount()
// (AccountID.cpp:178); noAccountID is the noAccount() sentinel at :185 —
// 20 bytes ending in 0x01.
var (
	xrpAccountID  = [20]byte{}
	noAccountID   = [20]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	jsonNullBytes = []byte("null")
)

type BookOffersMethod struct{ BaseHandler }

func (m *BookOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	probe := map[string]json.RawMessage{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &probe); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid params: %v", err))
		}
	}

	// Validation order mirrors rippled BookOffers.cpp:51-199 exactly so that
	// clients depending on rippled's specific failure precedence (e.g. the
	// fixtures in rippled/src/test/rpc/Book_test.cpp) see the same error
	// emitted first.
	paysRaw, ok := probe["taker_pays"]
	if !ok {
		return nil, types.RpcErrorMissingField("taker_pays")
	}
	getsRaw, ok := probe["taker_gets"]
	if !ok {
		return nil, types.RpcErrorMissingField("taker_gets")
	}
	if !isJSONObjectOrNull(paysRaw) {
		return nil, types.RpcErrorExpectedField("taker_pays", "object")
	}
	if !isJSONObjectOrNull(getsRaw) {
		return nil, types.RpcErrorExpectedField("taker_gets", "object")
	}
	paysInner := unmarshalObjectOrNull(paysRaw)
	getsInner := unmarshalObjectOrNull(getsRaw)

	paysCurrency, rpcErr := readJSONString(paysInner, "currency", "taker_pays.currency")
	if rpcErr != nil {
		return nil, rpcErr
	}
	getsCurrency, rpcErr := readJSONString(getsInner, "currency", "taker_gets.currency")
	if rpcErr != nil {
		return nil, rpcErr
	}

	if !isValidCurrencyCode(paysCurrency) {
		return nil, types.RpcErrorSrcCurMalformed(
			"Invalid field 'taker_pays.currency', bad currency.")
	}
	if !isValidCurrencyCode(getsCurrency) {
		return nil, types.RpcErrorDstAmtMalformed(
			"Invalid field 'taker_gets.currency', bad currency.")
	}

	paysIssuerStr, paysIssuerID, rpcErr := readAndValidateIssuer(paysInner, paysCurrency, true)
	if rpcErr != nil {
		return nil, rpcErr
	}
	getsIssuerStr, getsIssuerID, rpcErr := readAndValidateIssuer(getsInner, getsCurrency, false)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// taker (BookOffers.cpp:164-173).
	var takerStr string
	if rawTaker, ok := probe["taker"]; ok && !isJSONNull(rawTaker) {
		if !isJSONString(rawTaker) {
			return nil, types.RpcErrorExpectedField("taker", "string")
		}
		if err := json.Unmarshal(rawTaker, &takerStr); err != nil {
			return nil, types.RpcErrorExpectedField("taker", "string")
		}
		if _, _, err := addresscodec.DecodeClassicAddressToAccountID(takerStr); err != nil {
			return nil, types.RpcErrorInvalidField("taker")
		}
	}

	// domain (BookOffers.cpp:175-189). Non-string OR parseHex-fail both
	// produce the same rpcDOMAIN_MALFORMED with "Unable to parse domain.".
	var domain string
	if rawDomain, ok := probe["domain"]; ok && !isJSONNull(rawDomain) {
		if !isJSONString(rawDomain) {
			return nil, types.RpcErrorDomainMalformed("Unable to parse domain.")
		}
		var domainStr string
		if err := json.Unmarshal(rawDomain, &domainStr); err != nil {
			return nil, types.RpcErrorDomainMalformed("Unable to parse domain.")
		}
		// rippled base_uint.h:228 accepts the literal "0" as zero uint256.
		if domainStr == "0" {
			domain = "0000000000000000000000000000000000000000000000000000000000000000"
		} else {
			if len(domainStr) != 64 {
				return nil, types.RpcErrorDomainMalformed("Unable to parse domain.")
			}
			if _, err := hex.DecodeString(domainStr); err != nil {
				return nil, types.RpcErrorDomainMalformed("Unable to parse domain.")
			}
			domain = domainStr
		}
	}

	// bad market (BookOffers.cpp:191-195). Compare canonical forms: XRP
	// currency normalizes to zero, issuers normalize to their decoded
	// 20-byte AccountIDs (any valid encoding of the same account collides).
	if canonCurrency(paysCurrency) == canonCurrency(getsCurrency) && paysIssuerID == getsIssuerID {
		return nil, types.RpcErrorBadMarket()
	}

	// limit (BookOffers.cpp:197-199, readLimitField at RPCHelpers.cpp:703).
	if rpcErr := preValidateUintField(probe, "limit"); rpcErr != nil {
		return nil, rpcErr
	}
	limit := LimitBookOffers.Default
	if rawLimit, ok := probe["limit"]; ok && !isJSONNull(rawLimit) {
		var v uint32
		if err := json.Unmarshal(rawLimit, &v); err != nil {
			return nil, types.RpcErrorExpectedField("limit", "unsigned integer")
		}
		limit = v
		if !ctx.Unlimited {
			if limit < LimitBookOffers.Min {
				limit = LimitBookOffers.Min
			}
			if limit > LimitBookOffers.Max {
				limit = LimitBookOffers.Max
			}
		}
	}

	var spec types.LedgerSpecifier
	if rawLedgerHash, ok := probe["ledger_hash"]; ok && !isJSONNull(rawLedgerHash) {
		if err := json.Unmarshal(rawLedgerHash, &spec.LedgerHash); err != nil {
			return nil, types.RpcErrorExpectedField("ledger_hash", "string")
		}
	}
	if rawLedgerIndex, ok := probe["ledger_index"]; ok && !isJSONNull(rawLedgerIndex) {
		if err := json.Unmarshal(rawLedgerIndex, &spec.LedgerIndex); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid ledger_index: %v", err))
		}
	}
	ledgerIndex := "current"
	if spec.LedgerIndex != "" {
		ledgerIndex = spec.LedgerIndex.String()
	}

	takerPays := types.Amount{Currency: paysCurrency, Issuer: canonIssuerString(paysIssuerStr, paysCurrency)}
	takerGets := types.Amount{Currency: getsCurrency, Issuer: canonIssuerString(getsIssuerStr, getsCurrency)}

	// marker is a goXRPL extension. rippled's handler (BookOffers.cpp:201-214)
	// reads `marker` from params and threads it through to getBookPage, but
	// NetworkOPsImp::getBookPage doesn't actually use it (NetworkOPs.cpp:4627
	// shows the response field is commented out). We treat it as an opaque
	// 64-hex offer-index resume token; the service rejects non-matching shapes.
	var markerStr string
	if rawMarker, ok := probe["marker"]; ok && !isJSONNull(rawMarker) {
		if !isJSONString(rawMarker) {
			return nil, types.RpcErrorInvalidField("marker")
		}
		if err := json.Unmarshal(rawMarker, &markerStr); err != nil {
			return nil, types.RpcErrorInvalidField("marker")
		}
		if len(markerStr) != 64 {
			return nil, types.RpcErrorInvalidField("marker")
		}
		if _, err := hex.DecodeString(markerStr); err != nil {
			return nil, types.RpcErrorInvalidField("marker")
		}
	}

	result, err := ctx.Services.Ledger.GetBookOffers(ctx.Context, takerGets, takerPays, takerStr, domain, ledgerIndex, limit, markerStr)
	if err != nil {
		// Mirrors rippled AccountOffers.cpp:107-132 two-tier mapping:
		// malformed / wrong-scope marker → invalid_field_error("marker");
		// well-formed marker whose referent was consumed between pages →
		// rpcINVALID_PARAMS with a distinct message so clients can retry
		// against a pinned ledger.
		if errors.Is(err, svcerr.ErrStaleMarker) {
			return nil, types.RpcErrorInvalidParams("Invalid marker: object pointed to by marker is gone; retry with a pinned ledger_index or ledger_hash.")
		}
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, types.RpcErrorInvalidField("marker")
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get book offers: %v", err))
	}

	response := map[string]interface{}{
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"offers":       result.Offers,
		"validated":    result.Validated,
	}
	if result.Marker != "" {
		// Pair marker with limit echo, matching rippled's account_offers
		// convention (AccountOffers.cpp:172-176 emits both fields together).
		response["marker"] = result.Marker
		response["limit"] = limit
	}
	return response, nil
}

func ParseAmountFromJSON(data json.RawMessage) (types.Amount, error) {
	var xrpAmount string
	if err := json.Unmarshal(data, &xrpAmount); err == nil {
		return types.Amount{Value: xrpAmount}, nil
	}

	var iouAmount struct {
		Currency string `json:"currency"`
		Issuer   string `json:"issuer"`
		Value    string `json:"value,omitempty"`
	}
	if err := json.Unmarshal(data, &iouAmount); err != nil {
		return types.Amount{}, err
	}

	return types.Amount{
		Currency: iouAmount.Currency,
		Issuer:   iouAmount.Issuer,
		Value:    iouAmount.Value,
	}, nil
}

// isValidCurrencyCode reports whether a currency code is acceptable per
// rippled rules: empty or "XRP" (native), exactly 3 characters (ISO), or
// exactly 40 hex characters (issued-currency hex form).
// Reference: rippled UintTypes.cpp to_currency().
func isValidCurrencyCode(currency string) bool {
	if currency == "" || currency == "XRP" {
		return true
	}
	if len(currency) == 3 {
		return true
	}
	if len(currency) == 40 {
		if _, err := hex.DecodeString(currency); err == nil {
			return true
		}
	}
	return false
}

// readAndValidateIssuer decodes the issuer field for one side of the book and
// runs the rippled cross-checks (BookOffers.cpp:98-129 / :131-162). Returns
// the literal issuer string for downstream callers and the decoded AccountID
// for canonical-form comparisons (e.g. badMarket).
func readAndValidateIssuer(inner map[string]json.RawMessage, currency string, isPay bool) (string, [20]byte, *types.RpcError) {
	makeErr := types.RpcErrorDstIsrMalformed
	field := "taker_gets.issuer"
	if isPay {
		makeErr = types.RpcErrorSrcIsrMalformed
		field = "taker_pays.issuer"
	}

	var issuerStr string
	var issuerID [20]byte
	hasIssuer := false
	if rawIssuer, ok := inner["issuer"]; ok && !isJSONNull(rawIssuer) {
		if !isJSONString(rawIssuer) {
			return "", [20]byte{}, types.RpcErrorExpectedField(field, "string")
		}
		if err := json.Unmarshal(rawIssuer, &issuerStr); err != nil {
			return "", [20]byte{}, types.RpcErrorExpectedField(field, "string")
		}
		_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuerStr)
		if err != nil {
			return "", [20]byte{}, makeErr(fmt.Sprintf("Invalid field '%s', bad issuer.", field))
		}
		copy(issuerID[:], idBytes)
		if issuerID == noAccountID {
			return "", [20]byte{}, makeErr(fmt.Sprintf("Invalid field '%s', bad issuer account one.", field))
		}
		hasIssuer = true
	}

	isXRPCurrency := currency == "" || currency == "XRP"
	isXRPIssuer := !hasIssuer || issuerID == xrpAccountID

	if isXRPCurrency && !isXRPIssuer {
		return "", [20]byte{}, makeErr(fmt.Sprintf(
			"Unneeded field '%s' for XRP currency specification.", field))
	}
	if !isXRPCurrency && isXRPIssuer {
		return "", [20]byte{}, makeErr(fmt.Sprintf(
			"Invalid field '%s', expected non-XRP issuer.", field))
	}
	return issuerStr, issuerID, nil
}

// canonCurrency folds the two valid XRP spellings ("" and "XRP") to a single
// form for equality checks.
func canonCurrency(c string) string {
	if c == "" {
		return "XRP"
	}
	return c
}

// canonIssuerString returns the issuer string to pass downstream. For XRP
// currency we forward an empty string regardless of what the user sent
// (e.g. the canonical xrpAccountAddress); for IOU currency we forward what
// the user sent verbatim — by the time we get here it's been decoded
// successfully, so re-encoding round-trip is unnecessary.
func canonIssuerString(issuer, currency string) string {
	if currency == "" || currency == "XRP" {
		return ""
	}
	return issuer
}

// readJSONString extracts a required string field from a sub-object, returning
// rippled-shaped "Missing field" / "Invalid field, not string." errors.
func readJSONString(inner map[string]json.RawMessage, key, fieldPath string) (string, *types.RpcError) {
	raw, ok := inner[key]
	if !ok {
		return "", types.RpcErrorMissingField(fieldPath)
	}
	if !isJSONString(raw) {
		return "", types.RpcErrorExpectedField(fieldPath, "string")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", types.RpcErrorExpectedField(fieldPath, "string")
	}
	return s, nil
}

// preValidateUintField inspects the probed JSON for a numeric field that
// rippled requires to be an unsigned integer. A string-typed value yields the
// rippled-specific `Invalid field '<name>', not unsigned integer.` error
// (RPCHelpers.cpp:706-707) instead of the generic JSON-parse failure that
// `json.Unmarshal` into a `*uint32` would otherwise produce.
func preValidateUintField(probe map[string]json.RawMessage, field string) *types.RpcError {
	raw, ok := probe[field]
	if !ok || len(raw) == 0 || isJSONNull(raw) {
		return nil
	}
	first := raw[0]
	if first == '"' || first == 't' || first == 'f' || first == '[' || first == '{' {
		return types.RpcErrorExpectedField(field, "unsigned integer")
	}
	if first == '-' {
		return types.RpcErrorExpectedField(field, "unsigned integer")
	}
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), jsonNullBytes)
}

// isJSONObjectOrNull mirrors rippled isObjectOrNull(): true for `{...}` or
// `null`. Caller should still attempt to extract sub-fields, which will
// produce missing-field errors for null.
func isJSONObjectOrNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	if bytes.Equal(trimmed, jsonNullBytes) {
		return true
	}
	return trimmed[0] == '{'
}

func isJSONString(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '"'
}

// unmarshalObjectOrNull decodes a value passed by isJSONObjectOrNull. For
// the JSON `null` literal it returns an empty map so callers can probe for
// sub-fields uniformly.
func unmarshalObjectOrNull(raw json.RawMessage) map[string]json.RawMessage {
	if isJSONNull(raw) {
		return map[string]json.RawMessage{}
	}
	var out map[string]json.RawMessage
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]json.RawMessage{}
	}
	return out
}
