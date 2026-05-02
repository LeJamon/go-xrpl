package delegate

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// permissionMaxSize is the maximum number of permissions allowed in a DelegateSet.
// Reference: rippled Protocol.h — std::size_t constexpr permissionMaxSize = 10
const permissionMaxSize = 10

// notDelegatableTxTypes maps transaction type values that are notDelegatable.
// Reference: rippled transactions.macro — TRANSACTION(tag, value, name, Delegation::notDelegatable, ...)
// The key is the tx type value (not permissionValue), matching rippled's delegatableTx_ map.
var notDelegatableTxTypes = map[uint16]bool{
	3:   true, // ttACCOUNT_SET
	5:   true, // ttREGULAR_KEY_SET
	12:  true, // ttSIGNER_LIST_SET
	21:  true, // ttACCOUNT_DELETE
	64:  true, // ttDELEGATE_SET
	71:  true, // ttBATCH
	100: true, // ttAMENDMENT (EnableAmendment)
	101: true, // ttFEE (SetFee)
	102: true, // ttUNL_MODIFY (UNLModify)
}

// granularPermissionMin is the threshold above which a permission value is granular.
// Granular permissions have values > UINT16_MAX (65535) and are always delegatable.
// Reference: rippled Permissions.h — GranularPermissionType values start at 65537
const granularPermissionMin = 65536

func init() {
	if err := tx.Register(tx.TypeDelegateSet, func() tx.Transaction {
		return &DelegateSet{BaseTx: *tx.NewBaseTx(tx.TypeDelegateSet, "")}
	}); err != nil {
		panic(err)
	}
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

func (d *DelegateSet) TxType() tx.Type {
	return tx.TypeDelegateSet
}

// Reference: rippled DelegateSet.cpp preflight()
func (d *DelegateSet) Validate() error {
	if err := d.BaseTx.Validate(); err != nil {
		return err
	}

	// Check permissions array size.
	// Reference: rippled DelegateSet.cpp preflight() — permissions.size() > permissionMaxSize
	if len(d.Permissions) > permissionMaxSize {
		return fmt.Errorf("temARRAY_TOO_LARGE: permissions array exceeds maximum size of %d", permissionMaxSize)
	}

	// Cannot authorize self.
	// Reference: rippled DelegateSet.cpp preflight() — ctx.tx[sfAccount] == ctx.tx[sfAuthorize]
	if d.Authorize != "" && d.GetCommon().Account == d.Authorize {
		return fmt.Errorf("temMALFORMED: cannot delegate to self")
	}

	// Check for duplicate permission values.
	// Reference: rippled DelegateSet.cpp preflight() — permissionSet.insert check
	seen := make(map[string]bool)
	for _, p := range d.Permissions {
		pv := p.Permission.PermissionValue
		if pv == "" {
			continue
		}
		if seen[pv] {
			return fmt.Errorf("temMALFORMED: duplicate permission value %q", pv)
		}
		seen[pv] = true
	}

	return nil
}

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

func (d *DelegateSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePermissionDelegation}
}

// Reference: rippled DelegateSet.cpp preclaim() + doApply()
func (d *DelegateSet) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("delegate set apply",
		"account", d.Account,
		"authorize", d.Authorize,
		"permissions", d.Permissions,
	)

	// Preclaim: verify authorize target exists
	// Reference: rippled DelegateSet.cpp preclaim()
	authorizeID, err := state.DecodeAccountID(d.Authorize)
	if err != nil {
		return tx.TecNO_TARGET
	}
	if exists, _ := ctx.View.Exists(keylet.Account(authorizeID)); !exists {
		return tx.TecNO_TARGET
	}

	// Preclaim: check that all permissions are delegatable.
	// Reference: rippled DelegateSet.cpp preclaim() — Permission::isDelegatable()
	permValues := d.permissionValues()
	for _, pv := range permValues {
		if !isDelegatable(pv) {
			return tx.TecNO_PERMISSION
		}
	}

	delegateKey := keylet.DelegateKeylet(ctx.AccountID, authorizeID)

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

	ownerDirKey := keylet.OwnerDir(account)
	state.DirRemove(ctx.View, ownerDirKey, existingEntry.OwnerNode, delegateKey.Key, false)

	// Erase the delegate entry
	if err := ctx.View.Erase(delegateKey); err != nil {
		ctx.Log.Error("delegate set: unable to delete delegate from owner")
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

// isDelegatable checks whether a permission value represents a delegatable permission.
// Granular permissions (values > UINT16_MAX) are always delegatable.
// Transaction-level permissions use permissionValue = txType + 1, and are delegatable
// unless the tx type is explicitly marked as notDelegatable.
// Reference: rippled Permissions.cpp isDelegatable()
func isDelegatable(permissionValue uint32) bool {
	// Granular permissions are always delegatable.
	if permissionValue >= granularPermissionMin {
		return true
	}

	// Transaction-level: txType = permissionValue - 1
	txType := uint16(permissionValue - 1)
	if notDelegatableTxTypes[txType] {
		return false
	}

	// Default: delegatable (permissive, matching rippled's behavior for unknown types)
	return true
}
