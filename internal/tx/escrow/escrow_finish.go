package escrow

import (
	"encoding/hex"
	"sort"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
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

	// ComputationAllowance is the gas budget for the escrow's FinishFunction
	// (SmartEscrow). Required when finishing an escrow that has a FinishFunction.
	ComputationAllowance *uint32 `json:"ComputationAllowance,omitempty" xrpl:"ComputationAllowance,omitempty"`

	// wasmData/wasmDataSet capture a SmartEscrow finish function's update_data
	// mutation during Apply so it can be re-applied to the surviving escrow on
	// tecWASM_REJECTED, after the tx sandbox is discarded. Not serialized.
	wasmData    []byte
	wasmDataSet bool
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

	if err := tx.CheckFlags(e.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	if e.Owner == "" {
		return tx.Errorf(tx.TemMALFORMED, "Owner is required")
	}

	// Both Condition and Fulfillment must be present or absent together
	// Reference: rippled Escrow.cpp:644-646
	// "Present" means the field exists in the transaction (even if empty value).
	hasCondition := e.Condition != nil
	hasFulfillment := e.Fulfillment != nil
	if hasCondition != hasFulfillment {
		return tx.Errorf(tx.TemMALFORMED, "Condition and Fulfillment must be provided together")
	}

	// Validate CredentialIDs field
	// Reference: rippled Escrow.cpp preflight() calls credentials::checkFields()
	// Use HasField to detect empty arrays from binary parsing where omitempty
	// causes the Go struct field to be nil even though the field was present.
	if e.CredentialIDs != nil || e.HasField("CredentialIDs") {
		if len(e.CredentialIDs) == 0 || len(e.CredentialIDs) > 8 {
			return tx.Errorf(tx.TemMALFORMED, "CredentialIDs array size is invalid")
		}
		seen := make(map[string]bool, len(e.CredentialIDs))
		for _, id := range e.CredentialIDs {
			if seen[id] {
				return tx.Errorf(tx.TemMALFORMED, "Duplicate credential ID")
			}
			seen[id] = true
		}
	}

	return nil
}

func (e *EscrowFinish) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(e)
}

// CalculateBaseFee mirrors rippled's EscrowFinish::calculateBaseFee: the
// multisigned base fee plus, for a fulfillment-bearing EscrowFinish, a
// crypto-condition surcharge of base * (32 + fulfillment.size()/16), where
// fulfillment.size() is the decoded byte length. The CustomBaseFeeCalculator
// dispatch in preclaim.go skips the multisig multiplier, so it is applied here.
// Reference: rippled Escrow.cpp:682-693, Transactor.cpp:229-244
func (e *EscrowFinish) CalculateBaseFee(view tx.LedgerView, config tx.EngineConfig) uint64 {
	fs := escrowFeeSettings(view)

	base := config.BaseFee
	if fs != nil {
		base = fs.GetBaseFee()
	}

	fee := tx.CalculateMultiSigFee(base, len(e.GetCommon().Signers))

	if e.Fulfillment != nil {
		fee += base * (32 + vlByteLen(*e.Fulfillment)/16)
	}

	// SmartEscrow: the ComputationAllowance gas budget is priced at gasPrice
	// micro-drops per unit, rounded up to whole drops.
	// Reference: rippled EscrowFinish.cpp calculateBaseFee lines 167-174
	if e.ComputationAllowance != nil {
		gasPrice := uint64(state.DefaultGasPrice)
		if fs != nil {
			gasPrice = uint64(fs.GetGasPrice())
		}
		fee += (uint64(*e.ComputationAllowance)*gasPrice)/microDropsPerDrop + 1
	}

	return fee
}

// ApplyOnTec implements TecApplier. When tecEXPIRED is returned, this re-runs
// credential expiration deletion against the engine's view so the side-effects
// (credential deletion, owner count adjustment) persist even though the tx
// sandbox is rolled back for tec results.
// Reference: rippled Transactor.cpp - tecEXPIRED re-applies removeExpiredCredentials
func (e *EscrowFinish) ApplyOnTec(ctx *tx.ApplyContext) tx.Result {
	removeExpiredCredentials(ctx, e.CredentialIDs)
	return tx.TecEXPIRED
}

// ApplyWasmDataOnTec implements tx.WasmDataApplier. On tecWASM_REJECTED, it
// persists the finish function's update_data mutation to the escrow that
// survived the rejected finish, re-applying the Data write that the discarded
// sandbox carried. A no-op when the finish function did not mutate Data.
// Reference: rippled Transactor.cpp modifyWasmDataFields.
func (e *EscrowFinish) ApplyWasmDataOnTec(ctx *tx.ApplyContext) {
	if !e.wasmDataSet {
		return
	}
	ownerID, err := state.DecodeAccountID(e.Owner)
	if err != nil {
		return
	}
	escrowKey := keylet.Escrow(ownerID, e.OfferSequence)
	escrowData, err := ctx.View.Read(escrowKey)
	if err != nil || escrowData == nil {
		return
	}
	newEscrow, err := setEscrowData(escrowData, e.wasmData)
	if err != nil {
		return
	}
	_ = ctx.View.Update(escrowKey, newEscrow)
}

// Apply applies an EscrowFinish transaction
// Reference: rippled Escrow.cpp EscrowFinish::preclaim() + doApply()
func (e *EscrowFinish) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("escrow finish apply",
		"account", e.Account,
		"owner", e.Owner,
		"offerSequence", e.OfferSequence,
	)

	rules := ctx.Rules()

	// Amendment-gated check: CredentialIDs requires Credentials amendment
	// Reference: rippled Escrow.cpp preflight() credential check
	if len(e.CredentialIDs) > 0 && !rules.Enabled(amendment.FeatureCredentials) {
		return tx.TemDISABLED
	}

	// ComputationAllowance requires the SmartEscrow amendment.
	// Reference: rippled EscrowFinish.cpp preflight line 80
	if e.ComputationAllowance != nil && !rules.Enabled(amendment.FeatureSmartEscrow) {
		return tx.TemDISABLED
	}

	// ComputationAllowance bounds: the WASM runtime can be disabled by fee voting
	// (compute limit 0), and the allowance must be a positive value within that
	// limit. Reference: rippled EscrowFinish.cpp preflight lines 101-117
	if e.ComputationAllowance != nil {
		computeLimit := state.DefaultExtensionComputeLimit
		if fs := escrowFeeSettings(ctx.View); fs != nil {
			computeLimit = fs.GetExtensionComputeLimit()
		}
		if computeLimit == 0 {
			return tx.TemTEMP_DISABLED
		}
		if *e.ComputationAllowance == 0 || *e.ComputationAllowance > computeLimit {
			return tx.TemBAD_LIMIT
		}
	}

	// --- Preclaim: credential validation (before time checks) ---
	// Reference: rippled EscrowFinish::preclaim() calls credentials::valid()
	// This must run before doApply's time checks because rippled's preclaim
	// runs before doApply.
	if len(e.CredentialIDs) > 0 && rules.Enabled(amendment.FeatureCredentials) {
		if result := validateCredentials(ctx, e.CredentialIDs); result != tx.TesSUCCESS {
			return result
		}
	}

	ownerID, err := state.DecodeAccountID(e.Owner)
	if err != nil {
		return tx.TemINVALID
	}

	// Find the escrow
	escrowKey := keylet.Escrow(ownerID, e.OfferSequence)
	escrowData, err := ctx.View.Read(escrowKey)
	if err != nil || escrowData == nil {
		ctx.Log.Warn("escrow finish: escrow not found",
			"owner", e.Owner,
			"offerSequence", e.OfferSequence,
		)
		return tx.TecNO_TARGET
	}

	// Parse escrow
	escrowEntry, err := state.ParseEscrow(escrowData)
	if err != nil {
		ctx.Log.Error("escrow finish: failed to parse escrow", "error", err)
		return tx.TefINTERNAL
	}

	isXRP := escrowEntry.IsXRP

	// SmartEscrow preclaim: the escrow's FinishFunction and the transaction's
	// ComputationAllowance must be present together. Done in preclaim ordering
	// (before the time/condition checks) so a field mismatch is reported even
	// when the fulfillment is also wrong.
	// Reference: rippled EscrowFinish::preclaim() lines 260-278
	if result := smartEscrowFinishPreclaim(ctx, e, escrowData); result != tx.TesSUCCESS {
		return result
	}

	// Token escrow preclaim
	// Reference: rippled EscrowFinish::preclaim() lines 760-793
	if !isXRP && rules.Enabled(amendment.FeatureTokenEscrow) {
		escrowAmount := reconstructAmountFromEscrow(escrowEntry)
		if escrowEntry.MPTIssuanceID != "" {
			if result := escrowFinishPreclaimMPT(ctx.View, escrowEntry.DestinationID, escrowAmount); result != tx.TesSUCCESS {
				return result
			}
		} else if escrowAmount.Issuer != "" {
			if result := escrowFinishPreclaimIOU(ctx.View, escrowEntry.DestinationID, escrowAmount); result != tx.TesSUCCESS {
				return result
			}
		}
	}

	closeTime := ctx.Config.ParentCloseTime

	// --- doApply: Time validation ---
	// Reference: rippled Escrow.cpp doApply() lines 1030-1055
	if rules.Enabled(amendment.FeatureFix1571) {
		// fix1571: FinishAfter check — close time must be strictly after finish time
		if escrowEntry.FinishAfter > 0 && closeTime <= escrowEntry.FinishAfter {
			return tx.TecNO_PERMISSION
		}
		// fix1571: CancelAfter check — if past cancel time, finish not allowed
		if escrowEntry.CancelAfter > 0 && closeTime > escrowEntry.CancelAfter {
			return tx.TecNO_PERMISSION
		}
	} else {
		// Pre-fix1571: both use <= comparison (known bug in cancel check)
		if escrowEntry.FinishAfter > 0 && closeTime <= escrowEntry.FinishAfter {
			return tx.TecNO_PERMISSION
		}
		if escrowEntry.CancelAfter > 0 && closeTime <= escrowEntry.CancelAfter {
			return tx.TecNO_PERMISSION
		}
	}

	// Load the destination account once: it is needed for the deposit-auth
	// check(s) and for the final transfer. rippled peeks sled here.
	// Reference: rippled-smart-escrow EscrowFinish.cpp:326-327
	destIsSelf := ctx.AccountID == escrowEntry.DestinationID
	destKey := keylet.Account(escrowEntry.DestinationID)
	var destAccount *state.AccountRoot
	destMissing := false
	if destIsSelf {
		destAccount = ctx.Account
	} else {
		destData, readErr := ctx.View.Read(destKey)
		if readErr != nil || destData == nil {
			destMissing = true
		} else {
			parsed, parseErr := state.ParseAccountRoot(destData)
			if parseErr != nil {
				return tx.TefINTERNAL
			}
			destAccount = parsed
		}
	}

	// SmartEscrow relocates deposit-authorization to BEFORE the crypto-condition
	// and WASM steps, so an unauthorized finisher is rejected before the contract
	// runs. Without SmartEscrow the same check runs after the condition (below).
	// rippled gates the two positions on the amendment; it does not skip the
	// check under SmartEscrow.
	// Reference: rippled-smart-escrow EscrowFinish.cpp:328-338
	if rules.Enabled(amendment.FeatureSmartEscrow) {
		if destMissing {
			return tx.TecNO_DST
		}
		if result := checkEscrowDepositAuth(ctx, e, escrowEntry, destAccount); result != tx.TesSUCCESS {
			return result
		}
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
			return tx.TecCRYPTOCONDITION_ERROR
		}
	} else {
		// Escrow has a condition — fulfillment is required (non-empty)
		if txFulfillment == "" {
			ctx.Log.Warn("escrow finish: fulfillment required but not provided")
			return tx.TecCRYPTOCONDITION_ERROR
		}

		// Condition in tx must match condition on escrow (case-insensitive hex comparison)
		if !strings.EqualFold(txCondition, escrowEntry.Condition) {
			ctx.Log.Warn("escrow finish: condition mismatch")
			return tx.TecCRYPTOCONDITION_ERROR
		}

		// Verify fulfillment matches condition
		if err := validateCryptoCondition(txFulfillment, escrowEntry.Condition); err != nil {
			ctx.Log.Debug("escrow finish: fulfillment verification failed", "error", err)
			return tx.TecCRYPTOCONDITION_ERROR
		}
		ctx.Log.Debug("escrow finish: fulfillment verified successfully")
	}

	// SmartEscrow: if the escrow carries a finish function, execute it now that
	// the condition (if any) has been verified and — under SmartEscrow — deposit
	// authorization has already passed (checked above). A non-positive result
	// rejects the finish (tecWASM_REJECTED). Field pairing was checked in preclaim.
	// Reference: rippled EscrowFinish::doApply() lines 406-457
	if r := runSmartEscrow(ctx, e, escrowData); r != tx.TesSUCCESS {
		return r
	}

	// Deposit-authorization (non-SmartEscrow): runs after the condition check.
	// Under SmartEscrow this already ran earlier (before the WASM step); the two
	// positions are the complementary halves of rippled's amendment-gated layout.
	// Reference: rippled-smart-escrow EscrowFinish.cpp:393-403
	if !rules.Enabled(amendment.FeatureSmartEscrow) {
		if destMissing {
			return tx.TecNO_DST
		}
		if result := checkEscrowDepositAuth(ctx, e, escrowEntry, destAccount); result != tx.TesSUCCESS {
			return result
		}
	}

	// Remove escrow from owner directory
	// Reference: rippled Escrow.cpp doApply() lines 1120-1129
	ownerDirKey := keylet.OwnerDir(escrowEntry.Account)
	state.DirRemove(ctx.View, ownerDirKey, escrowEntry.OwnerNode, escrowKey.Key, false)

	// Remove escrow from destination directory (if cross-account)
	// Reference: rippled Escrow.cpp doApply() lines 1132-1140
	if escrowEntry.HasDestNode {
		destDirKey := keylet.OwnerDir(escrowEntry.DestinationID)
		state.DirRemove(ctx.View, destDirKey, escrowEntry.DestinationNode, escrowKey.Key, false)
	}

	// Transfer the escrowed amount to destination
	// Reference: rippled Escrow.cpp doApply() lines 1142-1184
	if isXRP {
		// XRP: credit destination balance
		destAccount.Balance += escrowEntry.Amount
	} else {
		if !rules.Enabled(amendment.FeatureTokenEscrow) {
			return tx.TemDISABLED
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

			if result := escrowUnlockMPT(
				ctx.View,
				escrowEntry.Account,
				escrowEntry.DestinationID,
				finalAmount,
				mptHexID,
				createAsset,
				destAccount.Balance,
				destAccount.OwnerCount,
				escrowEntry.DestinationID,
				ctx.Config.ReserveBase,
				ctx.Config.ReserveIncrement,
			); result != tx.TesSUCCESS {
				return result
			}
		} else {
			// IOU unlock
			// Reference: rippled Escrow.cpp escrowUnlockApplyHelper<Issue> lines 809-942
			if result := escrowUnlockIOU(
				ctx.View,
				lockedRate,
				destAccount.Balance,
				destAccount.OwnerCount,
				escrowEntry.DestinationID,
				escrowAmount,
				escrowEntry.Account,
				escrowEntry.DestinationID,
				createAsset,
				ctx.Config.ReserveBase,
				ctx.Config.ReserveIncrement,
			); result != tx.TesSUCCESS {
				return result
			}
		}

		// Remove escrow from issuer's owner directory
		// Reference: rippled Escrow.cpp doApply() lines 1174-1183
		if escrowEntry.HasIssuerNode {
			issuerID, issuerErr := state.DecodeAccountID(escrowAmount.Issuer)
			if issuerErr == nil {
				issuerDirKey := keylet.OwnerDir(issuerID)
				state.DirRemove(ctx.View, issuerDirKey, escrowEntry.IssuerNode, escrowKey.Key, false)
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
		if result := ctx.UpdateAccountRoot(escrowEntry.DestinationID, destAccount); result != tx.TesSUCCESS {
			return result
		}
	}

	// Delete the escrow
	// Reference: rippled Escrow.cpp doApply() line 1194: ctx_.view().erase(slep);
	if err := ctx.View.Erase(escrowKey); err != nil {
		ctx.Log.Error("escrow finish: failed to erase escrow", "error", err)
		return tx.TefINTERNAL
	}

	// Decrement OwnerCount for the escrow owner, releasing the reserve the escrow
	// held (a FinishFunction escrow may have consumed more than one slot).
	// Reference: rippled-smart-escrow EscrowFinish.cpp:534-538
	adjustOwnerCount(ctx, ownerID, -int(escrowDataReserve(escrowData)))

	return tx.TesSUCCESS
}

// checkEscrowDepositAuth runs the expired-credential pass and the deposit-
// authorization check for an EscrowFinish, mirroring rippled's
// verifyDepositPreauth: removeExpired (→ tecEXPIRED) followed by the
// lsfDepositAuth / DepositPreauth / credential-preauth checks. destAccount is
// the already-loaded destination account.
// Reference: rippled CredentialHelpers.cpp verifyDepositPreauth()
func checkEscrowDepositAuth(ctx *tx.ApplyContext, e *EscrowFinish, escrowEntry *state.EscrowData, destAccount *state.AccountRoot) tx.Result {
	rules := ctx.Rules()

	// Expired credentials are removed first and yield tecEXPIRED.
	if len(e.CredentialIDs) > 0 && rules.Enabled(amendment.FeatureCredentials) {
		if removeExpiredCredentials(ctx, e.CredentialIDs) {
			return tx.TecEXPIRED
		}
	}

	if rules.Enabled(amendment.FeatureDepositAuth) {
		if (destAccount.Flags&state.LsfDepositAuth) != 0 && ctx.AccountID != escrowEntry.DestinationID {
			// Account-based DepositPreauth.
			preauthKey := keylet.DepositPreauth(escrowEntry.DestinationID, ctx.AccountID)
			if exists, _ := ctx.View.Exists(preauthKey); !exists {
				// Fall back to credential-based preauth.
				if len(e.CredentialIDs) > 0 && rules.Enabled(amendment.FeatureCredentials) {
					if result := authorizedDepositPreauth(ctx, e.CredentialIDs, escrowEntry.DestinationID); result != tx.TesSUCCESS {
						return result
					}
				} else {
					return tx.TecNO_PERMISSION
				}
			}
		}
	}

	return tx.TesSUCCESS
}

// validateCredentials implements rippled's credentials::valid() preclaim check.
// For each credential ID, it reads the Credential SLE and validates:
// 1. The credential exists
// 2. The credential's Subject matches the transaction sender (src)
// 3. The credential has been accepted (lsfAccepted flag)
// Reference: rippled CredentialHelpers.cpp credentials::valid()
func validateCredentials(ctx *tx.ApplyContext, credentialIDs []string) tx.Result {
	for _, credIDHex := range credentialIDs {
		credHash, err := hex.DecodeString(credIDHex)
		if err != nil || len(credHash) != 32 {
			return tx.TecBAD_CREDENTIALS
		}

		var credID [32]byte
		copy(credID[:], credHash)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			// Credential doesn't exist
			return tx.TecBAD_CREDENTIALS
		}

		credEntry, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return tx.TecBAD_CREDENTIALS
		}

		// Subject must match the transaction sender
		if credEntry.Subject != ctx.AccountID {
			return tx.TecBAD_CREDENTIALS
		}

		// Credential must be accepted
		if (credEntry.Flags & credential.LsfCredentialAccepted) == 0 {
			return tx.TecBAD_CREDENTIALS
		}
	}

	return tx.TesSUCCESS
}

// removeExpiredCredentials checks for expired credentials and deletes them.
// Returns true if any credentials were expired.
// Reference: rippled credentials::removeExpired() in CredentialHelpers.cpp
func removeExpiredCredentials(ctx *tx.ApplyContext, credentialIDs []string) bool {
	if len(credentialIDs) == 0 {
		return false
	}

	closeTime := ctx.Config.ParentCloseTime
	anyExpired := false

	for _, idHex := range credentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			continue
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			continue
		}

		cred, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			continue
		}

		if cred.Expiration != nil && closeTime > *cred.Expiration {
			_ = credential.DeleteSLE(ctx.View, credKey, cred)
			anyExpired = true
		}
	}

	return anyExpired
}

// authorizedDepositPreauth implements rippled's credentials::authorizedDepositPreauth().
// It reads each credential, extracts the (Issuer, CredentialType) pairs,
// sorts them, and checks if a credential-based DepositPreauth exists for the destination.
// Reference: rippled CredentialHelpers.cpp credentials::authorizedDepositPreauth()
func authorizedDepositPreauth(ctx *tx.ApplyContext, credentialIDs []string, dst [20]byte) tx.Result {
	type credPair struct {
		issuer   [20]byte
		credType []byte
	}

	pairs := make([]credPair, 0, len(credentialIDs))
	for _, credIDHex := range credentialIDs {
		credHash, err := hex.DecodeString(credIDHex)
		if err != nil || len(credHash) != 32 {
			return tx.TefINTERNAL
		}

		var credID [32]byte
		copy(credID[:], credHash)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			return tx.TefINTERNAL
		}

		credEntry, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return tx.TefINTERNAL
		}

		pairs = append(pairs, credPair{
			issuer:   credEntry.Issuer,
			credType: credEntry.CredentialType,
		})
	}

	// Sort by (issuer, credType) to match rippled's sorted set
	sort.Slice(pairs, func(i, j int) bool {
		cmp := compareBytesSlice(pairs[i].issuer[:], pairs[j].issuer[:])
		if cmp != 0 {
			return cmp < 0
		}
		return compareBytesSlice(pairs[i].credType, pairs[j].credType) < 0
	})

	// Convert to keylet.CredentialPair for keylet computation
	sortedCreds := make([]keylet.CredentialPair, len(pairs))
	for i, p := range pairs {
		sortedCreds[i] = keylet.CredentialPair{
			Issuer:         p.issuer,
			CredentialType: p.credType,
		}
	}

	// Check if credential-based DepositPreauth exists
	dpKey := keylet.DepositPreauthCredentials(dst, sortedCreds)
	if exists, _ := ctx.View.Exists(dpKey); !exists {
		return tx.TecNO_PERMISSION
	}

	return tx.TesSUCCESS
}

// compareBytesSlice compares two byte slices lexicographically.
func compareBytesSlice(a, b []byte) int {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

// adjustOwnerCount adjusts the OwnerCount of the given account by delta.
// When the target account is ctx.Account (the transaction sender), it modifies
// ctx.Account directly (the engine writes it back). Otherwise it delegates to
// tx.AdjustOwnerCount which reads/writes through the view.
func adjustOwnerCount(ctx *tx.ApplyContext, accountID [20]byte, delta int) {
	if accountID == ctx.AccountID {
		if delta >= 0 {
			ctx.Account.OwnerCount += uint32(delta)
		} else {
			dec := uint32(-delta)
			if ctx.Account.OwnerCount < dec {
				dec = ctx.Account.OwnerCount
			}
			ctx.Account.OwnerCount -= dec
		}
		return
	}

	_ = tx.AdjustOwnerCount(ctx.View, accountID, delta)
}
