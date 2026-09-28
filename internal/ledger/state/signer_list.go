package state

import (
	"fmt"

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
	var wire entry.SignerList
	if err := wire.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode SignerList: %w", err)
	}
	ownerNode, err := wire.GetOwnerNode()
	if err != nil {
		return nil, err
	}
	values, err := wire.GetSignerEntries()
	if err != nil {
		return nil, err
	}
	signerList := &SignerListInfo{
		SignerListID: wire.SignerListID,
		SignerQuorum: wire.SignerQuorum,
		Flags:        wire.Flags,
		OwnerNode:    ownerNode,
	}
	for _, value := range values {
		account, err := value.GetAccountAddress()
		if err != nil {
			return nil, err
		}
		signer := AccountSignerEntry{Account: account, SignerWeight: value.SignerWeight}
		if value.HasWalletLocator() {
			signer.WalletLocator, err = value.GetWalletLocatorHex()
			if err != nil {
				return nil, err
			}
		}
		signerList.SignerEntries = append(signerList.SignerEntries, signer)
	}
	return signerList, nil
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
	ledgerEntry.SetOwnerNodeValue(ownerNode)
	ledgerEntry.SetSignerListID(0)
	ledgerEntry.SetFlags(flags)

	if owner != nil {
		if err := ledgerEntry.SetOwnerValue(*owner); err != nil {
			return nil, err
		}
	}

	signerEntries := make([]entry.SignerEntryValue, len(entries))
	for i, signer := range entries {
		if err := signerEntries[i].SetAccountAddress(signer.Account); err != nil {
			return nil, err
		}
		signerEntries[i].SetSignerWeight(signer.SignerWeight)
		if expandedSignerList && signer.WalletLocator != "" {
			if err := signerEntries[i].SetWalletLocatorHex(signer.WalletLocator); err != nil {
				return nil, err
			}
		}
	}
	if err := ledgerEntry.SetSignerEntriesValue(signerEntries); err != nil {
		return nil, err
	}

	return ledgerEntry.Encode()
}

// SerializeTicket serializes a Ticket ledger entry.
func SerializeTicket(ownerID [20]byte, ticketSeq uint32, ownerNode uint64) ([]byte, error) {
	var ticket entry.Ticket
	if err := ticket.SetAccountValue(ownerID); err != nil {
		return nil, err
	}
	ticket.SetTicketSequence(ticketSeq)
	ticket.SetOwnerNodeValue(ownerNode)
	ticket.SetFlags(0)
	return ticket.Encode()
}

// SerializeDepositPreauth serializes a DepositPreauth ledger entry.
func SerializeDepositPreauth(ownerID, authorizedID [20]byte, ownerNode uint64) ([]byte, error) {
	var preauth entry.DepositPreauth
	if err := preauth.SetAccountValue(ownerID); err != nil {
		return nil, err
	}
	if err := preauth.SetAuthorizeValue(authorizedID); err != nil {
		return nil, err
	}
	preauth.SetOwnerNodeValue(ownerNode)
	preauth.SetFlags(0)
	return preauth.Encode()
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
	var preauth entry.DepositPreauth
	if err := preauth.SetAccountValue(ownerID); err != nil {
		return nil, err
	}
	values := make([]entry.CredentialValue, len(credentials))
	for i, credential := range credentials {
		if err := values[i].SetIssuerAddress(credential.Issuer); err != nil {
			return nil, err
		}
		if err := values[i].SetCredentialTypeHex(credential.CredentialType); err != nil {
			return nil, err
		}
	}
	if err := preauth.SetAuthorizeCredentialsValue(values); err != nil {
		return nil, err
	}
	preauth.SetOwnerNodeValue(ownerNode)
	preauth.SetFlags(0)
	return preauth.Encode()
}

// DepositPreauthEntry holds parsed fields from a DepositPreauth ledger entry.
type DepositPreauthEntry struct {
	Account   [20]byte
	OwnerNode uint64
}

// ParseDepositPreauth parses a DepositPreauth ledger entry from binary data.
// Extracts Account and OwnerNode needed for removeFromLedger.
func ParseDepositPreauth(data []byte) (*DepositPreauthEntry, error) {
	var wire entry.DepositPreauth
	if err := wire.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode DepositPreauth: %w", err)
	}

	if wire.Account == "" {
		return nil, fmt.Errorf("failed to decode DepositPreauth: missing Account")
	}
	if wire.OwnerNode == "" {
		return nil, fmt.Errorf("failed to decode DepositPreauth: missing OwnerNode")
	}

	parsed := &DepositPreauthEntry{}
	var err error
	parsed.Account, err = wire.GetAccount()
	if err != nil {
		return nil, fmt.Errorf("failed to decode DepositPreauth: invalid Account: %w", err)
	}
	parsed.OwnerNode, err = wire.GetOwnerNode()
	if err != nil {
		return nil, fmt.Errorf("failed to decode DepositPreauth: invalid OwnerNode: %w", err)
	}

	return parsed, nil
}
