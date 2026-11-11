package tx

import (
	"errors"
	"fmt"
)

// Delegation states whether a delegate holding a transaction-level permission
// may submit a transaction type. It has no usable zero value, so every
// descriptor must choose.
type Delegation uint8

const (
	delegationUnset Delegation = iota
	Delegable
	NotDelegable
)

// Privilege is a bitfield of ledger operations a transaction type may perform.
// Invariant checks consult it instead of listing transaction types.
type Privilege uint16

const (
	PrivCreateAcct         Privilege = 0x0001 // may create an AccountRoot
	PrivCreatePseudoAcct   Privilege = 0x0002 // may create a pseudo-account
	PrivMustDeleteAcct     Privilege = 0x0004 // must delete an AccountRoot
	PrivMayDeleteAcct      Privilege = 0x0008 // may delete an AccountRoot
	PrivOverrideFreeze     Privilege = 0x0010 // may override some freeze rules
	PrivChangeNFTCounts    Privilege = 0x0020 // may mint or burn an NFT
	PrivCreateMPTIssuance  Privilege = 0x0040 // may create an MPT issuance
	PrivDestroyMPTIssuance Privilege = 0x0080 // may destroy an MPT issuance
	PrivMustAuthorizeMPT   Privilege = 0x0100 // must create or delete an MPToken (except by issuer)
	PrivMayAuthorizeMPT    Privilege = 0x0200 // may create or delete an MPToken (except by issuer)
	PrivMayDeleteMPT       Privilege = 0x0400 // may delete, not create, an MPToken
	PrivMustModifyVault    Privilege = 0x0800 // must create, modify or delete a vault
	PrivMayModifyVault     Privilege = 0x1000 // may create, modify or delete a vault
	PrivMayCreateMPT       Privilege = 0x2000 // may create, not delete, an MPToken
)

// ConsequencesKind declares how the transaction queue derives a type's
// consequences. It has no usable zero value, so every descriptor must choose.
type ConsequencesKind uint8

const (
	consequencesUnset ConsequencesKind = iota
	// NormalConsequences: no XRP spend beyond the fee, one sequence, not a blocker.
	NormalConsequences
	// BlockerConsequences: the transaction may invalidate later queued
	// transactions from the same account, e.g. by changing its signing keys.
	BlockerConsequences
	// CustomConsequences: the type implements ConsequencesCalculator.
	CustomConsequences
)

// Consequences is the effect of a transaction that the transaction queue
// accounts for beyond its fee.
type Consequences struct {
	Blocker bool
	// PotentialSpend is the XRP, in drops, the transaction may move besides the fee.
	PotentialSpend uint64
	// SequencesConsumed is the number of account sequences used; zero means one.
	SequencesConsumed uint32
}

// ConsequencesCalculator is implemented by every type declared CustomConsequences.
type ConsequencesCalculator interface {
	Consequences() Consequences
}

// Descriptor is the single declaration of a transaction type's static
// protocol attributes. Every consumer that varies by transaction type reads it
// through the registry instead of keeping its own table.
type Descriptor struct {
	Type         Type
	New          func() Transaction
	Delegation   Delegation
	Privileges   Privilege
	Consequences ConsequencesKind
	// DisallowedInBatch rejects the type as a Batch inner transaction.
	DisallowedInBatch bool
	// ReserveSponsorable permits spfSponsorReserve on the type.
	ReserveSponsorable bool
}

func (d Descriptor) validate() error {
	if t, ok := TypeFromName(d.Type.String()); !ok || t != d.Type {
		return errors.New("type has no canonical name")
	}
	if d.New == nil {
		return errors.New("missing constructor")
	}
	instance := d.New()
	if got := instance.TxType(); got != d.Type {
		return fmt.Errorf("constructor builds %s", got)
	}
	if d.Delegation == delegationUnset {
		return errors.New("delegation not declared")
	}
	if _, ok := txTemplates[d.Type]; !ok {
		return errors.New("no field template")
	}
	if d.Consequences == consequencesUnset {
		return errors.New("consequences not declared")
	}
	if _, custom := instance.(ConsequencesCalculator); custom != (d.Consequences == CustomConsequences) {
		return errors.New("CustomConsequences must be declared exactly when the type implements ConsequencesCalculator")
	}
	return nil
}
