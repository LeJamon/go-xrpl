// Package escrow implements EscrowCreate, EscrowFinish, and EscrowCancel transactions.
package escrow

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// maxMPTokenAmount is the maximum MPT value (int64 max).
// Reference: rippled include/xrpl/protocol/STAmount.h maxMPTokenAmount
const maxMPTokenAmount int64 = 0x7FFFFFFFFFFFFFFF // 9223372036854775807

func init() {
	tx.Register(tx.TypeEscrowCreate, func() tx.Transaction {
		return &EscrowCreate{BaseTx: *tx.NewBaseTx(tx.TypeEscrowCreate, "")}
	})
}

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

// NewEscrowCreate creates a new EscrowCreate transaction
func NewEscrowCreate(account, destination string, amount tx.Amount) *EscrowCreate {
	return &EscrowCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, account),
		Amount:      amount,
		Destination: destination,
	}
}

// TxType returns the transaction type
func (e *EscrowCreate) TxType() tx.Type {
	return tx.TypeEscrowCreate
}

// Validate validates the EscrowCreate transaction
// Reference: rippled Escrow.cpp EscrowCreate::preflight()
func (e *EscrowCreate) Validate() error {
	if err := e.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	// Reference: rippled Escrow.cpp preflight() fix1543 flag check
	if err := tx.CheckFlags(e.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	if err := tx.CheckDestRequired(e.Destination); err != nil {
		return err
	}

	// Rippled checks amount validity differently for XRP vs non-XRP.
	// For XRP, the zero/negative check runs immediately in preflight.
	// For non-XRP amounts, the amendment check (featureTokenEscrow) comes
	// FIRST, and the zero/negative + type-specific checks are in helpers
	// called after the amendment gate. Since Validate() is stateless (no
	// rules), we only check XRP here and defer non-XRP to Apply().
	// Reference: rippled Escrow.cpp preflight lines 130-148
	if e.Amount.IsNative() {
		if e.Amount.IsZero() || e.Amount.IsNegative() {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Amount must be positive")
		}
	} else if !e.Amount.IsMPT() {
		// IOU: zero/negative check runs in preflight for IOUs (no amendment
		// dependency). Reference: rippled escrowCreatePreflightHelper<Issue> line 97
		if e.Amount.IsZero() || e.Amount.IsNegative() {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Amount must be positive")
		}
		// IOU stateless check: bad currency (XRP as currency code is invalid).
		if e.Amount.Currency == "" || e.Amount.Currency == "XRP" {
			return tx.Errorf(tx.TemBAD_CURRENCY, "cannot escrow XRP as IOU")
		}
	}
	// MPT stateless checks (zero/negative, max amount) are deferred to Apply()
	// where featureMPTokensV1 is checked first (matching rippled's dispatch order).

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

	// NOTE: fix1571 check (FinishAfter or Condition required) is done in Apply()
	// where we have access to amendment rules. See EscrowCreate.Apply().

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

// Flatten returns a flat map of all transaction fields
func (e *EscrowCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(e)
}

// Preclaim performs stateful validation for EscrowCreate before doApply.
// Time checks are here so that the engine's TapRETRY gate can suppress
// tec results during retry passes, matching rippled's likelyToClaimFee
// semantics. Without this, replay-on-close would apply tecNO_PERMISSION
// on the final pass even though the initial apply succeeded.
// Reference: rippled Escrow.cpp EscrowCreate::doApply() lines 457-489
func (e *EscrowCreate) Preclaim(config tx.EngineConfig) tx.Result {
	rules := config.GetRules()
	closeTime := config.ParentCloseTime

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

	// Non-XRP amounts require featureTokenEscrow.
	// Reference: rippled Escrow.cpp preflight lines 131-148
	if !e.Amount.IsNative() && !rules.Enabled(amendment.FeatureTokenEscrow) {
		return tx.TemBAD_AMOUNT
	}

	// Non-XRP amount validity checks that were deferred from Validate()
	// because they depend on amendment state (matching rippled's dispatch order).
	// Reference: rippled escrowCreatePreflightHelper<MPTIssue> lines 106-119
	// Reference: rippled escrowCreatePreflightHelper<Issue> lines 92-103
	if !e.Amount.IsNative() {
		if e.Amount.IsMPT() {
			if !rules.Enabled(amendment.FeatureMPTokensV1) {
				return tx.TemDISABLED
			}
			if e.Amount.IsZero() || e.Amount.IsNegative() {
				return tx.TemBAD_AMOUNT
			}
			if raw, ok := e.Amount.MPTRaw(); ok {
				if raw > maxMPTokenAmount {
					return tx.TemBAD_AMOUNT
				}
			}
		} else {
			// IOU zero/negative check (deferred from Validate)
			if e.Amount.IsZero() || e.Amount.IsNegative() {
				return tx.TemBAD_AMOUNT
			}
		}
	}

	// Amendment-gated preflight: fix1571 requires FinishAfter or Condition
	// Reference: rippled Escrow.cpp:160-167
	if rules.Enabled(amendment.FeatureFix1571) {
		if e.FinishAfter == nil && (e.Condition == nil || *e.Condition == "") {
			return tx.TemMALFORMED
		}
	}

	isNative := e.Amount.IsNative()

	// Verify destination exists and is not a pseudo-account
	// Reference: rippled Escrow.cpp:511-512, 373-378
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

	// Token escrow preclaim validation
	// Reference: rippled Escrow.cpp EscrowCreate::preclaim() lines 362-395
	if !isNative && rules.Enabled(amendment.FeatureTokenEscrow) {
		if e.Amount.IsMPT() {
			if result := escrowCreatePreclaimMPT(ctx.View, rules, ctx.AccountID, destID, e.Amount); result != tx.TesSUCCESS {
				return result
			}
		} else {
			if result := escrowCreatePreclaimIOU(ctx.View, ctx.AccountID, destID, e.Amount); result != tx.TesSUCCESS {
				return result
			}
		}
	}

	// Reserve check
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

	// Create the escrow entry
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

	// Serialize escrow
	escrowData, err := serializeEscrow(e, accountID, destID, sequence, capturedTransferRate)
	if err != nil {
		ctx.Log.Error("escrow create: failed to serialize escrow", "error", err)
		return tx.TefINTERNAL
	}

	// Insert escrow - creation tracked automatically by ApplyStateTable
	if err := ctx.View.Insert(escrowKey, escrowData); err != nil {
		ctx.Log.Error("escrow create: failed to insert escrow", "error", err)
		return tx.TefINTERNAL
	}

	// Owner directory: insert escrow into owner's directory
	// Reference: rippled Escrow.cpp:550-558
	ownerDirKey := keylet.OwnerDir(accountID)
	_, err = state.DirInsert(ctx.View, ownerDirKey, escrowKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = accountID
	})
	if err != nil {
		ctx.Log.Error("escrow create: owner directory full", "error", err)
		return tx.TecDIR_FULL
	}

	// If cross-account, insert into destination's owner directory.
	// Note: rippled does NOT increment the destination's OwnerCount for
	// XRP escrows. Only the creator's OwnerCount is incremented.
	// Reference: rippled Escrow.cpp:561-569
	if destID != accountID {
		destDirKey := keylet.OwnerDir(destID)
		_, err = state.DirInsert(ctx.View, destDirKey, escrowKey.Key, func(dir *state.DirectoryNode) {
			dir.Owner = destID
		})
		if err != nil {
			ctx.Log.Error("escrow create: destination directory full", "error", err)
			return tx.TecDIR_FULL
		}
	}

	// For IOU escrows, also insert into the issuer's owner directory.
	// This helps track the total locked balance.
	// Reference: rippled Escrow.cpp:575-584
	if !isNative && !e.Amount.IsMPT() {
		issuerID, issuerErr := state.DecodeAccountID(e.Amount.Issuer)
		if issuerErr == nil && issuerID != accountID && issuerID != destID {
			issuerDirKey := keylet.OwnerDir(issuerID)
			_, err = state.DirInsert(ctx.View, issuerDirKey, escrowKey.Key, func(dir *state.DirectoryNode) {
				dir.Owner = issuerID
			})
			if err != nil {
				ctx.Log.Error("escrow create: issuer directory full", "error", err)
				return tx.TecDIR_FULL
			}
		}
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
func serializeEscrow(txn *EscrowCreate, ownerID, destID [20]byte, sequence uint32, transferRate uint32) ([]byte, error) {
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
			"value":            mptValue,
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
		"OwnerNode":       "0",
		"Flags":           uint32(0),
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
	// to escrow, so TecNO_LINE is the correct result.
	// TODO: EscrowFinish (unlock) will need trust line auto-creation for the
	// destination, since the destination may not yet have a trust line to the issuer.
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
