package state

import (
	"fmt"
	"slices"
	"strconv"

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
	Sponsor            string
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
	entry := &DelegateData{
		HasDestinationNode: decoded.HasDestinationNode(),
		Sponsor:            decoded.Sponsor,
	}

	var err error
	if decoded.HasAccount() {
		entry.Account, err = decoded.GetAccount()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasAuthorize() {
		entry.Authorize, err = decoded.GetAuthorize()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasOwnerNode() {
		entry.OwnerNode, err = decoded.GetOwnerNode()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasDestinationNode {
		entry.DestinationNode, err = decoded.GetDestinationNode()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasPreviousTxnID() {
		entry.PreviousTxnID, err = decoded.GetPreviousTxnID()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasPreviousTxnLgrSeq() {
		entry.PreviousTxnLgrSeq, err = decoded.GetPreviousTxnLgrSeq()
		if err != nil {
			return nil, err
		}
	}
	permissions, err := decoded.GetPermissions()
	if err != nil {
		return nil, err
	}
	entry.Permissions = make([]uint32, 0, len(permissions))
	for i, permission := range permissions {
		permissionValue, err := permission.GetPermissionValue()
		if err != nil {
			return nil, fmt.Errorf("Delegate.Permissions[%d].PermissionValue: %w", i, err)
		}
		entry.Permissions = append(entry.Permissions, permissionValue)
	}

	return entry, nil
}

// SerializeDelegate serializes a Delegate ledger entry. prevTxnID/prevTxnLgrSeq
// are the threading pointers carried over from an existing entry on the modify
// path; pass the zero hash on create (the apply layer stamps them afterward).
// destinationNode is the (optional) page in the authorized account's owner
// directory; pass nil to omit the field, matching its soeOPTIONAL status.
// Reference: rippled DelegateSet.cpp doApply()
func SerializeDelegate(account, authorize [20]byte, permissions []uint32, ownerNode uint64, destinationNode *uint64, sponsor string, prevTxnID [32]byte, prevTxnLgrSeq uint32) ([]byte, error) {
	permissionValues := make([]ledgerfields.PermissionValue, len(permissions))
	for i, value := range permissions {
		permissionValues[i].SetPermissionValue(value)
	}

	entry := &ledgerfields.Delegate{}
	if err := entry.SetAccountValue(account); err != nil {
		return nil, fmt.Errorf("failed to encode Delegate.Account: %w", err)
	}
	if err := entry.SetAuthorizeValue(authorize); err != nil {
		return nil, fmt.Errorf("failed to encode Delegate.Authorize: %w", err)
	}
	if err := entry.SetPermissionsValue(permissionValues); err != nil {
		return nil, fmt.Errorf("failed to encode Delegate.Permissions: %w", err)
	}
	entry.SetOwnerNodeValue(ownerNode)
	entry.SetFlagsValue(0)
	if sponsor != "" {
		entry.SetSponsor(sponsor)
	}

	if destinationNode != nil {
		entry.SetDestinationNodeValue(*destinationNode)
	}

	entry.SetPreviousTxnIDValue(prevTxnID)
	entry.SetPreviousTxnLgrSeqValue(prevTxnLgrSeq)

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
