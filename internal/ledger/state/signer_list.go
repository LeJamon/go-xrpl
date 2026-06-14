package state

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// LsfOneOwnerCount indicates this SignerList only costs 1 OwnerCount (set when
// featureMultiSignReserve is enabled).
const LsfOneOwnerCount = entry.LsfOneOwnerCount

// SignerListInfo holds parsed signer list data from a ledger entry.
type SignerListInfo struct {
	SignerListID  uint32
	SignerQuorum  uint32
	Flags         uint32
	OwnerNode     uint64
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
	Account       string
	SignerWeight  uint16
	WalletLocator string
}

// ParseSignerList parses a SignerList ledger entry from binary data.
func ParseSignerList(data []byte) (*SignerListInfo, error) {
	signerList := &SignerListInfo{
		SignerListID: 0,
	}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			switch f.FieldCode {
			case 2: // Flags
				signerList.Flags = f.UInt32()
			case 35: // SignerQuorum
				signerList.SignerQuorum = f.UInt32()
			}
		case stUInt64:
			if f.FieldCode == 4 { // OwnerNode
				signerList.OwnerNode = f.UInt64()
			}
		case stArray:
			if f.FieldCode == 4 { // SignerEntries
				signerList.SignerEntries = parseSignerEntries(f.Value)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to decode SignerList: %w", err)
	}

	return signerList, nil
}

// parseSignerEntries decodes the SignerEntries STArray content; each element is
// a SignerEntry STObject.
func parseSignerEntries(content []byte) []AccountSignerEntry {
	var entries []AccountSignerEntry
	_ = WalkFields(content, func(elem Field) error {
		if elem.TypeCode != stObject || elem.FieldCode != 11 { // SignerEntry
			return nil
		}
		e := AccountSignerEntry{}
		_ = WalkFields(elem.Value, func(inner Field) error {
			switch inner.TypeCode {
			case stAccountID:
				if inner.FieldCode == 1 { // Account
					if id, ok := inner.AccountID(); ok {
						e.Account, _ = EncodeAccountID(id)
					}
				}
			case stUInt16:
				if inner.FieldCode == 3 { // SignerWeight
					e.SignerWeight = inner.UInt16()
				}
			case stHash256:
				if inner.FieldCode == 7 { // WalletLocator
					e.WalletLocator = strings.ToUpper(hex.EncodeToString(inner.Value))
				}
			}
			return nil
		})
		entries = append(entries, e)
		return nil
	})
	return entries
}

// SerializeSignerList serializes a SignerList ledger entry.
// flags should be LsfOneOwnerCount when featureMultiSignReserve is enabled, 0 otherwise.
// expandedSignerList gates emission of WalletLocator, mirroring rippled's
// defensive check (a tag is never written when featureExpandedSignerList is off).
// Reference: rippled SetSignerList.cpp writeSignersToSLE()
func SerializeSignerList(quorum uint32, entries []SignerEntry, flags uint32, expandedSignerList bool, ownerNode uint64) ([]byte, error) {
	// rippled's ltSIGNER_LIST has no sfAccount (ledger_entries.macro:122-129);
	// emitting one diverges the SLE bytes (account_hash fork) and leaks an
	// "Account" entry into the metadata FinalFields.
	jsonObj := map[string]any{
		"LedgerEntryType": "SignerList",
		"SignerQuorum":    quorum,
		"OwnerNode":       strconv.FormatUint(ownerNode, 16),
		// rippled hardcodes sfSignerListID = 0 on every signer list and
		// always writes it (SetSignerList.cpp:428 writeSignersToSLE). Omitting
		// it diverges the SLE bytes (account_hash fork) and, when replacing a
		// rippled-created list, surfaces a spurious "SignerListID: 0" in the
		// ModifiedNode PreviousFields. A zero SignerListID is value-default
		// (STInteger::isDefault), so the CreatedNode NewFields filter drops
		// it automatically.
		"SignerListID": uint32(0),
	}

	// sfFlags is a soeREQUIRED common field (LedgerFormats.cpp commonFields), so
	// rippled serializes it on every SignerList — present at its default 0 from
	// the SLE template. writeSignersToSLE's `if (flags) setFieldU32(sfFlags,...)`
	// (SetSignerList.cpp:429-430) only *overwrites* the template default; when
	// flags==0 (the MultiSignReserve-disabled path) the field stays present at 0.
	// Omitting it when zero diverges the SLE state (account_hash fork). The
	// CreatedNode NewFields still excludes Flags=0 (default-filtered by the typed
	// metadata path); a ModifiedNode's FinalFields correctly carries Flags:0.
	jsonObj["Flags"] = flags

	if len(entries) > 0 {
		signerEntries := make([]map[string]any, len(entries))
		for i, entry := range entries {
			inner := map[string]any{
				"Account":      entry.Account,
				"SignerWeight": entry.SignerWeight,
			}
			// Reference: rippled SetSignerList.cpp:445-448
			if expandedSignerList && entry.WalletLocator != "" {
				inner["WalletLocator"] = entry.WalletLocator
			}
			signerEntries[i] = map[string]any{
				"SignerEntry": inner,
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
func SerializeTicket(ownerID [20]byte, ticketSeq uint32, ownerNode uint64) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "Ticket",
		"Account":         ownerAddress,
		"TicketSequence":  ticketSeq,
		"OwnerNode":       strconv.FormatUint(ownerNode, 16),
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
	entry := &DepositPreauthEntry{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt64:
			if f.FieldCode == 4 { // OwnerNode
				entry.OwnerNode = f.UInt64()
			}
		case stAccountID:
			if f.FieldCode == 1 { // Account
				if id, ok := f.AccountID(); ok {
					entry.Account = id
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to decode DepositPreauth: %w", err)
	}

	return entry, nil
}
