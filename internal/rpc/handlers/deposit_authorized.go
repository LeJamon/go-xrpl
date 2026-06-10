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
		SourceAccount      string          `json:"source_account"`
		DestinationAccount string          `json:"destination_account"`
		Credentials        json.RawMessage `json:"credentials,omitempty"`
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

	// Validate credentials array format before calling the service. A
	// present-but-empty (or null / non-array) credentials field is an
	// error, so presence must be distinguished from emptiness.
	var credentials []string
	if request.Credentials != nil {
		creds, err := parseCredentialsFormat(request.Credentials)
		if err != nil {
			return nil, err
		}
		credentials = creds
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Determine ledger index to use. rippled's lookupLedger defaults to the
	// open ("current") ledger in the absence of ledger_index/ledger_hash.
	ledgerIndex := resolveLedgerSelector(request.LedgerSpecifier)

	// The service performs the ledger-side checks (source/destination
	// existence, credential existence/acceptance/expiry/ownership/duplicates,
	// and the direct + credential-based preauth lookups).
	result, err := ctx.Services.Ledger.GetDepositAuthorized(
		ctx.Context,
		request.SourceAccount,
		request.DestinationAccount,
		ledgerIndex,
		credentials,
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
	}
	fillLedgerFields(response, ledgerIndex, FormatLedgerHash(result.LedgerHash), result.LedgerIndex, result.Validated)

	// Echo credentials in response if provided (matches rippled)
	if len(credentials) > 0 {
		response["credentials"] = credentials
	}

	return response, nil
}

// parseCredentialsFormat validates the credentials array format at the RPC
// level: a non-empty array, max size, valid hex hashes. Ledger-side
// validation (existence, acceptance, expiry, ownership, duplicates by
// issuer+type) is done in the service layer, matching rippled's order —
// duplicate hashes that don't exist on ledger report "credentials don't
// exist", not "duplicates in credentials".
// Reference: rippled DepositAuthorized.cpp credential parsing loop
func parseCredentialsFormat(raw json.RawMessage) ([]string, *types.RpcError) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 {
		return nil, types.RpcErrorExpectedField("credentials",
			"is non-empty array of CredentialID(hash256)")
	}

	if len(entries) > maxCredentialsArraySize {
		return nil, types.RpcErrorExpectedField("credentials", "array too long")
	}

	credentials := make([]string, 0, len(entries))
	for _, entry := range entries {
		var credStr string
		if err := json.Unmarshal(entry, &credStr); err != nil {
			return nil, types.RpcErrorExpectedField("credentials",
				"an array of CredentialID(hash256)")
		}
		// Each credential must be a valid 64-char hex string (32 bytes / 256 bits)
		if len(credStr) != 64 {
			return nil, types.RpcErrorExpectedField("credentials",
				"an array of CredentialID(hash256)")
		}
		if _, err := hex.DecodeString(credStr); err != nil {
			return nil, types.RpcErrorExpectedField("credentials",
				"an array of CredentialID(hash256)")
		}
		credentials = append(credentials, credStr)
	}

	return credentials, nil
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
