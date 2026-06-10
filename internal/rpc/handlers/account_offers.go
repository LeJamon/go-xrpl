package handlers

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountOffersMethod handles account_offers: it lists the Offer ledger
// entries the account currently owns.
type AccountOffersMethod struct{ BaseHandler }

func (m *AccountOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
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

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	ledgerIndex, selErr := resolveLedgerSelector(request.LedgerSpecifier)
	if selErr != nil {
		return nil, selErr
	}

	limit := ClampLimit(request.Limit, LimitAccountOffers, ctx.Unlimited)
	result, err := ctx.Services.Ledger.GetAccountOffers(ctx.Context, request.Account, ledgerIndex, limit)
	if err != nil {
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RpcErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrLedgerNotFound) {
			return nil, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get account offers: %v", err))
	}

	// Build response
	response := map[string]any{
		"account": result.Account,
		"offers":  result.Offers,
	}
	fillLedgerFields(response, ledgerIndex, FormatLedgerHash(result.LedgerHash), result.LedgerIndex, result.Validated)

	// rippled only includes limit when there is a marker (pagination continues)
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response, nil
}
