package escrow

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// EscrowFinish completes an escrow, releasing the escrowed XRP.
type EscrowFinish struct {
	tx.BaseTx

	// Owner is the account that created the escrow (required)
	Owner string `json:"Owner" xrpl:"Owner"`

	// OfferSequence is the sequence number of the EscrowCreate (required)
	OfferSequence uint32 `json:"OfferSequence" xrpl:"OfferSequence"`

	// Condition is the crypto-condition that was fulfilled (optional).
	// Pointer to distinguish "not set" (nil) from "set to empty" (ptr to "").
	Condition *string `json:"Condition,omitempty" xrpl:"Condition,omitempty"`

	// Fulfillment is the fulfillment for the condition (optional).
	// Pointer to distinguish "not set" (nil) from "set to empty" (ptr to "").
	Fulfillment *string `json:"Fulfillment,omitempty" xrpl:"Fulfillment,omitempty"`

	// CredentialIDs is a list of credential ledger entry IDs (uint256 hashes as hex strings)
	// Used for deposit preauth with credentials.
	// Reference: rippled sfCredentialIDs
	CredentialIDs []string `json:"CredentialIDs,omitempty" xrpl:"CredentialIDs,omitempty"`
}

func NewEscrowFinish(account, owner string, offerSequence uint32) *EscrowFinish {
	return &EscrowFinish{
		BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, account),
		Owner:         owner,
		OfferSequence: offerSequence,
	}
}

func (e *EscrowFinish) TxType() tx.Type {
	return tx.TypeEscrowFinish
}

// Reference: rippled Escrow.cpp EscrowFinish::preflight()
func (e *EscrowFinish) Validate() error {
	if err := e.BaseTx.Validate(); err != nil {
		return err
	}

	if e.Owner == "" {
		return ter.Errorf(ter.TemMALFORMED, "Owner is required")
	}

	// Both Condition and Fulfillment must be present or absent together.
	// "Present" means the field exists in the transaction (even if empty value).
	hasCondition := e.Condition != nil
	hasFulfillment := e.Fulfillment != nil
	if hasCondition != hasFulfillment {
		return ter.Errorf(ter.TemMALFORMED, "Condition and Fulfillment must be provided together")
	}

	return nil
}

func (e *EscrowFinish) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(e)
}

// GetFlagsMask returns the invalid-flags mask enforced at preflight0: any
// non-universal flag is rejected.
// Reference: rippled Escrow.cpp EscrowFinish::getFlagsMask.
func (e *EscrowFinish) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// CheckExtraFeatures gates the CredentialIDs field on the Credentials amendment.
// rippled evaluates this in checkExtraFeatures — before preflight1's common
// checks and before the tx-type preflight body — so a CredentialIDs-bearing
// EscrowFinish on a network without Credentials is temDISABLED ahead of every
// other TER, keyed on field presence (not element count).
// Reference: rippled Escrow.cpp EscrowFinish::checkExtraFeatures.
func (e *EscrowFinish) CheckExtraFeatures(rules *amendment.Rules) error {
	present := e.CredentialIDs != nil || e.HasField("CredentialIDs")
	if present && !rules.Enabled(amendment.FeatureCredentials) {
		return ter.Errorf(ter.TemDISABLED, "Credentials amendment not enabled")
	}
	return nil
}

// PreflightSigValidated runs the CredentialIDs shape check (empty / >8 /
// duplicate → temMALFORMED). rippled runs credentials::checkFields in
// preflightSigValidated, AFTER the signature is verified, so a mis-signed
// EscrowFinish surfaces temINVALID rather than this temMALFORMED.
// Reference: rippled Escrow.cpp EscrowFinish::preflightSigValidated.
func (e *EscrowFinish) PreflightSigValidated() error {
	present := e.CredentialIDs != nil || e.HasField("CredentialIDs")
	return credential.CheckFields(e.CredentialIDs, present, "Duplicate credential ID")
}

// CalculateBaseFee mirrors rippled's EscrowFinish::calculateBaseFee: the
// multisigned base fee plus, for a fulfillment-bearing EscrowFinish, a
// crypto-condition surcharge of base * (32 + fulfillment.size()/16), where
// fulfillment.size() is the decoded byte length. The CustomBaseFeeCalculator
// dispatch in preclaim.go skips the multisig multiplier, so it is applied here.
// Reference: rippled Escrow.cpp:682-693, Transactor.cpp:229-244
func (e *EscrowFinish) CalculateBaseFee(view tx.LedgerView, config tx.EngineConfig) uint64 {
	base := config.BaseFee
	if view != nil {
		if data, err := view.Read(keylet.Fees()); err == nil && data != nil {
			if fs, err := state.ParseFeeSettings(data); err == nil {
				base = fs.GetBaseFee()
			}
		}
	}

	fee := sign.CalculateMultiSigFee(
		base,
		len(e.GetCommon().Signers)+sign.SponsorSignerCount(e),
	)

	if e.Fulfillment != nil {
		fulfillmentLen := len(*e.Fulfillment) / 2
		if decoded, err := hex.DecodeString(*e.Fulfillment); err == nil {
			fulfillmentLen = len(decoded)
		}
		fee += base * (32 + uint64(fulfillmentLen)/16)
	}

	return fee
}

// ApplyOnTec implements TecApplier. When tecEXPIRED is returned, this re-runs
// credential expiration deletion against the engine's view so the side-effects
// (credential deletion, owner count adjustment) persist even though the tx
// sandbox is rolled back for tec results.
// Reference: rippled Transactor.cpp - tecEXPIRED re-applies removeExpiredCredentials
func (e *EscrowFinish) ApplyOnTec(ctx *tx.ApplyContext) {
	credential.RemoveExpiredCredentialsOnTec(ctx, e.CredentialIDs)
}

// Apply applies an EscrowFinish transaction
// Reference: rippled Escrow.cpp EscrowFinish::preclaim() + doApply()
// Preclaim runs EscrowFinish's ledger-aware checks in rippled's preclaim order:
// first the CredentialIDs validity check (tecBAD_CREDENTIALS, gated on
// featureCredentials), then — gated (like rippled) on featureTokenEscrow — the
// escrow existence (tecNO_TARGET) and, for a token escrow, the destination's
// auth/freeze state. Extracting these from Apply makes them visible to the
// preclaim-only paths (TxQ admission, simulate). The FinishAfter/CancelAfter time
// checks, the crypto-condition/fulfillment check, and the tecEXPIRED
// expired-credential deletion (ApplyOnTec) stay in Apply, mirroring rippled which
// keeps them in EscrowFinish::doApply. ValidCredentials never returns tecEXPIRED
// (expiry is handled separately in Apply), so no tecEXPIRED escapes preclaim here.
// Reference: rippled EscrowFinish.cpp preclaim().
func (e *EscrowFinish) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	rules := view.Rules()
	if rules != nil && rules.Enabled(amendment.FeatureCredentials) && len(e.CredentialIDs) > 0 {
		accountID, acctErr := state.DecodeAccountID(e.Account)
		if acctErr != nil {
			return ter.TemBAD_SRC_ACCOUNT
		}
		if result := credential.ValidCredentials(view, accountID, e.CredentialIDs); result != ter.TesSUCCESS {
			return result
		}
	}
	if rules == nil || !rules.Enabled(amendment.FeatureTokenEscrow) {
		return ter.TesSUCCESS
	}
	ownerID, err := state.DecodeAccountID(e.Owner)
	if err != nil {
		return ter.TemINVALID
	}
	escrowData, readErr := view.Read(keylet.Escrow(ownerID, e.OfferSequence))
	if readErr != nil || escrowData == nil {
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
	if escrowEntry.MPTIssuanceID != "" {
		return escrowFinishPreclaimMPT(view, escrowEntry.DestinationID, escrowAmount)
	}
	if escrowAmount.Issuer != "" {
		return escrowFinishPreclaimIOU(view, escrowEntry.DestinationID, escrowAmount)
	}
	return ter.TesSUCCESS
}

// Reference: rippled EscrowFinish.cpp doApply()
func (e *EscrowFinish) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("escrow finish apply",
		"account", e.Account,
		"owner", e.Owner,
		"offerSequence", e.OfferSequence,
	)

	ownerID, err := state.DecodeAccountID(e.Owner)
	if err != nil {
		return ter.TemINVALID
	}

	// Find the escrow
	escrowKey := keylet.Escrow(ownerID, e.OfferSequence)
	escrowData, err := ctx.View.Read(escrowKey)
	if err != nil || escrowData == nil {
		ctx.Log.Warn("escrow finish: escrow not found",
			"owner", e.Owner,
			"offerSequence", e.OfferSequence,
		)
		return ter.TecNO_TARGET
	}

	// Parse escrow
	escrowEntry, err := state.ParseEscrow(escrowData)
	if err != nil {
		ctx.Log.Error("escrow finish: failed to parse escrow", "error", err)
		return ter.TefINTERNAL
	}

	rules := ctx.Rules()
	isXRP := escrowEntry.IsXRP

	closeTime := ctx.Config.ParentCloseTime

	// --- doApply: Time validation ---
	// after() means strictly greater than: finish requires the close time to be
	// strictly after FinishAfter and not strictly after CancelAfter.
	// Reference: rippled EscrowFinish.cpp doApply().
	if escrowEntry.FinishAfter > 0 && closeTime <= escrowEntry.FinishAfter {
		return ter.TecNO_PERMISSION
	}
	if escrowEntry.CancelAfter > 0 && closeTime > escrowEntry.CancelAfter {
		return ter.TecNO_PERMISSION
	}

	// Crypto-condition verification
	// Reference: rippled Escrow.cpp doApply() lines 1057-1101
	txCondition := ""
	if e.Condition != nil {
		txCondition = *e.Condition
	}
	txFulfillment := ""
	if e.Fulfillment != nil {
		txFulfillment = *e.Fulfillment
	}

	if escrowEntry.Condition == "" {
		// Escrow has no condition — tx must NOT provide condition/fulfillment
		if txCondition != "" || txFulfillment != "" {
			ctx.Log.Warn("escrow finish: condition/fulfillment provided but escrow has no condition")
			return ter.TecCRYPTOCONDITION_ERROR
		}
	} else {
		// Escrow has a condition — fulfillment is required (non-empty)
		if txFulfillment == "" {
			ctx.Log.Warn("escrow finish: fulfillment required but not provided")
			return ter.TecCRYPTOCONDITION_ERROR
		}

		// Condition in tx must match condition on escrow (case-insensitive hex comparison)
		if !strings.EqualFold(txCondition, escrowEntry.Condition) {
			ctx.Log.Warn("escrow finish: condition mismatch")
			return ter.TecCRYPTOCONDITION_ERROR
		}

		// Verify fulfillment matches condition
		if err := validateCryptoCondition(txFulfillment, escrowEntry.Condition); err != nil {
			ctx.Log.Debug("escrow finish: fulfillment verification failed", "error", err)
			return ter.TecCRYPTOCONDITION_ERROR
		}
		ctx.Log.Debug("escrow finish: fulfillment verified successfully")
	}

	// Determine if finisher is the destination and/or the owner.
	destIsSelf := ctx.AccountID == escrowEntry.DestinationID

	// Read destination account for deposit auth check
	var destAccount *state.AccountRoot
	destKey := keylet.Account(escrowEntry.DestinationID)
	if destIsSelf {
		destAccount = ctx.Account
	} else {
		destData, err := ctx.View.Read(destKey)
		// A missing destination (nil data, nil error) means the account was
		// deleted after the escrow was created. Escrow cannot fund a new
		// account, so this is tecNO_DST — not a parse-time tefINTERNAL.
		// Reference: rippled Escrow.cpp:1105-1108
		if err != nil || destData == nil {
			return ter.TecNO_DST
		}
		destAccount, err = state.ParseAccountRoot(destData)
		if err != nil {
			return ter.TefINTERNAL
		}
	}

	// Deposit authorization check; expired-credential removal happens inside.
	// Reference: rippled Escrow.cpp doApply() — verifyDepositPreauth()
	if result := credential.VerifyDepositPreauth(ctx, e.CredentialIDs, ctx.AccountID, escrowEntry.DestinationID, destAccount); result != ter.TesSUCCESS {
		return result
	}

	// Remove escrow from owner directory
	// Reference: rippled Escrow.cpp doApply() lines 1120-1129
	ownerDirKey := keylet.OwnerDir(escrowEntry.Account)
	if result := tx.DirRemoveOrBadLedger(ctx.View, ownerDirKey, escrowEntry.OwnerNode, escrowKey.Key); result != ter.TesSUCCESS {
		return result
	}

	// Remove escrow from destination directory (if cross-account)
	// Reference: rippled Escrow.cpp doApply() lines 1132-1140
	if escrowEntry.HasDestNode {
		destDirKey := keylet.OwnerDir(escrowEntry.DestinationID)
		if result := tx.DirRemoveOrBadLedger(ctx.View, destDirKey, escrowEntry.DestinationNode, escrowKey.Key); result != ter.TesSUCCESS {
			return result
		}
	}

	// Transfer the escrowed amount to destination
	// Reference: rippled Escrow.cpp doApply() lines 1142-1184
	if isXRP {
		// XRP: credit destination balance
		destAccount.Balance += escrowEntry.Amount
	} else {
		if !rules.Enabled(amendment.FeatureTokenEscrow) {
			return ter.TemDISABLED
		}

		escrowAmount := reconstructAmountFromEscrow(escrowEntry)
		lockedRate := uint32(0)
		if escrowEntry.HasTransferRate {
			lockedRate = escrowEntry.TransferRate
		}
		if lockedRate == 0 {
			lockedRate = parityRate
		}

		// createAsset = destination is the tx submitter (they can create trust line for themselves)
		// Reference: rippled Escrow.cpp line 1155: bool const createAsset = destID == account_;
		createAsset := escrowEntry.DestinationID == ctx.AccountID

		// rippled checks the trust-line / MPToken reserve against mPriorBalance —
		// the submitter's balance before the fee was deducted. The reserve check
		// only runs when the destination is the submitter (createAsset), so add
		// the fee back only in that case; destAccount is then ctx.Account, whose
		// balance has already had the fee removed.
		// Reference: rippled Escrow.cpp:1162 (mPriorBalance argument).
		destReserveBalance := destAccount.Balance
		if destIsSelf {
			destReserveBalance = ctx.PriorBalance()
		}

		if escrowEntry.MPTIssuanceID != "" {
			// MPT unlock
			// Reference: rippled Escrow.cpp escrowUnlockApplyHelper<MPTIssue> lines 944-1012
			mptHexID := escrowEntry.MPTIssuanceID

			var originalAmount uint64
			if escrowEntry.MPTAmount != nil {
				originalAmount = uint64(*escrowEntry.MPTAmount)
			} else if raw, ok := escrowAmount.MPTRaw(); ok {
				originalAmount = uint64(raw)
			} else {
				originalAmount = uint64(escrowAmount.IOU().Mantissa())
			}

			// Compute transfer fee
			_, finalAmount := computeMPTTransferFee(
				ctx.View,
				lockedRate,
				mptHexID,
				escrowEntry.Account,
				escrowEntry.DestinationID,
				originalAmount,
			)

			// fixTokenEscrowV1 clears the gross (originally locked) amount and burns
			// the fee from supply; before it, only the net amount was accounted.
			grossAmount := finalAmount
			if rules.Enabled(amendment.FeatureFixTokenEscrowV1) {
				grossAmount = originalAmount
			}

			if result := escrowUnlockMPT(
				ctx.View,
				escrowEntry.Account,
				escrowEntry.DestinationID,
				finalAmount,
				grossAmount,
				mptHexID,
				createAsset,
				destReserveBalance,
				destAccount.OwnerCount,
				escrowEntry.DestinationID,
				true, // finish bumps the destination account's OwnerCount
				ctx.Config.ReserveBase,
				ctx.Config.ReserveIncrement,
			); result != ter.TesSUCCESS {
				return result
			}
		} else {
			// IOU unlock
			// Reference: rippled Escrow.cpp escrowUnlockApplyHelper<Issue> lines 809-942
			if result := escrowUnlockIOU(
				ctx.View,
				lockedRate,
				destReserveBalance,
				destAccount.OwnerCount,
				escrowEntry.DestinationID,
				escrowAmount,
				escrowEntry.Account,
				escrowEntry.DestinationID,
				createAsset,
				true, // finish bumps the destination account's OwnerCount
				ctx.Config.ReserveBase,
				ctx.Config.ReserveIncrement,
			); result != ter.TesSUCCESS {
				return result
			}
		}

		// Remove escrow from issuer's owner directory
		// Reference: rippled Escrow.cpp doApply() lines 1174-1183
		if escrowEntry.HasIssuerNode {
			issuerID, issuerErr := state.DecodeAccountID(escrowAmount.Issuer)
			if issuerErr == nil {
				issuerDirKey := keylet.OwnerDir(issuerID)
				if result := tx.DirRemoveOrBadLedger(ctx.View, issuerDirKey, escrowEntry.IssuerNode, escrowKey.Key); result != ter.TesSUCCESS {
					return result
				}
			}
		}
	}

	// When destIsSelf, the unlock functions (escrowUnlockMPT/escrowUnlockIOU)
	// may create new objects (MPToken or trust line) and adjust the
	// destination's OwnerCount through the view. Since destAccount is
	// ctx.Account (the same in-memory object the engine writes back), we must
	// re-synchronize it with the view so that the OwnerCount update is not
	// lost when the engine writes ctx.Account back.
	if destIsSelf && !isXRP {
		if updatedData, readErr := ctx.View.Read(destKey); readErr == nil && updatedData != nil {
			if updatedAcct, parseErr := state.ParseAccountRoot(updatedData); parseErr == nil {
				ctx.Account.OwnerCount = updatedAcct.OwnerCount
			}
		}
	}

	// Write destination account back
	// Reference: rippled Escrow.cpp doApply() line 1186: ctx_.view().update(sled);
	if !destIsSelf {
		if result := ctx.UpdateAccountRoot(escrowEntry.DestinationID, destAccount); result != ter.TesSUCCESS {
			return result
		}
	}

	// Delete the escrow
	// Reference: rippled Escrow.cpp doApply() line 1194: ctx_.view().erase(slep);
	if err := ctx.View.Erase(escrowKey); err != nil {
		ctx.Log.Error("escrow finish: failed to erase escrow", "error", err)
		return ter.TefINTERNAL
	}

	// Decrement OwnerCount for escrow owner only.
	// Reference: rippled Escrow.cpp doApply() lines 1188-1191
	adjustOwnerCount(ctx, ownerID, -1)

	return ter.TesSUCCESS
}

// adjustOwnerCount adjusts the OwnerCount of the given account by delta.
// When the target account is ctx.Account (the transaction sender), it modifies
// ctx.Account directly (the engine writes it back). Otherwise it delegates to
// tx.AdjustOwnerCount which reads/writes through the view.
func adjustOwnerCount(ctx *tx.ApplyContext, accountID [20]byte, delta int) {
	if accountID == ctx.AccountID {
		if delta > 0 {
			ctx.Account.OwnerCount += uint32(delta)
		} else if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
		return
	}

	_ = tx.AdjustOwnerCount(ctx.View, accountID, delta)
}
