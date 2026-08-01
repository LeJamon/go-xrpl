// Package escrow implements EscrowCreate, EscrowFinish, and EscrowCancel transactions.
package escrow

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// EscrowCreate creates an escrow that holds XRP until certain conditions are met.
type EscrowCreate struct {
	tx.BaseTx

	// Amount is the amount of XRP to escrow (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// Destination is the account to receive the XRP (required)
	Destination string `json:"Destination" xrpl:"Destination"`

	// DestinationTag is an arbitrary tag for the destination (optional)
	DestinationTag *uint32 `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`

	// CancelAfter is the time after which the escrow can be cancelled (optional)
	CancelAfter *uint32 `json:"CancelAfter,omitempty" xrpl:"CancelAfter,omitempty"`

	// FinishAfter is the time after which the escrow can be finished (optional)
	FinishAfter *uint32 `json:"FinishAfter,omitempty" xrpl:"FinishAfter,omitempty"`

	// Condition is the crypto-condition that must be fulfilled (optional).
	// Pointer to distinguish "not set" (nil) from "set to empty" (ptr to "").
	Condition *string `json:"Condition,omitempty" xrpl:"Condition,omitempty"`
}

func NewEscrowCreate(account, destination string, amount tx.Amount) *EscrowCreate {
	return &EscrowCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, account),
		Amount:      amount,
		Destination: destination,
	}
}

func (e *EscrowCreate) TxType() tx.Type {
	return tx.TypeEscrowCreate
}

// Reference: rippled Escrow.cpp EscrowCreate::preflight()
func (e *EscrowCreate) Validate() error {
	if err := e.BaseTx.Validate(); err != nil {
		return err
	}

	// sfDestination is a required field; a missing one is temMALFORMED before the
	// per-type preflight body (which lives in PreflightRules).
	if err := tx.CheckDestRequired(e.Destination); err != nil {
		return err
	}

	return nil
}

func (e *EscrowCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(e)
}

// GetFlagsMask returns the invalid-flags mask enforced at preflight0: any
// non-universal flag is rejected.
// Reference: rippled Escrow.cpp EscrowCreate::getFlagsMask.
func (e *EscrowCreate) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// CheckExtraFeatures enforces the MPT-escrow featureMPTokensV1 requirement ahead
// of the preflight body when fixCleanup3_2_0 is active, so a disabled MPTokensV1
// surfaces temDISABLED from the common framework. Pre-amendment the requirement
// is enforced by the per-asset preflight helper instead.
// Reference: rippled Escrow.cpp EscrowCreate::checkExtraFeatures.
func (e *EscrowCreate) CheckExtraFeatures(rules *amendment.Rules) error {
	if rules.FixCleanup3_2_0Enabled() && e.Amount.IsMPT() && !rules.Enabled(amendment.FeatureMPTokensV1) {
		return ter.Errorf(ter.TemDISABLED, "MPT escrow requires MPTokensV1")
	}
	return nil
}

// PreflightRules is the amendment-aware body of rippled's EscrowCreate::preflight.
// The whole sequence lives here (rather than split across Validate) so that a
// transaction malformed in two ways surfaces the same tem* code rippled reports:
// the rules-gated amount checks stay ahead of the rules-free expiration/condition
// checks, matching rippled's intra-preflight order.
// Reference: rippled Escrow.cpp EscrowCreate::preflight.
func (e *EscrowCreate) PreflightRules(rules *amendment.Rules) error {
	// Amount validity. For XRP the zero/negative check is unconditional. For
	// non-XRP amounts every check is gated behind featureTokenEscrow: disabled →
	// temBAD_AMOUNT; enabled → the per-asset helper (zero/negative → temBAD_AMOUNT,
	// then the reserved "XRP" currency code → temBAD_CURRENCY, MPT → temDISABLED).
	if !e.Amount.IsNative() {
		if !rules.Enabled(amendment.FeatureTokenEscrow) {
			return ter.Errorf(ter.TemBAD_AMOUNT, "cannot escrow non-XRP without TokenEscrow")
		}
		if err := escrowCreateNonXRPPreflight(rules, e.Amount); err != nil {
			return err
		}
	} else if e.Amount.IsZero() || e.Amount.IsNegative() {
		return ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
	}

	// Must have at least one timeout value.
	if e.CancelAfter == nil && e.FinishAfter == nil {
		return ter.Errorf(ter.TemBAD_EXPIRATION, "must specify CancelAfter or FinishAfter")
	}

	// When both are present, CancelAfter must be strictly after FinishAfter.
	if e.CancelAfter != nil && e.FinishAfter != nil && *e.CancelAfter <= *e.FinishAfter {
		return ter.Errorf(ter.TemBAD_EXPIRATION, "CancelAfter must be after FinishAfter")
	}

	// An escrow must specify a FinishAfter or a Condition, otherwise it could be
	// finished immediately.
	if e.FinishAfter == nil && (e.Condition == nil || *e.Condition == "") {
		return ter.Errorf(ter.TemMALFORMED, "escrow must specify FinishAfter or Condition")
	}

	// Condition format.
	if e.Condition != nil {
		if *e.Condition == "" {
			return ter.Errorf(ter.TemMALFORMED, "empty condition")
		}
		if err := ValidateConditionFormat(*e.Condition); err != nil {
			return ter.Errorf(ter.TemMALFORMED, "invalid condition")
		}
	}

	return nil
}

// Preclaim performs stateful validation for EscrowCreate before doApply.
//
// The destination/token checks run here (ahead of the time checks) to match
// rippled's preclaim ordering: rippled checks destination-exists, the
// pseudo-account guard, and the IOU/MPT preclaim helpers in preclaim
// (Escrow.cpp:362-395), and only then runs the time checks in doApply
// (:457-489). A past FinishAfter with a missing destination must surface
// tecNO_DST, not tecNO_PERMISSION.
//
// The time checks stay in Preclaim so that the engine's TapRETRY gate can
// suppress tec results during retry passes, matching rippled's
// likelyToClaimFee semantics. Without this, replay-on-close would apply
// tecNO_PERMISSION on the final pass even though the initial apply succeeded.
// Reference: rippled Escrow.cpp EscrowCreate::preclaim() lines 362-395 and
// doApply() lines 457-489.
func (e *EscrowCreate) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	rules := config.RequireRules()
	closeTime := config.ParentCloseTime

	// The flag mask (GetFlagsMask), the non-XRP amount validity checks, and the
	// fix1571 FinishAfter-or-Condition check all run in preflight now (GetFlagsMask
	// and PreflightRules); Preclaim keeps only the ledger-state-dependent checks.

	accountID, err := state.DecodeAccountID(e.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	destID, err := state.DecodeAccountID(e.Destination)
	if err != nil {
		return ter.TemINVALID
	}

	// Destination must exist and not be a pseudo-account.
	// Reference: rippled Escrow.cpp:369-378
	destAccount, result := readDestinationForEscrow(view, destID)
	if result != ter.TesSUCCESS {
		return result
	}
	if destAccount.IsPseudoAccount() {
		return ter.TecNO_PERMISSION
	}

	// Non-XRP token preclaim helpers.
	// Reference: rippled Escrow.cpp:380-393
	if !e.Amount.IsNative() {
		if e.Amount.IsMPT() {
			if result := escrowCreatePreclaimMPT(view, rules, accountID, destID, e.Amount); result != ter.TesSUCCESS {
				return result
			}
		} else {
			if result := escrowCreatePreclaimIOU(
				view,
				accountID,
				destID,
				e.Amount,
				config.NumberContext(),
			); result != ter.TesSUCCESS {
				return result
			}
		}
	}

	// Time validation against parent close time. after() means strictly greater
	// than.
	// Reference: rippled EscrowCreate.cpp doApply().
	if e.CancelAfter != nil && closeTime > *e.CancelAfter {
		return ter.TecNO_PERMISSION
	}
	if e.FinishAfter != nil && closeTime > *e.FinishAfter {
		return ter.TecNO_PERMISSION
	}

	return ter.TesSUCCESS
}

// readDestinationForEscrow reads and parses the destination AccountRoot from a
// LedgerView, returning tecNO_DST if it is absent. Used by Preclaim where there
// is no ApplyContext.
func readDestinationForEscrow(view tx.LedgerView, destID [20]byte) (*state.AccountRoot, ter.Result) {
	data, err := view.Read(keylet.Account(destID))
	if err != nil || data == nil {
		return nil, ter.TecNO_DST
	}
	acct, err := state.ParseAccountRoot(data)
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	return acct, ter.TesSUCCESS
}

// escrowCreateNonXRPPreflight runs the per-asset preflight validity checks for a
// non-XRP escrow amount, assuming featureTokenEscrow is enabled. Following
// rippled's helper order the zero/negative check comes first (temBAD_AMOUNT),
// then the reserved "XRP" currency code (temBAD_CURRENCY); MPT amounts require
// featureMPTokensV1 (temDISABLED) and must be positive.
// Reference: rippled Escrow.cpp escrowCreatePreflightHelper<Issue>/<MPTIssue>.
func escrowCreateNonXRPPreflight(rules *amendment.Rules, amount tx.Amount) error {
	if amount.IsMPT() {
		// Post-fixCleanup3_2_0 the MPTokensV1 requirement is enforced earlier by
		// CheckExtraFeatures (temDISABLED from the common framework); here it
		// only guards the pre-amendment path.
		if !rules.FixCleanup3_2_0Enabled() && !rules.Enabled(amendment.FeatureMPTokensV1) {
			return ter.Errorf(ter.TemDISABLED, "MPT escrow requires MPTokensV1")
		}
		if amount.IsZero() || amount.IsNegative() {
			return ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
		}
		return nil
	}

	if amount.IsZero() || amount.IsNegative() {
		return ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
	}
	if amount.Currency == "" || amount.Currency == "XRP" {
		return ter.Errorf(ter.TemBAD_CURRENCY, "cannot escrow XRP as IOU")
	}
	return nil
}

// Apply applies an EscrowCreate transaction
// Reference: rippled Escrow.cpp EscrowCreate::doApply()
func (e *EscrowCreate) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("escrow create apply",
		"account", e.Account,
		"destination", e.Destination,
		"amount", e.Amount,
		"finishAfter", e.FinishAfter,
		"cancelAfter", e.CancelAfter,
	)

	rules := ctx.Rules()

	// The non-XRP preflight gate (temBAD_AMOUNT when featureTokenEscrow is off),
	// the per-asset validity checks, and the fix1571 FinishAfter-or-Condition
	// check all run in Preclaim, the earliest rules-aware point. See Preclaim.

	isNative := e.Amount.IsNative()

	// Reserve and funding checks run before the destination checks, matching
	// rippled's doApply order (reserve/unfunded then the destination block).
	// Reference: rippled Escrow.cpp:496-509
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if ctx.Account.Balance < reserve {
		ctx.Log.Warn("escrow create: insufficient reserve",
			"balance", ctx.Account.Balance,
			"reserve", reserve,
		)
		return ter.TecINSUFFICIENT_RESERVE
	}

	// For XRP escrows, also check that the sender can afford the amount
	// on top of the reserve. IOU escrows are deducted from trust lines,
	// not the XRP balance.
	// Reference: rippled Escrow.cpp:505-508
	if isNative {
		drops := e.Amount.Drops()
		if drops <= 0 {
			return ter.TemINVALID
		}
		if ctx.Account.Balance < reserve+uint64(drops) {
			ctx.Log.Warn("escrow create: unfunded",
				"balance", ctx.Account.Balance,
				"needed", reserve+uint64(drops),
			)
			return ter.TecUNFUNDED
		}
	}

	// Verify destination exists and is not a pseudo-account. The destination
	// existence + pseudo-account + token preclaim checks were already run in
	// Preclaim; this re-read mirrors rippled's doApply destination block, which
	// follows the reserve/unfunded checks.
	// Reference: rippled Escrow.cpp:511-526
	destAccount, destID, result := ctx.LookupDestination(e.Destination)
	if result != ter.TesSUCCESS {
		ctx.Log.Warn("escrow create: destination lookup failed",
			"destination", e.Destination,
			"result", result,
		)
		return result
	}

	// Destination tag check
	// Reference: rippled Escrow.cpp:517-519
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && e.DestinationTag == nil {
		ctx.Log.Warn("escrow create: destination tag required",
			"destination", e.Destination,
		)
		return ter.TecDST_TAG_NEEDED
	}

	accountID, _ := state.DecodeAccountID(e.Account)
	sequence := e.GetCommon().SeqProxy()

	escrowKey := keylet.Escrow(accountID, sequence)

	// Capture transfer rate at escrow creation time.
	// This is stored in the escrow SLE so that at finish time the effective
	// rate is min(locked rate, current rate), protecting the destination from
	// issuer rate increases.
	// Reference: rippled Escrow.cpp EscrowCreate::doApply() lines 527-545
	var capturedTransferRate uint32
	if rules.Enabled(amendment.FeatureTokenEscrow) && !isNative {
		if e.Amount.IsMPT() {
			// MPT: get rate from issuance TransferFee
			mptKey, mptErr := mptIssuanceKeyFromHex(e.Amount.MPTIssuanceID())
			if mptErr == nil {
				issuanceData, _ := ctx.View.Read(mptKey)
				if issuanceData != nil {
					issuance, _ := state.ParseMPTokenIssuance(issuanceData)
					if issuance != nil {
						capturedTransferRate = getMPTTransferRate(issuance.TransferFee)
					}
				}
			}
		} else {
			// IOU: get rate from issuer account
			issuerID, _ := state.DecodeAccountID(e.Amount.Issuer)
			capturedTransferRate = tx.GetTransferRateByID(ctx.View, issuerID)
		}
	}

	// Insert the escrow into the owner directories BEFORE serializing it, so
	// the page indices are known and can be recorded on the Escrow object as
	// sfOwnerNode / sfDestinationNode / sfIssuerNode. rippled inserts the SLE
	// first and then mutates these node fields on it (Escrow.cpp:548-584);
	// because goXRPL serializes the SLE to bytes up front, the directory
	// inserts must precede serialization. DirInsert only references the escrow
	// key (not the object), so the ordering is equivalent.

	// Reference: rippled Escrow.cpp:550-559
	ownerDirKey := keylet.OwnerDir(accountID)
	ownerResult, err := state.DirInsert(ctx.View, ownerDirKey, escrowKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = accountID
	})
	if err != nil {
		ctx.Log.Error("escrow create: owner directory full", "error", err)
		return ter.TecDIR_FULL
	}
	ownerNode := ownerResult.Page

	// If cross-account, insert into destination's owner directory and record the
	// page in sfDestinationNode. Without it the Escrow SLE serializes differently
	// from rippled, diverging account_hash (issue #729). Note: rippled does NOT
	// increment the destination's OwnerCount for XRP escrows — only the creator's.
	// Reference: rippled Escrow.cpp:561-570
	var destNode uint64
	var hasDestNode bool
	if destID != accountID {
		destDirKey := keylet.OwnerDir(destID)
		destResult, derr := state.DirInsert(ctx.View, destDirKey, escrowKey.Key, false, func(dir *state.DirectoryNode) {
			dir.Owner = destID
		})
		if derr != nil {
			ctx.Log.Error("escrow create: destination directory full", "error", derr)
			return ter.TecDIR_FULL
		}
		destNode = destResult.Page
		hasDestNode = true
	}

	// For IOU escrows, also insert into the issuer's owner directory and record
	// the page in sfIssuerNode. This helps track the total locked balance.
	// Reference: rippled Escrow.cpp:572-584
	var issuerNode uint64
	var hasIssuerNode bool
	if !isNative && !e.Amount.IsMPT() {
		issuerID, issuerErr := state.DecodeAccountID(e.Amount.Issuer)
		if issuerErr == nil && issuerID != accountID && issuerID != destID {
			issuerDirKey := keylet.OwnerDir(issuerID)
			issuerResult, ierr := state.DirInsert(ctx.View, issuerDirKey, escrowKey.Key, false, func(dir *state.DirectoryNode) {
				dir.Owner = issuerID
			})
			if ierr != nil {
				ctx.Log.Error("escrow create: issuer directory full", "error", ierr)
				return ter.TecDIR_FULL
			}
			issuerNode = issuerResult.Page
			hasIssuerNode = true
		}
	}

	var condition string
	if e.Condition != nil {
		condition = *e.Condition
	}
	var seqPtr *uint32
	if rules.Enabled(amendment.FeatureFixIncludeKeyletFields) {
		sq := e.GetCommon().SeqProxy()
		seqPtr = &sq
	}
	escrowData, err := state.SerializeEscrow(accountID, destID, e.Amount, capturedTransferRate,
		ownerNode, destNode, hasDestNode, issuerNode, hasIssuerNode,
		e.FinishAfter, e.CancelAfter, condition,
		e.GetCommon().SourceTag, e.DestinationTag, seqPtr)
	if err != nil {
		return ctx.Internal("SerializeEscrow", err)
	}

	// Insert escrow - creation tracked automatically by ApplyStateTable
	if err := ctx.View.Insert(escrowKey, escrowData); err != nil {
		return ctx.Internal("insert escrow", err)
	}

	// Deduct the escrow amount from the sender.
	// Reference: rippled Escrow.cpp:587-599
	if isNative {
		// XRP: deduct from account balance
		ctx.Account.Balance -= uint64(e.Amount.Drops())
	} else if e.Amount.IsMPT() {
		// MPT: lock via MPToken/MPTIssuance fields
		// Reference: rippled View.cpp rippleLockEscrowMPT()
		if lockResult := escrowLockMPT(ctx.View, accountID, e.Amount); lockResult != ter.TesSUCCESS {
			return lockResult
		}
	} else {
		// IOU: lock via trust line (rippleCredit sender -> issuer)
		// Reference: rippled escrowLockApplyHelper<Issue>
		issuerID, issuerErr := state.DecodeAccountID(e.Amount.Issuer)
		if issuerErr != nil {
			return ter.TefINTERNAL
		}
		if issuerID == accountID {
			return ter.TecINTERNAL
		}
		if lockResult := escrowLockIOU(
			ctx.View,
			accountID,
			issuerID,
			e.Amount,
			ctx.NumberContext(),
		); lockResult != ter.TesSUCCESS {
			return lockResult
		}
	}

	// Increase owner count for the escrow creator
	ctx.Account.OwnerCount++

	return ter.TesSUCCESS
}

// escrowLockIOU locks an IOU amount by transferring it from sender to issuer
// via the trust line. This is the Go equivalent of rippled's
// escrowLockApplyHelper<Issue> which calls rippleCredit(sender, issuer, amount).
//
// rippled's rippleCredit() auto-creates trust lines if absent, but escrow
// locking intentionally does not: the sender must already hold the IOU, so a
// missing line means there is no balance to escrow (tecNO_LINE). A genuine view
// read error is the corrupt-ledger case (tecINTERNAL).
// Reference: rippled Escrow.cpp:408-431
func escrowLockIOU(
	view tx.LedgerView,
	senderID, issuerID [20]byte,
	amount tx.Amount,
	numberContext state.NumberContext,
) ter.Result {
	return rippleCreditEscrow(
		view,
		senderID,
		issuerID,
		amount,
		ter.TecINTERNAL,
		ter.TecNO_LINE,
		numberContext,
	)
}
