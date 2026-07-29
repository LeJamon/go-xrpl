package check

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// CheckCancel cancels a Check.
type CheckCancel struct {
	tx.BaseTx

	// CheckID is the ID of the check to cancel (required)
	CheckID string `json:"CheckID" xrpl:"CheckID"`
}

// NewCheckCancel creates a new CheckCancel transaction
func NewCheckCancel(account, checkID string) *CheckCancel {
	return &CheckCancel{
		BaseTx:  *tx.NewBaseTx(tx.TypeCheckCancel, account),
		CheckID: checkID,
	}
}

func (c *CheckCancel) TxType() tx.Type {
	return tx.TypeCheckCancel
}

// GetFlagsMask adopts the engine FlagsMasker seam. CancelCheck defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (c *CheckCancel) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// Validate implements preflight validation matching rippled's CancelCheck::preflight().
func (c *CheckCancel) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	if c.CheckID == "" {
		return ter.Errorf(ter.TemMALFORMED, "CheckID is required")
	}

	return nil
}

func (c *CheckCancel) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

func (c *CheckCancel) RequiredAmendments() [][32]byte {
	return nil
}

// Preclaim runs CheckCancel's ledger-aware checks: the check must exist
// (tecNO_ENTRY) and, while it is not yet expired, only its creator or destination
// may cancel it (tecNO_PERMISSION). Extracting these from Apply makes them visible
// to the preclaim-only paths (TxQ admission, simulate), matching rippled where
// they live in CancelCheck::preclaim.
// Reference: rippled CheckCancel.cpp preclaim().
func (c *CheckCancel) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	checkID, decErr := hex.DecodeString(c.CheckID)
	if decErr != nil || len(checkID) != 32 {
		return ter.TemINVALID
	}
	var checkKeyBytes [32]byte
	copy(checkKeyBytes[:], checkID)
	checkKey := keylet.Keylet{Key: checkKeyBytes}

	checkData, readErr := view.Read(checkKey)
	if readErr != nil || checkData == nil {
		return ter.TecNO_ENTRY
	}
	checkType, err := state.DecodeType(checkData)
	if err != nil || checkType != entry.TypeCheck {
		return ter.TecNO_ENTRY
	}
	check, parseErr := state.ParseCheck(checkData)
	if parseErr != nil {
		return ter.TefINTERNAL
	}
	accountID, acctErr := state.DecodeAccountID(c.Account)
	if acctErr != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	if !tx.HasExpiredField(check.Expiration, config.ParentCloseTime) {
		if check.Account != accountID && check.DestinationID != accountID {
			return ter.TecNO_PERMISSION
		}
	}
	return ter.TesSUCCESS
}

// Apply implements doApply matching rippled's CancelCheck::doApply.
func (c *CheckCancel) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("check cancel apply",
		"account", c.Account,
		"checkID", c.CheckID,
	)

	// Parse check ID
	checkID, err := hex.DecodeString(c.CheckID)
	if err != nil || len(checkID) != 32 {
		return ter.TemINVALID
	}

	var checkKeyBytes [32]byte
	copy(checkKeyBytes[:], checkID)
	checkKey := keylet.Keylet{Key: checkKeyBytes}

	// Read check
	// Reference: CancelCheck.cpp L55-60
	checkData, err := ctx.View.Read(checkKey)
	if err != nil || checkData == nil {
		ctx.Log.Warn("check cancel: check does not exist", "checkID", c.CheckID)
		return ter.TecNO_ENTRY
	}

	// View.Read is untyped, so reject a CheckID that resolves to a non-Check
	// object, matching rippled's tecNO_ENTRY.
	checkType, err := state.DecodeType(checkData)
	if err != nil || checkType != entry.TypeCheck {
		return ter.TecNO_ENTRY
	}

	// Parse check
	check, err := state.ParseCheck(checkData)
	if err != nil {
		return ter.TefINTERNAL
	}

	accountID := ctx.AccountID
	isCreator := check.Account == accountID

	// --- doApply ---

	srcID := check.Account
	dstID := check.DestinationID

	// Remove check from destination directory (if not self-send).
	// Reference: CancelCheck.cpp L102-113
	if srcID != dstID {
		destDirKey := keylet.OwnerDir(dstID)
		if result := tx.DirRemoveOrBadLedger(ctx.View, destDirKey, check.DestinationNode, checkKeyBytes); result != ter.TesSUCCESS {
			return result
		}
	}

	// Remove check from owner directory.
	// Reference: CancelCheck.cpp L114-122
	ownerDirKey := keylet.OwnerDir(srcID)
	if result := tx.DirRemoveOrBadLedger(ctx.View, ownerDirKey, check.OwnerNode, checkKeyBytes); result != ter.TesSUCCESS {
		return result
	}

	// Adjust creator's owner count.
	// Reference: CancelCheck.cpp L125-126
	if isCreator {
		// Canceller is the creator
		if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
	} else {
		// Update the creator's owner count. A missing creator account is
		// tolerated, matching rippled's adjustOwnerCount no-op on a null SLE;
		// a corrupt one is an internal error.
		creatorKey := keylet.Account(check.Account)
		creatorData, err := ctx.View.Read(creatorKey)
		if err == nil && creatorData != nil {
			creatorAccount, err := state.ParseAccountRoot(creatorData)
			if err != nil {
				return ter.TefINTERNAL
			}
			if creatorAccount.OwnerCount > 0 {
				creatorAccount.OwnerCount--
			}
			if result := ctx.UpdateAccountRoot(check.Account, creatorAccount); result != ter.TesSUCCESS {
				return result
			}
		}
	}

	// Delete the check.
	// Reference: CancelCheck.cpp L129
	if err := ctx.View.Erase(checkKey); err != nil {
		ctx.Log.Error("check cancel: unable to delete check", "checkID", c.CheckID)
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}
