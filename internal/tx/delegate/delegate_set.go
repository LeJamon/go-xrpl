package delegate

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	tx.Register(tx.TypeDelegateSet, func() tx.Transaction {
		return &DelegateSet{BaseTx: *tx.NewBaseTx(tx.TypeDelegateSet, "")}
	})
}

// DelegateSet sets up delegation for an account.
// Reference: rippled DelegateSet.cpp
type DelegateSet struct {
	tx.BaseTx

	// Authorize is the account to delegate to (required)
	Authorize string `json:"Authorize,omitempty" xrpl:"Authorize,omitempty"`

	// Permissions defines what the delegate can do.
	// Each permission has a PermissionValue which is a string name (e.g., "Payment")
	// that gets converted to a numeric value during Flatten/Apply.
	Permissions []Permission `json:"Permissions,omitempty" xrpl:"Permissions,omitempty"`
}

// Permission defines a permission grant wrapper.
// Matches rippled's sfPermission OBJECT wrapper.
type Permission struct {
	Permission PermissionData `json:"Permission"`
}

// PermissionData contains permission details.
// PermissionValue is the string name of the delegatable permission (e.g., "Payment").
type PermissionData struct {
	PermissionValue string `json:"PermissionValue,omitempty"`
}

// NewDelegateSet creates a new DelegateSet transaction
func NewDelegateSet(account string) *DelegateSet {
	return &DelegateSet{
		BaseTx: *tx.NewBaseTx(tx.TypeDelegateSet, account),
	}
}

// TxType returns the transaction type
func (d *DelegateSet) TxType() tx.Type {
	return tx.TypeDelegateSet
}

// Validate validates the DelegateSet transaction
func (d *DelegateSet) Validate() error {
	return d.BaseTx.Validate()
}

// Flatten returns a flat map of all transaction fields.
// Custom implementation to properly format Permissions as:
//
//	[{"Permission": {"PermissionValue": <uint32>}}, ...]
//
// Reference: rippled sfPermissions array with sfPermissionValue (UINT32, field 52)
func (d *DelegateSet) Flatten() (map[string]any, error) {
	m := d.BaseTx.GetCommon().ToMap()

	if d.Authorize != "" {
		m["Authorize"] = d.Authorize
	}

	if len(d.Permissions) > 0 {
		permsArray := make([]map[string]any, len(d.Permissions))
		for i, p := range d.Permissions {
			// Convert the string permission name to its numeric value
			pv := state.LookupPermissionValue(p.Permission.PermissionValue)
			permsArray[i] = map[string]any{
				"Permission": map[string]any{
					"PermissionValue": pv,
				},
			}
		}
		m["Permissions"] = permsArray
	}

	return m, nil
}

// RequiredAmendments returns the amendments required for this transaction type
func (d *DelegateSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePermissionDelegation}
}

// Apply applies the DelegateSet transaction to the ledger.
// Reference: rippled DelegateSet.cpp doApply()
func (d *DelegateSet) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("delegate set apply",
		"account", d.Account,
		"authorize", d.Authorize,
		"permissions", d.Permissions,
	)

	// Preclaim: verify authorize target exists
	authorizeID, err := state.DecodeAccountID(d.Authorize)
	if err != nil {
		return tx.TecNO_TARGET
	}
	if exists, _ := ctx.View.Exists(keylet.Account(authorizeID)); !exists {
		return tx.TecNO_TARGET
	}

	delegateKey := keylet.DelegateKeylet(ctx.AccountID, authorizeID)

	// Convert permissions to uint32 values
	permValues := d.permissionValues()

	// Check if delegate SLE already exists
	existingData, readErr := ctx.View.Read(delegateKey)
	if readErr == nil && existingData != nil {
		// Delegate SLE exists -- update or delete
		if len(permValues) == 0 {
			// Empty permissions -- delete the delegate entry
			return deleteDelegate(ctx, delegateKey, ctx.AccountID)
		}

		// Update the existing delegate with new permissions
		newData, serErr := state.SerializeDelegate(ctx.AccountID, authorizeID, permValues, 0)
		if serErr != nil {
			return tx.TefINTERNAL
		}

		// Preserve the existing OwnerNode by parsing old entry and re-serializing
		existingEntry, parseErr := state.ParseDelegate(existingData)
		if parseErr == nil {
			newData, serErr = state.SerializeDelegate(ctx.AccountID, authorizeID, permValues, existingEntry.OwnerNode)
			if serErr != nil {
				return tx.TefINTERNAL
			}
		}

		if err := ctx.View.Update(delegateKey, newData); err != nil {
			return tx.TefINTERNAL
		}
		return tx.TesSUCCESS
	}

	// Delegate SLE does not exist -- create new one
	if len(permValues) == 0 {
		// Nothing to create
		return tx.TesSUCCESS
	}

	// Check reserve
	// Reference: rippled DelegateSet.cpp -- mPriorBalance < accountReserve(ownerCount + 1)
	priorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if priorBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// Create the delegate SLE
	delegateData, serErr := state.SerializeDelegate(ctx.AccountID, authorizeID, permValues, 0)
	if serErr != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Insert(delegateKey, delegateData); err != nil {
		return tx.TefINTERNAL
	}

	// Insert into owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	dirResult, dirErr := state.DirInsert(ctx.View, ownerDirKey, delegateKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if dirErr != nil {
		return tx.TecDIR_FULL
	}

	// Update OwnerNode on the delegate entry if page != 0
	if dirResult.Page != 0 {
		newData, serErr := state.SerializeDelegate(ctx.AccountID, authorizeID, permValues, dirResult.Page)
		if serErr != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(delegateKey, newData); err != nil {
			return tx.TefINTERNAL
		}
	}

	ctx.Account.OwnerCount++
	return tx.TesSUCCESS
}

// deleteDelegate removes an existing delegate entry from the ledger.
// Reference: rippled DelegateSet.cpp deleteDelegate()
func deleteDelegate(ctx *tx.ApplyContext, delegateKey keylet.Keylet, account [20]byte) tx.Result {
	// Read the existing entry to get OwnerNode
	existingData, err := ctx.View.Read(delegateKey)
	if err != nil || existingData == nil {
		return tx.TefINTERNAL
	}

	existingEntry, parseErr := state.ParseDelegate(existingData)
	if parseErr != nil {
		return tx.TefINTERNAL
	}

	// Remove from owner directory
	ownerDirKey := keylet.OwnerDir(account)
	state.DirRemove(ctx.View, ownerDirKey, existingEntry.OwnerNode, delegateKey.Key, false)

	// Erase the delegate entry
	if err := ctx.View.Erase(delegateKey); err != nil {
		return tx.TefINTERNAL
	}

	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return tx.TesSUCCESS
}

// permissionValues extracts the uint32 permission values from the transaction's
// Permissions field. Uses the definitions package to convert permission names.
func (d *DelegateSet) permissionValues() []uint32 {
	var values []uint32
	for _, p := range d.Permissions {
		// The PermissionValue field holds the string name (e.g. "Payment")
		// which maps to txType + 1 via the definitions.
		if p.Permission.PermissionValue != "" {
			pv := state.LookupPermissionValue(p.Permission.PermissionValue)
			if pv > 0 {
				values = append(values, pv)
			}
		}
	}
	return values
}
