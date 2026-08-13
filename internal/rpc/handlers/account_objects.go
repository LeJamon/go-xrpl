package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/protocol"
)

// AccountObjectsMethod handles account_objects: it enumerates the raw ledger
// entries owned by the account, with type and deletion_blockers_only filters.
type AccountObjectsMethod struct{ baseHandler }

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
	"sponsorship":                          true,
}

var nonAccountObjectTypes = map[string]bool{
	"Amendments":    true,
	"DirectoryNode": true,
	"FeeSettings":   true,
	"LedgerHashes":  true,
	"NegativeUNL":   true,
}

var sponsorshipSupportedObjectTypes = map[string]bool{
	"Check":           true,
	"Credential":      true,
	"Delegate":        true,
	"DepositPreauth":  true,
	"Escrow":          true,
	"MPToken":         true,
	"MPTokenIssuance": true,
	"NFTokenPage":     true,
	"PayChannel":      true,
	"SignerList":      true,
}

func chooseAccountObjectType(raw json.RawMessage, present bool) (string, *rpcerrors.RpcError) {
	if !present {
		return "", nil
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", rpcerrors.RpcErrorExpectedField("type", "string")
	}
	filter, ok := value.(string)
	if !ok {
		return "", rpcerrors.RpcErrorExpectedField("type", "string")
	}

	for _, ledgerType := range protocol.LedgerEntryTypes() {
		if ledgerType.Deprecated || ledgerType.RPCName == "" {
			continue
		}
		if strings.EqualFold(ledgerType.Name, filter) || ledgerType.RPCName == filter {
			if nonAccountObjectTypes[ledgerType.Name] {
				return "", rpcerrors.RpcErrorInvalidField("type")
			}
			return ledgerType.RPCName, nil
		}
	}
	return "", rpcerrors.RpcErrorInvalidField("type")
}

func (m *AccountObjectsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	if rpcErr := validateJsonCppIntegerRange(params); rpcErr != nil {
		return nil, rpcErr
	}
	fields, account, parseErr := accountPageParams(params)
	if parseErr != nil {
		return nil, parseErr
	}
	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	ledgerIndex, ledgerFields, selErr := preflightAccountPage(ctx, params, account, "Failed to get account information", true)
	if selErr != nil {
		return nil, selErr
	}
	deletionBlockersOnly := false
	if raw, ok := fields["deletion_blockers_only"]; ok {
		deletionBlockersOnly = jsonCppBoolRaw(raw)
	}

	typeRaw, typePresent := fields["type"]
	effectiveType := ""
	forceEmptyResults := false
	deletionBlockerType := ""

	if deletionBlockersOnly {
		if typePresent {
			if err := json.Unmarshal(typeRaw, &deletionBlockerType); err != nil || !deletionBlockerTypes[deletionBlockerType] {
				deletionBlockerType = ""
				forceEmptyResults = true
			} else {
				effectiveType = deletionBlockerType
			}
		}
	} else {
		var typeErr *rpcerrors.RpcError
		effectiveType, typeErr = chooseAccountObjectType(typeRaw, typePresent)
		if typeErr != nil {
			return nil, typeErr
		}
	}
	limit, limitErr := readLimitField(params, limitAccountObjects, ctx.Role.IsUnlimited())
	if limitErr != nil {
		return nil, limitErr
	}
	markerStr := ""
	var mErr *rpcerrors.RpcError
	if markerRaw, ok := fields["marker"]; ok {
		markerStr, mErr = markerString(markerRaw)
	}
	if mErr != nil {
		return nil, mErr
	}

	var sponsoredFilter *bool
	if raw, ok := fields["sponsored"]; ok {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, rpcerrors.RpcErrorExpectedField("sponsored", "boolean")
		}
		sponsored, ok := value.(bool)
		if !ok {
			return nil, rpcerrors.RpcErrorExpectedField("sponsored", "boolean")
		}
		sponsoredFilter = &sponsored
	}

	result, err := ctx.Services.Ledger().GetAccountObjects(ctx.Context, account, ledgerIndex, effectiveType, limit, markerStr)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, rpcerrors.RpcErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, rpcerrors.RpcErrorInvalidField("marker")
		}
		return nil, rpcInternalError("account_objects: ledger query failed", err)
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
		if deletionBlockersOnly {
			objTypeLower := sleTypeToRPCName(obj.LedgerEntryType)
			if deletionBlockerType != "" && objTypeLower != deletionBlockerType {
				continue
			}
			if deletionBlockerType == "" && !deletionBlockerTypes[objTypeLower] {
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
		if sponsoredFilter != nil && accountObjectIsSponsored(decoded) != *sponsoredFilter {
			continue
		}
		decoded["index"] = strings.ToUpper(obj.Index)
		objects = append(objects, decoded)
	}

	response := map[string]any{
		"account":         result.Account,
		"account_objects": objects,
	}
	mergeLedgerFields(response, ledgerFields)

	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	setLoadMedium(ctx)
	return response, nil
}

func accountObjectIsSponsored(object map[string]any) bool {
	ledgerEntryType, _ := object["LedgerEntryType"].(string)
	switch ledgerEntryType {
	case "RippleState":
		return hasAccountField(object, "HighSponsor") || hasAccountField(object, "LowSponsor")
	default:
		return sponsorshipSupportedObjectTypes[ledgerEntryType] && hasAccountField(object, "Sponsor")
	}
}

func hasAccountField(object map[string]any, name string) bool {
	_, ok := object[name]
	return ok
}

// sleTypeToRPCName converts a PascalCase SLE type name to the rippled RPC name
// (lowercase/snake_case) used in the deletionBlockerTypes map.
func sleTypeToRPCName(sleType string) string {
	if info, ok := protocol.LedgerEntryTypeByName(sleType); ok && !info.Deprecated && info.RPCName != "" {
		return info.RPCName
	}
	return strings.ToLower(sleType)
}
