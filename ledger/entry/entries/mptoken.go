package entry

import (
	"errors"

	"github.com/LeJamon/goXRPLd/ledger/entry"
)

// MPTokenIssuance represents a Multi-Purpose Token issuance ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltMPTOKEN_ISSUANCE
type MPTokenIssuance struct {
	BaseEntry

	// Required fields
	Issuer            [20]byte // Account that issued this MPT
	Sequence          uint32   // Sequence number when created
	OwnerNode         uint64   // Directory node hint
	OutstandingAmount uint64   // Total amount currently outstanding

	// Default fields (always present but may be zero)
	TransferFee uint16 // Transfer fee in basis points
	AssetScale  uint8  // Decimal places for the token

	// Optional fields
	MaximumAmount   *uint64 // Maximum amount that can be issued
	LockedAmount    *uint64 // Amount currently locked
	MPTokenMetadata *[]byte // Metadata for the token
	DomainID        *[32]byte // Associated permissioned domain
}

func (m *MPTokenIssuance) Type() entry.Type {
	return entry.TypeMPTokenIssuance
}

func (m *MPTokenIssuance) Validate() error {
	if m.Issuer == [20]byte{} {
		return errors.New("issuer is required")
	}
	if m.MaximumAmount != nil && m.OutstandingAmount > *m.MaximumAmount {
		return errors.New("outstanding amount exceeds maximum")
	}
	return nil
}

func (m *MPTokenIssuance) Hash() ([32]byte, error) {
	hash := m.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= m.Issuer[i]
	}
	return hash, nil
}

// MPToken represents a Multi-Purpose Token holding ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltMPTOKEN
type MPToken struct {
	BaseEntry

	// Required fields
	Account           [20]byte // Account that holds this MPT
	MPTokenIssuanceID [32]byte // ID of the MPT issuance
	OwnerNode         uint64   // Directory node hint

	// Default fields (always present but may be zero)
	MPTAmount uint64 // Amount held

	// Optional fields
	LockedAmount *uint64 // Amount currently locked
}

func (m *MPToken) Type() entry.Type {
	return entry.TypeMPToken
}

func (m *MPToken) Validate() error {
	if m.Account == [20]byte{} {
		return errors.New("account is required")
	}
	if m.MPTokenIssuanceID == [32]byte{} {
		return errors.New("MPToken issuance ID is required")
	}
	return nil
}

func (m *MPToken) Hash() ([32]byte, error) {
	hash := m.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= m.Account[i]
	}
	return hash, nil
}
