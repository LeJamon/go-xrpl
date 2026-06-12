// Package escrow implements EscrowCreate, EscrowFinish, and EscrowCancel transactions.
package escrow

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// maxMPTokenAmount is the maximum MPT value (int64 max).
// Reference: rippled include/xrpl/protocol/STAmount.h maxMPTokenAmount
const maxMPTokenAmount int64 = 0x7FFFFFFFFFFFFFFF

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

	// The tfUniversalMask flag check is gated on fix1543 and runs in Preclaim,
	// where the amendment rules are available.

	if err := tx.CheckDestRequired(e.Destination); err != nil {
		return err
	}

	// For XRP the zero/negative check runs unconditionally in preflight. For
	// non-XRP amounts rippled gates every check behind featureTokenEscrow: with
	// the amendment disabled a non-XRP amount is temBAD_AMOUNT, and with it
	// enabled the per-asset helper runs (zero/negative, MPT range,
	// temBAD_CURRENCY for the reserved "XRP" currency code). Those amendment-
	// dependent checks are deferred to Preclaim (see L1).
	//
	// The lone exception is the "XRP"/empty IOU currency code: the binary codec
	// cannot even serialize it, so the transaction can never be hashed and would
	// surface tefINTERNAL before Preclaim runs. It is therefore rejected here in
	// Validate with temBAD_CURRENCY (matching the amendment-enabled outcome).
	// Reference: rippled Escrow.cpp preflight lines 130-148, escrowCreatePreflightHelper<Issue>
	if e.Amount.IsNative() {
		if e.Amount.IsZero() || e.Amount.IsNegative() {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Amount must be positive")
		}
	} else if !e.Amount.IsMPT() {
		if e.Amount.Currency == "" || e.Amount.Currency == "XRP" {
			return tx.Errorf(tx.TemBAD_CURRENCY, "cannot escrow XRP as IOU")
		}
	}

	// Must have at least one timeout value
	// Reference: rippled Escrow.cpp:151-152
	if e.CancelAfter == nil && e.FinishAfter == nil {
		return tx.Errorf(tx.TemBAD_EXPIRATION, "must specify CancelAfter or FinishAfter")
	}

	// If both times are specified, CancelAfter must be strictly after FinishAfter
	// Reference: rippled Escrow.cpp:156-158
	if e.CancelAfter != nil && e.FinishAfter != nil {
		if *e.CancelAfter <= *e.FinishAfter {
			return tx.Errorf(tx.TemBAD_EXPIRATION, "CancelAfter must be after FinishAfter")
		}
	}

	// NOTE: the fix1571 check (FinishAfter or Condition required) is amendment-
	// gated and runs in Preclaim, the earliest rules-aware point.

	// Validate condition format if present
	// Reference: rippled Escrow.cpp:170-190 condition deserialization
	if e.Condition != nil {
		if *e.Condition == "" {
			return tx.Errorf(tx.TemMALFORMED, "empty condition")
		}
		if err := ValidateConditionFormat(*e.Condition); err != nil {
			return tx.Errorf(tx.TemMALFORMED, "invalid condition")
		}
	}

	return nil
}

func (e *EscrowCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(e)
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
func (e *EscrowCreate) Preclaim(view tx.LedgerView, config tx.EngineConfig) tx.Result {
	rules := config.GetRules()
	closeTime := config.ParentCloseTime

	// fix1543: stray (non-universal) flags are rejected only once the amendment
	// is active. Reference: rippled Escrow.cpp:124. rippled runs this check first
	// in preflight; the gate is rules-aware, and go-xrpl exposes rules only at
	// Preclaim, so it runs after the common preflight/preclaim steps. The check
	// is the first statement of Preclaim, the earliest rules-aware point. For a
	// tx malformed in two ways this can surface a different tem code than rippled;
	// the result is tem-only (never enters a ledger) so there is no consensus
	// divergence.
	if rules.Enabled(amendment.FeatureFix1543) && (e.GetFlags()&tx.TfUniversalMask) != 0 {
		return tx.TemINVALID_FLAG
	}

	// Non-XRP preflight checks are all gated behind featureTokenEscrow: when it
	// is disabled, any non-XRP amount is temBAD_AMOUNT; when enabled, the
	// per-asset preflight helper runs (temBAD_CURRENCY for currency code "XRP",
	// temDISABLED/temBAD_AMOUNT for MPT). rippled runs these in preflight, which
	// has no rules access here, so they run at the top of Preclaim, the earliest
	// rules-aware point, before any stateful check.
	// Reference: rippled Escrow.cpp preflight lines 130-148, 88-119
	if !e.Amount.IsNative() {
		if !rules.Enabled(amendment.FeatureTokenEscrow) {
			return tx.TemBAD_AMOUNT
		}
		if result := escrowCreateNonXRPPreflight(rules, e.Amount); result != tx.TesSUCCESS {
			return result
		}
	}

	// fix1571: an escrow must specify a FinishAfter or a Condition (otherwise it
	// could be finished immediately). rippled gates this in preflight behind
	// fix1571. Reference: rippled Escrow.cpp:160-167
	if rules.Enabled(amendment.FeatureFix1571) {
		if e.FinishAfter == nil && (e.Condition == nil || *e.Condition == "") {
			return tx.TemMALFORMED
		}
	}

	accountID, err := state.DecodeAccountID(e.Account)
	if err != nil {
		return tx.TemBAD_SRC_ACCOUNT
	}
	destID, err := state.DecodeAccountID(e.Destination)
	if err != nil {
		return tx.TemINVALID
	}

	// Destination must exist and not be a pseudo-account.
	// Reference: rippled Escrow.cpp:369-378
	destAccount, result := readDestinationForEscrow(view, destID)
	if result != tx.TesSUCCESS {
		return result
	}
	if destAccount.IsPseudoAccount() {
		return tx.TecNO_PERMISSION
	}

	// Non-XRP token preclaim helpers.
	// Reference: rippled Escrow.cpp:380-393
	if !e.Amount.IsNative() {
		if e.Amount.IsMPT() {
			if result := escrowCreatePreclaimMPT(view, rules, accountID, destID, e.Amount); result != tx.TesSUCCESS {
				return result
			}
		} else {
			if result := escrowCreatePreclaimIOU(view, accountID, destID, e.Amount); result != tx.TesSUCCESS {
				return result
			}
		}
	}

	// Time validation against parent close time
	// Reference: rippled Escrow.cpp:457-489
	if rules.Enabled(amendment.FeatureFix1571) {
		// fix1571: after() means strictly greater than
		if e.CancelAfter != nil && closeTime > *e.CancelAfter {
			return tx.TecNO_PERMISSION
		}
		if e.FinishAfter != nil && closeTime > *e.FinishAfter {
			return tx.TecNO_PERMISSION
		}
	} else {
		// pre-fix1571: >= comparison
		if e.CancelAfter != nil && closeTime >= *e.CancelAfter {
			return tx.TecNO_PERMISSION
		}
		if e.FinishAfter != nil && closeTime >= *e.FinishAfter {
			return tx.TecNO_PERMISSION
		}
	}

	return tx.TesSUCCESS
}

// readDestinationForEscrow reads and parses the destination AccountRoot from a
// LedgerView, returning tecNO_DST if it is absent. Used by Preclaim where there
// is no ApplyContext.
func readDestinationForEscrow(view tx.LedgerView, destID [20]byte) (*state.AccountRoot, tx.Result) {
	data, err := view.Read(keylet.Account(destID))
	if err != nil || data == nil {
		return nil, tx.TecNO_DST
	}
	acct, err := state.ParseAccountRoot(data)
	if err != nil {
		return nil, tx.TefINTERNAL
	}
	return acct, tx.TesSUCCESS
}

// escrowCreateNonXRPPreflight runs the per-asset preflight validity checks for a
// non-XRP escrow amount, assuming featureTokenEscrow is enabled. IOU amounts
// must be positive and may not use the reserved "XRP" currency code; MPT
// amounts require featureMPTokensV1 and must be positive and within range.
// Reference: rippled Escrow.cpp escrowCreatePreflightHelper<Issue> lines 92-103
// and escrowCreatePreflightHelper<MPTIssue> lines 106-119.
func escrowCreateNonXRPPreflight(rules *amendment.Rules, amount tx.Amount) tx.Result {
	if amount.IsMPT() {
		if !rules.Enabled(amendment.FeatureMPTokensV1) {
			return tx.TemDISABLED
		}
		if amount.IsZero() || amount.IsNegative() {
			return tx.TemBAD_AMOUNT
		}
		if raw, ok := amount.MPTRaw(); ok && raw > maxMPTokenAmount {
			return tx.TemBAD_AMOUNT
		}
		return tx.TesSUCCESS
	}

	if amount.IsZero() || amount.IsNegative() {
		return tx.TemBAD_AMOUNT
	}
	if amount.Currency == "" || amount.Currency == "XRP" {
		return tx.TemBAD_CURRENCY
	}
	return tx.TesSUCCESS
}

// Apply applies an EscrowCreate transaction
// Reference: rippled Escrow.cpp EscrowCreate::doApply()
func (e *EscrowCreate) Apply(ctx *tx.ApplyContext) tx.Result {
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
		return tx.TecINSUFFICIENT_RESERVE
	}

	// For XRP escrows, also check that the sender can afford the amount
	// on top of the reserve. IOU escrows are deducted from trust lines,
	// not the XRP balance.
	// Reference: rippled Escrow.cpp:505-508
	if isNative {
		drops := e.Amount.Drops()
		if drops <= 0 {
			return tx.TemINVALID
		}
		if ctx.Account.Balance < reserve+uint64(drops) {
			ctx.Log.Warn("escrow create: unfunded",
				"balance", ctx.Account.Balance,
				"needed", reserve+uint64(drops),
			)
			return tx.TecUNFUNDED
		}
	}

	// Verify destination exists and is not a pseudo-account. The destination
	// existence + pseudo-account + token preclaim checks were already run in
	// Preclaim; this re-read mirrors rippled's doApply destination block, which
	// follows the reserve/unfunded checks.
	// Reference: rippled Escrow.cpp:511-526
	destAccount, destID, result := ctx.LookupDestination(e.Destination)
	if result != tx.TesSUCCESS {
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
		return tx.TecDST_TAG_NEEDED
	}

	// DisallowXRP check (only when DepositAuth amendment is NOT enabled)
	// Reference: rippled Escrow.cpp:523-525
	if !rules.Enabled(amendment.FeatureDepositAuth) {
		if (destAccount.Flags & state.LsfDisallowXRP) != 0 {
			return tx.TecNO_TARGET
		}
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
			capturedTransferRate = getTransferRateForIssuer(ctx.View, issuerID)
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
		return tx.TecDIR_FULL
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
			return tx.TecDIR_FULL
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
				return tx.TecDIR_FULL
			}
			issuerNode = issuerResult.Page
			hasIssuerNode = true
		}
	}

	escrowData, err := serializeEscrow(e, accountID, destID, sequence, capturedTransferRate,
		ownerNode, destNode, hasDestNode, issuerNode, hasIssuerNode)
	if err != nil {
		ctx.Log.Error("escrow create: failed to serialize escrow", "error", err)
		return tx.TefINTERNAL
	}

	// Insert escrow - creation tracked automatically by ApplyStateTable
	if err := ctx.View.Insert(escrowKey, escrowData); err != nil {
		ctx.Log.Error("escrow create: failed to insert escrow", "error", err)
		return tx.TefINTERNAL
	}

	// Deduct the escrow amount from the sender.
	// Reference: rippled Escrow.cpp:587-599
	if isNative {
		// XRP: deduct from account balance
		ctx.Account.Balance -= uint64(e.Amount.Drops())
	} else if e.Amount.IsMPT() {
		// MPT: lock via MPToken/MPTIssuance fields
		// Reference: rippled View.cpp rippleLockEscrowMPT()
		if lockResult := escrowLockMPT(ctx.View, accountID, e.Amount); lockResult != tx.TesSUCCESS {
			return lockResult
		}
	} else {
		// IOU: lock via trust line (rippleCredit sender -> issuer)
		// Reference: rippled escrowLockApplyHelper<Issue>
		issuerID, issuerErr := state.DecodeAccountID(e.Amount.Issuer)
		if issuerErr != nil {
			return tx.TefINTERNAL
		}
		if issuerID == accountID {
			return tx.TecINTERNAL
		}
		if lockResult := escrowLockIOU(ctx.View, accountID, issuerID, e.Amount); lockResult != tx.TesSUCCESS {
			return lockResult
		}
	}

	// Increase owner count for the escrow creator
	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

// serializeEscrow serializes an Escrow ledger entry.
// For XRP escrows, Amount is a drops string. For IOU escrows, Amount is the
// full IOU object (value/currency/issuer). For MPT escrows, Amount is
// {value, mpt_issuance_id}. transferRate is stored when non-zero and not
// equal to the parity rate (1_000_000_000).
func serializeEscrow(txn *EscrowCreate, ownerID, destID [20]byte, sequence uint32, transferRate uint32,
	ownerNode uint64, destNode uint64, hasDestNode bool, issuerNode uint64, hasIssuerNode bool) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	destAddress, err := addresscodec.EncodeAccountIDToClassicAddress(destID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode destination address: %w", err)
	}

	// Amount: XRP uses a drops string, IOU uses {value, currency, issuer},
	// MPT uses {value, mpt_issuance_id}.
	var amountVal any
	if txn.Amount.IsNative() {
		amountVal = fmt.Sprintf("%d", txn.Amount.Drops())
	} else if txn.Amount.IsMPT() {
		// MPT amounts are whole numbers — use MPTRaw() to avoid IOU
		// normalization which loses precision for large values (>16 digits).
		mptValue := txn.Amount.Value()
		if raw, ok := txn.Amount.MPTRaw(); ok {
			mptValue = fmt.Sprintf("%d", raw)
		}
		amountVal = map[string]any{
			"value":           mptValue,
			"mpt_issuance_id": txn.Amount.MPTIssuanceID(),
		}
	} else {
		amountVal = map[string]any{
			"value":    txn.Amount.Value(),
			"currency": txn.Amount.Currency,
			"issuer":   txn.Amount.Issuer,
		}
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "Escrow",
		"Account":         ownerAddress,
		"Destination":     destAddress,
		"Amount":          amountVal,
		"OwnerNode":       fmt.Sprintf("%x", ownerNode),
		"Flags":           uint32(0),
	}

	// sfDestinationNode: the page in the destination's owner directory holding
	// this escrow (cross-account only). sfIssuerNode: the page in the issuer's
	// owner directory (IOU escrows with a third-party issuer). Both are UInt64
	// fields serialized as hex, mirroring rippled Escrow.cpp:569,583.
	if hasDestNode {
		jsonObj["DestinationNode"] = fmt.Sprintf("%x", destNode)
	}
	if hasIssuerNode {
		jsonObj["IssuerNode"] = fmt.Sprintf("%x", issuerNode)
	}

	if txn.FinishAfter != nil {
		jsonObj["FinishAfter"] = *txn.FinishAfter
	}

	if txn.CancelAfter != nil {
		jsonObj["CancelAfter"] = *txn.CancelAfter
	}

	if txn.Condition != nil && *txn.Condition != "" {
		jsonObj["Condition"] = *txn.Condition
	}

	// SourceTag from Common fields
	if txn.GetCommon().SourceTag != nil {
		jsonObj["SourceTag"] = *txn.GetCommon().SourceTag
	}

	if txn.DestinationTag != nil {
		jsonObj["DestinationTag"] = *txn.DestinationTag
	}

	if transferRate > 0 && transferRate != 1_000_000_000 {
		jsonObj["TransferRate"] = transferRate
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Escrow: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// escrowLockIOU locks an IOU amount by transferring it from sender to issuer
// via the trust line. This is the Go equivalent of rippled's
// escrowLockApplyHelper<Issue> which calls rippleCredit(sender, issuer, amount).
// Reference: rippled Escrow.cpp:408-431
func escrowLockIOU(view tx.LedgerView, senderID, issuerID [20]byte, amount tx.Amount) tx.Result {
	if amount.IsZero() {
		return tx.TesSUCCESS
	}

	// Read the trust line between sender and issuer.
	// Note: rippled's rippleCredit() auto-creates trust lines via trustCreate()
	// if absent. We intentionally skip auto-creation here because for escrow
	// locking the sender must already hold the IOU, which requires an existing
	// trust line. If the trust line is missing, the sender cannot have a balance
	// to escrow, so TecNO_LINE is the correct result. (The unlock side does
	// auto-create the destination's line when the destination submits the
	// finish — see escrowUnlockIOU in token_helpers.go.)
	trustLineKey := keylet.Line(senderID, issuerID, amount.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil {
		return tx.TecINTERNAL
	}
	if trustLineData == nil {
		return tx.TecNO_LINE
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Determine account ordering for balance convention:
	// positive balance = low account owes high account
	// rippleCredit(sender, issuer, amount) means sender pays issuer.
	// When sender is low: subtract from balance (sender pays)
	// When sender is high: add to balance (sender pays from high side)
	senderIsLow := state.CompareAccountIDsForLine(senderID, issuerID) < 0

	if senderIsLow {
		newBalance, err := rs.Balance.Sub(amount)
		if err != nil {
			return tx.TefINTERNAL
		}
		rs.Balance = newBalance
	} else {
		newBalance, err := rs.Balance.Add(amount)
		if err != nil {
			return tx.TefINTERNAL
		}
		rs.Balance = newBalance
	}

	updated, err := state.SerializeRippleState(rs)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := view.Update(trustLineKey, updated); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}
