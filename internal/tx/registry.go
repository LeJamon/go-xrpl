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
