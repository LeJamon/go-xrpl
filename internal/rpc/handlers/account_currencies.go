package handlers

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountCurrenciesMethod handles account_currencies: it reports the
// currencies the account can send and receive, derived from its trust lines.
type AccountCurrenciesMethod struct{ BaseHandler }

func (m *AccountCurrenciesMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	var request struct {
		types.AccountParam
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

	ledgerIndex, selErr := resolveLedgerSelector(params)
	if selErr != nil {
		return nil, selErr
	}

	// Get account currencies from the ledger service
	result, err := ctx.Services.Ledger.GetAccountCurrencies(
		ctx.Context,
		request.Account,
		ledgerIndex,
	)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		if errors.Is(err, svcerr.ErrAccountMalformed) {
			return nil, types.RPCErrorActMalformed("Account malformed.")
		}
		return nil, mapAccountQueryErr(err, fmt.Sprintf("Failed to get account currencies: %v", err))
	}

	// Build response
	response := map[string]any{
		"receive_currencies": result.ReceiveCurrencies,
		"send_currencies":    result.SendCurrencies,
	}
	fillLedgerFields(response, ledgerIndex, FormatLedgerHash(result.LedgerHash), result.LedgerIndex, ctx.Services.Ledger.GetCurrentLedgerIndex(), result.Validated)

	return response, nil
}
