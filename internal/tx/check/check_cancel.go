package check

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
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

// Validate implements preflight validation matching rippled's CancelCheck::preflight().
func (c *CheckCancel) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	// No flags allowed except universal flags
	// Reference: CancelCheck.cpp L42-47
	if err := tx.CheckFlags(c.GetFlags(), tx.TfUniversalMask); err != nil {
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
	return [][32]byte{amendment.FeatureChecks}
}

// Apply implements preclaim + doApply matching rippled's CancelCheck.
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
	if state.EntryType(checkData) != "Check" {
		return ter.TecNO_ENTRY
	}

	// Parse check
	check, err := state.ParseCheck(checkData)
	if err != nil {
		return ter.TefINTERNAL
	}

	accountID := ctx.AccountID
	isCreator := check.Account == accountID
	isDestination := check.DestinationID == accountID

	// Permission check based on expiration
	// Reference: CancelCheck.cpp L64-83
	// If expiration exists AND current time < expiration (not yet expired):
	//   Only creator or destination can cancel
	// If expired or no expiration: anyone can cancel expired, but only creator/dest for non-expired
	// If the check is not yet expired, only the creator or destination may
	// cancel it; once expired, anyone can.
	if !tx.HasExpiredField(check.Expiration, ctx.Config.ParentCloseTime) {
		if !isCreator && !isDestination {
			return ter.TecNO_PERMISSION
		}
	}

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
