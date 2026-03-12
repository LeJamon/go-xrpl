package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// DepositAuthorizedMethod handles the deposit_authorized RPC method
type DepositAuthorizedMethod struct{}

func (m *DepositAuthorizedMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		SourceAccount      string   `json:"source_account"`
		DestinationAccount string   `json:"destination_account"`
		Credentials        []string `json:"credentials,omitempty"`
		types.LedgerSpecifier
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if request.SourceAccount == "" {
		return nil, types.RpcErrorInvalidParams("Missing field 'source_account'.")
	}

	if request.DestinationAccount == "" {
		return nil, types.RpcErrorInvalidParams("Missing field 'destination_account'.")
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Determine ledger index to use
	ledgerIndex := "validated"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Call the service
	result, err := types.Services.Ledger.GetDepositAuthorized(
		request.SourceAccount,
		request.DestinationAccount,
		ledgerIndex,
	)
	if err != nil {
		// Handle specific errors
		errMsg := err.Error()

		// Source account not found
		if errMsg == "source account not found" {
			return nil, &types.RpcError{
				Code:    types.RpcSRC_ACT_NOT_FOUND,
				Message: "Source account not found.",
			}
		}

		// Destination account not found
		if errMsg == "destination account not found" {
			return nil, &types.RpcError{
				Code:    types.RpcDST_ACT_NOT_FOUND,
				Message: "Destination account not found.",
			}
		}

		// Check for malformed source_account address
		if len(errMsg) > 32 && errMsg[:32] == "invalid source_account address: " {
			return nil, &types.RpcError{
				Code:    types.RpcACT_MALFORMED,
				Message: "Account malformed.",
			}
		}

		// Check for malformed destination_account address
		if len(errMsg) > 37 && errMsg[:37] == "invalid destination_account address: " {
			return nil, &types.RpcError{
				Code:    types.RpcACT_MALFORMED,
				Message: "Account malformed.",
			}
		}

		return nil, types.RpcErrorInternal(errMsg)
	}

	// Build response
	response := map[string]interface{}{
		"source_account":      result.SourceAccount,
		"destination_account": result.DestinationAccount,
		"deposit_authorized":  result.DepositAuthorized,
		"ledger_hash":         FormatLedgerHash(result.LedgerHash),
		"ledger_index":        result.LedgerIndex,
		"validated":           result.Validated,
	}

	// Echo credentials in response if provided (matches rippled)
	if len(request.Credentials) > 0 {
		response["credentials"] = request.Credentials
	}

	return response, nil
}

func (m *DepositAuthorizedMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *DepositAuthorizedMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *DepositAuthorizedMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
