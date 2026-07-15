package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
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

type accountObjectLedgerType struct {
	canonical string
	rpcName   string
	valid     bool
}

var accountObjectLedgerTypes = []accountObjectLedgerType{
	{"NFTokenOffer", "nft_offer", true},
	{"Check", "check", true},
	{"DID", "did", true},
	{"NegativeUNL", "nunl", false},
	{"NFTokenPage", "nft_page", true},
	{"SignerList", "signer_list", true},
	{"Ticket", "ticket", true},
	{"AccountRoot", "account", true},
	{"DirectoryNode", "directory", false},
	{"Amendments", "amendments", false},
	{"LedgerHashes", "hashes", false},
	{"Bridge", "bridge", true},
	{"Offer", "offer", true},
	{"DepositPreauth", "deposit_preauth", true},
	{"XChainOwnedClaimID", "xchain_owned_claim_id", true},
	{"RippleState", "state", true},
	{"FeeSettings", "fee", false},
	{"XChainOwnedCreateAccountClaimID", "xchain_owned_create_account_claim_id", true},
	{"Escrow", "escrow", true},
	{"PayChannel", "payment_channel", true},
	{"AMM", "amm", true},
	{"MPTokenIssuance", "mpt_issuance", true},
	{"MPToken", "mptoken", true},
	{"Oracle", "oracle", true},
	{"Credential", "credential", true},
	{"PermissionedDomain", "permissioned_domain", true},
	{"Delegate", "delegate", true},
	{"Vault", "vault", true},
	{"LoanBroker", "loan_broker", true},
	{"Loan", "loan", true},
}

func chooseAccountObjectType(raw json.RawMessage, present bool) (string, *types.RPCError) {
	if !present {
		return "", nil
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", types.RPCErrorExpectedField("type", "string")
	}
	filter, ok := value.(string)
	if !ok {
		return "", types.RPCErrorExpectedField("type", "string")
	}

	for _, ledgerType := range accountObjectLedgerTypes {
		if strings.EqualFold(ledgerType.canonical, filter) || ledgerType.rpcName == filter {
			if !ledgerType.valid {
				return "", types.RPCErrorInvalidField("type")
			}
			return ledgerType.rpcName, nil
		}
	}
	return "", types.RPCErrorInvalidField("type")
}

func (m *AccountObjectsMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	if rpcErr := validateJsonCppIntegerRange(params); rpcErr != nil {
		return nil, rpcErr
	}
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
	limit, limitErr := ReadLimitField(params, LimitAccountObjects, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
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
		var typeErr *types.RPCError
		effectiveType, typeErr = chooseAccountObjectType(typeRaw, typePresent)
		if typeErr != nil {
			return nil, typeErr
		}
	}
	markerStr := ""
	var mErr *types.RPCError
	if markerRaw, ok := fields["marker"]; ok && !isJSONNull(markerRaw) {
		markerStr, mErr = markerString(markerRaw)
	}
	if mErr != nil {
		return nil, mErr
	}

	result, err := ctx.Services.Ledger.GetAccountObjects(ctx.Context, account, ledgerIndex, effectiveType, limit, markerStr)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RPCErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, types.RPCErrorInvalidField("marker")
		}
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to get account objects: %v", err))
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
	case "FeeSettings":
		return "fee"
	case "LedgerHashes":
		return "hashes"
	case "Loan":
		return "loan"
	case "LoanBroker":
		return "loan_broker"
	case "MPToken":
		return "mptoken"
	case "MPTokenIssuance":
		return "mpt_issuance"
	case "NFTokenOffer":
		return "nft_offer"
	case "NFTokenPage":
		return "nft_page"
	case "NegativeUNL":
		return "nunl"
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
