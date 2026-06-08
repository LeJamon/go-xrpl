package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
)

// VaultInfoMethod handles the vault_info RPC method
type VaultInfoMethod struct{ BaseHandler }

func (m *VaultInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	var request struct {
		types.LedgerSpecifier
		VaultID string `json:"vault_id,omitempty"`
		Owner   string `json:"owner,omitempty"`
		Seq     uint32 `json:"seq,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
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

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
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
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid owner address: %v", err))
		}
		var ownerID [20]byte
		copy(ownerID[:], ownerBytes)

		vaultKeylet := keylet.Vault(ownerID, request.Seq)
		vaultKey = vaultKeylet.Key
	}

	vaultEntry, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, vaultKey, ledgerIndex)
	if err != nil {
		return nil, types.RpcErrorEntryNotFound("Vault not found")
	}

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

			mptIssuanceEntry, mptErr := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, mptIssuanceKey, ledgerIndex)
			if mptErr == nil {
				mptIssuanceDecoded, mptDecodeErr := binarycodec.Decode(hex.EncodeToString(mptIssuanceEntry.Node))
				if mptDecodeErr == nil {
					vaultDecoded["shares"] = mptIssuanceDecoded
				}
			}
		}
	}

	// Build the response
	response := map[string]any{
		"vault":        vaultDecoded,
		"ledger_index": vaultEntry.LedgerIndex,
		"validated":    vaultEntry.Validated,
	}

	if vaultEntry.LedgerHash != [32]byte{} {
		response["ledger_hash"] = FormatLedgerHash(vaultEntry.LedgerHash)
	}

	return response, nil
}
