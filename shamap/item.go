package shamap

import (
	"bytes"
	"errors"
	"fmt"
)

var (
	ErrInvalidRefCount = errors.New("invalid reference count")
	ErrZeroRefCount    = errors.New("reference count is already zero")
)

// Item represents a leaf-level item stored in the SHAMap
type Item struct {
	key  [32]byte
	data []byte
}

// NewItem creates a new SHAMapItem with the given key and data. The data
// slice is copied so subsequent caller mutations cannot affect the item.
func NewItem(key [32]byte, data []byte) *Item {
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	return &Item{
		key:  key,
		data: dataCopy,
	}
}

// Key returns the key of the item
func (item *Item) Key() [32]byte {
	return item.key
}

// Data returns a copy of the data to prevent external modifications
func (item *Item) Data() []byte {
	result := make([]byte, len(item.data))
	copy(result, item.data)
	return result
}

// DataUnsafe returns the internal data slice without copying
// Use with caution - caller must not modify the returned slice
func (item *Item) DataUnsafe() []byte {
	return item.data
}

// Size returns the size of the data
func (item *Item) Size() int {
	return len(item.data)
}

// Clone creates a deep copy of the item
func (item *Item) Clone() (*Item, error) {
	if item == nil {
		return nil, errors.New("cannot clone nil item")
	}

	dataCopy := make([]byte, len(item.data))
	copy(dataCopy, item.data)
	return &Item{key: item.key, data: dataCopy}, nil
}

// String returns a string representation of the item (useful for debugging)
func (item *Item) String() string {
	if item == nil {
		return "Item(nil)"
	}
	return fmt.Sprintf("Item(key=%x, size=%d)", item.key[:4], len(item.data))
}

// Equal returns true if two items have the same key and data
func (item *Item) Equal(other *Item) bool {
	if item == nil || other == nil {
		return item == other
	}

	if item.key != other.key {
		return false
	}

	return bytes.Equal(item.data, other.data)
}

// IsEmpty returns true if the item has no data
func (item *Item) IsEmpty() bool {
	return item == nil || len(item.data) == 0
}

// Validate performs basic validation on the item
func (item *Item) Validate() error {
	if item == nil {
		return errors.New("item is nil")
	}

	// Check for zero key (might be invalid depending on use case)
	zeroKey := [32]byte{}
	if item.key == zeroKey {
		return errors.New("item has zero key")
	}

	// Items with no data might be valid in some contexts
	if len(item.data) == 0 {
		return errors.New("item has no data")
	}

	return nil
}
