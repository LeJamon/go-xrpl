package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// GatewayBalancesMethod handles the gateway_balances RPC method
type GatewayBalancesMethod struct{ BaseHandler }

func (m *GatewayBalancesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		HotWallet json.RawMessage `json:"hotwallet,omitempty"`
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

	// Parse hotwallet parameter - can be a string or array of strings
	var hotWallets []string
	if len(request.HotWallet) > 0 {
		// Try to parse as a single string first
		var singleWallet string
		if err := json.Unmarshal(request.HotWallet, &singleWallet); err == nil {
			// JSON null also unmarshals to ""; rippled treats null as a valid
			// empty hotwallet set but an empty-string hotwallet as an
			// unparseable address.
			if singleWallet != "" {
				hotWallets = []string{singleWallet}
			} else if string(bytes.TrimSpace(request.HotWallet)) != "null" {
				if ctx.ApiVersion < 2 {
					return nil, types.RpcErrorInvalidHotWallet()
				}
				return nil, types.RpcErrorInvalidParams("Invalid field 'hotwallet'.")
			}
		} else {
			// Try to parse as an array of strings
			var walletArray []string
			if err := json.Unmarshal(request.HotWallet, &walletArray); err == nil {
				hotWallets = walletArray
			} else {
				// Invalid hotwallet format
				if ctx.ApiVersion < 2 {
					return nil, types.RpcErrorInvalidHotWallet()
				}
				return nil, types.RpcErrorInvalidParams("Invalid field 'hotwallet'.")
			}
		}
	}

	ledgerIndex, selErr := resolveLedgerSelector(request.LedgerSpecifier)
	if selErr != nil {
		return nil, selErr
	}

	// Get gateway balances from the ledger service
	result, err := ctx.Services.Ledger.GetGatewayBalances(
		ctx.Context,
		request.Account,
		hotWallets,
		ledgerIndex,
	)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RpcErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrAccountMalformed) {
			return nil, types.RpcErrorActMalformed("Account malformed.")
		}
		if errors.Is(err, svcerr.ErrInvalidHotWallet) {
			if ctx.ApiVersion < 2 {
				return nil, types.RpcErrorInvalidHotWallet()
			}
			return nil, types.RpcErrorInvalidParams("Invalid field 'hotwallet'.")
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get gateway balances: %v", err))
	}

	// Build response matching rippled's GatewayBalances.cpp format: rippled only
	// emits obligations/balances/frozen_balances/assets/locked when non-empty
	// (GatewayBalances.cpp:241-288 `if (!...empty())`).
	response := map[string]any{
		"account": result.Account,
	}
	fillLedgerFields(response, ledgerIndex, FormatLedgerHash(result.LedgerHash), result.LedgerIndex, result.Validated)

	// Helper to convert account->[]CurrencyBalance map to JSON-friendly structure
	convertBalanceMap := func(src map[string][]types.CurrencyBalance) map[string]any {
		out := make(map[string]any)
		for acct, bals := range src {
			balArray := make([]map[string]any, len(bals))
			for i, b := range bals {
				balArray[i] = map[string]any{
					"currency": b.Currency,
					"value":    b.Value,
				}
			}
			out[acct] = balArray
		}
		return out
	}

	if len(result.Obligations) > 0 {
		response["obligations"] = result.Obligations
	}
	if len(result.Balances) > 0 {
		response["balances"] = convertBalanceMap(result.Balances)
	}
	if len(result.Assets) > 0 {
		response["assets"] = convertBalanceMap(result.Assets)
	}
	if len(result.FrozenBalances) > 0 {
		response["frozen_balances"] = convertBalanceMap(result.FrozenBalances)
	}
	if len(result.Locked) > 0 {
		response["locked"] = result.Locked
	}

	return response, nil
}
