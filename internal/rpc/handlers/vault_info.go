package handlers

import (
	"encoding/hex"
	"encoding/json"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// VaultInfoMethod handles the vault_info RPC method
type VaultInfoMethod struct{}

func (m *VaultInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.LedgerSpecifier
		VaultID string `json:"vault_id,omitempty"`
		Owner   string `json:"owner,omitempty"`
		Seq     uint32 `json:"seq,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	hasVaultID := request.VaultID != ""
	hasOwner := request.Owner != ""
	hasSeq := request.Seq > 0

	// Validate parameter combinations
	if hasVaultID && (hasOwner || hasSeq) {
		return nil, types.RpcErrorInvalidParams("Cannot specify vault_id with owner/seq")
	}
	if !hasVaultID && (!hasOwner || !hasSeq) {
		return nil, types.RpcErrorInvalidParams("Must specify either vault_id or (owner + seq)")
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Determine ledger index to use
	ledgerIndex := "validated"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	var vaultKey [32]byte

	if hasVaultID {
		// Direct vault ID lookup
		vaultIDBytes, err := hex.DecodeString(request.VaultID)
		if err != nil || len(vaultIDBytes) != 32 {
			return nil, types.RpcErrorInvalidParams("Invalid vault_id: must be 64-character hex string")
		}
		copy(vaultKey[:], vaultIDBytes)
	} else {
		// Lookup by owner + seq
		_, ownerBytes, err := addresscodec.DecodeClassicAddressToAccountID(request.Owner)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid owner address: " + err.Error())
		}
		var ownerID [20]byte
		copy(ownerID[:], ownerBytes)

		vaultKeylet := keylet.Vault(ownerID, request.Seq)
		vaultKey = vaultKeylet.Key
	}

	// Get the Vault entry
	vaultEntry, err := types.Services.Ledger.GetLedgerEntry(vaultKey, ledgerIndex)
	if err != nil {
		return nil, &types.RpcError{
			Code:    21,
			Message: "Vault not found",
		}
	}

	// Decode the Vault entry
	vaultDecoded, decodeErr := binarycodec.Decode(hex.EncodeToString(vaultEntry.Node))
	if decodeErr != nil {
		return nil, types.RpcErrorInternal("Failed to decode Vault: " + decodeErr.Error())
	}

	// Get the ShareMPTID to lookup the MPToken issuance
	shareMPTIDHex, ok := vaultDecoded["ShareMPTID"].(string)
	if ok && shareMPTIDHex != "" {
		shareMPTIDBytes, hexErr := hex.DecodeString(shareMPTIDHex)
		if hexErr == nil && len(shareMPTIDBytes) == 32 {
			var mptIssuanceKey [32]byte
			copy(mptIssuanceKey[:], shareMPTIDBytes)

			// Get the MPToken issuance entry
			mptIssuanceEntry, mptErr := types.Services.Ledger.GetLedgerEntry(mptIssuanceKey, ledgerIndex)
			if mptErr == nil {
				mptIssuanceDecoded, mptDecodeErr := binarycodec.Decode(hex.EncodeToString(mptIssuanceEntry.Node))
				if mptDecodeErr == nil {
					// Add shares info to vault response
					vaultDecoded["shares"] = mptIssuanceDecoded
				}
			}
		}
	}

	// Build the response
	response := map[string]interface{}{
		"vault":        vaultDecoded,
		"ledger_index": vaultEntry.LedgerIndex,
		"validated":    vaultEntry.Validated,
	}

	if vaultEntry.LedgerHash != [32]byte{} {
		response["ledger_hash"] = hex.EncodeToString(vaultEntry.LedgerHash[:])
	}

	return response, nil
}

func (m *VaultInfoMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *VaultInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
