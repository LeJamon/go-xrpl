package shamap

import (
	"errors"
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

// DataSize returns the serialized payload size without copying it.
func (item *Item) DataSize() int {
	return len(item.data)
}

func (item *Item) dataBytes() []byte {
	return item.data
}

func (item *Item) clone() (*Item, error) {
	if item == nil {
		return nil, errors.New("cannot clone nil item")
	}

	dataCopy := make([]byte, len(item.data))
	copy(dataCopy, item.data)
	return &Item{key: item.key, data: dataCopy}, nil
}
