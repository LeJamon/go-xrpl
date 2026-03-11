package entry

import (
	"encoding/binary"
	"errors"
)

// AccountRoot represents an account in the ledger
type AccountRoot struct {
	BaseEntry
	Account    [20]byte
	Sequence   uint32
	Balance    uint64
	OwnerCount uint32

	// Optional fields
	Domain                *string
	EmailHash             *[32]byte
	RegularKey            *[20]byte
	TickSize              *uint8
	TransferRate          *uint32
	FirstNFTokenSequence  *uint32
}

func (a *AccountRoot) Type() Type {
	return TypeAccountRoot
}

func (a *AccountRoot) Validate() error {
	if a.Account == [20]byte{} {
		return errors.New("account ID is required")
	}
	if a.Balance < 0 {
		return errors.New("balance cannot be negative")
	}
	if a.TransferRate != nil && *a.TransferRate > 0 && *a.TransferRate < 1000000000 {
		return errors.New("transfer rate must be 0 or >= 1000000000")
	}
	return nil
}

func (a *AccountRoot) Hash() ([32]byte, error) {
	// Start with base hash
	hash := a.BaseEntry.Hash()

	// Add account-specific fields
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[:4], a.Sequence)
	binary.BigEndian.PutUint32(buf[4:], a.OwnerCount)

	// XOR the account specific data into the hash
	for i := 0; i < 8; i++ {
		hash[i] ^= buf[i]
	}

	// XOR in the account ID
	for i := 0; i < 20; i++ {
		hash[i+8] ^= a.Account[i]
	}

	return hash, nil
}
