package escrow

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeEscrowCancel, func() tx.Transaction {
		return &EscrowCancel{BaseTx: *tx.NewBaseTx(tx.TypeEscrowCancel, "")}
	})
}

// EscrowCancel cancels an escrow, returning the escrowed XRP to the creator.
type EscrowCancel struct {
	tx.BaseTx

	// Owner is the account that created the escrow (required)
	Owner string `json:"Owner" xrpl:"Owner"`

	// OfferSequence is the sequence number of the EscrowCreate (required)
	OfferSequence uint32 `json:"OfferSequence" xrpl:"OfferSequence"`
}

// NewEscrowCancel creates a new EscrowCancel transaction
func NewEscrowCancel(account, owner string, offerSequence uint32) *EscrowCancel {
	return &EscrowCancel{
		BaseTx:        *tx.NewBaseTx(tx.TypeEscrowCancel, account),
		Owner:         owner,
		OfferSequence: offerSequence,
	}
}

// TxType returns the transaction type
func (e *EscrowCancel) TxType() tx.Type {
	return tx.TypeEscrowCancel
}

// Validate validates the EscrowCancel transaction
// Reference: rippled Escrow.cpp EscrowCancel::preflight()
func (e *EscrowCancel) Validate() error {
	if err := e.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	if e.GetFlags()&tx.TfUniversalMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags")
	}

	if e.Owner == "" {
		return errors.New("temMALFORMED: Owner is required")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (e *EscrowCancel) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(e)
}

// Apply applies an EscrowCancel transaction
// Reference: rippled Escrow.cpp EscrowCancel::preclaim() + doApply()
func (ec *EscrowCancel) Apply(ctx *tx.ApplyContext) tx.Result {
	rules := ctx.Rules()

	// Get the escrow owner's account ID
	ownerID, err := sle.DecodeAccountID(ec.Owner)
	if err != nil {
		return tx.TemINVALID
	}

	// Find the escrow
	escrowKey := keylet.Escrow(ownerID, ec.OfferSequence)
	escrowData, err := ctx.View.Read(escrowKey)
	if err != nil || escrowData == nil {
		return tx.TecNO_TARGET
	}

	// Parse escrow
	escrowEntry, err := sle.ParseEscrow(escrowData)
	if err != nil {
		return tx.TefINTERNAL
	}

	closeTime := ctx.Config.ParentCloseTime

	// Time validation — cancel is only allowed after CancelAfter time
	// Reference: rippled Escrow.cpp preclaim() lines 1310-1329
	if rules.Enabled(amendment.FeatureFix1571) {
		// fix1571: must have CancelAfter set, and close time must be past it
		if escrowEntry.CancelAfter == 0 {
			return tx.TecNO_PERMISSION
		}
		if closeTime <= escrowEntry.CancelAfter {
			return tx.TecNO_PERMISSION
		}
	} else {
		// Pre-fix1571: same logic
		if escrowEntry.CancelAfter == 0 || closeTime <= escrowEntry.CancelAfter {
			return tx.TecNO_PERMISSION
		}
	}

	// Return the escrowed amount to the owner and decrement owner count.
	// When the canceller IS the owner, modify ctx.Account directly
	// (because the engine writes ctx.Account back after Apply, which would
	// overwrite any separate table updates for the same account).
	ownerIsSelf := ownerID == ctx.AccountID
	if ownerIsSelf {
		ctx.Account.Balance += escrowEntry.Amount
		if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
	} else {
		ownerKey := keylet.Account(ownerID)
		ownerData, err := ctx.View.Read(ownerKey)
		if err != nil {
			return tx.TefINTERNAL
		}

		ownerAccount, err := sle.ParseAccountRoot(ownerData)
		if err != nil {
			return tx.TefINTERNAL
		}

		ownerAccount.Balance += escrowEntry.Amount
		if ownerAccount.OwnerCount > 0 {
			ownerAccount.OwnerCount--
		}

		ownerUpdatedData, err := sle.SerializeAccountRoot(ownerAccount)
		if err != nil {
			return tx.TefINTERNAL
		}

		if err := ctx.View.Update(ownerKey, ownerUpdatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Remove escrow from owner directory
	// Reference: rippled Escrow.cpp doApply() lines 1350-1360
	ownerDirKey := keylet.OwnerDir(escrowEntry.Account)
	sle.DirRemove(ctx.View, ownerDirKey, escrowEntry.OwnerNode, escrowKey.Key, false)

	// Remove escrow from destination directory (if cross-account)
	if escrowEntry.HasDestNode {
		destDirKey := keylet.OwnerDir(escrowEntry.DestinationID)
		sle.DirRemove(ctx.View, destDirKey, escrowEntry.DestinationNode, escrowKey.Key, false)
	}

	// Delete the escrow - deletion tracked automatically by ApplyStateTable
	if err := ctx.View.Erase(escrowKey); err != nil {
		return tx.TefINTERNAL
	}

	// If cross-account, also decrement destination's OwnerCount
	// Reference: rippled — if (sle[sfAccount] != sle[sfDestination]) adjustOwnerCount(dest, -1)
	if escrowEntry.Account != escrowEntry.DestinationID {
		adjustOwnerCount(ctx, escrowEntry.DestinationID, -1)
	}

	return tx.TesSUCCESS
}
