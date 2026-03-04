package check

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeCheckCancel, func() tx.Transaction {
		return &CheckCancel{BaseTx: *tx.NewBaseTx(tx.TypeCheckCancel, "")}
	})
}

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

// TxType returns the transaction type
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
	if c.GetFlags()&tx.TfUniversalMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags")
	}

	if c.CheckID == "" {
		return errors.New("CheckID is required")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (c *CheckCancel) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

// RequiredAmendments returns the amendments required for this transaction type
func (c *CheckCancel) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureChecks}
}

// Apply implements preclaim + doApply matching rippled's CancelCheck.
func (c *CheckCancel) Apply(ctx *tx.ApplyContext) tx.Result {
	// Parse check ID
	checkID, err := hex.DecodeString(c.CheckID)
	if err != nil || len(checkID) != 32 {
		return tx.TemINVALID
	}

	var checkKeyBytes [32]byte
	copy(checkKeyBytes[:], checkID)
	checkKey := keylet.Keylet{Key: checkKeyBytes}

	// Read check
	// Reference: CancelCheck.cpp L55-60
	checkData, err := ctx.View.Read(checkKey)
	if err != nil || checkData == nil {
		return tx.TecNO_ENTRY
	}

	// Parse check
	check, err := sle.ParseCheck(checkData)
	if err != nil {
		return tx.TefINTERNAL
	}

	accountID := ctx.AccountID
	isCreator := check.Account == accountID
	isDestination := check.DestinationID == accountID

	// Permission check based on expiration
	// Reference: CancelCheck.cpp L64-83
	// If expiration exists AND current time < expiration (not yet expired):
	//   Only creator or destination can cancel
	// If expired or no expiration: anyone can cancel expired, but only creator/dest for non-expired
	if check.Expiration == 0 {
		// No expiration set - only creator or destination can cancel
		if !isCreator && !isDestination {
			return tx.TecNO_PERMISSION
		}
	} else if check.Expiration > ctx.Config.ParentCloseTime {
		// Not yet expired - only creator or destination can cancel
		if !isCreator && !isDestination {
			return tx.TecNO_PERMISSION
		}
	}
	// If expired (Expiration > 0 && Expiration <= ParentCloseTime), anyone can cancel

	// --- doApply ---

	// Delete the check
	if err := ctx.View.Erase(checkKey); err != nil {
		return tx.TefINTERNAL
	}

	// Adjust creator's owner count
	if isCreator {
		// Canceller is the creator
		if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
	} else {
		// Need to update the creator's owner count
		creatorKey := keylet.Account(check.Account)
		creatorData, err := ctx.View.Read(creatorKey)
		if err == nil {
			creatorAccount, err := sle.ParseAccountRoot(creatorData)
			if err == nil && creatorAccount.OwnerCount > 0 {
				creatorAccount.OwnerCount--
				creatorUpdatedData, _ := sle.SerializeAccountRoot(creatorAccount)
				ctx.View.Update(creatorKey, creatorUpdatedData)
			}
		}
	}

	return tx.TesSUCCESS
}
