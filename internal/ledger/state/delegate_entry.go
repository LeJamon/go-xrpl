package state

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
)

// DelegateData holds parsed fields from a Delegate ledger entry.
// Reference: rippled ledger_entries.macro ltDELEGATE
type DelegateData struct {
	Account     [20]byte // Account that granted the delegation
	Authorize   [20]byte // Account that received the delegation
	OwnerNode   uint64
	Permissions []uint32 // Permission values (txType+1 or granular permission)
	// Round-trips so a no-op modify re-serializes byte-identically and the apply
	// layer's unchanged-entry guard prunes it (ApplyStateTable.cpp:154-157).
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// ParseDelegate parses a Delegate ledger entry from binary data.
// Extracts Account, Authorize, OwnerNode, and the Permissions array.
// Reference: rippled DelegateUtils.cpp — sfPermissions array with sfPermissionValue fields
func ParseDelegate(data []byte) (*DelegateData, error) {
	entry := &DelegateData{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			if f.FieldCode == 5 { // PreviousTxnLgrSeq
				entry.PreviousTxnLgrSeq = f.UInt32()
			}
		case stUInt64:
			if f.FieldCode == 4 { // OwnerNode
				entry.OwnerNode = f.UInt64()
			}
		case stHash256:
			if f.FieldCode == 5 { // PreviousTxnID
				entry.PreviousTxnID = f.Hash256()
			}
		case stAccountID:
			switch f.FieldCode {
			case 1: // Account
				if id, ok := f.AccountID(); ok {
					entry.Account = id
				}
			case 5: // Authorize
				if id, ok := f.AccountID(); ok {
					entry.Authorize = id
				}
			}
		case stArray:
			if f.FieldCode == 29 { // Permissions
				perms, err := parseDelegatePermissions(f.Value)
				if err != nil {
					return err
				}
				entry.Permissions = perms
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to decode Delegate: %w", err)
	}

	return entry, nil
}

// parseDelegatePermissions decodes the Permissions STArray content; each element
// is a Permission STObject carrying a UInt32 PermissionValue. Zero values are
// skipped.
func parseDelegatePermissions(content []byte) ([]uint32, error) {
	var perms []uint32
	err := WalkFields(content, func(elem Field) error {
		if elem.TypeCode != stObject || elem.FieldCode != 15 { // Permission
			return nil
		}
		return WalkFields(elem.Value, func(inner Field) error {
			if inner.TypeCode == stUInt32 && inner.FieldCode == 52 { // PermissionValue
				if v := inner.UInt32(); v > 0 {
					perms = append(perms, v)
				}
			}
			return nil
		})
	})
	return perms, err
}

// SerializeDelegate serializes a Delegate ledger entry. prevTxnID/prevTxnLgrSeq
// are the threading pointers carried over from an existing entry on the modify
// path; pass the zero hash on create (the apply layer stamps them afterward).
// Reference: rippled DelegateSet.cpp doApply()
func SerializeDelegate(account, authorize [20]byte, permissions []uint32, ownerNode uint64, prevTxnID [32]byte, prevTxnLgrSeq uint32) ([]byte, error) {
	accountAddr, err := addresscodec.EncodeAccountIDToClassicAddress(account[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode account address: %w", err)
	}
	authorizeAddr, err := addresscodec.EncodeAccountIDToClassicAddress(authorize[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode authorize address: %w", err)
	}

	// Build Permissions array
	permsArray := make([]map[string]any, len(permissions))
	for i, pv := range permissions {
		permsArray[i] = map[string]any{
			"Permission": map[string]any{
				"PermissionValue": pv,
			},
		}
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "Delegate",
		"Account":         accountAddr,
		"Authorize":       authorizeAddr,
		"Permissions":     permsArray,
		"OwnerNode":       fmt.Sprintf("%X", ownerNode),
		"Flags":           uint32(0),
	}

	// Emit only once threaded; a fresh entry's pointers are stamped by the apply layer.
	var emptyHash [32]byte
	if prevTxnID != emptyHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(prevTxnID[:]))
		jsonObj["PreviousTxnLgrSeq"] = prevTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Delegate: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// HasTxPermission checks if the Delegate SLE grants permission for the given
// transaction type. The permission value for a tx type is txType + 1.
// Reference: rippled DelegateUtils.cpp checkTxPermission()
func (d *DelegateData) HasTxPermission(txType uint32) bool {
	txPermission := txType + 1
	return slices.Contains(d.Permissions, txPermission)
}

// LookupPermissionValue converts a permission name (e.g., "Payment") to its
// numeric delegatable permission value using the definitions package.
// Returns 0 if the name is not found.
func LookupPermissionValue(name string) uint32 {
	pv, err := definitions.Get().GetDelegatablePermissionValueByName(name)
	if err != nil {
		return 0
	}
	return uint32(pv)
}
