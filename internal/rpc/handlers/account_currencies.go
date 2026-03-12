package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountCurrenciesMethod handles the account_currencies RPC method
type AccountCurrenciesMethod struct{}

func (m *AccountCurrenciesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Strict bool `json:"strict,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if request.Account == "" {
		return nil, types.RpcErrorInvalidParams("Missing field 'account'.")
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Get account currencies from the ledger service
	result, err := types.Services.Ledger.GetAccountCurrencies(
		request.Account,
		ledgerIndex,
	)
	if err != nil {
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    types.RpcACT_NOT_FOUND,
				Message: "Account not found.",
			}
		}
		// Check for malformed account address
		if len(err.Error()) > 24 && err.Error()[:24] == "invalid account address:" {
			return nil, &types.RpcError{
				Code:    types.RpcACT_NOT_FOUND,
				Message: "Account malformed.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get account currencies: " + err.Error())
	}

	// Build response
	response := map[string]interface{}{
		"ledger_hash":        FormatLedgerHash(result.LedgerHash),
		"ledger_index":       result.LedgerIndex,
		"receive_currencies": result.ReceiveCurrencies,
		"send_currencies":    result.SendCurrencies,
		"validated":          result.Validated,
	}

	return response, nil
}

func (m *AccountCurrenciesMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *AccountCurrenciesMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *AccountCurrenciesMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
