package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

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

	// Validate source_account: must be present and a valid Base58 address.
	// Reference: rippled DepositAuthorized.cpp — parseBase58 → rpcACT_MALFORMED
	if request.SourceAccount == "" {
		return nil, types.RpcErrorInvalidParams("Missing field 'source_account'.")
	}
	if err := ValidateAccount(request.SourceAccount); err != nil {
		return nil, err
	}

	// Validate destination_account: must be present and a valid Base58 address.
	// Reference: rippled DepositAuthorized.cpp — parseBase58 → rpcACT_MALFORMED
	if request.DestinationAccount == "" {
		return nil, types.RpcErrorInvalidParams("Missing field 'destination_account'.")
	}
	if err := ValidateAccount(request.DestinationAccount); err != nil {
		return nil, err
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

	// Call the service with credentials for ledger-side validation.
	// TODO: The service layer is responsible for the following ledger-side
	// credential checks (matching rippled DepositAuthorized.cpp):
	//   1. Credential existence — read(keylet::credential(credH)) → rpcBAD_CREDENTIALS "credentials don't exist"
	//   2. Credential accepted — (flags & lsfAccepted) → rpcBAD_CREDENTIALS "credentials aren't accepted"
	//   3. Credential expiry — checkExpired(sleCred, parentCloseTime) → rpcBAD_CREDENTIALS "credentials are expired"
	//   4. Credential ownership — sleCred[sfSubject] == srcAcct → rpcBAD_CREDENTIALS "credentials doesn't belong to the root account"
	//   5. Credential duplicates by (issuer, credentialType) — rpcBAD_CREDENTIALS "duplicates in credentials"
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
// This performs format-only checks: non-empty, max size, valid hex hashes, no duplicates.
// Ledger-side validation (existence, acceptance, expiry, ownership) is done in the
// service layer.
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

	seen := make(map[string]struct{}, len(credentials))
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

		// Detect duplicate credential hashes.
		// Reference: rippled DepositAuthorized.cpp — sorted.emplace() → rpcBAD_CREDENTIALS "duplicates in credentials"
		// Note: rippled's full duplicate detection uses (issuer, credentialType) pairs from the
		// ledger SLE. Here we catch the simpler case of identical hash strings, which is a strict
		// subset. The service layer performs the full (issuer, credentialType) dedup with ledger data.
		normalized := strings.ToUpper(credStr)
		if _, exists := seen[normalized]; exists {
			return types.RpcErrorBadCredentials("duplicates in credentials.")
		}
		seen[normalized] = struct{}{}
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
