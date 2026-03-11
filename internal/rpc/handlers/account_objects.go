package handlers

import (
	"encoding/hex"
	"encoding/json"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountObjectsMethod handles the account_objects RPC method
type AccountObjectsMethod struct{}

// deletionBlockerTypes lists SLE types that block account deletion
var deletionBlockerTypes = map[string]bool{
	"RippleState":   true,
	"Check":         true,
	"Escrow":        true,
	"PayChannel":    true,
	"NFTokenPage":   true,
	"NFTokenOffer":  true,
	"MPToken":       true,
	"Credential":    true,
	"Bridge":        true,
}

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

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	result, err := types.Services.Ledger.GetAccountObjects(request.Account, ledgerIndex, request.Type, request.Limit)
	if err != nil {
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    19,
				Message: "Account not found.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get account objects: " + err.Error())
	}

	// Build account_objects array with deserialized fields
	objects := make([]map[string]interface{}, 0, len(result.AccountObjects))
	for _, obj := range result.AccountObjects {
		hexData := hex.EncodeToString(obj.Data)
		decoded, err := binarycodec.Decode(hexData)
		if err != nil {
			// Fallback to raw data if decode fails
			objects = append(objects, map[string]interface{}{
				"index":           obj.Index,
				"LedgerEntryType": obj.LedgerEntryType,
				"data":            hexData,
			})
			continue
		}
		decoded["index"] = obj.Index
		objects = append(objects, decoded)
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

func (m *AccountObjectsMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
