package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountObjectsMethod handles the account_objects RPC method
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

func (m *AccountObjectsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Type                 string `json:"type,omitempty"`
		DeletionBlockersOnly bool   `json:"deletion_blockers_only,omitempty"`
		types.PaginationParams
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

	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	limit := ClampLimit(request.Limit, LimitAccountObjects, ctx.IsAdmin)

	// Determine effective type filter based on deletion_blockers_only and type params.
	// Matches rippled's doAccountObjects logic in AccountObjects.cpp.
	effectiveType := request.Type

	if request.DeletionBlockersOnly {
		if request.Type != "" {
			// Both deletion_blockers_only and type set: intersect.
			// If the requested type is a deletion blocker, use it; otherwise
			// return empty results (no type matches).
			typeLower := strings.ToLower(request.Type)
			if !deletionBlockerTypes[typeLower] {
				// The type is not a deletion blocker, so no objects will match.
				// We still need to validate it's a known type though.
				if !validLedgerEntryTypeNames[typeLower] {
					return nil, types.RpcErrorInvalidField("type")
				}
				if !validAccountObjectTypes[typeLower] {
					return nil, types.RpcErrorInvalidField("type")
				}
				// Valid type but not a blocker: return empty result.
				// We need ledger info, so still query with an impossible filter.
				// Use a type that will match nothing.
				effectiveType = "__none__"
			}
			// else: type IS a blocker, pass it through normally
		}
		// If only deletion_blockers_only is set (no type), we need to filter
		// results to only blocker types after retrieval.
	} else if request.Type != "" {
		// Validate the type parameter against known types.
		// rippled's chooseLedgerEntryType returns rpcINVALID_PARAMS for unknown types.
		// isAccountObjectsValidType further rejects amendments, directory, fee, hashes, nunl.
		typeLower := strings.ToLower(request.Type)
		if !validLedgerEntryTypeNames[typeLower] {
			return nil, types.RpcErrorInvalidField("type")
		}
		if !validAccountObjectTypes[typeLower] {
			return nil, types.RpcErrorInvalidField("type")
		}
	}

	result, err := ctx.Services.Ledger.GetAccountObjects(request.Account, ledgerIndex, effectiveType, limit)
	if err != nil {
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    19,
				Message: "Account not found.",
			}
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get account objects: %v", err))
	}

	// Build account_objects array with deserialized fields
	objects := make([]map[string]interface{}, 0, len(result.AccountObjects))
	for _, obj := range result.AccountObjects {
		// If deletion_blockers_only is set without a specific type, filter here.
		if request.DeletionBlockersOnly && request.Type == "" {
			objTypeLower := sleTypeToRPCName(obj.LedgerEntryType)
			if !deletionBlockerTypes[objTypeLower] {
				continue
			}
		}

		hexData := hex.EncodeToString(obj.Data)
		decoded, err := binarycodec.Decode(hexData)
		if err != nil {
			// Fallback to raw data if decode fails
			objects = append(objects, map[string]interface{}{
				"index":           obj.Index,
				"LedgerEntryType": obj.LedgerEntryType,
				"data":            hexData,
			})
			continue
		}
		decoded["index"] = obj.Index
		objects = append(objects, decoded)
	}

	response := map[string]interface{}{
		"account":         result.Account,
		"account_objects": objects,
		"ledger_hash":     FormatLedgerHash(result.LedgerHash),
		"ledger_index":    result.LedgerIndex,
		"validated":       result.Validated,
		"limit":           limit,
	}

	if result.Marker != "" {
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
