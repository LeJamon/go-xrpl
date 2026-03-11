package state

import (
	"encoding/hex"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
)

// lsfOneOwnerCount is the flag indicating that this SignerList only costs
// 1 OwnerCount (set when featureMultiSignReserve is enabled).
// Reference: rippled LedgerFormats.h lsfOneOwnerCount = 0x00020000
const LsfOneOwnerCount uint32 = 0x00020000

// SignerListInfo holds parsed signer list data from a ledger entry.
type SignerListInfo struct {
	SignerListID   uint32
	SignerQuorum  uint32
	Flags         uint32
	SignerEntries []AccountSignerEntry
}

// AccountSignerEntry represents a single signer entry parsed from the ledger.
type AccountSignerEntry struct {
	Account       string
	SignerWeight  uint16
	WalletLocator string
}

// SignerEntry represents a signer entry for serialization.
type SignerEntry struct {
	Account      string
	SignerWeight uint16
}

// ParseSignerList parses a SignerList ledger entry from binary data.
func ParseSignerList(data []byte) (*SignerListInfo, error) {
	hexStr := hex.EncodeToString(data)
	decoded, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode SignerList: %w", err)
	}

	signerList := &SignerListInfo{
		SignerListID: 0,
	}

	if flags, ok := decoded["Flags"]; ok {
		switch v := flags.(type) {
		case float64:
			signerList.Flags = uint32(v)
		case int:
			signerList.Flags = uint32(v)
		case uint32:
			signerList.Flags = v
		}
	}

	if quorum, ok := decoded["SignerQuorum"]; ok {
		switch v := quorum.(type) {
		case float64:
			signerList.SignerQuorum = uint32(v)
		case int:
			signerList.SignerQuorum = uint32(v)
		case uint32:
			signerList.SignerQuorum = v
		}
	}

	if entries, ok := decoded["SignerEntries"]; ok {
		if entriesArray, ok := entries.([]interface{}); ok {
			for _, entryWrapper := range entriesArray {
				if entryMap, ok := entryWrapper.(map[string]interface{}); ok {
					var signerEntry map[string]interface{}
					if se, ok := entryMap["SignerEntry"]; ok {
						signerEntry, _ = se.(map[string]interface{})
					} else {
						signerEntry = entryMap
					}

					if signerEntry != nil {
						entry := AccountSignerEntry{}
						if account, ok := signerEntry["Account"].(string); ok {
							entry.Account = account
						}
						if weight, ok := signerEntry["SignerWeight"]; ok {
							switch v := weight.(type) {
							case float64:
								entry.SignerWeight = uint16(v)
							case int:
								entry.SignerWeight = uint16(v)
							case uint16:
								entry.SignerWeight = v
							}
						}
						if walletLocator, ok := signerEntry["WalletLocator"].(string); ok {
							entry.WalletLocator = walletLocator
						}
						signerList.SignerEntries = append(signerList.SignerEntries, entry)
					}
				}
			}
		}
	}

	return signerList, nil
}

// SerializeSignerList serializes a SignerList ledger entry.
// flags should be LsfOneOwnerCount when featureMultiSignReserve is enabled, 0 otherwise.
// Reference: rippled SetSignerList.cpp writeSignersToSLE()
func SerializeSignerList(quorum uint32, entries []SignerEntry, ownerID [20]byte, flags uint32) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "SignerList",
		"Account":         ownerAddress,
		"SignerQuorum":    quorum,
		"OwnerNode":       "0",
	}

	// Only set Flags if non-zero, matching rippled's writeSignersToSLE behavior.
	// Reference: rippled SetSignerList.cpp:429 - if (flags) ledgerEntry->setFieldU32(sfFlags, flags);
	if flags != 0 {
		jsonObj["Flags"] = flags
	}

	if len(entries) > 0 {
		signerEntries := make([]map[string]any, len(entries))
		for i, entry := range entries {
			signerEntries[i] = map[string]any{
				"SignerEntry": map[string]any{
					"Account":      entry.Account,
					"SignerWeight": entry.SignerWeight,
				},
			}
		}
		jsonObj["SignerEntries"] = signerEntries
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode SignerList: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// SerializeTicket serializes a Ticket ledger entry.
func SerializeTicket(ownerID [20]byte, ticketSeq uint32) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "Ticket",
		"Account":         ownerAddress,
		"TicketSequence":  ticketSeq,
		"OwnerNode":       "0",
		"Flags":           uint32(0),
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Ticket: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// SerializeDepositPreauth serializes a DepositPreauth ledger entry.
func SerializeDepositPreauth(ownerID, authorizedID [20]byte) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	authorizedAddress, err := addresscodec.EncodeAccountIDToClassicAddress(authorizedID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode authorized address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "DepositPreauth",
		"Account":         ownerAddress,
		"Authorize":       authorizedAddress,
		"OwnerNode":       "0",
		"Flags":           uint32(0),
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode DepositPreauth: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// DepositPreauthCredential represents a credential in a credential-based deposit preauth entry.
type DepositPreauthCredential struct {
	Issuer         string // base58 address
	CredentialType string // hex-encoded
}

// SerializeDepositPreauthCredentials serializes a credential-based DepositPreauth ledger entry.
// The credentials should already be sorted.
// Reference: rippled DepositPreauth.cpp doApply() sfAuthorizeCredentials branch
func SerializeDepositPreauthCredentials(ownerID [20]byte, credentials []DepositPreauthCredential) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	// Build the AuthorizeCredentials array
	credArray := make([]map[string]any, len(credentials))
	for i, c := range credentials {
		credArray[i] = map[string]any{
			"Credential": map[string]any{
				"Issuer":         c.Issuer,
				"CredentialType": c.CredentialType,
			},
		}
	}

	jsonObj := map[string]any{
		"LedgerEntryType":      "DepositPreauth",
		"Account":              ownerAddress,
		"AuthorizeCredentials": credArray,
		"OwnerNode":            "0",
		"Flags":                uint32(0),
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode DepositPreauth (credentials): %w", err)
	}

	return hex.DecodeString(hexStr)
}

// DepositPreauthEntry holds parsed fields from a DepositPreauth ledger entry.
type DepositPreauthEntry struct {
	Account   [20]byte
	OwnerNode uint64
}

// ParseDepositPreauth parses a DepositPreauth ledger entry from binary data.
// Extracts Account and OwnerNode needed for removeFromLedger.
func ParseDepositPreauth(data []byte) (*DepositPreauthEntry, error) {
	hexStr := hex.EncodeToString(data)
	jsonObj, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode DepositPreauth: %w", err)
	}

	entry := &DepositPreauthEntry{}

	if account, ok := jsonObj["Account"].(string); ok {
		accountID, err := DecodeAccountID(account)
		if err != nil {
			return nil, fmt.Errorf("failed to decode Account: %w", err)
		}
		entry.Account = accountID
	}

	if ownerNode, ok := jsonObj["OwnerNode"].(string); ok {
		entry.OwnerNode = parseUint64Hex(ownerNode)
	}

	return entry, nil
}
