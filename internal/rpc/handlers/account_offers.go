package handlers

import (
	"encoding/json"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
)

// AccountOffersMethod handles account_offers: it lists the Offer ledger
// entries the account currently owns.
type AccountOffersMethod struct{ baseHandler }

func (m *AccountOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	fields, account, parseErr := accountPageParams(params)
	if parseErr != nil {
		return nil, parseErr
	}
	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	ledgerIndex, ledgerFields, selErr := preflightAccountPage(ctx, params, account, "Failed to get account information", true)
	if selErr != nil {
		return nil, selErr
	}

	limit, limitErr := readLimitField(params, limitAccountOffers, ctx.Role.IsUnlimited())
	if limitErr != nil {
		return nil, limitErr
	}
	marker, mErr := markerString(fields["marker"])
	if mErr != nil {
		return nil, mErr
	}
	if _, present := fields["marker"]; present {
		if marker == "" {
			return nil, rpcerrors.RpcErrorInvalidField("marker")
		}
	}

	result, err := ctx.Services.Ledger().GetAccountOffers(ctx.Context, account, ledgerIndex, limit, marker)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, rpcerrors.RpcErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, rpcerrors.RpcErrorInvalidField("marker")
		}
		if errors.Is(err, svcerr.ErrStaleMarker) {
			return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
		return nil, rpcInternalError("account_offers: ledger query failed", err)
	}

	// Build response
	response := map[string]any{
		"account": result.Account,
		"offers":  result.Offers,
	}
	mergeLedgerFields(response, ledgerFields)

	// rippled only includes limit when there is a marker (pagination continues)
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	setLoadMedium(ctx)
	return response, nil
}
