package state

import (
	"bytes"
	"slices"
)

// OrderedLedgerKeys indexes transaction-local keys. Reads are logarithmic;
// updates move only the small local overlay, never the underlying ledger.
// The zero value is ready to use. Like its owning view it is not concurrent.
type OrderedLedgerKeys [][32]byte

func compareLedgerKeys(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) }

func (keys *OrderedLedgerKeys) Add(key [32]byte) {
	i, found := slices.BinarySearchFunc(*keys, key, compareLedgerKeys)
	if !found {
		*keys = slices.Insert(*keys, i, key)
	}
}

func (keys *OrderedLedgerKeys) Remove(key [32]byte) {
	i, found := slices.BinarySearchFunc(*keys, key, compareLedgerKeys)
	if found {
		*keys = slices.Delete(*keys, i, i+1)
	}
}

func (keys OrderedLedgerKeys) Successor(key [32]byte) ([32]byte, bool) {
	i, found := slices.BinarySearchFunc(keys, key, compareLedgerKeys)
	if found {
		i++
	}
	if i == len(keys) {
		return [32]byte{}, false
	}
	return keys[i], true
}

// SortedLedgerKeys matches the ascending uint256 order used by rippled's
// flat_set. Mutation order matters for deleted directory-page metadata.
func SortedLedgerKeys[V any](entries map[[32]byte]V) [][32]byte {
	keys := make([][32]byte, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, compareLedgerKeys)
	return keys
}
