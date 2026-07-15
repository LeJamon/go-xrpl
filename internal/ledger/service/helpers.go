package service

import (
	"errors"
	"strconv"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/protocol"
)

func formatHashHex(hash [32]byte) string {
	return protocol.Hash256Hex(hash)
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
	case "loan":
		return "Loan"
	case "loan_broker":
		return "LoanBroker"
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
