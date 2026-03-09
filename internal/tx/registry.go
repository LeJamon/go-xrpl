package tx

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// ErrUnknownTransactionType is returned when a transaction type is unknown
var ErrUnknownTransactionType = errors.New("unknown transaction type")

// registry holds all registered transaction type factories
var (
	registryMu sync.RWMutex
	registry   = make(map[Type]func() Transaction)
)

// Register registers a transaction type factory. Called from init() in each transaction type file.
// Panics if the type is already registered (indicates a bug).
func Register(t Type, factory func() Transaction) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[t]; exists {
		panic(fmt.Sprintf("tx: transaction type %d (%s) already registered", t, t.String()))
	}
	registry[t] = factory
}

// NewFromType creates a new transaction of the given type using the registered factory.
func NewFromType(txType Type) (Transaction, error) {
	registryMu.RLock()
	factory, ok := registry[txType]
	registryMu.RUnlock()
	if !ok {
		return nil, ErrUnknownTransactionType
	}
	return factory(), nil
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

	tx, err := NewFromType(txType)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, tx); err != nil {
		return nil, err
	}

	return tx, nil
}

// ToJSON converts a Transaction to JSON
func ToJSON(tx Transaction) ([]byte, error) {
	flat, err := tx.Flatten()
	if err != nil {
		return nil, err
	}
	return json.Marshal(flat)
}

// Validate validates a transaction and returns any errors
func Validate(tx Transaction) error {
	return tx.Validate()
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
