package entry

import (
	"errors"
)

// DelegatePermission represents a single delegated permission.
// The PermissionValue encodes the permitted transaction type as txType + 1,
// or a granular permission value (> 65535).
// Reference: rippled DelegateUtils.cpp — permissionValue == tx.getTxnType() + 1
type DelegatePermission struct {
	PermissionValue uint32 // Numeric permission value
}

// Permission represents a single delegated permission (legacy name for compatibility)
type Permission struct {
	PermissionType string // Type of permission (e.g., "Payment", "TrustSet")
}

// Delegate represents a delegation of permissions ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltDELEGATE
type Delegate struct {
	BaseEntry

	// Required fields
	Account             [20]byte             // Account that granted the delegation
	Authorize           [20]byte             // Account that received the delegation
	Permissions         []Permission         // List of delegated permissions (legacy)
	DelegatePermissions []DelegatePermission // List of delegated permissions (numeric)
	OwnerNode           uint64               // Directory node hint
}

func (d *Delegate) Type() Type {
	return TypeDelegate
}

func (d *Delegate) Validate() error {
	if d.Account == [20]byte{} {
		return errors.New("account is required")
	}
	if d.Authorize == [20]byte{} {
		return errors.New("authorize is required")
	}
	if d.Account == d.Authorize {
		return errors.New("cannot delegate to self")
	}
	if len(d.DelegatePermissions) == 0 && len(d.Permissions) == 0 {
		return errors.New("at least one permission is required")
	}
	return nil
}

func (d *Delegate) Hash() ([32]byte, error) {
	hash := d.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= d.Account[i]
		hash[i] ^= d.Authorize[i]
	}
	return hash, nil
}

// HasTxPermission checks if this delegate SLE grants permission for the given
// transaction type. The permission value for a tx type is txType + 1.
// Reference: rippled DelegateUtils.cpp checkTxPermission()
func (d *Delegate) HasTxPermission(txType uint32) bool {
	txPermission := txType + 1
	for _, perm := range d.DelegatePermissions {
		if perm.PermissionValue == txPermission {
			return true
		}
	}
	return false
}
