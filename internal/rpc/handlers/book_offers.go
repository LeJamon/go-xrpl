package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// Canonical base58 encodings of the two reserved AccountIDs from rippled
// AccountID.cpp:178-189. xrpAccountAddress is the zero AccountID returned by
// `xrpAccount()`; accountOneAddress is the `noAccount()` sentinel.
const (
	xrpAccountAddress = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"
	accountOneAddress = "rrrrrrrrrrrrrrrrrrrrBZbvji"
)

type BookOffersMethod struct{ BaseHandler }

func (m *BookOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		TakerGets json.RawMessage `json:"taker_gets"`
		TakerPays json.RawMessage `json:"taker_pays"`
		Taker     string          `json:"taker,omitempty"`
		// Domain is a pointer so we can distinguish absent ("no domain") from
		// explicit empty / non-string ("domainMalformed"), matching rippled
		// BookOffers.cpp:175-189 (params.isMember(jss::domain) check).
		Domain *string `json:"domain,omitempty"`
		// Limit is a pointer so we can distinguish "absent" (use default)
		// from "explicit 0" (return zero offers), matching rippled
		// readLimitField semantics (RPCHelpers.cpp:703-712).
		Limit *uint32 `json:"limit,omitempty"`
		// Marker and Proof are accepted but unused; rippled threads them to
		// getBookPage which does not act on them. Declared here so strict
		// JSON decoders never reject a well-formed rippled request.
		Marker json.RawMessage `json:"marker,omitempty"`
		Proof  json.RawMessage `json:"proof,omitempty"`
		types.LedgerSpecifier
	}

	// Pre-validate "limit" so a string value yields rippled's specific
	// `Invalid field 'limit', not unsigned integer.` (RPCHelpers.cpp:706-707)
	// instead of a generic JSON-parse error.
	if rpcErr := preValidateUintField(params, "limit"); rpcErr != nil {
		return nil, rpcErr
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if len(request.TakerGets) == 0 || len(request.TakerPays) == 0 {
		return nil, types.RpcErrorInvalidParams("Both taker_gets and taker_pays are required")
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Parse taker_gets amount
	takerGets, err := ParseAmountFromJSON(request.TakerGets)
	if err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid taker_gets: %v", err))
	}

	// Parse taker_pays amount
	takerPays, err := ParseAmountFromJSON(request.TakerPays)
	if err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid taker_pays: %v", err))
	}

	// Validate currencies. Source side uses srcCurMalformed
	// (rippled BookOffers.cpp:80-86); destination side uses dstAmtMalformed
	// (BookOffers.cpp:90-96).
	if !isValidCurrencyCode(takerPays.Currency) {
		return nil, types.RpcErrorSrcCurMalformed(
			"Invalid field 'taker_pays.currency', bad currency.")
	}
	if !isValidCurrencyCode(takerGets.Currency) {
		return nil, types.RpcErrorDstAmtMalformed(
			"Invalid field 'taker_gets.currency', bad currency.")
	}

	// Reject noAccount() / ACCOUNT_ONE issuers and XRP/issuer mismatches on
	// both sides. Matches rippled BookOffers.cpp:100-129 (pay) and :133-162
	// (get).
	if rpcErr := validateBookSide(takerPays, true); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := validateBookSide(takerGets, false); rpcErr != nil {
		return nil, rpcErr
	}

	// Validate taker (base58 classic address). Matches rippled BookOffers.cpp:164-173.
	if request.Taker != "" {
		if _, _, err := addresscodec.DecodeClassicAddressToAccountID(request.Taker); err != nil {
			return nil, types.RpcErrorInvalidField("taker")
		}
	}

	// Validate domain (uint256 hex). Matches rippled BookOffers.cpp:175-189.
	// rippled's uint256::parseHex accepts the literal "0" as the zero value
	// in addition to the strict 64-char form (base_uint.h:228-234). An
	// explicit empty-string domain is malformed because parseHex("") fails
	// the length check.
	var domain string
	if request.Domain != nil {
		domain = *request.Domain
		if domain != "0" {
			if len(domain) != 64 {
				return nil, types.RpcErrorDomainMalformed()
			}
			if _, derr := hex.DecodeString(domain); derr != nil {
				return nil, types.RpcErrorDomainMalformed()
			}
		} else {
			domain = "0000000000000000000000000000000000000000000000000000000000000000"
		}
	}

	// Reject equal markets. Matches rippled BookOffers.cpp:191-195.
	if takerPays.Currency == takerGets.Currency && takerPays.Issuer == takerGets.Issuer {
		return nil, types.RpcErrorBadMarket()
	}

	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Limit handling mirrors rippled readLimitField (RPCHelpers.cpp:703-712):
	// absent -> rdefault (60); present -> clamp to [rmin, rmax] = [0, 100],
	// unless the role is unlimited (admin/identified) in which case the raw
	// value is passed through.
	limit := LimitBookOffers.Default
	if request.Limit != nil {
		limit = *request.Limit
		if !ctx.Unlimited {
			if limit < LimitBookOffers.Min {
				limit = LimitBookOffers.Min
			}
			if limit > LimitBookOffers.Max {
				limit = LimitBookOffers.Max
			}
		}
	}
	result, err := ctx.Services.Ledger.GetBookOffers(ctx.Context, takerGets, takerPays, request.Taker, domain, ledgerIndex, limit)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get book offers: %v", err))
	}

	// rippled book_offers does not echo a "limit" field in the response.
	return map[string]interface{}{
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"offers":       result.Offers,
		"validated":    result.Validated,
	}, nil
}

// ParseAmountFromJSON parses an amount from JSON (either XRP string or IOU object)
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

// validateBookSide enforces rippled's issuer-vs-currency cross checks for
// one side of a book request (BookOffers.cpp:98-162). `isPay=true` selects
// the rpcSRC_* error codes; `isPay=false` selects rpcDST_*.
func validateBookSide(amt types.Amount, isPay bool) *types.RpcError {
	makeErr := types.RpcErrorDstIsrMalformed
	field := "taker_gets.issuer"
	if isPay {
		makeErr = types.RpcErrorSrcIsrMalformed
		field = "taker_pays.issuer"
	}

	// Reject the rippled noAccount() sentinel. Matches BookOffers.cpp:110-114, :143-146.
	if amt.Issuer == accountOneAddress {
		return makeErr(fmt.Sprintf("Invalid field '%s', bad issuer account one.", field))
	}

	isXRPCurrency := amt.Currency == "" || amt.Currency == "XRP"
	isXRPIssuer := amt.Issuer == "" || amt.Issuer == xrpAccountAddress

	if isXRPCurrency && !isXRPIssuer {
		return makeErr(fmt.Sprintf(
			"Unneeded field '%s' for XRP currency specification.", field))
	}
	if !isXRPCurrency && isXRPIssuer {
		return makeErr(fmt.Sprintf(
			"Invalid field '%s', expected non-XRP issuer.", field))
	}
	return nil
}

// preValidateUintField inspects the raw JSON params for a numeric field that
// rippled requires to be an unsigned integer. A string-typed value yields the
// rippled-specific `Invalid field '<name>', not unsigned integer.` error
// (RPCHelpers.cpp:706-707) instead of the generic JSON-parse failure that
// `json.Unmarshal` into a `*uint32` would otherwise produce.
func preValidateUintField(params json.RawMessage, field string) *types.RpcError {
	if len(params) == 0 {
		return nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(params, &probe); err != nil {
		return nil
	}
	raw, ok := probe[field]
	if !ok || len(raw) == 0 {
		return nil
	}
	first := raw[0]
	if first == '"' || first == 't' || first == 'f' || first == '[' || first == '{' || first == 'n' {
		return types.RpcErrorExpectedField(field, "unsigned integer")
	}
	if first == '-' {
		return types.RpcErrorExpectedField(field, "unsigned integer")
	}
	return nil
}
