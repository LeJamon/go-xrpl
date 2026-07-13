package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountOffersMethod handles account_offers: it lists the Offer ledger
// entries the account currently owns.
type AccountOffersMethod struct{ BaseHandler }

func (m *AccountOffersMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	fields, account, parseErr := accountPageParams(params)
	if parseErr != nil {
		return nil, parseErr
	}
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	ledgerIndex, selErr := preflightAccountPage(ctx, params, account, "Failed to get account information")
	if selErr != nil {
		return nil, selErr
	}

	limit, limitErr := ReadLimitField(params, LimitAccountOffers, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	markerStr, mErr := markerString(fields["marker"])
	if mErr != nil {
		return nil, mErr
	}
	result, err := ctx.Services.Ledger.GetAccountOffers(ctx.Context, account, ledgerIndex, limit, markerStr)
	if err != nil {
		return nil, mapAccountQueryErr(err, fmt.Sprintf("Failed to get account offers: %v", err))
	}

	// Build response
	response := map[string]any{
		"account": result.Account,
		"offers":  result.Offers,
	}
	fillLedgerFields(response, ledgerIndex, FormatLedgerHash(result.LedgerHash), result.LedgerIndex, ctx.Services.Ledger.GetCurrentLedgerIndex(), result.Validated)

	// rippled only includes limit when there is a marker (pagination continues)
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response, nil
}
