package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

type BookOffersMethod struct{ BaseHandler }

func (m *BookOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		TakerGets json.RawMessage `json:"taker_gets"`
		TakerPays json.RawMessage `json:"taker_pays"`
		Taker     string          `json:"taker,omitempty"`
		Domain    string          `json:"domain,omitempty"`
		// Limit is a pointer so we can distinguish "absent" (use default)
		// from "explicit 0" (return zero offers), matching rippled
		// readLimitField semantics (RPCHelpers.cpp:703-712).
		Limit *uint32 `json:"limit,omitempty"`
		types.LedgerSpecifier
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

	// Validate currencies (rippled rejects non-standard currency codes)
	if rpcErr := validateCurrency(takerPays.Currency); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := validateCurrency(takerGets.Currency); rpcErr != nil {
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
	// in addition to the strict 64-char form (base_uint.h:228-234).
	domain := request.Domain
	if domain != "" && domain != "0" {
		if len(domain) != 64 {
			return nil, types.RpcErrorDomainMalformed()
		}
		if _, derr := hex.DecodeString(domain); derr != nil {
			return nil, types.RpcErrorDomainMalformed()
		}
	}
	if domain == "0" {
		domain = "0000000000000000000000000000000000000000000000000000000000000000"
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

// validateCurrency checks that a currency code is valid per rippled rules:
// empty or "XRP" (native), exactly 3 characters (ISO), or exactly 40 hex characters.
// Reference: rippled UintTypes.cpp to_currency()
func validateCurrency(currency string) *types.RpcError {
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
	return types.RpcErrorSrcCurMalformed("Source currency is malformed.")
}
