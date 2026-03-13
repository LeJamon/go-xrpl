package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// BookOffersMethod handles the book_offers RPC method
type BookOffersMethod struct{ BaseHandler }

func (m *BookOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		TakerGets json.RawMessage `json:"taker_gets"`
		TakerPays json.RawMessage `json:"taker_pays"`
		Taker     string          `json:"taker,omitempty"`
		types.LedgerSpecifier
		types.PaginationParams
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if len(request.TakerGets) == 0 || len(request.TakerPays) == 0 {
		return nil, types.RpcErrorInvalidParams("Both taker_gets and taker_pays are required")
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Parse taker_gets amount
	takerGets, err := ParseAmountFromJSON(request.TakerGets)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid taker_gets: " + err.Error())
	}

	// Parse taker_pays amount
	takerPays, err := ParseAmountFromJSON(request.TakerPays)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid taker_pays: " + err.Error())
	}

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Clamp the limit using rippled's bookOffers range {0, 60, 100}.
	// When the user omits "limit" (zero value), ClampLimit returns the default (60).
	limit := ClampLimit(request.Limit, LimitBookOffers, ctx.IsAdmin)
	result, err := types.Services.Ledger.GetBookOffers(takerGets, takerPays, ledgerIndex, limit)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to get book offers: " + err.Error())
	}

	// Build response matching rippled's book_offers structure.
	//
	// TODO(#107): owner_funds, taker_gets_funded, taker_pays_funded
	// These fields require computing the offer owner's available balance and
	// adjusting for transfer fees. This is a service-layer concern implemented
	// in rippled's NetworkOPsImp::getBookPage (see NetworkOPs.cpp).
	// Currently the service layer returns these fields if it computes them;
	// otherwise they are omitted from the BookOffer struct (omitempty).
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

// ParseAmountFromJSON parses an amount from JSON (either XRP string or IOU object)
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
