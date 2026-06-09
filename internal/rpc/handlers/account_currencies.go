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

func (m *AccountCurrenciesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Strict bool `json:"strict,omitempty"`
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

	ledgerIndex := resolveLedgerIndex(request.LedgerIndex)

	// Get account currencies from the ledger service
	result, err := ctx.Services.Ledger.GetAccountCurrencies(
		ctx.Context,
		request.Account,
		ledgerIndex,
	)
	if err != nil {
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RpcErrorActNotFound("Account not found.")
		}
		if len(err.Error()) > 24 && err.Error()[:24] == "invalid account address:" {
			return nil, &types.RpcError{
				Code:    types.RpcACT_NOT_FOUND,
				Message: "Account malformed.",
			}
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get account currencies: %v", err))
	}

	// Build response
	response := map[string]any{
		"ledger_hash":        FormatLedgerHash(result.LedgerHash),
		"ledger_index":       result.LedgerIndex,
		"receive_currencies": result.ReceiveCurrencies,
		"send_currencies":    result.SendCurrencies,
		"validated":          result.Validated,
	}

	return response, nil
}
