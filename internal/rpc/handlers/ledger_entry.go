package handlers

import (
	"encoding/hex"
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// LedgerEntryMethod handles the ledger_entry RPC method
type LedgerEntryMethod struct{}

func (m *LedgerEntryMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Parse parameters - this method supports multiple ways to specify objects
	var request struct {
		types.LedgerSpecifier
		// Object specification methods (mutually exclusive):
		Index          string `json:"index,omitempty"`        // Direct object ID
		AccountRoot    string `json:"account_root,omitempty"` // Account address
		Check          string `json:"check,omitempty"`        // Check object ID
		DepositPreauth struct {
			Owner      string `json:"owner"`
			Authorized string `json:"authorized"`
		} `json:"deposit_preauth,omitempty"`
		DirectoryNode string `json:"directory,omitempty"` // Directory ID
		Escrow        struct {
			Owner string `json:"owner"`
			Seq   uint32 `json:"seq"`
		} `json:"escrow,omitempty"`
		Offer struct {
			Account string `json:"account"`
			Seq     uint32 `json:"seq"`
		} `json:"offer,omitempty"`
		PaymentChannel string `json:"payment_channel,omitempty"` // Channel ID
		RippleState    struct {
			Accounts []string `json:"accounts"`
			Currency string   `json:"currency"`
		} `json:"ripple_state,omitempty"`
		SignerList string `json:"signer_list,omitempty"` // Account address
		Ticket     struct {
			Account  string `json:"account"`
			TicketID uint32 `json:"ticket_id"`
		} `json:"ticket,omitempty"`
		NFTPage string `json:"nft_page,omitempty"` // NFT page ID

		Binary bool `json:"binary,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	// Check if ledger service is available
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Determine ledger index to use
	ledgerIndex := "validated"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Determine the entry key
	var entryKey [32]byte
	var keySet bool

	// Check for direct index specification
	if request.Index != "" {
		decoded, err := hex.DecodeString(request.Index)
		if err != nil || len(decoded) != 32 {
			return nil, types.RpcErrorInvalidParams("Invalid index: must be 64-character hex string")
		}
		copy(entryKey[:], decoded)
		keySet = true
	}

	// Check for other object types
	if !keySet && request.Check != "" {
		decoded, err := hex.DecodeString(request.Check)
		if err != nil || len(decoded) != 32 {
			return nil, types.RpcErrorInvalidParams("Invalid check: must be 64-character hex string")
		}
		copy(entryKey[:], decoded)
		keySet = true
	}

	if !keySet && request.PaymentChannel != "" {
		decoded, err := hex.DecodeString(request.PaymentChannel)
		if err != nil || len(decoded) != 32 {
			return nil, types.RpcErrorInvalidParams("Invalid payment_channel: must be 64-character hex string")
		}
		copy(entryKey[:], decoded)
		keySet = true
	}

	if !keySet && request.DirectoryNode != "" {
		decoded, err := hex.DecodeString(request.DirectoryNode)
		if err != nil || len(decoded) != 32 {
			return nil, types.RpcErrorInvalidParams("Invalid directory: must be 64-character hex string")
		}
		copy(entryKey[:], decoded)
		keySet = true
	}

	if !keySet && request.NFTPage != "" {
		decoded, err := hex.DecodeString(request.NFTPage)
		if err != nil || len(decoded) != 32 {
			return nil, types.RpcErrorInvalidParams("Invalid nft_page: must be 64-character hex string")
		}
		copy(entryKey[:], decoded)
		keySet = true
	}

	if !keySet {
		return nil, types.RpcErrorInvalidParams("Must specify object by index, check, payment_channel, directory, or nft_page")
	}

	// Get ledger entry from the ledger service
	result, err := types.Services.Ledger.GetLedgerEntry(entryKey, ledgerIndex)
	if err != nil {
		if err.Error() == "entry not found" {
			return nil, &types.RpcError{
				Code:    21, // entryNotFound
				Message: "Requested ledger entry not found.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get ledger entry: " + err.Error())
	}

	response := map[string]interface{}{
		"index":        result.Index,
		"ledger_hash":  hex.EncodeToString(result.LedgerHash[:]),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	if request.Binary {
		response["node_binary"] = result.NodeBinary
	} else {
		response["node"] = hex.EncodeToString(result.Node)
	}

	return response, nil
}

func (m *LedgerEntryMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *LedgerEntryMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
