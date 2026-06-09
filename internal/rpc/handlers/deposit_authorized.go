package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// maxCredentialsArraySize matches rippled's protocol constant.
// Reference: rippled/include/xrpl/protocol/Protocol.h maxCredentialsArraySize = 8
const maxCredentialsArraySize = 8

// DepositAuthorizedMethod handles the deposit_authorized RPC method
type DepositAuthorizedMethod struct{}

func (m *DepositAuthorizedMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
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

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Determine ledger index to use
	ledgerIndex := "validated"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// The service performs the ledger-side checks (source/destination
	// existence, credential existence/acceptance/expiry/ownership/duplicates,
	// and the direct + credential-based preauth lookups).
	result, err := ctx.Services.Ledger.GetDepositAuthorized(
		ctx.Context,
		request.SourceAccount,
		request.DestinationAccount,
		ledgerIndex,
		request.Credentials,
	)
	if err != nil {
		switch {
		case errors.Is(err, svcerr.ErrSrcAccountNotFound):
			return nil, types.RpcErrorSrcActNotFound("Source account not found.")
		case errors.Is(err, svcerr.ErrDstAccountNotFound):
			return nil, types.RpcErrorDstActNotFound("Destination account not found.")
		case errors.Is(err, svcerr.ErrBadCredentials):
			// Detail follows the sentinel as "bad credentials: <detail>";
			// strip the prefix so the wire message matches rippled's
			// DepositAuthorized.cpp emit ("credentials don't exist", etc.).
			detail := err.Error()
			if idx := strings.Index(detail, ": "); idx >= 0 {
				detail = detail[idx+2:]
			}
			return nil, types.RpcErrorBadCredentials(detail)
		}
		return nil, types.RpcErrorInternal(err.Error())
	}

	// Build response
	response := map[string]any{
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

// validateCredentialsFormat validates the credentials array format at the RPC level:
// non-empty, max size, valid hex hashes. Ledger-side validation (existence,
// acceptance, expiry, ownership, duplicates by issuer+type) is done in the
// service layer, matching rippled's order — duplicate hashes that don't exist
// on ledger report "credentials don't exist", not "duplicates in credentials".
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
