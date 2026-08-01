package state

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// DelegateData holds parsed fields from a Delegate ledger entry.
// Reference: rippled ledger_entries.macro ltDELEGATE
type DelegateData struct {
	Account   [20]byte // Account that granted the delegation
	Authorize [20]byte // Account that received the delegation
	OwnerNode uint64
	// DestinationNode is the page of the entry in the authorized account's owner
	// directory. Optional: only present on entries created once the Delegate
	// object is linked into both accounts' directories.
	DestinationNode    uint64
	HasDestinationNode bool
	Permissions        []uint32 // Permission values (txType+1 or granular permission)
	// Round-trips so a no-op modify re-serializes byte-identically and the apply
	// layer's unchanged-entry guard prunes it (ApplyStateTable.cpp:154-157).
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// ParseDelegate parses a Delegate ledger entry from binary data.
// Extracts Account, Authorize, OwnerNode, and the Permissions array.
// Reference: rippled DelegateUtils.cpp — sfPermissions array with sfPermissionValue fields
func ParseDelegate(data []byte) (*DelegateData, error) {
	var decoded ledgerfields.Delegate
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode Delegate: %w", err)
	}
	fields := decoded.ToMap()
	entry := &DelegateData{
		HasDestinationNode: fields["DestinationNode"] != nil,
		PreviousTxnLgrSeq:  decoded.PreviousTxnLgrSeq,
	}

	var err error
	if _, ok := fields["Account"]; ok {
		entry.Account, err = decodeLedgerAccount("Delegate.Account", decoded.Account)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := fields["Authorize"]; ok {
		entry.Authorize, err = decodeLedgerAccount("Delegate.Authorize", decoded.Authorize)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := fields["OwnerNode"]; ok {
		entry.OwnerNode, err = parseLedgerUint64("Delegate.OwnerNode", decoded.OwnerNode)
		if err != nil {
			return nil, err
		}
	}
	if entry.HasDestinationNode {
		entry.DestinationNode, err = parseLedgerUint64("Delegate.DestinationNode", decoded.DestinationNode)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := fields["PreviousTxnID"]; ok {
		if err := decodeLedgerHex("Delegate.PreviousTxnID", decoded.PreviousTxnID, entry.PreviousTxnID[:]); err != nil {
			return nil, err
		}
	}
	entry.Permissions, err = decodeDelegatePermissions(decoded.Permissions)
	if err != nil {
		return nil, err
	}

	return entry, nil
}

func decodeDelegatePermissions(values []any) ([]uint32, error) {
	var perms []uint32
	for i, value := range values {
		wrapper, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Delegate.Permissions[%d]: expected object, got %T", i, value)
		}
		value, ok = wrapper["Permission"]
		if !ok {
			continue
		}
		permission, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Delegate.Permissions[%d].Permission: expected object, got %T", i, value)
		}
		value, ok = permission["PermissionValue"]
		if !ok {
			continue
		}
		var permissionValue uint32
		switch value := value.(type) {
		case string:
			permissionValue = LookupPermissionValue(value)
		case uint32:
			permissionValue = value
		default:
			return nil, fmt.Errorf("Delegate.Permissions[%d].Permission.PermissionValue: expected string, got %T", i, value)
		}
		if permissionValue > 0 {
			perms = append(perms, permissionValue)
		}
	}
	return perms, nil
}

// SerializeDelegate serializes a Delegate ledger entry. prevTxnID/prevTxnLgrSeq
// are the threading pointers carried over from an existing entry on the modify
// path; pass the zero hash on create (the apply layer stamps them afterward).
// destinationNode is the (optional) page in the authorized account's owner
// directory; pass nil to omit the field, matching its soeOPTIONAL status.
// Reference: rippled DelegateSet.cpp doApply()
func SerializeDelegate(account, authorize [20]byte, permissions []uint32, ownerNode uint64, destinationNode *uint64, prevTxnID [32]byte, prevTxnLgrSeq uint32) ([]byte, error) {
	accountAddr, err := addresscodec.EncodeAccountIDToClassicAddress(account[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode account address: %w", err)
	}
	authorizeAddr, err := addresscodec.EncodeAccountIDToClassicAddress(authorize[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode authorize address: %w", err)
	}

	// Build Permissions array
	permsArray := make([]any, len(permissions))
	for i, pv := range permissions {
		permsArray[i] = map[string]any{
			"Permission": map[string]any{
				"PermissionValue": pv,
			},
		}
	}

	entry := &ledgerfields.Delegate{}
	entry.SetAccount(accountAddr)
	entry.SetAuthorize(authorizeAddr)
	entry.SetPermissions(permsArray)
	entry.SetOwnerNode(fmt.Sprintf("%X", ownerNode))
	entry.SetFlags(0)

	if destinationNode != nil {
		entry.SetDestinationNode(fmt.Sprintf("%X", *destinationNode))
	}

	// Emit only once threaded; a fresh entry's pointers are stamped by the apply layer.
	var emptyHash [32]byte
	if prevTxnID != emptyHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(prevTxnID[:])))
		entry.SetPreviousTxnLgrSeq(prevTxnLgrSeq)
	}

	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode Delegate: %w", err)
	}
	return data, nil
}

// HasTxPermission checks if the Delegate SLE grants permission for the given
// transaction type. The permission value for a tx type is txType + 1.
// Reference: rippled DelegateUtils.cpp checkTxPermission()
func (d *DelegateData) HasTxPermission(txType uint32) bool {
	txPermission := txType + 1
	return slices.Contains(d.Permissions, txPermission)
}

// LookupPermissionValue converts a permission value to its numeric form. It
// accepts a permission name (e.g. "Payment") and, for values that have no
// registered name, a plain decimal string. rippled's sfPermissionValue is a
// plain UINT32, so a wire value with no known name decodes to its decimal form
// (see the codec's enumToStr); accepting it here lets those values round-trip
// and reach the delegatability check. Returns 0 when neither form resolves.
func LookupPermissionValue(name string) uint32 {
	if pv, err := definitions.Get().DelegatablePermissionValue(name); err == nil {
		return uint32(pv)
	}
	if n, err := strconv.ParseUint(name, 10, 32); err == nil {
		return uint32(n)
	}
	return 0
}

// IsGranularPermissionValue reports whether the numeric permission value is a
// registered granular permission (rippled getGranularName != nullopt). Unknown
// values in the granular range are not granular and must fall through to the
// transaction-type delegatability path.
func IsGranularPermissionValue(value uint32) bool {
	return definitions.Get().IsGranularPermission(int32(value))
}
