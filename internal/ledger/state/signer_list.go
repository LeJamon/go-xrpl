package state

import (
	"fmt"
	"strconv"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
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
	return parseSignerList(data, false)
}

// ParseSignerListLegacy parses a historical go-xrpl SignerList blob using the
// explicitly declared compatibility fields.
func ParseSignerListLegacy(data []byte) (*SignerListInfo, error) {
	return parseSignerList(data, true)
}

func parseSignerList(data []byte, legacy bool) (*SignerListInfo, error) {
	decoded := entry.NewByName("SignerList")
	if decoded == nil {
		return nil, fmt.Errorf("failed to decode SignerList: decoder is not registered")
	}
	var err error
	if legacy {
		err = entry.DecodeLegacy(decoded, data)
	} else {
		err = decoded.Decode(data)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to decode SignerList: %w", err)
	}
	wire, ok := decoded.(*entry.SignerList)
	if !ok {
		return nil, fmt.Errorf("failed to decode SignerList: decoder has type %T", decoded)
	}

	signerList := &SignerListInfo{
		SignerListID: wire.SignerListID,
		SignerQuorum: wire.SignerQuorum,
		Flags:        wire.Flags,
	}
	if wire.OwnerNode != "" {
		ownerNode, err := strconv.ParseUint(wire.OwnerNode, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to decode SignerList: invalid OwnerNode: %w", err)
		}
		signerList.OwnerNode = ownerNode
	}
	entries, err := signerEntriesFromGenerated(wire.SignerEntries)
	if err != nil {
		return nil, fmt.Errorf("failed to decode SignerList: %w", err)
	}
	signerList.SignerEntries = entries
	return signerList, nil
}

func signerEntriesFromGenerated(values []any) ([]AccountSignerEntry, error) {
	if len(values) == 0 {
		return nil, nil
	}
	entries := make([]AccountSignerEntry, 0, len(values))
	for _, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("SignerEntries element has type %T", value)
		}
		rawSigner, ok := object["SignerEntry"]
		if !ok {
			continue
		}
		signer, ok := rawSigner.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("SignerEntry has type %T", rawSigner)
		}

		parsed := AccountSignerEntry{}
		if account, ok := signer["Account"].(string); ok {
			parsed.Account = account
		}
		if weight, ok := signer["SignerWeight"].(int); ok {
			parsed.SignerWeight = uint16(weight)
		}
		if walletLocator, ok := signer["WalletLocator"].(string); ok {
			parsed.WalletLocator = walletLocator
		}
		entries = append(entries, parsed)
	}
	return entries, nil
}

// SerializeSignerList serializes a SignerList ledger entry.
// flags should be LsfOneOwnerCount when featureMultiSignReserve is enabled, 0 otherwise.
// expandedSignerList gates emission of WalletLocator, mirroring rippled's
// defensive check (a tag is never written when featureExpandedSignerList is off).
// owner is non-nil only when fixIncludeKeyletFields is active, in which case
// sfOwner (a keylet input) is stored.
// Reference: rippled SetSignerList.cpp writeSignersToSLE()
func SerializeSignerList(quorum uint32, entries []SignerEntry, flags uint32, expandedSignerList bool, ownerNode uint64, owner *[20]byte) ([]byte, error) {
	ledgerEntry := &entry.SignerList{}
	ledgerEntry.SetSignerQuorum(quorum)
	ledgerEntry.SetOwnerNode(strconv.FormatUint(ownerNode, 16))
	ledgerEntry.SetSignerListID(0)
	ledgerEntry.SetFlags(flags)

	if owner != nil {
		ownerAddr, err := addresscodec.EncodeAccountIDToClassicAddress(owner[:])
		if err != nil {
			return nil, fmt.Errorf("failed to encode signer list owner address: %w", err)
		}
		ledgerEntry.SetOwner(ownerAddr)
	}

	signerEntries := make([]any, len(entries))
	for i, signer := range entries {
		inner := map[string]any{
			"Account":      signer.Account,
			"SignerWeight": signer.SignerWeight,
		}
		if expandedSignerList && signer.WalletLocator != "" {
			inner["WalletLocator"] = signer.WalletLocator
		}
		signerEntries[i] = map[string]any{"SignerEntry": inner}
	}
	ledgerEntry.SetSignerEntries(signerEntries)

	return ledgerEntry.Encode()
}

// SerializeTicket serializes a Ticket ledger entry.
func SerializeTicket(ownerID [20]byte, ticketSeq uint32, ownerNode uint64) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	entry := &entry.Ticket{}
	entry.SetAccount(ownerAddress)
	entry.SetTicketSequence(ticketSeq)
	entry.SetOwnerNode(strconv.FormatUint(ownerNode, 16))
	entry.SetFlags(0)
	return entry.Encode()
}

// SerializeDepositPreauth serializes a DepositPreauth ledger entry.
func SerializeDepositPreauth(ownerID, authorizedID [20]byte, ownerNode uint64) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	authorizedAddress, err := addresscodec.EncodeAccountIDToClassicAddress(authorizedID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode authorized address: %w", err)
	}

	entry := &entry.DepositPreauth{}
	entry.SetAccount(ownerAddress)
	entry.SetAuthorize(authorizedAddress)
	entry.SetOwnerNode(strconv.FormatUint(ownerNode, 16))
	entry.SetFlags(0)
	return entry.Encode()
}

// DepositPreauthCredential represents a credential in a credential-based deposit preauth entry.
type DepositPreauthCredential struct {
	Issuer         string // base58 address
	CredentialType string // hex-encoded
}

// SerializeDepositPreauthCredentials serializes a credential-based DepositPreauth ledger entry.
// The credentials should already be sorted.
// Reference: rippled DepositPreauth.cpp doApply() sfAuthorizeCredentials branch
func SerializeDepositPreauthCredentials(ownerID [20]byte, credentials []DepositPreauthCredential, ownerNode uint64) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	credArray := make([]any, len(credentials))
	for i, c := range credentials {
		credArray[i] = map[string]any{
			"Credential": map[string]any{
				"Issuer":         c.Issuer,
				"CredentialType": c.CredentialType,
			},
		}
	}

	entry := &entry.DepositPreauth{}
	entry.SetAccount(ownerAddress)
	entry.SetAuthorizeCredentials(credArray)
	entry.SetOwnerNode(strconv.FormatUint(ownerNode, 16))
	entry.SetFlags(0)
	return entry.Encode()
}

// DepositPreauthEntry holds parsed fields from a DepositPreauth ledger entry.
type DepositPreauthEntry struct {
	Account   [20]byte
	OwnerNode uint64
}

// ParseDepositPreauth parses a DepositPreauth ledger entry from binary data.
// Extracts Account and OwnerNode needed for removeFromLedger.
func ParseDepositPreauth(data []byte) (*DepositPreauthEntry, error) {
	decoded := entry.NewByName("DepositPreauth")
	if decoded == nil {
		return nil, fmt.Errorf("failed to decode DepositPreauth: decoder is not registered")
	}
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode DepositPreauth: %w", err)
	}
	wire, ok := decoded.(*entry.DepositPreauth)
	if !ok {
		return nil, fmt.Errorf("failed to decode DepositPreauth: decoder has type %T", decoded)
	}

	if wire.Account == "" {
		return nil, fmt.Errorf("failed to decode DepositPreauth: missing Account")
	}
	if wire.OwnerNode == "" {
		return nil, fmt.Errorf("failed to decode DepositPreauth: missing OwnerNode")
	}

	parsed := &DepositPreauthEntry{}
	var err error
	parsed.Account, err = DecodeAccountID(wire.Account)
	if err != nil {
		return nil, fmt.Errorf("failed to decode DepositPreauth: invalid Account: %w", err)
	}
	parsed.OwnerNode, err = strconv.ParseUint(wire.OwnerNode, 16, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode DepositPreauth: invalid OwnerNode: %w", err)
	}

	return parsed, nil
}
