package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// GatewayBalancesMethod handles the gateway_balances RPC method
type GatewayBalancesMethod struct{ BaseHandler }

func (m *GatewayBalancesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Strict    bool            `json:"strict,omitempty"`
		HotWallet json.RawMessage `json:"hotwallet,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if err := RequireAccount(request.Account); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Parse hotwallet parameter - can be a string or array of strings
	var hotWallets []string
	if len(request.HotWallet) > 0 {
		// Try to parse as a single string first
		var singleWallet string
		if err := json.Unmarshal(request.HotWallet, &singleWallet); err == nil {
			if singleWallet != "" {
				hotWallets = []string{singleWallet}
			}
		} else {
			// Try to parse as an array of strings
			var walletArray []string
			if err := json.Unmarshal(request.HotWallet, &walletArray); err == nil {
				hotWallets = walletArray
			} else {
				// Invalid hotwallet format
				if ctx.ApiVersion < 2 {
					return nil, &types.RpcError{
						Code:    types.RpcINVALID_PARAMS,
						Message: "Invalid hotwallet.",
					}
				}
				return nil, types.RpcErrorInvalidParams("Invalid field 'hotwallet'.")
			}
		}
	}

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Get gateway balances from the ledger service
	result, err := types.Services.Ledger.GetGatewayBalances(
		request.Account,
		hotWallets,
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
		// Check for invalid hotwallet
		if len(err.Error()) > 20 && err.Error()[:20] == "invalid hotwallet ad" {
			if ctx.ApiVersion < 2 {
				return nil, &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: "Invalid hotwallet.",
				}
			}
			return nil, types.RpcErrorInvalidParams("Invalid field 'hotwallet'.")
		}
		return nil, types.RpcErrorInternal("Failed to get gateway balances: " + err.Error())
	}

	// Build response
	response := map[string]interface{}{
		"account":      result.Account,
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	// Add optional fields only if they have values
	if len(result.Obligations) > 0 {
		response["obligations"] = result.Obligations
	}

	if len(result.Balances) > 0 {
		balances := make(map[string]interface{})
		for acct, bals := range result.Balances {
			balArray := make([]map[string]interface{}, len(bals))
			for i, b := range bals {
				balArray[i] = map[string]interface{}{
					"currency": b.Currency,
					"value":    b.Value,
				}
			}
			balances[acct] = balArray
		}
		response["balances"] = balances
	}

	if len(result.FrozenBalances) > 0 {
		frozenBalances := make(map[string]interface{})
		for acct, bals := range result.FrozenBalances {
			balArray := make([]map[string]interface{}, len(bals))
			for i, b := range bals {
				balArray[i] = map[string]interface{}{
					"currency": b.Currency,
					"value":    b.Value,
				}
			}
			frozenBalances[acct] = balArray
		}
		response["frozen_balances"] = frozenBalances
	}

	if len(result.Assets) > 0 {
		assets := make(map[string]interface{})
		for acct, bals := range result.Assets {
			balArray := make([]map[string]interface{}, len(bals))
			for i, b := range bals {
				balArray[i] = map[string]interface{}{
					"currency": b.Currency,
					"value":    b.Value,
				}
			}
			assets[acct] = balArray
		}
		response["assets"] = assets
	}

	if len(result.Locked) > 0 {
		response["locked"] = result.Locked
	}

	return response, nil
}

