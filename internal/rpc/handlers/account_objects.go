package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountObjectsMethod handles account_objects: it enumerates the raw ledger
// entries owned by the account, with type and deletion_blockers_only filters.
type AccountObjectsMethod struct{ BaseHandler }

// deletionBlockerTypes lists SLE types that block account deletion.
// Matches rippled's deletionBlockers[] in doAccountObjects (AccountObjects.cpp).
// These are types for which nonObligationDeleter() returns nullptr in DeleteAccount.cpp.
var deletionBlockerTypes = map[string]bool{
	"check":                                true,
	"escrow":                               true,
	"nft_page":                             true,
	"payment_channel":                      true,
	"state":                                true,
	"xchain_owned_claim_id":                true,
	"xchain_owned_create_account_claim_id": true,
	"bridge":                               true,
	"mpt_issuance":                         true,
	"mptoken":                              true,
	"permissioned_domain":                  true,
	"vault":                                true,
}

// validAccountObjectTypes maps rippled's RPC type names to true.
// Matches chooseLedgerEntryType() in RPCHelpers.cpp, which accepts both the
// canonical name (case-insensitive) and the rpcName (case-sensitive).
// isAccountObjectsValidType() excludes: amendments, directory, fee, hashes, nunl.
var validAccountObjectTypes = map[string]bool{
	"account":                              true,
	"amm":                                  true,
	"bridge":                               true,
	"check":                                true,
	"credential":                           true,
	"delegate":                             true,
	"deposit_preauth":                      true,
	"did":                                  true,
	"escrow":                               true,
	"mptoken":                              true,
	"mpt_issuance":                         true,
	"nft_offer":                            true,
	"nft_page":                             true,
	"offer":                                true,
	"oracle":                               true,
	"payment_channel":                      true,
	"permissioned_domain":                  true,
	"signer_list":                          true,
	"state":                                true,
	"ticket":                               true,
	"vault":                                true,
	"xchain_owned_claim_id":                true,
	"xchain_owned_create_account_claim_id": true,
}

// validLedgerEntryTypeNames contains all known ledger entry type rpcNames
// (from ledger_entries.macro). Used to distinguish "valid type but not for
// account_objects" from "completely unknown type".
var validLedgerEntryTypeNames = map[string]bool{
	"account":                              true,
	"amendments":                           true,
	"amm":                                  true,
	"bridge":                               true,
	"check":                                true,
	"credential":                           true,
	"delegate":                             true,
	"deposit_preauth":                      true,
	"did":                                  true,
	"directory":                            true,
	"escrow":                               true,
	"fee":                                  true,
	"hashes":                               true,
	"mptoken":                              true,
	"mpt_issuance":                         true,
	"nft_offer":                            true,
	"nft_page":                             true,
	"nunl":                                 true,
	"offer":                                true,
	"oracle":                               true,
	"payment_channel":                      true,
	"permissioned_domain":                  true,
	"signer_list":                          true,
	"state":                                true,
	"ticket":                               true,
	"vault":                                true,
	"xchain_owned_claim_id":                true,
	"xchain_owned_create_account_claim_id": true,
}

func (m *AccountObjectsMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	fields, account, parseErr := accountPageParams(params)
	if parseErr != nil {
		return nil, parseErr
	}
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	ledgerIndex, selErr := preflightAccountPage(ctx, params, account, "Failed to get account information")
	if selErr != nil {
		return nil, selErr
	}

	var objectType string
	if rawType, ok := fields["type"]; ok {
		if isJSONNull(rawType) || json.Unmarshal(rawType, &objectType) != nil {
			return nil, types.RPCErrorInvalidField("type")
		}
	}
	deletionBlockersOnly := legacyBoolValue(fields["deletion_blockers_only"])

	// Determine effective type filter based on deletion_blockers_only and type params.
	// Matches rippled's doAccountObjects logic in AccountObjects.cpp.
	effectiveType := objectType
	// forceEmptyResults short-circuits an impossible filter (a non-blocker
	// type combined with deletion_blockers_only) without using a magic
	// service-level sentinel. The service is still called so ledger
	// metadata + the account-existence check fire.
	forceEmptyResults := false

	if deletionBlockersOnly {
		if objectType != "" {
			typeLower := strings.ToLower(objectType)
			if !deletionBlockerTypes[typeLower] {
				if !validLedgerEntryTypeNames[typeLower] {
					return nil, types.RPCErrorInvalidField("type")
				}
				if !validAccountObjectTypes[typeLower] {
					return nil, types.RPCErrorInvalidField("type")
				}
				// Valid type but not a blocker. Drop the filter so the
				// service still returns ledger info / account-existence,
				// and clear the returned objects below.
				effectiveType = ""
				forceEmptyResults = true
			}
		}
		// If only deletion_blockers_only is set (no type), we need to filter
		// results to only blocker types after retrieval.
	} else if objectType != "" {
		// Validate the type parameter against known types.
		// rippled's chooseLedgerEntryType returns rpcINVALID_PARAMS for unknown types.
		// isAccountObjectsValidType further rejects amendments, directory, fee, hashes, nunl.
		typeLower := strings.ToLower(objectType)
		if !validLedgerEntryTypeNames[typeLower] {
			return nil, types.RPCErrorInvalidField("type")
		}
		if !validAccountObjectTypes[typeLower] {
			return nil, types.RPCErrorInvalidField("type")
		}
	}
	limit, limitErr := ReadLimitField(params, LimitAccountObjects, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}

	markerStr, mErr := markerString(fields["marker"])
	if mErr != nil {
		return nil, mErr
	}

	result, err := ctx.Services.Ledger.GetAccountObjects(ctx.Context, account, ledgerIndex, effectiveType, limit, markerStr)
	if err != nil {
		return nil, mapAccountQueryErr(err, fmt.Sprintf("Failed to get account objects: %v", err))
	}

	// Build account_objects array with deserialized fields. When
	// forceEmptyResults is set (deletion_blockers_only intersected with a
	// non-blocker type), skip enumeration entirely and keep the ledger
	// metadata from the service response.
	objects := make([]map[string]any, 0, len(result.AccountObjects))
	if forceEmptyResults {
		result.AccountObjects = nil
	}
	for _, obj := range result.AccountObjects {
		// If deletion_blockers_only is set without a specific type, filter here.
		if deletionBlockersOnly && objectType == "" {
			objTypeLower := sleTypeToRPCName(obj.LedgerEntryType)
			if !deletionBlockerTypes[objTypeLower] {
				continue
			}
		}

		hexData := hex.EncodeToString(obj.Data)
		decoded, err := binarycodec.Decode(hexData)
		if err != nil {
			// Fallback to raw data if decode fails
			objects = append(objects, map[string]any{
				"index":           strings.ToUpper(obj.Index),
				"LedgerEntryType": obj.LedgerEntryType,
				"data":            strings.ToUpper(hexData),
			})
			continue
		}
		decoded["index"] = strings.ToUpper(obj.Index)
		objects = append(objects, decoded)
	}

	response := map[string]any{
		"account":         result.Account,
		"account_objects": objects,
	}
	fillLedgerFields(response, ledgerIndex, FormatLedgerHash(result.LedgerHash), result.LedgerIndex, ctx.Services.Ledger.GetCurrentLedgerIndex(), result.Validated)

	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response, nil
}

// sleTypeToRPCName converts a PascalCase SLE type name to the rippled RPC name
// (lowercase/snake_case) used in the deletionBlockerTypes map.
func sleTypeToRPCName(sleType string) string {
	switch sleType {
	case "AccountRoot":
		return "account"
	case "AMM":
		return "amm"
	case "Bridge":
		return "bridge"
	case "Check":
		return "check"
	case "Credential":
		return "credential"
	case "Delegate":
		return "delegate"
	case "DepositPreauth":
		return "deposit_preauth"
	case "DID":
		return "did"
	case "DirectoryNode":
		return "directory"
	case "Escrow":
		return "escrow"
	case "MPToken":
		return "mptoken"
	case "MPTokenIssuance":
		return "mpt_issuance"
	case "NFTokenOffer":
		return "nft_offer"
	case "NFTokenPage":
		return "nft_page"
	case "Offer":
		return "offer"
	case "Oracle":
		return "oracle"
	case "PayChannel":
		return "payment_channel"
	case "PermissionedDomain":
		return "permissioned_domain"
	case "RippleState":
		return "state"
	case "SignerList":
		return "signer_list"
	case "Ticket":
		return "ticket"
	case "Vault":
		return "vault"
	case "XChainOwnedClaimID":
		return "xchain_owned_claim_id"
	case "XChainOwnedCreateAccountClaimID":
		return "xchain_owned_create_account_claim_id"
	default:
		return strings.ToLower(sleType)
	}
}
