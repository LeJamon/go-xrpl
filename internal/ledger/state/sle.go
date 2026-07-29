package state

import (
	"encoding/binary"
	"errors"

	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

// GetOwnerNode extracts the OwnerNode (UInt64 type=3, field=4) from raw
// binary SLE data. Returns 0 if the field is absent or the data is malformed.
// Used by DirRemove callers to find the right directory page when erasing a
// ledger entry.
func GetOwnerNode(data []byte) uint64 {
	var ownerNode uint64
	errFound := errors.New("found")
	err := WalkFields(data, func(f Field) error {
		if f.TypeCode == stUInt64 && f.FieldCode == 4 {
			ownerNode = binary.BigEndian.Uint64(f.Value)
			return errFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, errFound) {
		return 0
	}
	return ownerNode
}

// DecodeType extracts and validates the typed LedgerEntryType header.
func DecodeType(data []byte) (entry.Type, error) {
	if len(data) < 3 {
		return 0, errors.New("data too short to contain LedgerEntryType")
	}
	if data[0] != 0x11 {
		return 0, errors.New("unexpected header byte, expected 0x11 for LedgerEntryType")
	}
	typ := entry.Type(binary.BigEndian.Uint16(data[1:3]))
	if _, ok := protocol.LedgerEntryTypeByCode(typ); !ok {
		return 0, errors.New("unknown LedgerEntryType")
	}
	return typ, nil
}

// GetLedgerEntryType extracts the raw LedgerEntryType code.
//
// Deprecated: use DecodeType.
func GetLedgerEntryType(data []byte) (uint16, error) {
	typ, err := DecodeType(data)
	return uint16(typ), err
}

// MatchesKeyletType reports whether data satisfies a keylet's type constraint.
// TypeAny imposes no serialization constraint.
func MatchesKeyletType(k keylet.Keylet, data []byte) bool {
	switch k.Type {
	case entry.TypeAny:
		return true
	}
	entryType, err := DecodeType(data)
	if err != nil {
		return false
	}
	if k.Type == entry.TypeChild {
		return entryType != entry.TypeDirectoryNode
	}
	return entryType == k.Type
}
