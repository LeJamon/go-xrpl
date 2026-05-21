package service

import (
	"bytes"
	"errors"
	"sort"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// formatHashHex formats a hash as hex string
func formatHashHex(hash [32]byte) string {
	const hexChars = "0123456789ABCDEF"
	result := make([]byte, 64)
	for i, b := range hash {
		result[i*2] = hexChars[b>>4]
		result[i*2+1] = hexChars[b&0x0F]
	}
	return string(result)
}

// hexDecode decodes a hex string to bytes
func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errors.New("odd length hex string")
	}
	result := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		var b byte
		for j := 0; j < 2; j++ {
			c := s[i+j]
			switch {
			case c >= '0' && c <= '9':
				b = b<<4 | (c - '0')
			case c >= 'a' && c <= 'f':
				b = b<<4 | (c - 'a' + 10)
			case c >= 'A' && c <= 'F':
				b = b<<4 | (c - 'A' + 10)
			default:
				return nil, errors.New("invalid hex character")
			}
		}
		result[i/2] = b
	}
	return result, nil
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

// amountsMatchCurrency checks if two amounts have the same currency (ignoring value)
func amountsMatchCurrency(a, b tx.Amount) bool {
	if a.IsNative() && b.IsNative() {
		return true
	}
	if a.IsNative() != b.IsNative() {
		return false
	}
	return a.Currency == b.Currency && a.Issuer == b.Issuer
}

// calculateOfferQuality calculates the quality (price) of an offer
func calculateOfferQuality(pays, gets tx.Amount) string {
	// Quality = TakerPays / TakerGets
	paysVal := parseAmountValue(pays)
	getsVal := parseAmountValue(gets)
	if getsVal == 0 {
		return "0"
	}
	quality := paysVal / getsVal
	return strconv.FormatFloat(quality, 'g', -1, 64)
}

// parseAmountValue parses an amount value as float
func parseAmountValue(amt tx.Amount) float64 {
	if amt.IsNative() {
		return float64(amt.Drops())
	}
	return amt.Float64()
}

// formatHash formats a hash as a string
func formatHash(hash [32]byte) string {
	return string(hash[:])
}

// sortBookOffersByQualityWithRaw sorts by ascending BookDirectory bytes
// (rippled's directory walk order, best-quality first), keeping raw in
// lockstep. A float-based sort would lose precision on closely-quoted
// offers, so we compare the raw quality bytes directly.
func sortBookOffersByQualityWithRaw(offers []BookOffer, raw []*state.LedgerOffer) {
	if raw == nil {
		sort.SliceStable(offers, func(i, j int) bool {
			qi, _ := strconv.ParseFloat(offers[i].Quality, 64)
			qj, _ := strconv.ParseFloat(offers[j].Quality, 64)
			return qi < qj
		})
		return
	}
	indices := make([]int, len(offers))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(a, b int) bool {
		i, j := indices[a], indices[b]
		return bytes.Compare(raw[i].BookDirectory[:], raw[j].BookDirectory[:]) < 0
	})
	sortedOffers := make([]BookOffer, len(offers))
	sortedRaw := make([]*state.LedgerOffer, len(raw))
	for newIdx, oldIdx := range indices {
		sortedOffers[newIdx] = offers[oldIdx]
		sortedRaw[newIdx] = raw[oldIdx]
	}
	copy(offers, sortedOffers)
	copy(raw, sortedRaw)
}

// helper function to format ledger range
func formatRange(min, max uint32) string {
	return strconv.FormatUint(uint64(min), 10) + "-" + strconv.FormatUint(uint64(max), 10)
}

// getLedgerEntryType extracts the entry type from serialized data.
// Type codes match LEDGER_ENTRY_TYPES in definitions.json.
func getLedgerEntryType(data []byte) string {
	if len(data) < 3 {
		return ""
	}
	if data[0] != 0x11 { // UInt16 field header for LedgerEntryType
		return ""
	}
	entryType := uint16(data[1])<<8 | uint16(data[2])
	switch entryType {
	case 55: // NFTokenOffer
		return "NFTokenOffer"
	case 67: // Check
		return "Check"
	case 73: // DID
		return "DID"
	case 78: // NegativeUNL
		return "NegativeUNL"
	case 80: // NFTokenPage
		return "NFTokenPage"
	case 83: // SignerList
		return "SignerList"
	case 84: // Ticket
		return "Ticket"
	case 97: // AccountRoot
		return "AccountRoot"
	case 100: // DirectoryNode
		return "DirectoryNode"
	case 102: // Amendments
		return "Amendments"
	case 104: // LedgerHashes
		return "LedgerHashes"
	case 105: // Bridge
		return "Bridge"
	case 111: // Offer
		return "Offer"
	case 112: // DepositPreauth
		return "DepositPreauth"
	case 113: // XChainOwnedClaimID
		return "XChainOwnedClaimID"
	case 114: // RippleState
		return "RippleState"
	case 115: // FeeSettings
		return "FeeSettings"
	case 116: // XChainOwnedCreateAccountClaimID
		return "XChainOwnedCreateAccountClaimID"
	case 117: // Escrow
		return "Escrow"
	case 120: // PayChannel
		return "PayChannel"
	case 121: // AMM
		return "AMM"
	case 126: // MPTokenIssuance
		return "MPTokenIssuance"
	case 127: // MPToken
		return "MPToken"
	case 128: // Oracle
		return "Oracle"
	case 129: // Credential
		return "Credential"
	case 130: // PermissionedDomain
		return "PermissionedDomain"
	case 131: // Delegate
		return "Delegate"
	case 132: // Vault
		return "Vault"
	default:
		return ""
	}
}

// normalizeObjectType maps rippled's RPC type names (lowercase/snake_case)
// to the PascalCase names used by getLedgerEntryType.
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

// isObjectForAccount checks if a ledger object belongs to an account
func isObjectForAccount(data []byte, accountID [20]byte, entryType string) bool {
	// This is a simplified check - in production, properly parse the object
	// For now, check if the account ID appears in the data
	for i := 0; i <= len(data)-20; i++ {
		match := true
		for j := 0; j < 20; j++ {
			if data[i+j] != accountID[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
