package entry

import (
	"github.com/LeJamon/goXRPLd/ledger/entry"
)

// DisabledValidator represents a validator that has been disabled
type DisabledValidator struct {
	PublicKey      [33]byte // Validator's public key
	FirstLedgerSeq uint32   // Ledger sequence when disabled
}

// NegativeUNL represents the Negative Unique Node List ledger entry
// This is a singleton object - only one exists in the ledger
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltNEGATIVE_UNL
type NegativeUNL struct {
	BaseEntry

	// Optional fields (all are optional for this singleton)
	DisabledValidators  []DisabledValidator // List of disabled validators
	ValidatorToDisable  *[33]byte           // Validator being voted to disable
	ValidatorToReEnable *[33]byte           // Validator being voted to re-enable
}

func (n *NegativeUNL) Type() entry.Type {
	return entry.TypeNegativeUNL
}

func (n *NegativeUNL) Validate() error {
	// NegativeUNL is a singleton with all optional fields
	return nil
}

func (n *NegativeUNL) Hash() ([32]byte, error) {
	return n.BaseEntry.Hash(), nil
}
