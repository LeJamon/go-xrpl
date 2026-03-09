package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountInfoMethod handles the account_info RPC method.
// PARTIAL: Core account data works. Missing:
//
// TODO [account_info]: Support ledger lookup by hash.
//   - When ledger_hash is provided, resolve the ledger by hash first,
//     then query account info from that specific ledger state.
//   - Requires: GetAccountInfo() to accept a ledger hash parameter,
//     or resolve hash→sequence first via GetLedgerByHash().
//
// TODO [account_info]: Load actual signer lists when signer_lists=true.
//   - Requires: Reading SignerList SLE from the account's owner directory
//   - Reference: rippled AccountInfo.cpp lines 180-220
//   - Should return array of signer list objects with SignerQuorum + SignerEntries
type AccountInfoMethod struct{}

func (m *AccountInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Parse parameters
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Queue       bool `json:"queue,omitempty"`
		SignerLists bool `json:"signer_lists,omitempty"`
		Strict      bool `json:"strict,omitempty"`
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
	} else if request.LedgerHash != "" {
		// TODO [account_info]: resolve ledger by hash (see type-level TODO)
		ledgerIndex = "validated"
	}

	// Get account info from the ledger
	info, err := types.Services.Ledger.GetAccountInfo(request.Account, ledgerIndex)
	if err != nil {
		// Check for specific error types
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    19, // actNotFound
				Message: "Account not found.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get account info: " + err.Error())
	}

	// Build account_data response
	accountData := map[string]interface{}{
		"Account":         info.Account,
		"Balance":         info.Balance,
		"Flags":           info.Flags,
		"LedgerEntryType": "AccountRoot",
		"OwnerCount":      info.OwnerCount,
		"Sequence":        info.Sequence,
	}

	// Add optional fields if present
	if info.RegularKey != "" {
		accountData["RegularKey"] = info.RegularKey
	}
	if info.Domain != "" {
		accountData["Domain"] = info.Domain
	}
	if info.EmailHash != "" {
		accountData["EmailHash"] = info.EmailHash
	}
	if info.TransferRate > 0 {
		accountData["TransferRate"] = info.TransferRate
	}
	if info.TickSize > 0 {
		accountData["TickSize"] = info.TickSize
	}

	response := map[string]interface{}{
		"account_data": accountData,
		"ledger_hash":  info.LedgerHash,
		"ledger_index": info.LedgerIndex,
		"validated":    info.Validated,
	}

	// Add queue data if requested and this is current ledger
	if request.Queue && ledgerIndex == "current" {
		response["queue_data"] = map[string]interface{}{
			"auth_change_queued":    false,
			"highest_sequence":      info.Sequence,
			"lowest_sequence":       info.Sequence,
			"max_spend_drops_total": info.Balance,
			"transactions":          []interface{}{},
			"txn_count":             0,
		}
	}

	// Add signer lists if requested
	// TODO [account_info]: load signer lists from ledger (see type-level TODO)
	if request.SignerLists {
		response["signer_lists"] = []interface{}{}
	}

	return response, nil
}

func (m *AccountInfoMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *AccountInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
