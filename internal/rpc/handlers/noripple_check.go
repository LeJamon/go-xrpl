package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// NoRippleCheckMethod handles the noripple_check RPC method
// Reference: rippled/src/xrpld/rpc/handlers/NoRippleCheck.cpp
type NoRippleCheckMethod struct{ BaseHandler }

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

	if err := ValidateAccount(request.Account); err != nil {
		return nil, err
	}

	// rippled: missing_field_error("role")
	if request.Role == "" {
		return nil, types.RpcErrorMissingField("role")
	}

	// rippled: invalid_field_error("role")
	if request.Role != "gateway" && request.Role != "user" {
		return nil, types.RpcErrorInvalidField("role")
	}

	// API v2+ requires transactions to be a boolean
	// Reference: NoRippleCheck.cpp lines 95-99
	if ctx.ApiVersion > 1 && params != nil {
		var rawParams map[string]json.RawMessage
		if err := json.Unmarshal(params, &rawParams); err == nil {
			if txField, ok := rawParams["transactions"]; ok {
				var boolVal bool
				if err := json.Unmarshal(txField, &boolVal); err != nil {
					return nil, types.RpcErrorInvalidField("transactions")
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

	// Apply limit clamping matching rippled's readLimitField with noRippleCheck tuning
	limit := ClampLimit(request.Limit, LimitNoRippleCheck, ctx.IsAdmin)

	result, err := types.Services.Ledger.GetNoRippleCheck(
		request.Account,
		request.Role,
		ledgerIndex,
		limit,
		request.Transactions,
	)
	if err != nil {
		if err.Error() == "account not found" {
			return nil, types.RpcErrorActNotFound("Account not found.")
		}
		if len(err.Error()) > 24 && err.Error()[:24] == "invalid account address:" {
			return nil, types.RpcErrorActMalformed("Account malformed.")
		}
		return nil, types.RpcErrorInternal(err.Error())
	}

	// Build response matching rippled's NoRippleCheck.cpp format
	response := map[string]interface{}{
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	// Problems is always present (may be empty array)
	// Reference: NoRippleCheck.cpp line 123: result["problems"] = Json::arrayValue
	if result.Problems != nil {
		response["problems"] = result.Problems
	} else {
		response["problems"] = []string{}
	}

	// When transactions=true, rippled always includes the transactions array
	// even if empty. Reference: NoRippleCheck.cpp line 108:
	//   jvTransactions = transactions ? (result[jss::transactions] = Json::arrayValue) : dummy;
	if request.Transactions {
		if len(result.Transactions) > 0 {
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
		} else {
			response["transactions"] = []map[string]interface{}{}
		}
	}

	return response, nil
}
