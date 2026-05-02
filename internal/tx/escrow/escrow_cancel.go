package escrow

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	if err := tx.Register(tx.TypeEscrowCancel, func() tx.Transaction {
		return &EscrowCancel{BaseTx: *tx.NewBaseTx(tx.TypeEscrowCancel, "")}
	}); err != nil {
		panic(err)
	}
}

// EscrowCancel cancels an escrow, returning the escrowed XRP to the creator.
type EscrowCancel struct {
	tx.BaseTx

	// Owner is the account that created the escrow (required)
	Owner string `json:"Owner" xrpl:"Owner"`

	// OfferSequence is the sequence number of the EscrowCreate (required)
	OfferSequence uint32 `json:"OfferSequence" xrpl:"OfferSequence"`
}

func NewEscrowCancel(account, owner string, offerSequence uint32) *EscrowCancel {
	return &EscrowCancel{
		BaseTx:        *tx.NewBaseTx(tx.TypeEscrowCancel, account),
		Owner:         owner,
		OfferSequence: offerSequence,
	}
}

func (e *EscrowCancel) TxType() tx.Type {
	return tx.TypeEscrowCancel
}

// Reference: rippled Escrow.cpp EscrowCancel::preflight()
func (e *EscrowCancel) Validate() error {
	if err := e.BaseTx.Validate(); err != nil {
		return err
	}

	if err := tx.CheckFlags(e.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	if e.Owner == "" {
		return tx.Errorf(tx.TemMALFORMED, "Owner is required")
	}

	return nil
}

func (e *EscrowCancel) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(e)
}

// Apply applies an EscrowCancel transaction
// Reference: rippled Escrow.cpp EscrowCancel::preclaim() + doApply()
func (e *EscrowCancel) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("escrow cancel apply",
		"account", e.Account,
		"owner", e.Owner,
		"offerSequence", e.OfferSequence,
	)

	rules := ctx.Rules()

	ownerID, err := state.DecodeAccountID(e.Owner)
	if err != nil {
		return tx.TemINVALID
	}

	// Find the escrow
	escrowKey := keylet.Escrow(ownerID, e.OfferSequence)
	escrowData, err := ctx.View.Read(escrowKey)
	if err != nil || escrowData == nil {
		ctx.Log.Warn("escrow cancel: escrow not found",
			"owner", e.Owner,
			"offerSequence", e.OfferSequence,
		)
		return tx.TecNO_TARGET
	}

	// Parse escrow
	escrowEntry, err := state.ParseEscrow(escrowData)
	if err != nil {
		ctx.Log.Error("escrow cancel: failed to parse escrow", "error", err)
		return tx.TefINTERNAL
	}

	isXRP := escrowEntry.IsXRP

	// Token preclaim validation (IOU/MPT)
	// Reference: rippled Escrow.cpp EscrowCancel::preclaim() lines 1269-1295
	if !isXRP && rules.Enabled(amendment.FeatureTokenEscrow) {
		escrowAmount := reconstructAmountFromEscrow(escrowEntry)
		if escrowAmount.IsMPT() {
			if result := escrowCancelPreclaimMPT(ctx.View, escrowEntry.Account, escrowAmount); result != tx.TesSUCCESS {
				return result
			}
		} else if escrowAmount.Issuer != "" {
			if result := escrowCancelPreclaimIOU(ctx.View, escrowEntry.Account, escrowAmount); result != tx.TesSUCCESS {
				return result
			}
		}
	}

	closeTime := ctx.Config.ParentCloseTime

	// Time validation — cancel is only allowed after CancelAfter time
	// Reference: rippled Escrow.cpp doApply() lines 1310-1329
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

	// Remove escrow from owner directory
	// Reference: rippled Escrow.cpp doApply() lines 1333-1342
	ownerDirKey := keylet.OwnerDir(escrowEntry.Account)
	state.DirRemove(ctx.View, ownerDirKey, escrowEntry.OwnerNode, escrowKey.Key, false)

	// Remove escrow from destination directory (if cross-account)
	// Reference: rippled Escrow.cpp doApply() lines 1345-1356
	if escrowEntry.HasDestNode {
		destDirKey := keylet.OwnerDir(escrowEntry.DestinationID)
		state.DirRemove(ctx.View, destDirKey, escrowEntry.DestinationNode, escrowKey.Key, false)
	}

	// Return the escrowed amount to the owner.
	// When the canceller IS the owner, modify ctx.Account directly
	// (because the engine writes ctx.Account back after Apply, which would
	// overwrite any separate table updates for the same account).
	ownerIsSelf := ownerID == ctx.AccountID

	if isXRP {
		// XRP: add balance directly
		// Reference: rippled Escrow.cpp doApply() line 1363
		if ownerIsSelf {
			ctx.Account.Balance += escrowEntry.Amount
		} else {
			ownerKey := keylet.Account(ownerID)
			ownerData, err := ctx.View.Read(ownerKey)
			if err != nil {
				ctx.Log.Error("escrow cancel: failed to read owner account", "error", err)
				return tx.TefINTERNAL
			}

			ownerAccount, err := state.ParseAccountRoot(ownerData)
			if err != nil {
				ctx.Log.Error("escrow cancel: failed to parse owner account", "error", err)
				return tx.TefINTERNAL
			}

			ownerAccount.Balance += escrowEntry.Amount
			if result := ctx.UpdateAccountRoot(ownerID, ownerAccount); result != tx.TesSUCCESS {
				return result
			}
		}
	} else {
		// IOU or MPT token escrow cancel
		// Reference: rippled Escrow.cpp doApply() lines 1364-1398
		if !rules.Enabled(amendment.FeatureTokenEscrow) {
			return tx.TemDISABLED
		}

		escrowAmount := reconstructAmountFromEscrow(escrowEntry)

		// createAsset = true when the escrow creator is the one canceling.
		// This allows trust line / MPToken creation if needed.
		// Reference: rippled line 1370: bool const createAsset = account == account_;
		createAsset := escrowEntry.Account == ctx.AccountID

		if escrowAmount.IsMPT() {
			// MPT cancel: return tokens to sender (sender == receiver == escrow creator).
			// parityRate means no transfer fee on cancel.
			// Reference: rippled line 1371-1387 (escrowUnlockApplyHelper<MPTIssue>)
			mptRaw, _ := escrowAmount.MPTRaw()
			finalAmount := uint64(mptRaw)

			// Get dest (= owner) balance and ownerCount for reserve check
			var ownerBalance uint64
			var ownerOwnerCount uint32
			if ownerIsSelf {
				ownerBalance = ctx.Account.Balance
				ownerOwnerCount = ctx.Account.OwnerCount
			} else {
				ownerData, _ := ctx.View.Read(keylet.Account(ownerID))
				ownerAccount, _ := state.ParseAccountRoot(ownerData)
				if ownerAccount != nil {
					ownerBalance = ownerAccount.Balance
					ownerOwnerCount = ownerAccount.OwnerCount
				}
			}

			if result := escrowUnlockMPT(
				ctx.View,
				escrowEntry.Account, escrowEntry.Account, // sender == receiver (cancel returns to creator)
				finalAmount,
				escrowAmount.MPTIssuanceID(),
				createAsset,
				ownerBalance,
				ownerOwnerCount,
				escrowEntry.Account,
				ctx.Config.ReserveBase, ctx.Config.ReserveIncrement,
			); result != tx.TesSUCCESS {
				return result
			}
		} else {
			// IOU cancel: return tokens to sender (sender == receiver == escrow creator).
			// parityRate means no transfer fee on cancel.
			// Reference: rippled line 1371-1387 (escrowUnlockApplyHelper<Issue>)
			var ownerBalance uint64
			var ownerOwnerCount uint32
			if ownerIsSelf {
				ownerBalance = ctx.Account.Balance
				ownerOwnerCount = ctx.Account.OwnerCount
			} else {
				ownerData, _ := ctx.View.Read(keylet.Account(ownerID))
				ownerAccount, _ := state.ParseAccountRoot(ownerData)
				if ownerAccount != nil {
					ownerBalance = ownerAccount.Balance
					ownerOwnerCount = ownerAccount.OwnerCount
				}
			}

			if result := escrowUnlockIOU(
				ctx.View,
				parityRate,
				ownerBalance,
				ownerOwnerCount,
				escrowEntry.Account, // destID
				escrowAmount,
				escrowEntry.Account, escrowEntry.Account, // senderID == receiverID (cancel returns to creator)
				createAsset,
				ctx.Config.ReserveBase, ctx.Config.ReserveIncrement,
			); result != tx.TesSUCCESS {
				return result
			}
		}

		// When ownerIsSelf, the unlock functions may create new objects
		// (MPToken or trust line) and adjust OwnerCount through the view.
		// Re-synchronize ctx.Account so the engine write-back doesn't lose it.
		if ownerIsSelf {
			ownerKey := keylet.Account(ownerID)
			if updatedData, readErr := ctx.View.Read(ownerKey); readErr == nil && updatedData != nil {
				if updatedAcct, parseErr := state.ParseAccountRoot(updatedData); parseErr == nil {
					ctx.Account.OwnerCount = updatedAcct.OwnerCount
				}
			}
		}

		// Remove escrow from issuer's owner directory, if present
		// Reference: rippled Escrow.cpp doApply() lines 1389-1398
		if escrowEntry.HasIssuerNode {
			issuerID, err := state.DecodeAccountID(escrowAmount.Issuer)
			if err == nil {
				issuerDirKey := keylet.OwnerDir(issuerID)
				state.DirRemove(ctx.View, issuerDirKey, escrowEntry.IssuerNode, escrowKey.Key, false)
			}
		}
	}

	// Decrement owner count
	// Reference: rippled Escrow.cpp doApply() line 1401
	adjustOwnerCount(ctx, ownerID, -1)

	// Delete the escrow
	// Reference: rippled Escrow.cpp doApply() line 1405
	if err := ctx.View.Erase(escrowKey); err != nil {
		ctx.Log.Error("escrow cancel: failed to erase escrow", "error", err)
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}
