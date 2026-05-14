package entry

import (
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
	Domain               *string
	EmailHash            *[16]byte
	RegularKey           *[20]byte
	TickSize             *uint8
	TransferRate         *uint32
	FirstNFTokenSequence *uint32
}

func (a *AccountRoot) Type() Type {
	return TypeAccountRoot
}

func (a *AccountRoot) Validate() error {
	if a.Account == [20]byte{} {
		return errors.New("account ID is required")
	}
	if a.TransferRate != nil && *a.TransferRate > 0 && *a.TransferRate < 1000000000 {
		return errors.New("transfer rate must be 0 or >= 1000000000")
	}
	return nil
}
