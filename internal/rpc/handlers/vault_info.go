package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
)

// VaultInfoMethod handles the vault_info RPC method
type VaultInfoMethod struct{ baseHandler }

func (m *VaultInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	targetLedger, validated, lookupErr := lookupLedger(ctx, parsedLedgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}
	response := vaultInfoLedgerFields(targetLedger, validated)
	ledgerIndex, selectorErr := resolveLedgerSelector(parsedLedgerSpec)
	if selectorErr != nil {
		return nil, selectorErr
	}

	var rawParams map[string]json.RawMessage
	if err := parseParams(params, &rawParams); err != nil {
		return nil, err
	}
	vaultKey, parseErr := parseVaultInfoKey(rawParams)
	if parseErr != nil {
		return nil, parseErr.WithExtra(response)
	}
	if vaultKey == ([32]byte{}) {
		return nil, rpcerrors.RpcErrorEntryNotFound("").WithExtra(response)
	}

	vaultEntry, err := ctx.Services.Ledger().GetLedgerEntry(ctx.Context, vaultKey, ledgerIndex)
	if err != nil || vaultEntry == nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr.WithExtra(response)
		}
		return nil, rpcerrors.RpcErrorEntryNotFound("").WithExtra(response)
	}

	vaultDecoded, decodeErr := decodeLedgerEntryNode(vaultEntry.Node)
	if decodeErr != nil {
		return nil, rpcInternalError("vault_info: vault decoding failed", decodeErr)
	}
	if vaultDecoded["LedgerEntryType"] != "Vault" {
		return nil, rpcerrors.RpcErrorEntryNotFound("").WithExtra(response)
	}

	shareMPTIDHex, ok := vaultDecoded["ShareMPTID"].(string)
	shareMPTIDBytes, shareErr := hex.DecodeString(shareMPTIDHex)
	if !ok || shareErr != nil || len(shareMPTIDBytes) != 24 {
		return nil, rpcInternalInvariantError("vault_info: vault has invalid ShareMPTID").WithExtra(response)
	}
	var shareMPTID [24]byte
	copy(shareMPTID[:], shareMPTIDBytes)
	mptIssuanceKey := keylet.MPTIssuance(shareMPTID).Key

	mptIssuanceEntry, mptErr := ctx.Services.Ledger().GetLedgerEntry(ctx.Context, mptIssuanceKey, ledgerIndex)
	if mptErr != nil || mptIssuanceEntry == nil {
		if rerr := mapLedgerLookupErr(mptErr); rerr != nil {
			return nil, rerr.WithExtra(response)
		}
		return nil, rpcerrors.RpcErrorEntryNotFound("").WithExtra(response)
	}
	mptIssuanceDecoded, mptDecodeErr := decodeLedgerEntryNode(mptIssuanceEntry.Node)
	if mptDecodeErr != nil {
		return nil, rpcInternalError("vault_info: MPTokenIssuance decoding failed", mptDecodeErr).WithExtra(response)
	}
	if mptIssuanceDecoded["LedgerEntryType"] != "MPTokenIssuance" {
		return nil, rpcerrors.RpcErrorEntryNotFound("").WithExtra(response)
	}

	addLedgerEntryJSONFields(vaultDecoded, strings.ToUpper(hex.EncodeToString(vaultKey[:])))
	addLedgerEntryJSONFields(mptIssuanceDecoded, strings.ToUpper(hex.EncodeToString(mptIssuanceKey[:])))
	vaultDecoded["shares"] = mptIssuanceDecoded
	response["vault"] = vaultDecoded
	return response, nil
}

func parseVaultInfoKey(params map[string]json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	vaultIDRaw, hasVaultID := params["vault_id"]
	ownerRaw, hasOwner := params["owner"]
	seqRaw, hasSeq := params["seq"]

	if hasVaultID && !hasOwner && !hasSeq {
		vaultID, ok := rawJSONString(vaultIDRaw)
		if !ok {
			return [32]byte{}, rpcerrors.RpcErrorExpectedField("vault_id", "hex string")
		}
		vaultKey, ok := parseVaultInfoID(vaultID)
		if !ok {
			return [32]byte{}, rpcerrors.RpcErrorExpectedField("vault_id", "hex string")
		}
		return vaultKey, nil
	}

	if !hasVaultID && hasOwner && hasSeq {
		owner, ok := rawJSONString(ownerRaw)
		if !ok {
			return [32]byte{}, rpcerrors.RpcErrorActMalformed("Invalid field 'owner', not AccountID.")
		}
		ownerID, err := decodeAccountID(owner)
		if err != nil {
			return [32]byte{}, rpcerrors.RpcErrorActMalformed("Invalid field 'owner', not AccountID.")
		}
		sequence, ok := parseJSONUInt32(seqRaw)
		if !ok || sequence == 0 {
			return [32]byte{}, rpcerrors.RpcErrorExpectedField("seq", "a positive 32-bit integer")
		}
		return keylet.Vault(ownerID, sequence).Key, nil
	}

	return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Must specify either 'vault_id' or both 'owner' and 'seq'.")
}

func parseVaultInfoID(value string) ([32]byte, bool) {
	var key [32]byte
	if value == "0" {
		return key, true
	}
	if len(value) != 64 {
		return key, false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return key, false
	}
	copy(key[:], decoded)
	return key, true
}

func vaultInfoLedgerFields(ledger types.LedgerReader, validated bool) map[string]any {
	response := map[string]any{"validated": validated}
	if ledger.IsClosed() {
		response["ledger_hash"] = FormatLedgerHash(ledger.Hash())
		response["ledger_index"] = ledger.Sequence()
	} else {
		response["ledger_current_index"] = ledger.Sequence()
	}
	return response
}
