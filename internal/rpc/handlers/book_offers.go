package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
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

func (m *BookOffersMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	if err := RequireNotBusyBookOffers(ctx); err != nil {
		return nil, err
	}
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	probe := map[string]json.RawMessage{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &probe); err != nil {
			return nil, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid params: %v", err))
		}
	}

	ledgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	ledgerIndex, selectorErr := resolveLedgerSelector(ledgerSpec)
	if selectorErr != nil {
		return nil, selectorErr
	}

	ledger, validated, lookupErr := LookupLedger(ctx, ledgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}

	// Validation order mirrors rippled BookOffers.cpp:51-199 exactly so that
	// clients depending on rippled's specific failure precedence (e.g. the
	// fixtures in rippled/src/test/rpc/Book_test.cpp) see the same error
	// emitted first.
	paysRaw, ok := probe["taker_pays"]
	if !ok {
		return nil, types.RPCErrorMissingField("taker_pays")
	}
	getsRaw, ok := probe["taker_gets"]
	if !ok {
		return nil, types.RPCErrorMissingField("taker_gets")
	}
	if !isJSONObjectOrNull(paysRaw) {
		return nil, types.RPCErrorExpectedField("taker_pays", "object")
	}
	if !isJSONObjectOrNull(getsRaw) {
		return nil, types.RPCErrorExpectedField("taker_gets", "object")
	}
	paysInner := unmarshalObjectOrNull(paysRaw)
	getsInner := unmarshalObjectOrNull(getsRaw)

	if rpcErr := validateTakerBookJSON(paysInner, "taker_pays"); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := validateTakerBookJSON(getsInner, "taker_gets"); rpcErr != nil {
		return nil, rpcErr
	}

	takerPays, paysIssuerID, rpcErr := parseTakerBookAsset(paysInner, true)
	if rpcErr != nil {
		return nil, rpcErr
	}
	takerGets, getsIssuerID, rpcErr := parseTakerBookAsset(getsInner, false)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if !takerPays.IsMPT() {
		issuer, issuerID, issuerErr := readAndValidateIssuer(paysInner, takerPays.Currency, true)
		if issuerErr != nil {
			return nil, issuerErr
		}
		takerPays.Issuer = canonIssuerString(issuer, takerPays.Currency)
		paysIssuerID = issuerID
	}
	if !takerGets.IsMPT() {
		issuer, issuerID, issuerErr := readAndValidateIssuer(getsInner, takerGets.Currency, false)
		if issuerErr != nil {
			return nil, issuerErr
		}
		takerGets.Issuer = canonIssuerString(issuer, takerGets.Currency)
		getsIssuerID = issuerID
	}

	// taker (BookOffers.cpp:164-173).
	var takerStr string
	if rawTaker, ok := probe["taker"]; ok {
		if !isJSONString(rawTaker) {
			return nil, types.RPCErrorExpectedField("taker", "string")
		}
		if err := json.Unmarshal(rawTaker, &takerStr); err != nil {
			return nil, types.RPCErrorExpectedField("taker", "string")
		}
		if _, _, err := addresscodec.DecodeClassicAddressToAccountID(takerStr); err != nil {
			return nil, types.RPCErrorInvalidField("taker")
		}
	}

	// domain (BookOffers.cpp:175-189). Non-string OR parseHex-fail both
	// produce the same rpcDOMAIN_MALFORMED with "Unable to parse domain.".
	var domain string
	if rawDomain, ok := probe["domain"]; ok {
		if !isJSONString(rawDomain) {
			return nil, types.RPCErrorDomainMalformed("Unable to parse domain.")
		}
		var domainStr string
		if err := json.Unmarshal(rawDomain, &domainStr); err != nil {
			return nil, types.RPCErrorDomainMalformed("Unable to parse domain.")
		}
		// rippled base_uint.h:228 accepts the literal "0" as zero uint256.
		if domainStr == "0" {
			domain = "0000000000000000000000000000000000000000000000000000000000000000"
		} else {
			if len(domainStr) != 64 {
				return nil, types.RPCErrorDomainMalformed("Unable to parse domain.")
			}
			if _, err := hex.DecodeString(domainStr); err != nil {
				return nil, types.RPCErrorDomainMalformed("Unable to parse domain.")
			}
			domain = domainStr
		}
	}

	// bad market (BookOffers.cpp:191-195). Compare canonical forms: XRP
	// currency normalizes to zero, issuers normalize to their decoded
	// 20-byte AccountIDs (any valid encoding of the same account collides).
	if sameBookAsset(takerPays, paysIssuerID, takerGets, getsIssuerID) {
		return nil, types.RPCErrorBadMarket()
	}

	// limit (BookOffers.cpp:197-199, readLimitField at RPCHelpers.cpp:703).
	if rpcErr := preValidateUintField(probe, "limit"); rpcErr != nil {
		return nil, rpcErr
	}
	limit := LimitBookOffers.Default
	if rawLimit, ok := probe["limit"]; ok && !isJSONNull(rawLimit) {
		var v uint32
		if err := json.Unmarshal(rawLimit, &v); err != nil {
			return nil, types.RPCErrorExpectedField("limit", "unsigned integer")
		}
		// rippled readLimitField rejects an explicit limit=0 for every role.
		if v == 0 {
			return nil, types.RPCErrorInvalidField("limit")
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

	// Rippled enables proof handling solely when the member is present,
	// regardless of its JSON value (BookOffers.cpp:201).
	_, withProofs := probe["proof"]

	result, err := ctx.Services.Ledger.GetBookOffers(ctx.Context, takerGets, takerPays, takerStr, domain, ledgerIndex, limit, "", withProofs)
	if err != nil {
		// Mirrors rippled AccountOffers.cpp:107-132 two-tier mapping:
		// malformed / wrong-scope marker → invalid_field_error("marker");
		// well-formed marker whose referent was consumed between pages →
		// rpcINVALID_PARAMS with a distinct message so clients can retry
		// against a pinned ledger.
		if errors.Is(err, svcerr.ErrStaleMarker) {
			return nil, types.RPCErrorInvalidParams("Invalid marker: object pointed to by marker is gone; retry with a pinned ledger_index or ledger_hash.")
		}
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, types.RPCErrorInvalidField("marker")
		}
		if errors.Is(err, svcerr.ErrLedgerNotFound) {
			return nil, types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to get book offers: %v", err))
	}

	response := ledgerEntryResponseFields(ledger, validated)
	response["offers"] = result.Offers
	return response, nil
}

func validateTakerBookJSON(inner map[string]json.RawMessage, name string) *types.RPCError {
	_, hasCurrency := inner["currency"]
	_, hasMPT := inner["mpt_issuance_id"]
	if !hasCurrency && !hasMPT {
		return types.RPCErrorMissingField(name + ".currency")
	}
	if hasMPT {
		if hasCurrency {
			return types.RPCErrorInvalidField(name)
		}
		if _, hasIssuer := inner["issuer"]; hasIssuer {
			return types.RPCErrorInvalidField(name)
		}
	}
	if raw, ok := inner["currency"]; ok && !isJSONString(raw) {
		return types.RPCErrorExpectedField(name+".currency", "string")
	}
	if raw, ok := inner["mpt_issuance_id"]; ok && !isJSONString(raw) {
		return types.RPCErrorExpectedField(name+".currency", "string")
	}
	return nil
}

func parseTakerBookAsset(inner map[string]json.RawMessage, isPay bool) (types.Amount, [20]byte, *types.RPCError) {
	if raw, ok := inner["currency"]; ok {
		var currency string
		_ = json.Unmarshal(raw, &currency)
		if !keylet.IsValidCurrencyCode(currency) {
			if isPay {
				return types.Amount{}, [20]byte{}, types.RPCErrorSrcCurMalformed(
					"Invalid field 'taker_pays.currency', bad currency.")
			}
			return types.Amount{}, [20]byte{}, types.RPCErrorDstAmtMalformed(
				"Invalid field 'taker_gets.currency', bad currency.")
		}
		return types.Amount{Currency: currency}, [20]byte{}, nil
	}

	var value string
	_ = json.Unmarshal(inner["mpt_issuance_id"], &value)
	id, ok := parseBookMPTID(value)
	if !ok {
		field := "taker_gets.mpt_issuance_id"
		makeErr := types.RPCErrorDstAmtMalformed
		if isPay {
			field = "taker_pays.mpt_issuance_id"
			makeErr = types.RPCErrorSrcCurMalformed
		}
		return types.Amount{}, [20]byte{}, makeErr(fmt.Sprintf("Invalid field '%s'", field))
	}
	var issuer [20]byte
	copy(issuer[:], id[4:])
	return types.Amount{MPTIssuanceID: strings.ToUpper(hex.EncodeToString(id[:]))}, issuer, nil
}

func parseBookMPTID(value string) ([24]byte, bool) {
	var id [24]byte
	if value == "0" {
		return id, true
	}
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != len(id) {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

func sameBookAsset(a types.Amount, aIssuer [20]byte, b types.Amount, bIssuer [20]byte) bool {
	if a.IsMPT() || b.IsMPT() {
		return a.IsMPT() && b.IsMPT() && strings.EqualFold(a.MPTIssuanceID, b.MPTIssuanceID)
	}
	return canonCurrency(a.Currency) == canonCurrency(b.Currency) && aIssuer == bIssuer
}

// readAndValidateIssuer decodes the issuer field for one side of the book and
// runs the rippled cross-checks (BookOffers.cpp:98-129 / :131-162). Returns
// the literal issuer string for downstream callers and the decoded AccountID
// for canonical-form comparisons (e.g. badMarket).
func readAndValidateIssuer(inner map[string]json.RawMessage, currency string, isPay bool) (string, [20]byte, *types.RPCError) {
	makeErr := types.RPCErrorDstIsrMalformed
	field := "taker_gets.issuer"
	if isPay {
		makeErr = types.RPCErrorSrcIsrMalformed
		field = "taker_pays.issuer"
	}

	var issuerStr string
	var issuerID [20]byte
	hasIssuer := false
	if rawIssuer, ok := inner["issuer"]; ok {
		if !isJSONString(rawIssuer) {
			return "", [20]byte{}, types.RPCErrorExpectedField(field, "string")
		}
		if err := json.Unmarshal(rawIssuer, &issuerStr); err != nil {
			return "", [20]byte{}, types.RPCErrorExpectedField(field, "string")
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

// preValidateUintField inspects the probed JSON for a numeric field that
// rippled requires to be an unsigned integer. A string-typed value yields the
// rippled-specific `Invalid field '<name>', not unsigned integer.` error
// (RPCHelpers.cpp:706-707) instead of the generic JSON-parse failure that
// `json.Unmarshal` into a `*uint32` would otherwise produce.
func preValidateUintField(probe map[string]json.RawMessage, field string) *types.RPCError {
	raw, ok := probe[field]
	if !ok || len(raw) == 0 || isJSONNull(raw) {
		return nil
	}
	first := raw[0]
	if first == '"' || first == 't' || first == 'f' || first == '[' || first == '{' {
		return types.RPCErrorExpectedField(field, "unsigned integer")
	}
	if first == '-' {
		return types.RPCErrorExpectedField(field, "unsigned integer")
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

// preResolveLedger mirrors rippled BookOffers.cpp:45-49 (RPC::lookupLedger).
// For the keyword specifiers (validated/current/closed/"") we defer to the
// service layer which always has those handles. For an explicit ledger_hash
// or numeric ledger_index we pre-resolve so a bogus value returns
// lgrNotFound / lgrIdxMalformed before any field-level validation runs.
func preResolveLedger(ctx *types.RPCContext, selector string) *types.RPCError {
	switch selector {
	case "", "current", "closed", "validated":
		return nil
	}
	if len(selector) == 64 {
		raw, _ := hex.DecodeString(selector)
		var h [32]byte
		copy(h[:], raw)
		if l, lerr := ctx.Services.Ledger.GetLedgerByHash(h); lerr != nil || l == nil {
			return types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		return nil
	}

	seq, perr := parseLedgerIndex(selector)
	if perr != nil {
		return types.RPCErrorExpectedField("ledger_index", "string or number")
	}
	if l, lerr := ctx.Services.Ledger.GetLedgerBySequence(uint32(seq)); lerr != nil || l == nil {
		return types.RPCErrorLgrNotFound("ledgerNotFound")
	}
	return nil
}
