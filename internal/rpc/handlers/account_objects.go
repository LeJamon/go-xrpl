package handlers

import (
	"encoding/hex"
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountObjectsMethod handles the account_objects RPC method
type AccountObjectsMethod struct{}

func (m *AccountObjectsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Type                 string `json:"type,omitempty"`
		DeletionBlockersOnly bool   `json:"deletion_blockers_only,omitempty"`
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

	// Get account objects from the ledger service
	result, err := types.Services.Ledger.GetAccountObjects(request.Account, ledgerIndex, request.Type, request.Limit)
	if err != nil {
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    19, // actNotFound
				Message: "Account not found.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get account objects: " + err.Error())
	}

	// Build account_objects array
	objects := make([]map[string]interface{}, len(result.AccountObjects))
	for i, obj := range result.AccountObjects {
		objects[i] = map[string]interface{}{
			"index":           obj.Index,
			"LedgerEntryType": obj.LedgerEntryType,
			"data":            hex.EncodeToString(obj.Data),
		}
	}

	response := map[string]interface{}{
		"account":         result.Account,
		"account_objects": objects,
		"ledger_hash":     FormatLedgerHash(result.LedgerHash),
		"ledger_index":    result.LedgerIndex,
		"validated":       result.Validated,
	}

	if result.Marker != "" {
		response["marker"] = result.Marker
	}

	return response, nil
}

func (m *AccountObjectsMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *AccountObjectsMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
