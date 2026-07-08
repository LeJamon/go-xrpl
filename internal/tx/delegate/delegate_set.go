package delegate

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
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
	65:  true, // ttVAULT_CREATE
	66:  true, // ttVAULT_SET
	67:  true, // ttVAULT_DELETE
	68:  true, // ttVAULT_DEPOSIT
	69:  true, // ttVAULT_WITHDRAW
	70:  true, // ttVAULT_CLAWBACK
	71:  true, // ttBATCH
	100: true, // ttAMENDMENT (EnableAmendment)
	101: true, // ttFEE (SetFee)
	102: true, // ttUNL_MODIFY (UNLModify)
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

// GetFlagsMask adopts the engine FlagsMasker seam. DelegateSet defines no
// type-specific flags (rippled does not override getFlagsMask), so the base
// universal mask is checked at preflight0 — before the account/fee/NetworkID
// checks — matching rippled's Transactor::getFlagsMask default.
func (d *DelegateSet) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// Reference: rippled DelegateSet.cpp preflight()
func (d *DelegateSet) Validate() error {
	if err := d.BaseTx.Validate(); err != nil {
		return err
	}

	// Check permissions array size.
	// Reference: rippled DelegateSet.cpp preflight() — permissions.size() > permissionMaxSize
	if len(d.Permissions) > permissionMaxSize {
		return ter.Errorf(ter.TemARRAY_TOO_LARGE, "permissions array exceeds maximum size of %d", permissionMaxSize)
	}

	// Cannot authorize self.
	// Reference: rippled DelegateSet.cpp preflight() — ctx.tx[sfAccount] == ctx.tx[sfAuthorize]
	if d.Authorize != "" && d.GetCommon().Account == d.Authorize {
		return ter.Errorf(ter.TemMALFORMED, "cannot delegate to self")
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
			return ter.Errorf(ter.TemMALFORMED, "duplicate permission value %q", pv)
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
	return [][32]byte{amendment.FeaturePermissionDelegationV1_1}
}

// PreflightRules rejects any requested permission that is not delegatable with
// temMALFORMED. The check is unconditional: the whole transaction is gated by
// PermissionDelegationV1_1, which folds in the former fixDelegateV1_1 rule.
// Reference: rippled DelegateSet.cpp preflight().
func (d *DelegateSet) PreflightRules(rules *amendment.Rules) error {
	for _, p := range d.Permissions {
		pv := state.LookupPermissionValue(p.Permission.PermissionValue)
		if !isDelegatable(pv, rules) {
			return ter.Errorf(ter.TemMALFORMED, "permission %q is not delegatable", p.Permission.PermissionValue)
		}
	}
	return nil
}

// Reference: rippled DelegateSet.cpp preclaim() + doApply()
func (d *DelegateSet) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("delegate set apply",
		"account", d.Account,
		"authorize", d.Authorize,
		"permissions", d.Permissions,
	)

	// Preclaim: verify authorize target exists
	// Reference: rippled DelegateSet.cpp preclaim()
	authorizeID, err := state.DecodeAccountID(d.Authorize)
	if err != nil {
		return ter.TecNO_TARGET
	}
	if exists, _ := ctx.View.Exists(keylet.Account(authorizeID)); !exists {
		return ter.TecNO_TARGET
	}

	permValues := d.permissionValues()
	delegateKey := keylet.Delegate(ctx.AccountID, authorizeID)

	existingData, readErr := ctx.View.Read(delegateKey)
	delegateExists := readErr == nil && existingData != nil

	// Preclaim: deleting a delegate object that does not exist is invalid.
	// Reference: rippled DelegateSet.cpp preclaim().
	if len(permValues) == 0 && !delegateExists {
		return ter.TecNO_ENTRY
	}

	if delegateExists {
		// Empty permissions -- delete the delegate entry.
		if len(permValues) == 0 {
			return deleteDelegate(ctx, delegateKey, ctx.AccountID)
		}

		// Update the existing delegate with new permissions, preserving the
		// OwnerNode, DestinationNode, and threading pointers.
		existingEntry, parseErr := state.ParseDelegate(existingData)
		if parseErr != nil {
			return ter.TefINTERNAL
		}
		var destNode *uint64
		if existingEntry.HasDestinationNode {
			dn := existingEntry.DestinationNode
			destNode = &dn
		}
		newData, serErr := state.SerializeDelegate(ctx.AccountID, authorizeID, permValues, existingEntry.OwnerNode, destNode, existingEntry.PreviousTxnID, existingEntry.PreviousTxnLgrSeq)
		if serErr != nil {
			return ter.TefINTERNAL
		}
		if err := ctx.View.Update(delegateKey, newData); err != nil {
			return ter.TefINTERNAL
		}
		return ter.TesSUCCESS
	}

	// Delegate SLE does not exist -- create a new one (permValues is non-empty,
	// guaranteed by the empty-list preclaim check above).
	//
	// Check reserve against the prior balance (before the actual fee was
	// deducted), allowing the account to dip into the reserve to pay fees.
	if result := ctx.CheckReserveWithFee(ctx.Account.OwnerCount + 1); result != ter.TesSUCCESS {
		return result
	}

	// Add to the delegating account's owner directory (OwnerNode).
	ownerDir, dirErr := state.DirInsert(ctx.View, keylet.OwnerDir(ctx.AccountID), delegateKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if dirErr != nil {
		return ter.TecDIR_FULL
	}

	// Add to the authorized account's owner directory (DestinationNode) so the
	// entry is found and cleaned up when either account is deleted.
	authDir, authErr := state.DirInsert(ctx.View, keylet.OwnerDir(authorizeID), delegateKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = authorizeID
	})
	if authErr != nil {
		return ter.TecDIR_FULL
	}
	destNode := authDir.Page

	delegateData, serErr := state.SerializeDelegate(ctx.AccountID, authorizeID, permValues, ownerDir.Page, &destNode, [32]byte{}, 0)
	if serErr != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Insert(delegateKey, delegateData); err != nil {
		return ter.TefINTERNAL
	}

	// Only the delegating account's owner count is incremented on creation.
	ctx.Account.OwnerCount++
	return ter.TesSUCCESS
}

// deleteDelegate removes an existing delegate entry from the ledger.
// Reference: rippled DelegateSet.cpp deleteDelegate()
func deleteDelegate(ctx *tx.ApplyContext, delegateKey keylet.Keylet, account [20]byte) ter.Result {
	// Read the existing entry to get OwnerNode
	existingData, err := ctx.View.Read(delegateKey)
	if err != nil || existingData == nil {
		return ter.TefINTERNAL
	}

	existingEntry, parseErr := state.ParseDelegate(existingData)
	if parseErr != nil {
		return ter.TefINTERNAL
	}

	// Remove from the delegating account's owner directory.
	state.DirRemove(ctx.View, keylet.OwnerDir(account), existingEntry.OwnerNode, delegateKey.Key, false)

	// Remove from the authorized account's owner directory, if linked there.
	if existingEntry.HasDestinationNode {
		state.DirRemove(ctx.View, keylet.OwnerDir(existingEntry.Authorize), existingEntry.DestinationNode, delegateKey.Key, false)
	}

	// Erase the delegate entry
	if err := ctx.View.Erase(delegateKey); err != nil {
		ctx.Log.Error("delegate set: unable to delete delegate from owner")
		return ter.TefINTERNAL
	}

	// Only the delegating account's owner count was incremented on creation.
	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return ter.TesSUCCESS
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

// isDelegatable reports whether a permission value may be delegated under the
// given amendment rules.
//
// Granular permissions are always delegatable. Otherwise the value must map to
// a registered transaction type whose introducing amendment (if any) is enabled
// and which is not explicitly non-delegatable.
// Reference: rippled Permissions.cpp Permission::isDelegable().
func isDelegatable(permissionValue uint32, rules *amendment.Rules) bool {
	// Granular permissions are always delegatable — but only KNOWN granular
	// permissions. rippled short-circuits on getGranularName(value) != nullopt,
	// so an unknown value in the granular range (>= 65536) is NOT treated as
	// granular; it falls through to the transaction-type path below, where it is
	// not a registered type and is therefore rejected.
	if state.IsGranularPermissionValue(permissionValue) {
		return true
	}

	txType, known := permissionTxType(permissionValue)
	if !known {
		return false
	}

	// Delegation is only allowed if the transaction type's introducing amendment
	// (if any) is enabled.
	if feature, gated := txIntroducingAmendment(txType); gated && (rules == nil || !rules.Enabled(feature)) {
		return false
	}

	if notDelegatableTxTypes[txType] {
		return false
	}
	return true
}

// permissionTxType maps a transaction-level permission value to its tx type
// (value - 1), reporting whether that type is registered.
func permissionTxType(permissionValue uint32) (uint16, bool) {
	if permissionValue == 0 {
		return 0, false
	}
	txType := uint16(permissionValue - 1)
	if _, err := tx.NewFromType(tx.Type(txType)); err != nil {
		return txType, false
	}
	return txType, true
}

// txIntroducingAmendment returns the amendment that gates a transaction type,
// if one exists. Types available without an amendment report gated=false.
func txIntroducingAmendment(txType uint16) (feature [32]byte, gated bool) {
	txn, err := tx.NewFromType(tx.Type(txType))
	if err != nil {
		return [32]byte{}, false
	}
	required := txn.RequiredAmendments()
	if len(required) == 0 {
		return [32]byte{}, false
	}
	return required[0], true
}
