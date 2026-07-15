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
	fields, fieldsErr := rawJSONFields(params)
	if fieldsErr != nil {
		return nil, fieldsErr
	}
	accountRaw, ok := fields["account"]
	if !ok {
		return nil, types.RPCErrorMissingField("account")
	}
	account, ok := rawJSONString(accountRaw)
	if !ok {
		return nil, types.RPCErrorInvalidField("account")
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	ledgerIndex, selErr := resolveLedgerSelector(parsedLedgerSpec)
	if selErr != nil {
		return nil, selErr
	}
	ledger, validated, lookupErr := LookupLedger(ctx, parsedLedgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if !types.IsValidClassicAddress(account) {
		return nil, types.RPCErrorActMalformed("Account malformed.").WithExtra(ledgerEntryResponseFields(ledger, validated))
	}
	if accountErr := requireAccountExists(ctx, account, ledgerIndex); accountErr != nil {
		return nil, accountErr
	}

	limit, limitErr := ReadLimitField(params, LimitAccountOffers, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	marker := ""
	if markerRaw, ok := fields["marker"]; ok {
		var valid bool
		marker, valid = rawJSONString(markerRaw)
		if !valid {
			return nil, types.RPCErrorExpectedField("marker", "string")
		}
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
	response := ledgerEntryResponseFields(ledger, validated)
	response["account"] = result.Account
	response["offers"] = result.Offers

	// rippled only includes limit when there is a marker (pagination continues)
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response, nil
}
