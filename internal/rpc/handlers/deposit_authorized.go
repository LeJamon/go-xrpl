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
type DepositAuthorizedMethod struct{ baseHandler }

func (m *DepositAuthorizedMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	rawFields, fieldsErr := rawJSONFields(params)
	if fieldsErr != nil {
		return nil, fieldsErr
	}
	sourceRaw, hasSource := rawFields["source_account"]
	if !hasSource {
		return nil, types.RpcErrorMissingField("source_account")
	}
	sourceAccount, validSource := rawJSONString(sourceRaw)
	if !validSource {
		return nil, types.RpcErrorExpectedField("source_account", "a string")
	}
	if !types.IsValidClassicAddress(sourceAccount) {
		return nil, types.RpcErrorActMalformed("Account malformed.")
	}
	destinationRaw, hasDestination := rawFields["destination_account"]
	if !hasDestination {
		return nil, types.RpcErrorMissingField("destination_account")
	}
	destinationAccount, validDestination := rawJSONString(destinationRaw)
	if !validDestination {
		return nil, types.RpcErrorExpectedField("destination_account", "a string")
	}
	if !types.IsValidClassicAddress(destinationAccount) {
		return nil, types.RpcErrorActMalformed("Account malformed.")
	}

	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Determine ledger index to use. rippled's lookupLedger defaults to the
	// open ("current") ledger in the absence of ledger_index/ledger_hash.
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	ledgerIndex, selErr := resolveLedgerSelector(parsedLedgerSpec)
	if selErr != nil {
		return nil, selErr
	}
	ledger, validated, lookupErr := lookupLedger(ctx, parsedLedgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}
	lookupExtra := ledgerEntryResponseFields(ledger, validated)
	if _, err := ctx.Services.Ledger.GetAccountInfo(ctx.Context, sourceAccount, ledgerIndex); err != nil {
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RpcErrorSrcActNotFound("Source account not found.").WithExtra(lookupExtra)
		}
		return nil, rpcInternalError("deposit_authorized: source account lookup failed", err)
	}
	if _, err := ctx.Services.Ledger.GetAccountInfo(ctx.Context, destinationAccount, ledgerIndex); err != nil {
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RpcErrorDstActNotFound("Destination account not found.").WithExtra(lookupExtra)
		}
		return nil, rpcInternalError("deposit_authorized: destination account lookup failed", err)
	}

	var credentials []string
	if credentialsRaw, ok := rawFields["credentials"]; ok {
		creds, err := parseCredentialsFormat(credentialsRaw)
		if err != nil {
			return nil, err
		}
		credentials = creds
	}

	// The service performs the ledger-side checks (source/destination
	// existence, credential existence/acceptance/expiry/ownership/duplicates,
	// and the direct + credential-based preauth lookups).
	result, err := ctx.Services.Ledger.GetDepositAuthorized(
		ctx.Context,
		sourceAccount,
		destinationAccount,
		ledgerIndex,
		credentials,
	)
	if err != nil {
		switch {
		case errors.Is(err, svcerr.ErrLedgerNotFound):
			return nil, types.RpcErrorLgrNotFound("ledgerNotFound")
		case errors.Is(err, svcerr.ErrSrcAccountNotFound):
			return nil, types.RpcErrorSrcActNotFound("Source account not found.").WithExtra(lookupExtra)
		case errors.Is(err, svcerr.ErrDstAccountNotFound):
			return nil, types.RpcErrorDstActNotFound("Destination account not found.").WithExtra(lookupExtra)
		case errors.Is(err, svcerr.ErrBadCredentials):
			// Detail follows the sentinel as "bad credentials: <detail>";
			// strip the prefix so the wire message matches rippled's
			// DepositAuthorized.cpp emit ("credentials don't exist", etc.).
			detail := err.Error()
			if idx := strings.Index(detail, ": "); idx >= 0 {
				detail = detail[idx+2:]
			}
			return nil, types.RpcErrorBadCredentials(detail).WithExtra(lookupExtra)
		}
		return nil, rpcInternalError("deposit_authorized: ledger query failed", err)
	}

	// Build response
	response := ledgerEntryResponseFields(ledger, validated)
	response["source_account"] = result.SourceAccount
	response["destination_account"] = result.DestinationAccount
	response["deposit_authorized"] = result.DepositAuthorized

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

func (m *DepositAuthorizedMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
