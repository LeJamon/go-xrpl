package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountLinesMethod handles the account_lines RPC method
type AccountLinesMethod struct{}

func (m *AccountLinesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Peer string `json:"peer,omitempty"`
		types.PaginationParams
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if request.Account == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: account")
	}

	// Check if ledger service is available
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Get account lines from the ledger service
	result, err := types.Services.Ledger.GetAccountLines(request.Account, ledgerIndex, request.Peer, request.Limit)
	if err != nil {
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    19, // actNotFound
				Message: "Account not found.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get account lines: " + err.Error())
	}

	// Build response
	response := map[string]interface{}{
		"account":      result.Account,
		"lines":        result.Lines,
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	if result.Marker != "" {
		response["marker"] = result.Marker
	}

	return response, nil
}

func (m *AccountLinesMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *AccountLinesMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
