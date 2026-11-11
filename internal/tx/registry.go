package tx

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// ErrUnknownTransactionType is returned when a transaction type is unknown
var ErrUnknownTransactionType = errors.New("unknown transaction type")

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

// Descriptor is the single declaration of a transaction type's static
// protocol attributes. Every consumer that varies by transaction type reads it
// through the registry instead of keeping its own table.
type Descriptor struct {
	Type       Type
	New        func() Transaction
	Delegation Delegation
	Privileges Privilege
	// DisallowedInBatch rejects the type as a Batch inner transaction.
	DisallowedInBatch bool
	// ReserveSponsorable permits spfSponsorReserve on the type.
	ReserveSponsorable bool
}

var (
	registryMu sync.RWMutex
	registry   = make(map[Type]Descriptor)
)

// Register records a transaction type's descriptor. It panics on a duplicate
// type or an incomplete or inconsistent descriptor, so a forgotten attribute
// fails at startup instead of silently taking a default.
func Register(d Descriptor) {
	if err := d.validate(); err != nil {
		panic(fmt.Sprintf("tx: invalid descriptor for transaction type %d (%s): %v", d.Type, d.Type.String(), err))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[d.Type]; exists {
		panic(fmt.Sprintf("tx: transaction type %d (%s) already registered", d.Type, d.Type.String()))
	}
	registry[d.Type] = d
}

func (d Descriptor) validate() error {
	if t, ok := TypeFromName(d.Type.String()); !ok || t != d.Type {
		return errors.New("type has no canonical name")
	}
	if d.New == nil {
		return errors.New("missing constructor")
	}
	if got := d.New().TxType(); got != d.Type {
		return fmt.Errorf("constructor builds %s", got)
	}
	if d.Delegation == delegationUnset {
		return errors.New("delegation not declared")
	}
	return nil
}

// Lookup returns the descriptor registered for t.
func Lookup(t Type) (Descriptor, bool) {
	registryMu.RLock()
	d, ok := registry[t]
	registryMu.RUnlock()
	return d, ok
}

// PrivilegesOf returns the privileges declared for txType; an unregistered
// type has none.
func PrivilegesOf(txType Type) Privilege {
	d, _ := Lookup(txType)
	return d.Privileges
}

// NewFromType creates a new transaction of the given type using the registered factory.
func NewFromType(txType Type) (Transaction, error) {
	d, ok := Lookup(txType)
	if !ok {
		return nil, ErrUnknownTransactionType
	}
	return d.New(), nil
}

// FromJSON creates a Transaction from a JSON object
func FromJSON(data []byte) (Transaction, error) {
	var raw struct {
		TransactionType string `json:"TransactionType"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	txType, ok := TypeFromName(raw.TransactionType)
	if !ok {
		return nil, ErrUnknownTransactionType
	}
	if txType == TypeBatch {
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, err
		}
		if reason := batchMapConstructionChecksFailureReason(fields); reason != "" {
			return nil, ter.Errorf(ter.TemMALFORMED, "%s", reason)
		}
	}
	presentFields, err := jsonPresentFields(data)
	if err != nil {
		return nil, err
	}
	if txType == TypeClawback {
		if err := checkTemplate(txType, presentFields); err != nil {
			return nil, err
		}
	}

	tx, err := NewFromType(txType)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, tx); err != nil {
		return nil, err
	}
	tx.GetCommon().SetPresentFields(presentFields)

	return tx, nil
}

func jsonPresentFields(data []byte) (map[string]bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	fields := make(map[string]bool, len(raw))
	for name := range raw {
		fields[name] = true
	}
	return fields, nil
}

// ToJSON converts a Transaction to JSON
func ToJSON(tx Transaction) ([]byte, error) {
	flat, err := tx.Flatten()
	if err != nil {
		return nil, err
	}
	return json.Marshal(flat)
}

// SupportedTypes returns all registered transaction types.
func SupportedTypes() []Type {
	registryMu.RLock()
	defer registryMu.RUnlock()
	types := make([]Type, 0, len(registry))
	for t := range registry {
		types = append(types, t)
	}
	return types
}
