package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// BookOffersMethod handles the book_offers RPC method
type BookOffersMethod struct{ BaseHandler }

func (m *BookOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		TakerGets json.RawMessage  `json:"taker_gets"`
		TakerPays json.RawMessage  `json:"taker_pays"`
		Taker     *json.RawMessage `json:"taker,omitempty"`
		types.LedgerSpecifier
		types.PaginationParams
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

	takerPays, rpcErr := parseBookSide(request.TakerPays, "taker_pays",
		types.RpcErrorSrcCurMalformed, types.RpcErrorSrcIsrMalformed)
	if rpcErr != nil {
		return nil, rpcErr
	}
	takerGets, rpcErr := parseBookSide(request.TakerGets, "taker_gets",
		types.RpcErrorDstAmtMalformed, types.RpcErrorDstIsrMalformed)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if sameMarket(takerGets, takerPays) {
		return nil, types.RpcErrorBadMarket()
	}

	taker, rpcErr := parseTaker(request.Taker)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
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

// ParseAmountFromJSON parses an amount from JSON (either XRP string or IOU object).
func ParseAmountFromJSON(data json.RawMessage) (types.Amount, error) {
	// Try parsing as string first (XRP amount)
	var xrpAmount string
	if err := json.Unmarshal(data, &xrpAmount); err == nil {
		return types.Amount{Value: xrpAmount}, nil
	}

	// Try parsing as IOU object
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

// parseBookSide mirrors rippled BookOffers.cpp:51-162 for taker_pays /
// taker_gets parsing: object-or-null shape, required currency field,
// currency validation, issuer parsing with noAccount rejection, and the
// XRP-vs-IOU issuer cross-checks. The two error constructors capture the
// side-specific tokens (rpcSRC_* for taker_pays, rpcDST_* for taker_gets).
func parseBookSide(
	data json.RawMessage,
	field string,
	currencyErr func(string) *types.RpcError,
	issuerErr func(string) *types.RpcError,
) (types.Amount, *types.RpcError) {
	var raw struct {
		Currency *string `json:"currency"`
		Issuer   *string `json:"issuer"`
		Value    string  `json:"value,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return types.Amount{}, types.RpcErrorObjectField(field)
	}
	if raw.Currency == nil {
		return types.Amount{}, types.RpcErrorMissingField(field + ".currency")
	}
	if rpcErr := validateCurrencyField(*raw.Currency, currencyErr); rpcErr != nil {
		return types.Amount{}, rpcErr
	}
	currency := *raw.Currency
	isXRP := currency == "" || currency == "XRP"

	var issuer string
	if raw.Issuer != nil {
		issuer = *raw.Issuer
		_, accID, err := addresscodec.DecodeClassicAddressToAccountID(issuer)
		if err != nil {
			return types.Amount{}, issuerErr("Invalid field '" + field + ".issuer', bad issuer.")
		}
		if len(accID) == 20 && string(accID) == string(accountOneID[:]) {
			return types.Amount{}, issuerErr("Invalid field '" + field + ".issuer', bad issuer account one.")
		}
	}

	issuerIsXRP := issuer == ""
	if isXRP && !issuerIsXRP {
		return types.Amount{}, issuerErr("Unneeded field '" + field + ".issuer' for XRP currency specification.")
	}
	if !isXRP && issuerIsXRP {
		return types.Amount{}, issuerErr("Invalid field '" + field + ".issuer', expected non-XRP issuer.")
	}

	return types.Amount{Currency: currency, Issuer: issuer, Value: raw.Value}, nil
}

// accountOneID is rippled's noAccount() sentinel — base58 "rrrrrrrrrrrrrrrrrrrrBZbvji",
// the canonical "ACCOUNT_ONE" id that to_issuer accepts but the handler rejects.
var accountOneID = func() [20]byte {
	var id [20]byte
	_, raw, _ := addresscodec.DecodeClassicAddressToAccountID(state.AccountOneAddress)
	copy(id[:], raw)
	return id
}()

// validateCurrencyField wraps validateCurrency with the side-specific error
// constructor (rippled emits rpcSRC_CUR_MALFORMED for taker_pays.currency and
// rpcDST_AMT_MALFORMED for taker_gets.currency).
func validateCurrencyField(currency string, errFn func(string) *types.RpcError) *types.RpcError {
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
	return errFn("Invalid currency code.")
}

// parseTaker mirrors BookOffers.cpp:164-173: the parameter must be a string
// when present, and an empty / unparsable string is invalid_field_error.
func parseTaker(raw *json.RawMessage) (string, *types.RpcError) {
	if raw == nil {
		return "", nil
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
