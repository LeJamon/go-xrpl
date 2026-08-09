package escrow

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

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

	if e.Owner == "" {
		return ter.Errorf(ter.TemMALFORMED, "Owner is required")
	}

	return nil
}

func (e *EscrowCancel) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(e)
}

// GetFlagsMask returns the invalid-flags mask enforced at preflight0: any
// non-universal flag is rejected.
// Reference: rippled Escrow.cpp EscrowCancel::getFlagsMask.
func (e *EscrowCancel) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// Preclaim runs EscrowCancel's token-escrow ledger checks, gated (like rippled)
// on featureTokenEscrow: the escrow must exist (tecNO_TARGET) and, for a token
// escrow, the issuer's auth/freeze state must permit the return. Extracting these
// from Apply makes them visible to the preclaim-only paths (TxQ admission,
// simulate). The CancelAfter time checks stay in Apply, mirroring rippled which
// keeps them in EscrowCancel::doApply, not preclaim.
// Reference: rippled EscrowCancel.cpp preclaim().
func (e *EscrowCancel) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	rules := view.Rules()
	if rules == nil || !rules.Enabled(amendment.FeatureTokenEscrow) {
		return ter.TesSUCCESS
	}
	ownerID, err := state.DecodeAccountID(e.Owner)
	if err != nil {
		return ter.TemINVALID
	}
	escrowData, readErr := view.Read(keylet.Escrow(ownerID, e.OfferSequence))
	if readErr != nil {
		return ter.TefINTERNAL
	}
	if escrowData == nil {
		return ter.TecNO_TARGET
	}
	escrowEntry, parseErr := state.ParseEscrow(escrowData)
	if parseErr != nil {
		return ter.TefINTERNAL
	}
	if escrowEntry.IsXRP {
		return ter.TesSUCCESS
	}
	escrowAmount := reconstructAmountFromEscrow(escrowEntry)
	if escrowAmount.IsMPT() {
		return escrowCancelPreclaimMPT(view, escrowEntry.Account, escrowAmount)
	}
	if escrowAmount.Issuer != "" {
		return escrowCancelPreclaimIOU(view, escrowEntry.Account, escrowAmount)
	}
	return ter.TesSUCCESS
}

// Apply applies an EscrowCancel transaction
// Reference: rippled Escrow.cpp EscrowCancel::doApply()
func (e *EscrowCancel) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("escrow cancel apply",
		"account", e.Account,
		"owner", e.Owner,
		"offerSequence", e.OfferSequence,
	)

	rules := ctx.Rules()

	ownerID, err := state.DecodeAccountID(e.Owner)
	if err != nil {
		return ter.TemINVALID
	}

	// Find the escrow
	escrowKey := keylet.Escrow(ownerID, e.OfferSequence)
	escrowData, err := ctx.View.Read(escrowKey)
	if err != nil {
		return ctx.Internal("read escrow", err)
	}
	if escrowData == nil {
		if rules.Enabled(amendment.FeatureTokenEscrow) {
			return ter.TecINTERNAL
		}
		ctx.Log.Warn("escrow cancel: escrow not found",
			"owner", e.Owner,
			"offerSequence", e.OfferSequence,
		)
		return ter.TecNO_TARGET
	}

	// Parse escrow
	escrowEntry, err := state.ParseEscrow(escrowData)
	if err != nil {
		ctx.Log.Error("escrow cancel: failed to parse escrow", "error", err)
		return ter.TefINTERNAL
	}

	isXRP := escrowEntry.IsXRP
	sponsorAddress, err := tx.LedgerEntrySponsor(escrowData, "Sponsor")
	if err != nil {
		return ctx.Internal("EscrowCancel.Sponsor", err)
	}

	closeTime := ctx.Config.ParentCloseTime

	// Time validation — cancel is only allowed strictly after CancelAfter, which
	// must be set.
	// Reference: rippled EscrowCancel.cpp doApply().
	if escrowEntry.CancelAfter == 0 {
		return ter.TecNO_PERMISSION
	}
	if closeTime <= escrowEntry.CancelAfter {
		return ter.TecNO_PERMISSION
	}

	// Remove escrow from owner directory
	// Reference: rippled Escrow.cpp doApply() lines 1333-1342
	ownerDirKey := keylet.OwnerDir(escrowEntry.Account)
	if result := tx.DirRemoveOrBadLedger(ctx.View, ownerDirKey, escrowEntry.OwnerNode, escrowKey.Key); result != ter.TesSUCCESS {
		return result
	}

	// Remove escrow from destination directory (if cross-account)
	// Reference: rippled Escrow.cpp doApply() lines 1345-1356
	if escrowEntry.HasDestNode {
		destDirKey := keylet.OwnerDir(escrowEntry.DestinationID)
		if result := tx.DirRemoveOrBadLedger(ctx.View, destDirKey, escrowEntry.DestinationNode, escrowKey.Key); result != ter.TesSUCCESS {
			return result
		}
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
				return ter.TefINTERNAL
			}

			ownerAccount, err := state.ParseAccountRoot(ownerData)
			if err != nil {
				ctx.Log.Error("escrow cancel: failed to parse owner account", "error", err)
				return ter.TefINTERNAL
			}

			ownerAccount.Balance += escrowEntry.Amount
			if result := ctx.UpdateAccountRoot(ownerID, ownerAccount); result != ter.TesSUCCESS {
				return result
			}
		}
	} else {
		// IOU or MPT token escrow cancel
		// Reference: rippled Escrow.cpp doApply() lines 1364-1398
		if !rules.Enabled(amendment.FeatureTokenEscrow) {
			return ter.TemDISABLED
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
			mptRaw, ok := escrowAmount.MPTRaw()
			if !ok {
				return ter.TefINTERNAL
			}
			finalAmount := uint64(mptRaw)

			// Get dest (= owner) balance and ownerCount for the reserve check.
			ownerBalance, ownerOwnerCount, snapshotResult := ownerReserveSnapshot(ctx, ownerID, ownerIsSelf)
			if snapshotResult != ter.TesSUCCESS {
				return snapshotResult
			}

			if result := escrowUnlockMPT(
				ctx.View,
				ctx,
				escrowEntry.Account, escrowEntry.Account, // sender == receiver (cancel returns to creator)
				finalAmount,
				finalAmount, // cancel applies parityRate: gross == net, no fee to burn
				escrowAmount.MPTIssuanceID(),
				createAsset,
				ownerBalance,
				ownerOwnerCount,
				escrowEntry.Account,
				// Pre-fixCleanup3_2_0 the refund used the erased escrow SLE for the
				// reserve/bump (owner count 0); the amendment uses the creator's
				// account entry so the returned MPToken's reserve is charged to it.
				rules.Enabled(amendment.FeatureFixCleanup3_2_0),
				ctx.Config.ReserveBase, ctx.Config.ReserveIncrement,
			); result != ter.TesSUCCESS {
				return result
			}
		} else {
			// IOU cancel: return tokens to sender (sender == receiver == escrow creator).
			// parityRate means no transfer fee on cancel.
			// Reference: rippled line 1371-1387 (escrowUnlockApplyHelper<Issue>)
			ownerBalance, ownerOwnerCount, snapshotResult := ownerReserveSnapshot(ctx, ownerID, ownerIsSelf)
			if snapshotResult != ter.TesSUCCESS {
				return snapshotResult
			}

			if result := escrowUnlockIOU(
				ctx.View,
				ctx,
				parityRate,
				ownerBalance,
				ownerOwnerCount,
				escrowEntry.Account, // destID
				escrowAmount,
				escrowEntry.Account, escrowEntry.Account, // senderID == receiverID (cancel returns to creator)
				createAsset,
				// Pre-fixCleanup3_2_0 the refund used the erased escrow SLE for the
				// reserve/bump (owner count 0); the amendment uses the creator's
				// account entry so a newly-created trust line's reserve is charged
				// to it.
				rules.Enabled(amendment.FeatureFixCleanup3_2_0),
				ctx.Config.ReserveBase, ctx.Config.ReserveIncrement,
				ctx.NumberContext(),
			); result != ter.TesSUCCESS {
				return result
			}
		}

		// Remove escrow from issuer's owner directory, if present
		// Reference: rippled Escrow.cpp doApply() lines 1389-1398
		if escrowEntry.HasIssuerNode {
			issuerID, err := state.DecodeAccountID(escrowAmount.Issuer)
			if err != nil {
				return ter.TefBAD_LEDGER
			}
			issuerDirKey := keylet.OwnerDir(issuerID)
			if result := tx.DirRemoveOrBadLedger(ctx.View, issuerDirKey, escrowEntry.IssuerNode, escrowKey.Key); result != ter.TesSUCCESS {
				return result
			}
		}

		if ownerIsSelf {
			if result := resyncSelfOwnerCount(ctx); result != ter.TesSUCCESS {
				return result
			}
		}
	}

	if result := tx.DecreaseOwnerCountFor(ctx, ownerID, sponsorAddress, 1); result != ter.TesSUCCESS {
		return result
	}

	// Delete the escrow
	// Reference: rippled Escrow.cpp doApply() line 1405
	if err := ctx.View.Erase(escrowKey); err != nil {
		ctx.Log.Error("escrow cancel: failed to erase escrow", "error", err)
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// ownerReserveSnapshot returns the escrow owner's balance and owner count for the
// token-unlock reserve check. rippled passes mPriorBalance — the submitter's
// balance before the fee — and the reserve check only runs when the owner is the
// submitter (createAsset), so the fee is added back in the self case. For a
// third-party owner the current ledger balance is used.
// Reference: rippled Escrow.cpp:1377 (mPriorBalance argument).
func ownerReserveSnapshot(ctx *tx.ApplyContext, ownerID [20]byte, ownerIsSelf bool) (uint64, uint32, ter.Result) {
	if ownerIsSelf {
		return ctx.PriorBalance(), ctx.Account.OwnerCount, ter.TesSUCCESS
	}
	ownerData, err := ctx.View.Read(keylet.Account(ownerID))
	if err != nil {
		return 0, 0, ctx.Internal("read escrow owner", err)
	}
	if ownerData == nil {
		return 0, 0, ter.TefBAD_LEDGER
	}
	ownerAccount, err := state.ParseAccountRoot(ownerData)
	if err != nil {
		return 0, 0, ctx.Internal("parse escrow owner", err)
	}
	return ownerAccount.Balance, ownerAccount.OwnerCount, ter.TesSUCCESS
}
