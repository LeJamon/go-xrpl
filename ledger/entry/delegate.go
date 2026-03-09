package entry

import (
	"errors"

)

// Permission represents a single delegated permission
type Permission struct {
	PermissionType string // Type of permission (e.g., "Payment", "TrustSet")
}

// Delegate represents a delegation of permissions ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltDELEGATE
type Delegate struct {
	BaseEntry

	// Required fields
	Account     [20]byte     // Account that granted the delegation
	Authorize   [20]byte     // Account that received the delegation
	Permissions []Permission // List of delegated permissions
	OwnerNode   uint64       // Directory node hint
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
	if len(d.Permissions) == 0 {
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
