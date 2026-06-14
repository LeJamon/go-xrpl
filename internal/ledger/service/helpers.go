package service

import (
	"errors"
	"strconv"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
)

// formatHashHex renders a 32-byte hash as a 64-char uppercase hex string,
// matching rippled's uint256 JSON emit (uint256::to_string). Use this for any
// [32]byte value that crosses a JSON boundary (RPC response, marker, key).
func formatHashHex(hash [32]byte) string {
	const hexChars = "0123456789ABCDEF"
	result := make([]byte, 64)
	for i, b := range hash {
		result[i*2] = hexChars[b>>4]
		result[i*2+1] = hexChars[b&0x0F]
	}
	return string(result)
}

// decodeAccountIDLocal decodes an account address to its 20-byte ID
func decodeAccountIDLocal(address string) ([20]byte, error) {
	var accountID [20]byte
	if address == "" {
		return accountID, errors.New("empty address")
	}
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		return accountID, err
	}
	copy(accountID[:], accountIDBytes)
	return accountID, nil
}

// helper function to format ledger range
func formatRange(min, max uint32) string {
	return strconv.FormatUint(uint64(min), 10) + "-" + strconv.FormatUint(uint64(max), 10)
}

// normalizeObjectType maps rippled's RPC type names (lowercase/snake_case)
// to the PascalCase ledger-entry type names.
func normalizeObjectType(objType string) string {
	switch objType {
	case "account":
		return "AccountRoot"
	case "amendments":
		return "Amendments"
	case "amm":
		return "AMM"
	case "bridge":
		return "Bridge"
	case "check":
		return "Check"
	case "credential":
		return "Credential"
	case "delegate":
		return "Delegate"
	case "deposit_preauth":
		return "DepositPreauth"
	case "did":
		return "DID"
	case "directory":
		return "DirectoryNode"
	case "escrow":
		return "Escrow"
	case "fee":
		return "FeeSettings"
	case "hashes":
		return "LedgerHashes"
	case "mptoken":
		return "MPToken"
	case "mpt_issuance":
		return "MPTokenIssuance"
	case "nft_offer":
		return "NFTokenOffer"
	case "nft_page":
		return "NFTokenPage"
	case "nunl":
		return "NegativeUNL"
	case "offer":
		return "Offer"
	case "oracle":
		return "Oracle"
	case "payment_channel":
		return "PayChannel"
	case "permissioned_domain":
		return "PermissionedDomain"
	case "state":
		return "RippleState"
	case "signer_list":
		return "SignerList"
	case "ticket":
		return "Ticket"
	case "vault":
		return "Vault"
	case "xchain_owned_claim_id":
		return "XChainOwnedClaimID"
	case "xchain_owned_create_account_claim_id":
		return "XChainOwnedCreateAccountClaimID"
	default:
		return objType
	}
}
