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
	lsfAllowTrustLineClawback  uint32 = 0x80000000
)

// AccountInfoMethod handles the account_info RPC method.
type AccountInfoMethod struct{}

func (m *AccountInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
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

	if err := RequireAccount(request.Account); err != nil {
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

	// Build account_data
	accountData := map[string]interface{}{
		"Account":         info.Account,
		"Balance":         info.Balance,
		"Flags":           info.Flags,
		"LedgerEntryType": "AccountRoot",
		"OwnerCount":      info.OwnerCount,
		"Sequence":        info.Sequence,
	}

	// Add optional fields if present
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
	if info.PreviousTxnLgrSeq > 0 {
		accountData["PreviousTxnLgrSeq"] = info.PreviousTxnLgrSeq
	}

	// Build account_flags from Flags bitmask
	flags := info.Flags
	accountFlags := map[string]bool{
		"defaultRipple":         flags&lsfDefaultRipple != 0,
		"depositAuth":          flags&lsfDepositAuth != 0,
		"disableMasterKey":     flags&lsfDisableMaster != 0,
		"disallowIncomingXRP":  flags&lsfDisallowXRP != 0,
		"globalFreeze":         flags&lsfGlobalFreeze != 0,
		"noFreeze":             flags&lsfNoFreeze != 0,
		"passwordSpent":        flags&lsfPasswordSpent != 0,
		"requireAuthorization": flags&lsfRequireAuth != 0,
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

	// Add queue data if requested
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

	// Load signer lists if requested
	if request.SignerLists {
		signerLists := m.loadSignerLists(request.Account, ledgerIndex)
		// API v1: nested under account_data
		accountData["signer_lists"] = signerLists
	}

	return response, nil
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

func (m *AccountInfoMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *AccountInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *AccountInfoMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
