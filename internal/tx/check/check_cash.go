package check

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
)

// CheckCash cashes a Check, drawing from the sender's balance.
type CheckCash struct {
	tx.BaseTx

	// CheckID is the ID of the check to cash (required)
	CheckID string `json:"CheckID" xrpl:"CheckID"`

	// Amount is the exact amount to receive (optional, mutually exclusive with DeliverMin)
	Amount *tx.Amount `json:"Amount,omitempty" xrpl:"Amount,omitempty,amount"`

	// DeliverMin is the minimum amount to receive (optional, mutually exclusive with Amount)
	DeliverMin *tx.Amount `json:"DeliverMin,omitempty" xrpl:"DeliverMin,omitempty,amount"`
}

// NewCheckCash creates a new CheckCash transaction
func NewCheckCash(account, checkID string) *CheckCash {
	return &CheckCash{
		BaseTx:  *tx.NewBaseTx(tx.TypeCheckCash, account),
		CheckID: checkID,
	}
}

func (c *CheckCash) TxType() tx.Type {
	return tx.TypeCheckCash
}

// Validate implements preflight validation matching rippled's CashCheck::preflight().
func (c *CheckCash) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	// No flags allowed except universal flags
	// Reference: CashCheck.cpp L45-50
	if err := tx.CheckFlags(c.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	if c.CheckID == "" {
		return tx.Errorf(tx.TemMALFORMED, "CheckID is required")
	}

	// Must have exactly one of Amount or DeliverMin
	// Reference: CashCheck.cpp L52-62
	hasAmount := c.Amount != nil
	hasDeliverMin := c.DeliverMin != nil

	if hasAmount == hasDeliverMin {
		return tx.Errorf(tx.TemMALFORMED, "must specify exactly one of Amount or DeliverMin")
	}

	// Validate the provided amount
	// Reference: CashCheck.cpp L65-77
	if hasAmount {
		if c.Amount.Signum() <= 0 {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Amount must be positive")
		}
		if !c.Amount.IsNative() && c.Amount.Currency == "XRP" {
			return tx.Errorf(tx.TemBAD_CURRENCY, "invalid currency")
		}
	}

	if hasDeliverMin {
		if c.DeliverMin.Signum() <= 0 {
			return tx.Errorf(tx.TemBAD_AMOUNT, "DeliverMin must be positive")
		}
		if !c.DeliverMin.IsNative() && c.DeliverMin.Currency == "XRP" {
			return tx.Errorf(tx.TemBAD_CURRENCY, "invalid currency")
		}
	}

	return nil
}

func (c *CheckCash) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

// SetExactAmount sets the exact amount to receive
func (c *CheckCash) SetExactAmount(amount tx.Amount) {
	c.Amount = &amount
	c.DeliverMin = nil
}

// SetDeliverMin sets the minimum amount to receive
func (c *CheckCash) SetDeliverMin(amount tx.Amount) {
	c.DeliverMin = &amount
	c.Amount = nil
}

func (c *CheckCash) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureChecks}
}

// Apply implements preclaim + doApply matching rippled's CashCheck.
func (c *CheckCash) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("check cash apply",
		"account", c.Account,
		"checkID", c.CheckID,
		"amount", c.Amount,
		"deliverMin", c.DeliverMin,
	)

	// Parse check ID
	checkIDBytes, err := hex.DecodeString(c.CheckID)
	if err != nil || len(checkIDBytes) != 32 {
		return tx.TemINVALID
	}

	var checkKeyBytes [32]byte
	copy(checkKeyBytes[:], checkIDBytes)
	checkKey := keylet.Keylet{Key: checkKeyBytes}

	// Read check
	// Reference: CashCheck.cpp L85-90
	checkData, err := ctx.View.Read(checkKey)
	if err != nil || checkData == nil {
		return tx.TecNO_ENTRY
	}

	// Parse check
	check, err := state.ParseCheck(checkData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Verify the account is the destination
	// Reference: CashCheck.cpp L93-98
	accountID := ctx.AccountID
	if check.DestinationID != accountID {
		return tx.TecNO_PERMISSION
	}

	// A check written to self should have been caught at creation time, but
	// guard defensively here as rippled does.
	// Reference: CashCheck.cpp L99-106
	if check.Account == accountID {
		return tx.TecINTERNAL
	}

	// Read source (check writer) and destination accounts. If the check
	// exists, both should be present; a missing one is a corrupt ledger.
	// Reference: CashCheck.cpp L107-116
	srcKey := keylet.Account(check.Account)
	srcData, err := ctx.View.Read(srcKey)
	if err != nil || srcData == nil {
		return tx.TecNO_ENTRY
	}

	destKey := keylet.Account(accountID)
	destData, err := ctx.View.Read(destKey)
	if err != nil || destData == nil {
		return tx.TecNO_ENTRY
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check RequireDestTag on destination
	// Reference: CashCheck.cpp L118-126
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && !check.HasDestTag {
		return tx.TecDST_TAG_NEEDED
	}

	// Check expiration
	// Reference: CashCheck.cpp L129-133
	if tx.HasExpiredField(check.Expiration, ctx.Config.ParentCloseTime) {
		return tx.TecEXPIRED
	}

	// Currency and issuer of the requested amount (Amount or DeliverMin) must
	// match the check's SendMax. This guards against cashing a check with the
	// wrong asset — including the cross-type case of an IOU check cashed with a
	// native XRP amount (or vice versa), where the currencies differ.
	// Reference: CashCheck.cpp L135-155
	value := c.Amount
	if value == nil {
		value = c.DeliverMin
	}
	if result := matchesCheckSendMax(*value, check.SendMaxAmount); result != tx.TesSUCCESS {
		return result
	}

	// Determine the cash amount
	if c.Amount != nil {
		return c.applyCashWithAmount(ctx, check, checkKey)
	}
	return c.applyCashWithDeliverMin(ctx, check, checkKey)
}

// matchesCheckSendMax reports whether the requested cash amount's currency and
// issuer match the check's SendMax. A mismatch returns temMALFORMED, matching
// rippled's preclaim where the currency is compared before the issuer.
// XRP and an issued currency never match (their currencies differ).
// Reference: CashCheck.cpp L144-155.
func matchesCheckSendMax(value, sendMax state.Amount) tx.Result {
	if value.IsNative() != sendMax.IsNative() {
		return tx.TemMALFORMED
	}
	if value.IsNative() {
		return tx.TesSUCCESS
	}
	if value.Currency != sendMax.Currency {
		return tx.TemMALFORMED
	}
	if value.Issuer != sendMax.Issuer {
		return tx.TemMALFORMED
	}
	return tx.TesSUCCESS
}

// applyCashWithAmount handles the exact Amount case for both XRP and IOU.
func (c *CheckCash) applyCashWithAmount(ctx *tx.ApplyContext, check *state.CheckData, checkKey keylet.Keylet) tx.Result {
	amount := c.Amount

	// For XRP checks
	if amount.IsNative() {
		return c.applyCashXRP(ctx, check, checkKey, uint64(amount.Drops()), false)
	}

	// IOU Amount
	return c.applyCashIOUAmount(ctx, check, checkKey, *amount, false)
}

// applyCashWithDeliverMin handles the DeliverMin case for both XRP and IOU.
func (c *CheckCash) applyCashWithDeliverMin(ctx *tx.ApplyContext, check *state.CheckData, checkKey keylet.Keylet) tx.Result {
	deliverMin := c.DeliverMin

	// For XRP checks
	if deliverMin.IsNative() {
		return c.applyCashXRP(ctx, check, checkKey, uint64(deliverMin.Drops()), true)
	}

	// IOU DeliverMin
	return c.applyCashIOUAmount(ctx, check, checkKey, *deliverMin, true)
}

// applyCashXRP handles XRP check cashing for both the exact Amount and the
// DeliverMin paths. requestedDrops is the Amount (exact) or DeliverMin
// (minimum); isDeliverMin selects which. rippled handles both in one path:
// after the funds checks it delivers min(srcLiquid, SendMax) for DeliverMin and
// exactly the requested drops for Amount. Reference: CashCheck.cpp L294-334.
func (c *CheckCash) applyCashXRP(ctx *tx.ApplyContext, check *state.CheckData, checkKey keylet.Keylet, requestedDrops uint64, isDeliverMin bool) tx.Result {
	// Requested amount cannot exceed SendMax.
	// Reference: CashCheck.cpp L156-160
	if requestedDrops > check.SendMax {
		return tx.TecPATH_PARTIAL
	}

	// Check creator has sufficient liquid XRP
	creatorKey := keylet.Account(check.Account)
	creatorData, err := ctx.View.Read(creatorKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	creatorAccount, err := state.ParseAccountRoot(creatorData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Preclaim funds check: the creator's zero-clamped liquid XRP plus one
	// released reserve increment must cover the requested amount. Mirrors
	// rippled's accountFunds(value) + fees().increment guard, which returns
	// tecPATH_PARTIAL when the writer is at or above their reserve.
	// Reference: CashCheck.cpp L162-185.
	if requestedDrops > xrpAvailableFunds(creatorAccount, ctx) {
		return tx.TecPATH_PARTIAL
	}

	// doApply funds check: xrpLiquid with the released check reserve (-1 owner
	// count). Distinct from the preclaim guard in the below-reserve window,
	// where rippled returns tecUNFUNDED_PAYMENT. For DeliverMin, xrpDeliver
	// collapses to DeliverMin in the underfunded case (min(sendMax, srcLiquid)
	// never exceeds srcLiquid). Reference: CashCheck.cpp L304-319.
	srcLiquid := xrpLiquidAfterCheck(creatorAccount, ctx)
	if srcLiquid < requestedDrops {
		return tx.TecUNFUNDED_PAYMENT
	}

	// For DeliverMin, deliver as much as possible up to SendMax; for an exact
	// Amount, deliver exactly the requested drops.
	cashAmount := requestedDrops
	if isDeliverMin {
		cashAmount = min(srcLiquid, check.SendMax)

		// Set delivered_amount metadata for the DeliverMin XRP path when fix1623
		// is enabled. Reference: CashCheck.cpp L322-324.
		if ctx.Rules().Enabled(amendment.FeatureFix1623) {
			deliveredAmt := tx.NewXRPAmount(int64(cashAmount))
			ctx.Metadata.DeliveredAmount = &deliveredAmt
		}
	}

	// Transfer XRP
	creatorAccount.Balance -= cashAmount
	ctx.Account.Balance += cashAmount

	// Remove check from directories before erasing
	if result := removeCheckFromDirectories(ctx, check, checkKey.Key); result != tx.TesSUCCESS {
		return result
	}

	// Decrease creator's owner count
	if creatorAccount.OwnerCount > 0 {
		creatorAccount.OwnerCount--
	}

	// Update creator account
	if result := ctx.UpdateAccountRoot(check.Account, creatorAccount); result != tx.TesSUCCESS {
		return result
	}

	// Delete the check
	if err := ctx.View.Erase(checkKey); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// xrpAvailableFunds returns the check writer's preclaim-stage available XRP:
// their zero-clamped liquid balance at the current owner count plus one
// reserve increment, since cashing the check releases its reserve.
// Mirrors rippled's accountFunds(value) + fees().increment for native amounts.
// Reference: CashCheck.cpp L162-185, View.cpp xrpLiquid (zero-clamp).
func xrpAvailableFunds(creator *state.AccountRoot, ctx *tx.ApplyContext) uint64 {
	reserve := ctx.AccountReserve(creator.OwnerCount)
	var liquid uint64
	if creator.Balance > reserve {
		liquid = creator.Balance - reserve
	}
	return liquid + ctx.Config.ReserveIncrement
}

// xrpLiquidAfterCheck returns the writer's zero-clamped liquid XRP computed with
// the check's reserve already released (owner count minus one), matching
// rippled's xrpLiquid(psb, srcId, -1). This is the amount actually available to
// fund the transfer in doApply. Reference: CashCheck.cpp L304, View.cpp xrpLiquid.
func xrpLiquidAfterCheck(creator *state.AccountRoot, ctx *tx.ApplyContext) uint64 {
	ownerCount := creator.OwnerCount
	if ownerCount > 0 {
		ownerCount--
	}
	reserve := ctx.AccountReserve(ownerCount)
	if creator.Balance > reserve {
		return creator.Balance - reserve
	}
	return 0
}

// applyCashIOUAmount handles IOU check cashing for both Amount and DeliverMin.
// When isDeliverMin is true, the requestedAmount is treated as the minimum and
// the flow engine delivers as much as possible up to SendMax.
// Reference: CashCheck.cpp L252-end
func (c *CheckCash) applyCashIOUAmount(ctx *tx.ApplyContext, check *state.CheckData, checkKey keylet.Keylet, requestedAmount tx.Amount, isDeliverMin bool) tx.Result {
	accountID := ctx.AccountID
	sendMax := check.SendMaxAmount

	// --- Preclaim checks for IOU ---
	// The requested amount's currency/issuer was already verified against the
	// check's SendMax in Apply (matchesCheckSendMax), matching rippled's
	// preclaim order.

	// Requested amount (whether Amount or DeliverMin) cannot exceed SendMax
	// Reference: CashCheck.cpp L156-160
	if requestedAmount.Compare(sendMax) > 0 {
		return tx.TecPATH_PARTIAL
	}

	issuerID, err := state.DecodeAccountID(sendMax.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	srcID := check.Account

	// Check source has sufficient non-frozen funds
	// Reference: CashCheck.cpp L162-185
	// Applies to BOTH Amount and DeliverMin paths (rippled checks value > availableFunds
	// where value is either Amount or DeliverMin).
	srcFunds := tx.AccountFunds(ctx.View, srcID, requestedAmount, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
	if requestedAmount.Compare(srcFunds) > 0 {
		return tx.TecPATH_PARTIAL
	}

	// IOU-specific preclaim: destination is not issuer
	// Reference: CashCheck.cpp L187-247
	if accountID != issuerID {
		// Check trust line existence
		trustLineKey := keylet.Line(accountID, issuerID, sendMax.Currency)
		trustLineExists, _ := ctx.View.Exists(trustLineKey)

		rules := ctx.Rules()
		checkCashMakesTrustLine := rules.Enabled(amendment.FeatureCheckCashMakesTrustLine)

		if !trustLineExists && !checkCashMakesTrustLine {
			return tx.TecNO_LINE
		}

		// Check issuer existence
		// Reference: CashCheck.cpp L201-208
		issuerKey := keylet.Account(issuerID)
		issuerData, err := ctx.View.Read(issuerKey)
		if err != nil || issuerData == nil {
			return tx.TecNO_ISSUER
		}
		issuerAccount, err := state.ParseAccountRoot(issuerData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Check RequireAuth on issuer
		// Reference: CashCheck.cpp L210-234
		if (issuerAccount.Flags & state.LsfRequireAuth) != 0 {
			if !trustLineExists {
				// Can't auto-create trust line when auth is required
				return tx.TecNO_AUTH
			}

			// Check if destination is authorized
			trustLineData, err := ctx.View.Read(trustLineKey)
			if err != nil {
				return tx.TefINTERNAL
			}
			trustLine, err := state.ParseRippleState(trustLineData)
			if err != nil {
				return tx.TefINTERNAL
			}

			// Check auth flag based on canonical ordering
			// Reference: CashCheck.cpp L222-226
			// canonical_gt means dstId > issuerId
			dstGtIssuer := state.CompareAccountIDs(accountID, issuerID) > 0
			var authFlag uint32
			if dstGtIssuer {
				authFlag = state.LsfLowAuth // issuer is LOW
			} else {
				authFlag = state.LsfHighAuth // issuer is HIGH
			}

			if (trustLine.Flags & authFlag) == 0 {
				return tx.TecNO_AUTH
			}
		}

		// Check if issuer froze destination's trust line
		// Reference: CashCheck.cpp L240-246
		// isFrozen(view, dstId, currency, issuerId) checks:
		// 1. Global freeze on issuer
		// 2. Issuer's freeze flag on the trust line
		if isIssuerFrozenForAccount(ctx.View, accountID, issuerID, sendMax.Currency) {
			return tx.TecFROZEN
		}
	}

	// --- doApply: Execute IOU transfer using flow engine ---
	// Reference: CashCheck.cpp L252-end

	// Handle trust line creation with CheckCashMakesTrustLine amendment
	rules := ctx.Rules()
	checkCashMakesTrustLine := rules.Enabled(amendment.FeatureCheckCashMakesTrustLine)

	// Determine the trust line key for destination ↔ issuer
	destLow := state.CompareAccountIDs(issuerID, accountID) > 0

	if accountID != issuerID && checkCashMakesTrustLine {
		trustLineKey := keylet.Line(accountID, issuerID, sendMax.Currency)
		trustLineExists, _ := ctx.View.Exists(trustLineKey)

		if !trustLineExists {
			// Check reserve for creating trust line
			// Reference: CashCheck.cpp L373-378
			feeDrops := parseFee(c.Fee)
			priorBalance := ctx.Account.Balance + feeDrops
			reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
			if priorBalance < reserve {
				return tx.TecNO_LINE_INSUF_RESERVE
			}

			// Create trust line
			if result := createTrustLineForCheckCash(ctx, accountID, issuerID, sendMax.Currency); result != tx.TesSUCCESS {
				return result
			}
		}
	}

	// Temporarily tweak the trust line limit on destination's side to allow
	// the flow engine to deliver through it. This matches rippled's behavior:
	// CashCheck.cpp L418-439 - saves the limit, sets it to max, runs flow,
	// then restores it via scope_exit.
	// Reference: CashCheck.cpp L422-439
	var savedLimit *state.Amount
	if accountID != issuerID && checkCashMakesTrustLine {
		trustLineKey := keylet.Line(accountID, issuerID, sendMax.Currency)
		trustLineData, err := ctx.View.Read(trustLineKey)
		if err != nil {
			return tx.TecNO_LINE
		}
		rs, err := state.ParseRippleState(trustLineData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Save and tweak the destination's limit
		if destLow {
			saved := rs.LowLimit
			savedLimit = &saved
			rs.LowLimit = state.NewIssuedAmountFromValue(state.MaxMantissa, state.MaxExponent, sendMax.Currency, rs.LowLimit.Issuer)
		} else {
			saved := rs.HighLimit
			savedLimit = &saved
			rs.HighLimit = state.NewIssuedAmountFromValue(state.MaxMantissa, state.MaxExponent, sendMax.Currency, rs.HighLimit.Issuer)
		}

		updatedData, err := state.SerializeRippleState(rs)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(trustLineKey, updatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Determine flow parameters
	var flowAmount tx.Amount // What to deliver
	if isDeliverMin {
		// For DeliverMin, request delivery of SendMax (maximum possible)
		flowAmount = sendMax
	} else {
		// For exact Amount, deliver exactly what was requested
		flowAmount = requestedAmount
	}

	// Execute flow using RippleCalculate
	// Reference: CashCheck.cpp L442-455
	_, actualOut, _, sandbox, flowResult := payment.RippleCalculate(
		ctx.View,
		srcID,        // source (check creator)
		accountID,    // destination (check casher)
		flowAmount,   // amount to deliver
		&sendMax,     // SendMax as input limit
		nil,          // no explicit paths
		true,         // use default path
		isDeliverMin, // partial payment for DeliverMin
		false,        // no limit quality
		ctx.TxHash,
		ctx.Config.LedgerSequence,
	)

	if flowResult != tx.TesSUCCESS && flowResult != tx.TecPATH_PARTIAL {
		ctx.Log.Warn("check cash: flow failed", "result", flowResult)
		// Restore the trust line limit before returning
		if savedLimit != nil {
			restoreTrustLineLimit(ctx, accountID, issuerID, sendMax.Currency, destLow, *savedLimit)
		}
		return flowResult
	}

	// For DeliverMin, check that actual output >= deliverMin
	// Reference: CashCheck.cpp L463-475
	if isDeliverMin {
		actualOutAmount := payment.FromEitherAmount(actualOut)
		if actualOutAmount.Compare(requestedAmount) < 0 {
			ctx.Log.Warn("check cash: flow did not produce DeliverMin", "actual", actualOutAmount, "deliverMin", requestedAmount)
			// Restore the trust line limit before returning
			if savedLimit != nil {
				restoreTrustLineLimit(ctx, accountID, issuerID, sendMax.Currency, destLow, *savedLimit)
			}
			return tx.TecPATH_PARTIAL
		}
	}

	// For exact Amount, flow must have succeeded
	if !isDeliverMin && flowResult != tx.TesSUCCESS {
		// Restore the trust line limit before returning
		if savedLimit != nil {
			restoreTrustLineLimit(ctx, accountID, issuerID, sendMax.Currency, destLow, *savedLimit)
		}
		return tx.TecPATH_PARTIAL
	}

	// Apply flow sandbox changes
	if sandbox != nil {
		if err := sandbox.ApplyToView(ctx.View); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Restore the trust line limit after applying flow changes.
	// The flow engine may have modified the balance, but we need to
	// restore the original limit that was tweaked.
	// Reference: CashCheck.cpp scope_exit at L426-429
	if savedLimit != nil {
		restoreTrustLineLimit(ctx, accountID, issuerID, sendMax.Currency, destLow, *savedLimit)
	}

	// Set delivered_amount metadata. Reference: CashCheck.cpp L463-480.
	// - DeliverMin without CheckCashMakesTrustLine: set when fix1623 enabled.
	// - CheckCashMakesTrustLine: always set, regardless of fix1623/DeliverMin.
	if checkCashMakesTrustLine ||
		(isDeliverMin && ctx.Rules().Enabled(amendment.FeatureFix1623)) {
		deliveredAmt := payment.FromEitherAmount(actualOut)
		ctx.Metadata.DeliveredAmount = &deliveredAmt
	}

	// Remove check from directories before erasing.
	// Reference: CashCheck.cpp L487-508
	if result := removeCheckFromDirectories(ctx, check, checkKey.Key); result != tx.TesSUCCESS {
		return result
	}

	// Decrease creator's owner count and delete the check
	creatorKey := keylet.Account(srcID)
	creatorData, err := ctx.View.Read(creatorKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	creatorAccount, err := state.ParseAccountRoot(creatorData)
	if err != nil {
		return tx.TefINTERNAL
	}

	if creatorAccount.OwnerCount > 0 {
		creatorAccount.OwnerCount--
	}

	if result := ctx.UpdateAccountRoot(srcID, creatorAccount); result != tx.TesSUCCESS {
		return result
	}

	// Delete the check
	if err := ctx.View.Erase(checkKey); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// isIssuerFrozenForAccount reports rippled's isFrozen(view, account, currency,
// issuer) for an IOU: the issuer's global freeze, or — when account != issuer —
// the issuer-side individual freeze of the trust line. It delegates to the
// shared freeze primitives rather than re-reading and re-deriving the freeze
// flags. The account == issuer corner short-circuits identically: the
// shared IsTrustlineFrozen reads the self-self line (absent) and returns false,
// so only the global freeze applies.
// Reference: rippled/src/xrpld/ledger/detail/View.cpp isFrozen().
func isIssuerFrozenForAccount(view tx.LedgerView, accountID, issuerID [20]byte, currency string) bool {
	issuerAddr, err := state.EncodeAccountID(issuerID)
	if err != nil {
		return false
	}
	if tx.IsGlobalFrozen(view, issuerAddr) {
		return true
	}
	return tx.IsTrustlineFrozen(view, accountID, issuerID, currency)
}

// createTrustLineForCheckCash creates a trust line between the check casher
// (destination) and the issuer when the CheckCashMakesTrustLine amendment is
// enabled, delegating to the shared tx.TrustCreate. The casher is the account
// being set (it pays the reserve); the issuer is the peer. The casher is the
// transaction sender, so its OwnerCount is bumped on ctx.Account, which the
// engine writes back.
// Reference: CashCheck.cpp L349-412, View.cpp trustCreate L1329-1445
func createTrustLineForCheckCash(ctx *tx.ApplyContext, destID, issuerID [20]byte, currency string) tx.Result {
	trustLineKey := keylet.Line(destID, issuerID, currency)

	destStr, err := state.EncodeAccountID(destID)
	if err != nil {
		return tx.TefINTERNAL
	}

	// The account-being-set's (casher's) noRipple is derived from its own
	// lsfDefaultRipple, exactly as rippled's check trustCreate call.
	// Reference: rippled CashCheck.cpp:393 (sleDst->getFlags() & lsfDefaultRipple) == 0.
	destData, err := ctx.View.Read(keylet.Account(destID))
	if err != nil {
		return tx.TefINTERNAL
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}
	destNoRipple := destAccount.Flags&state.LsfDefaultRipple == 0

	destLow := state.CompareAccountIDsForLine(destID, issuerID) < 0

	result := tx.TrustCreate(ctx.View, tx.TrustCreateParams{
		SrcHigh:     destLow,
		Src:         issuerID,
		Dst:         destID,
		LineKey:     trustLineKey,
		LimitIssuer: destID,
		NoRipple:    destNoRipple,
		Balance:     state.NewIssuedAmountFromValue(0, state.MinExponent, currency, state.AccountOneAddress),
		Limit:       state.NewIssuedAmountFromValue(0, state.MinExponent, currency, destStr),
	})
	if result != tx.TesSUCCESS {
		return result
	}

	// The casher owns the new line and pays its reserve.
	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

// restoreTrustLineLimit restores the original trust line limit after flow.
// Reference: CashCheck.cpp scope_exit at L426-429
func restoreTrustLineLimit(ctx *tx.ApplyContext, destID, issuerID [20]byte, currency string, destLow bool, savedLimit state.Amount) {
	trustLineKey := keylet.Line(destID, issuerID, currency)
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil {
		return
	}
	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return
	}

	if destLow {
		rs.LowLimit = savedLimit
	} else {
		rs.HighLimit = savedLimit
	}

	updatedData, err := state.SerializeRippleState(rs)
	if err != nil {
		return
	}
	ctx.View.Update(trustLineKey, updatedData)
}

// removeCheckFromDirectories removes a check from both source and destination
// owner directories. Must be called before erasing the check SLE.
// Reference: CashCheck.cpp L487-508
func removeCheckFromDirectories(ctx *tx.ApplyContext, check *state.CheckData, checkKeyBytes [32]byte) tx.Result {
	srcID := check.Account
	dstID := check.DestinationID

	// Remove from destination directory (if not self-send)
	if srcID != dstID {
		destDirKey := keylet.OwnerDir(dstID)
		if result := tx.DirRemoveOrBadLedger(ctx.View, destDirKey, check.DestinationNode, checkKeyBytes); result != tx.TesSUCCESS {
			return result
		}
	}

	// Remove from owner directory
	ownerDirKey := keylet.OwnerDir(srcID)
	return tx.DirRemoveOrBadLedger(ctx.View, ownerDirKey, check.OwnerNode, checkKeyBytes)
}
