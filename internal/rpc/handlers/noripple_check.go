package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// NoRippleCheckMethod handles the noripple_check RPC method
type NoRippleCheckMethod struct{}

func (m *NoRippleCheckMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Role         string `json:"role,omitempty"` // "gateway" or "user"
		Transactions bool   `json:"transactions,omitempty"`
		Limit        uint32 `json:"limit,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if err := RequireAccount(request.Account); err != nil {
		return nil, err
	}

	if request.Role == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: role")
	}

	if request.Role != "gateway" && request.Role != "user" {
		return nil, types.RpcErrorInvalidParams("Invalid field 'role'.")
	}

	// API v2+ requires transactions to be a boolean
	if ctx.ApiVersion > 1 && params != nil {
		// Check if transactions field exists and is not a boolean
		var rawParams map[string]json.RawMessage
		if err := json.Unmarshal(params, &rawParams); err == nil {
			if txField, ok := rawParams["transactions"]; ok {
				// Try to unmarshal as bool
				var boolVal bool
				if err := json.Unmarshal(txField, &boolVal); err != nil {
					return nil, types.RpcErrorInvalidParams("Invalid field 'transactions'.")
				}
			}
		}
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
	result, err := types.Services.Ledger.GetNoRippleCheck(
		request.Account,
		request.Role,
		ledgerIndex,
		request.Limit,
		request.Transactions,
	)
	if err != nil {
		// Handle specific errors
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
		return nil, types.RpcErrorInternal(err.Error())
	}

	// Build response
	response := map[string]interface{}{
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	// Problems is always present (may be empty array)
	if result.Problems != nil {
		response["problems"] = result.Problems
	} else {
		response["problems"] = []string{}
	}

	// Transactions only included if requested
	if request.Transactions && len(result.Transactions) > 0 {
		transactions := make([]map[string]interface{}, len(result.Transactions))
		for i, tx := range result.Transactions {
			txMap := map[string]interface{}{
				"TransactionType": tx.TransactionType,
				"Account":         tx.Account,
				"Fee":             tx.Fee,
				"Sequence":        tx.Sequence,
			}
			if tx.SetFlag != 0 {
				txMap["SetFlag"] = tx.SetFlag
			}
			if tx.Flags != 0 {
				txMap["Flags"] = tx.Flags
			}
			if tx.LimitAmount != nil {
				txMap["LimitAmount"] = tx.LimitAmount
			}
			transactions[i] = txMap
		}
		response["transactions"] = transactions
	}

	return response, nil
}

func (m *NoRippleCheckMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *NoRippleCheckMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *NoRippleCheckMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
