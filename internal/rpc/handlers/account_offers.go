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
	marker, mErr := markerString(fields["marker"])
	if mErr != nil {
		return nil, mErr
	}
	if _, present := fields["marker"]; present {
		if marker == "" {
			return nil, types.RPCErrorInvalidField("marker")
		}
	}

	result, err := ctx.Services.Ledger.GetAccountOffers(ctx.Context, account, ledgerIndex, limit, marker)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RPCErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, types.RPCErrorInvalidField("marker")
		}
		if errors.Is(err, svcerr.ErrStaleMarker) {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to get account offers: %v", err))
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
