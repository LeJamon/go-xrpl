package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountRoot flag constants matching rippled's lsfXxx values
const (
	lsfPasswordSpent            uint32 = 0x00010000
	lsfRequireDestTag           uint32 = 0x00020000
	lsfRequireAuth              uint32 = 0x00040000
	lsfDisallowXRP              uint32 = 0x00080000
	lsfDisableMaster            uint32 = 0x00100000
	lsfNoFreeze                 uint32 = 0x00200000
	lsfGlobalFreeze             uint32 = 0x00400000
	lsfDefaultRipple            uint32 = 0x00800000
	lsfDepositAuth              uint32 = 0x01000000
	lsfAMM                      uint32 = 0x02000000
	lsfDisallowIncomingNFTOffer uint32 = 0x04000000
	lsfDisallowIncomingCheck    uint32 = 0x08000000
	lsfDisallowIncomingPayChan  uint32 = 0x10000000
	lsfDisallowIncomingTrustln  uint32 = 0x20000000
	lsfAllowTrustLineClawback   uint32 = 0x80000000
)

// AccountInfoMethod handles the account_info RPC method.
type AccountInfoMethod struct{ BaseHandler }

func (m *AccountInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Parse the raw JSON to inspect field types before struct unmarshaling.
	// This allows us to check for the "ident" alias and validate signer_lists type.
	var rawFields map[string]json.RawMessage
	if params != nil {
		if err := json.Unmarshal(params, &rawFields); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Queue       bool `json:"queue,omitempty"`
		SignerLists bool `json:"signer_lists,omitempty"`
		Strict      bool `json:"strict,omitempty"`
	}
	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// Support "ident" as alias for "account" (matching rippled behavior)
	if request.Account == "" {
		if identRaw, ok := rawFields["ident"]; ok {
			var ident string
			if err := json.Unmarshal(identRaw, &ident); err != nil {
				// ident is present but not a string
				return nil, types.RpcErrorInvalidField("ident")
			}
			request.Account = ident
		}
	}

	if err := ValidateAccount(request.Account); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Determine ledger index
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	} else if request.LedgerHash != "" {
		ledgerIndex = "validated"
	}

	// Queue is only valid for the current (open) ledger.
	// Matching rippled: if queue=true but ledger is not open, return rpcINVALID_PARAMS.
	if request.Queue && ledgerIndex != "current" {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}

	// API v2: signer_lists must be a bool if present.
	// Reject non-bool values (string, number, etc.) matching rippled behavior.
	if ctx.ApiVersion > 1 {
		if signerListsRaw, ok := rawFields["signer_lists"]; ok {
			// Check if the raw JSON value is a boolean (true or false)
			trimmed := strings.TrimSpace(string(signerListsRaw))
			if trimmed != "true" && trimmed != "false" {
				return nil, types.RpcErrorInvalidParams("Invalid parameters.")
			}
		}
	}

	info, err := types.Services.Ledger.GetAccountInfo(request.Account, ledgerIndex)
	if err != nil {
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    19,
				Message: "Account not found.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get account info: " + err.Error())
	}

	// Build account_data by decoding the full SLE binary via binarycodec,
	// matching rippled's injectSLE which serializes all fields from the SLE.
	accountData := m.buildAccountData(info)

	// Build account_flags from Flags bitmask
	flags := info.Flags
	accountFlags := map[string]bool{
		"defaultRipple":         flags&lsfDefaultRipple != 0,
		"depositAuth":           flags&lsfDepositAuth != 0,
		"disableMasterKey":      flags&lsfDisableMaster != 0,
		"disallowIncomingXRP":   flags&lsfDisallowXRP != 0,
		"globalFreeze":          flags&lsfGlobalFreeze != 0,
		"noFreeze":              flags&lsfNoFreeze != 0,
		"passwordSpent":         flags&lsfPasswordSpent != 0,
		"requireAuthorization":  flags&lsfRequireAuth != 0,
		"requireDestinationTag": flags&lsfRequireDestTag != 0,
	}
	// Conditional flags (always include them — amendment gating is separate)
	accountFlags["disallowIncomingNFTokenOffer"] = flags&lsfDisallowIncomingNFTOffer != 0
	accountFlags["disallowIncomingCheck"] = flags&lsfDisallowIncomingCheck != 0
	accountFlags["disallowIncomingPayChan"] = flags&lsfDisallowIncomingPayChan != 0
	accountFlags["disallowIncomingTrustline"] = flags&lsfDisallowIncomingTrustln != 0
	accountFlags["allowTrustLineClawback"] = flags&lsfAllowTrustLineClawback != 0

	response := map[string]interface{}{
		"account_data":  accountData,
		"account_flags": accountFlags,
		"ledger_hash":   info.LedgerHash,
		"ledger_index":  info.LedgerIndex,
		"validated":     info.Validated,
	}

	// Add queue data if requested (only for current/open ledger — validated above)
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

	// Add index field (SLE key) to account_data
	if info.Index != "" {
		accountData["index"] = strings.ToUpper(info.Index)
	}

	// Load signer lists if requested
	if request.SignerLists {
		signerLists := m.loadSignerLists(request.Account, ledgerIndex)
		if ctx.ApiVersion > 1 {
			// API v2: signer_lists at top level
			response["signer_lists"] = signerLists
		} else {
			// API v1: nested under account_data
			accountData["signer_lists"] = signerLists
		}
	}

	return response, nil
}

// buildAccountData constructs account_data from the full SLE binary.
// When RawData is available, uses binarycodec.Decode to get all fields
// (matching rippled's injectSLE → sle.getJson). Falls back to manual
// construction from the AccountInfo struct fields if RawData is absent.
func (m *AccountInfoMethod) buildAccountData(info *types.AccountInfo) map[string]interface{} {
	// Try full SLE decode from raw binary data
	if len(info.RawData) > 0 {
		hexData := hex.EncodeToString(info.RawData)
		decoded, err := binarycodec.Decode(hexData)
		if err == nil {
			return decoded
		}
		// Fall through to manual construction on decode error
	}

	// Fallback: manually construct from AccountInfo struct fields
	accountData := map[string]interface{}{
		"Account":         info.Account,
		"Balance":         info.Balance,
		"Flags":           info.Flags,
		"LedgerEntryType": "AccountRoot",
		"OwnerCount":      info.OwnerCount,
		"Sequence":        info.Sequence,
	}

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
	if info.PreviousTxnID != "" {
		accountData["PreviousTxnID"] = info.PreviousTxnID
	}
	// Always include PreviousTxnLgrSeq when present (don't skip on 0)
	if info.PreviousTxnID != "" {
		accountData["PreviousTxnLgrSeq"] = info.PreviousTxnLgrSeq
	}

	return accountData
}

// loadSignerLists retrieves signer list objects for an account
func (m *AccountInfoMethod) loadSignerLists(account string, ledgerIndex string) []interface{} {
	result, err := types.Services.Ledger.GetAccountObjects(account, ledgerIndex, "SignerList", 10)
	if err != nil || len(result.AccountObjects) == 0 {
		return []interface{}{}
	}

	var signerLists []interface{}
	for _, obj := range result.AccountObjects {
		// Decode the raw SLE binary to JSON
		hexData := hex.EncodeToString(obj.Data)
		decoded, err := binarycodec.Decode(hexData)
		if err != nil {
			continue
		}
		// Add the index field
		decoded["index"] = strings.ToUpper(obj.Index)
		signerLists = append(signerLists, decoded)
	}
	if signerLists == nil {
		return []interface{}{}
	}
	return signerLists
}
