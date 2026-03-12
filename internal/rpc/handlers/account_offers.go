package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountOffersMethod handles the account_offers RPC method
type AccountOffersMethod struct{ BaseHandler }

func (m *AccountOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Strict bool `json:"strict,omitempty"`
		types.PaginationParams
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if err := ValidateAccount(request.Account); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Get account offers from the ledger service
	limit := ClampLimit(request.Limit, LimitAccountOffers, ctx.IsAdmin)
	result, err := types.Services.Ledger.GetAccountOffers(request.Account, ledgerIndex, limit)
	if err != nil {
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    19, // actNotFound
				Message: "Account not found.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get account offers: " + err.Error())
	}

	// Build response
	response := map[string]interface{}{
		"account":      result.Account,
		"offers":       result.Offers,
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	// rippled only includes limit when there is a marker (pagination continues)
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response, nil
}

