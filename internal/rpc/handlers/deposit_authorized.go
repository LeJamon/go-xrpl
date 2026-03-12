package handlers

import (
	"encoding/hex"
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// maxCredentialsArraySize matches rippled's protocol constant.
// Reference: rippled/include/xrpl/protocol/Protocol.h maxCredentialsArraySize = 8
const maxCredentialsArraySize = 8

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

	// Validate credentials array format before calling the service.
	// This matches rippled DepositAuthorized.cpp credential validation order.
	if len(request.Credentials) > 0 {
		if err := validateCredentialsFormat(request.Credentials); err != nil {
			return nil, err
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

	// Call the service with credentials for ledger-side validation
	result, err := types.Services.Ledger.GetDepositAuthorized(
		request.SourceAccount,
		request.DestinationAccount,
		ledgerIndex,
		request.Credentials,
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

		// Credential validation errors from the service layer
		if len(errMsg) > 16 && errMsg[:16] == "bad_credentials:" {
			return nil, types.RpcErrorBadCredentials(errMsg[17:])
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

// validateCredentialsFormat validates the credentials array format at the RPC level.
// This performs format-only checks (non-empty, max size, valid hex hashes).
// Ledger-side validation (existence, acceptance, expiry, ownership, duplicates)
// is done in the service layer.
// Reference: rippled DepositAuthorized.cpp credential parsing loop
func validateCredentialsFormat(credentials []string) *types.RpcError {
	if len(credentials) == 0 {
		return types.RpcErrorInvalidParams(
			"Invalid field 'credentials', is non-empty array of CredentialID(hash256).")
	}

	if len(credentials) > maxCredentialsArraySize {
		return types.RpcErrorInvalidParams(
			"Invalid field 'credentials', array too long.")
	}

	for _, credStr := range credentials {
		// Each credential must be a valid 64-char hex string (32 bytes / 256 bits)
		if len(credStr) != 64 {
			return types.RpcErrorInvalidParams(
				"Invalid field 'credentials', an array of CredentialID(hash256).")
		}
		if _, err := hex.DecodeString(credStr); err != nil {
			return types.RpcErrorInvalidParams(
				"Invalid field 'credentials', an array of CredentialID(hash256).")
		}
	}

	return nil
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
