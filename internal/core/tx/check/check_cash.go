package check

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeCheckCash, func() tx.Transaction {
		return &CheckCash{BaseTx: *tx.NewBaseTx(tx.TypeCheckCash, "")}
	})
}

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

// TxType returns the transaction type
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
	if c.GetFlags()&tx.TfUniversalMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags")
	}

	if c.CheckID == "" {
		return errors.New("temMALFORMED: CheckID is required")
	}

	// Must have exactly one of Amount or DeliverMin
	// Reference: CashCheck.cpp L52-62
	hasAmount := c.Amount != nil
	hasDeliverMin := c.DeliverMin != nil

	if hasAmount == hasDeliverMin {
		return errors.New("temMALFORMED: must specify exactly one of Amount or DeliverMin")
	}

	// Validate the provided amount
	// Reference: CashCheck.cpp L65-77
	if hasAmount {
		if c.Amount.Signum() <= 0 {
			return errors.New("temBAD_AMOUNT: Amount must be positive")
		}
		if !c.Amount.IsNative() && c.Amount.Currency == "XRP" {
			return errors.New("temBAD_CURRENCY: invalid currency")
		}
	}

	if hasDeliverMin {
		if c.DeliverMin.Signum() <= 0 {
			return errors.New("temBAD_AMOUNT: DeliverMin must be positive")
		}
		if !c.DeliverMin.IsNative() && c.DeliverMin.Currency == "XRP" {
			return errors.New("temBAD_CURRENCY: invalid currency")
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
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

// RequiredAmendments returns the amendments required for this transaction type
func (c *CheckCash) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureChecks}
}

// Apply implements preclaim + doApply matching rippled's CashCheck.
func (c *CheckCash) Apply(ctx *tx.ApplyContext) tx.Result {
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
	check, err := sle.ParseCheck(checkData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Verify the account is the destination
	// Reference: CashCheck.cpp L93-98
	accountID := ctx.AccountID
	if check.DestinationID != accountID {
		return tx.TecNO_PERMISSION
	}

	// Read destination account for DestTag check
	// Reference: CashCheck.cpp L118-126
	destKey := keylet.Account(accountID)
	destData, err := ctx.View.Read(destKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	destAccount, err := sle.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check RequireDestTag on destination
	if (destAccount.Flags&sle.LsfRequireDestTag) != 0 && !check.HasDestTag {
		return tx.TecDST_TAG_NEEDED
	}

	// Check expiration
	// Reference: CashCheck.cpp L129-133
	if check.Expiration > 0 && check.Expiration <= ctx.Config.ParentCloseTime {
		return tx.TecEXPIRED
	}

	// Determine the cash amount
	if c.Amount != nil {
		return c.applyCashWithAmount(ctx, check, checkKey)
	}
	return c.applyCashWithDeliverMin(ctx, check, checkKey)
}

// applyCashWithAmount handles the exact Amount case for both XRP and IOU.
func (c *CheckCash) applyCashWithAmount(ctx *tx.ApplyContext, check *sle.CheckData, checkKey keylet.Keylet) tx.Result {
	amount := c.Amount

	// For XRP checks
	if amount.IsNative() {
		return c.applyCashXRPAmount(ctx, check, checkKey, uint64(amount.Drops()))
	}

	// IOU Amount
	return c.applyCashIOUAmount(ctx, check, checkKey, *amount, false)
}

// applyCashWithDeliverMin handles the DeliverMin case for both XRP and IOU.
func (c *CheckCash) applyCashWithDeliverMin(ctx *tx.ApplyContext, check *sle.CheckData, checkKey keylet.Keylet) tx.Result {
	deliverMin := c.DeliverMin

	// For XRP checks
	if deliverMin.IsNative() {
		return c.applyCashXRPDeliverMin(ctx, check, checkKey, uint64(deliverMin.Drops()))
	}

	// IOU DeliverMin
	return c.applyCashIOUAmount(ctx, check, checkKey, *deliverMin, true)
}

// applyCashXRPAmount handles XRP check cashing with exact Amount.
func (c *CheckCash) applyCashXRPAmount(ctx *tx.ApplyContext, check *sle.CheckData, checkKey keylet.Keylet, cashDrops uint64) tx.Result {
	// Amount cannot exceed SendMax
	// Reference: CashCheck.cpp L156-160
	if cashDrops > check.SendMax {
		return tx.TecPATH_PARTIAL
	}

	// Check creator has sufficient liquid XRP
	creatorKey := keylet.Account(check.Account)
	creatorData, err := ctx.View.Read(creatorKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	creatorAccount, err := sle.ParseAccountRoot(creatorData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Calculate creator's liquid XRP (balance - reserve after check deletion)
	creatorReserve := ctx.AccountReserve(creatorAccount.OwnerCount - 1)
	var srcLiquid uint64
	if creatorAccount.Balance > creatorReserve {
		srcLiquid = creatorAccount.Balance - creatorReserve
	}

	if srcLiquid < cashDrops {
		return tx.TecPATH_PARTIAL
	}

	// Transfer XRP
	creatorAccount.Balance -= cashDrops
	ctx.Account.Balance += cashDrops

	// Decrease creator's owner count
	if creatorAccount.OwnerCount > 0 {
		creatorAccount.OwnerCount--
	}

	// Update creator account
	creatorUpdatedData, err := sle.SerializeAccountRoot(creatorAccount)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(creatorKey, creatorUpdatedData); err != nil {
		return tx.TefINTERNAL
	}

	// Delete the check
	if err := ctx.View.Erase(checkKey); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// applyCashXRPDeliverMin handles XRP check cashing with DeliverMin.
func (c *CheckCash) applyCashXRPDeliverMin(ctx *tx.ApplyContext, check *sle.CheckData, checkKey keylet.Keylet, deliverMinDrops uint64) tx.Result {
	// DeliverMin cannot exceed SendMax
	if check.SendMax < deliverMinDrops {
		return tx.TecPATH_PARTIAL
	}

	// Check creator has sufficient liquid XRP
	creatorKey := keylet.Account(check.Account)
	creatorData, err := ctx.View.Read(creatorKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	creatorAccount, err := sle.ParseAccountRoot(creatorData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Calculate creator's liquid XRP (check will be deleted, so -1 owner count)
	creatorReserve := ctx.AccountReserve(creatorAccount.OwnerCount - 1)
	var srcLiquid uint64
	if creatorAccount.Balance > creatorReserve {
		srcLiquid = creatorAccount.Balance - creatorReserve
	}

	// Cash amount = min(sendMax, srcLiquid), must be >= deliverMin
	cashAmount := check.SendMax
	if srcLiquid < cashAmount {
		cashAmount = srcLiquid
	}

	if cashAmount < deliverMinDrops {
		return tx.TecPATH_PARTIAL
	}

	// Transfer XRP
	creatorAccount.Balance -= cashAmount
	ctx.Account.Balance += cashAmount

	// Decrease creator's owner count
	if creatorAccount.OwnerCount > 0 {
		creatorAccount.OwnerCount--
	}

	// Update creator account
	creatorUpdatedData, err := sle.SerializeAccountRoot(creatorAccount)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(creatorKey, creatorUpdatedData); err != nil {
		return tx.TefINTERNAL
	}

	// Delete the check
	if err := ctx.View.Erase(checkKey); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// applyCashIOUAmount handles IOU check cashing for both Amount and DeliverMin.
// When isDeliverMin is true, the requestedAmount is treated as the minimum and
// the flow engine delivers as much as possible up to SendMax.
// Reference: CashCheck.cpp L252-end
func (c *CheckCash) applyCashIOUAmount(ctx *tx.ApplyContext, check *sle.CheckData, checkKey keylet.Keylet, requestedAmount tx.Amount, isDeliverMin bool) tx.Result {
	accountID := ctx.AccountID
	sendMax := check.SendMaxAmount

	// --- Preclaim checks for IOU ---

	// Currency/issuer must match check's SendMax
	// Reference: CashCheck.cpp L144-155
	if requestedAmount.Currency != sendMax.Currency {
		return tx.TemMALFORMED
	}
	if requestedAmount.Issuer != sendMax.Issuer {
		return tx.TemMALFORMED
	}

	// Requested amount (whether Amount or DeliverMin) cannot exceed SendMax
	// Reference: CashCheck.cpp L156-160
	if requestedAmount.Compare(sendMax) > 0 {
		return tx.TecPATH_PARTIAL
	}

	issuerID, err := sle.DecodeAccountID(sendMax.Issuer)
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
		if err != nil {
			return tx.TecNO_ISSUER
		}
		issuerAccount, err := sle.ParseAccountRoot(issuerData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Check RequireAuth on issuer
		// Reference: CashCheck.cpp L210-234
		if (issuerAccount.Flags & sle.LsfRequireAuth) != 0 {
			if !trustLineExists {
				// Can't auto-create trust line when auth is required
				return tx.TecNO_AUTH
			}

			// Check if destination is authorized
			trustLineData, err := ctx.View.Read(trustLineKey)
			if err != nil {
				return tx.TefINTERNAL
			}
			trustLine, err := sle.ParseRippleState(trustLineData)
			if err != nil {
				return tx.TefINTERNAL
			}

			// Check auth flag based on canonical ordering
			// Reference: CashCheck.cpp L222-226
			// canonical_gt means dstId > issuerId
			dstGtIssuer := sle.CompareAccountIDs(accountID, issuerID) > 0
			var authFlag uint32
			if dstGtIssuer {
				authFlag = sle.LsfLowAuth // issuer is LOW
			} else {
				authFlag = sle.LsfHighAuth // issuer is HIGH
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
	destLow := sle.CompareAccountIDs(issuerID, accountID) > 0

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
	var savedLimit *sle.Amount
	if accountID != issuerID && checkCashMakesTrustLine {
		trustLineKey := keylet.Line(accountID, issuerID, sendMax.Currency)
		trustLineData, err := ctx.View.Read(trustLineKey)
		if err != nil {
			return tx.TecNO_LINE
		}
		rs, err := sle.ParseRippleState(trustLineData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Save and tweak the destination's limit
		if destLow {
			saved := rs.LowLimit
			savedLimit = &saved
			rs.LowLimit = sle.NewIssuedAmountFromValue(sle.MaxMantissa, sle.MaxExponent, sendMax.Currency, rs.LowLimit.Issuer)
		} else {
			saved := rs.HighLimit
			savedLimit = &saved
			rs.HighLimit = sle.NewIssuedAmountFromValue(sle.MaxMantissa, sle.MaxExponent, sendMax.Currency, rs.HighLimit.Issuer)
		}

		updatedData, err := sle.SerializeRippleState(rs)
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
		srcID,      // source (check creator)
		accountID,  // destination (check casher)
		flowAmount, // amount to deliver
		&sendMax,   // SendMax as input limit
		nil,        // no explicit paths
		true,       // use default path
		isDeliverMin, // partial payment for DeliverMin
		false,      // no limit quality
		ctx.TxHash,
		ctx.Config.LedgerSequence,
	)

	if flowResult != tx.TesSUCCESS && flowResult != tx.TecPATH_PARTIAL {
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

	// Decrease creator's owner count and delete the check
	creatorKey := keylet.Account(srcID)
	creatorData, err := ctx.View.Read(creatorKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	creatorAccount, err := sle.ParseAccountRoot(creatorData)
	if err != nil {
		return tx.TefINTERNAL
	}

	if creatorAccount.OwnerCount > 0 {
		creatorAccount.OwnerCount--
	}

	creatorUpdatedData, err := sle.SerializeAccountRoot(creatorAccount)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(creatorKey, creatorUpdatedData); err != nil {
		return tx.TefINTERNAL
	}

	// Delete the check
	if err := ctx.View.Erase(checkKey); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// isIssuerFrozenForAccount checks if the issuer has frozen the account's trust line.
// This matches rippled's isFrozen(view, account, currency, issuer).
// Reference: rippled/src/xrpld/ledger/detail/View.cpp isFrozen()
func isIssuerFrozenForAccount(view tx.LedgerView, accountID, issuerID [20]byte, currency string) bool {
	// Check global freeze on issuer
	issuerKey := keylet.Account(issuerID)
	issuerData, err := view.Read(issuerKey)
	if err != nil {
		return false
	}
	issuerAccount, err := sle.ParseAccountRoot(issuerData)
	if err != nil {
		return false
	}
	if (issuerAccount.Flags & sle.LsfGlobalFreeze) != 0 {
		return true
	}

	if issuerID == accountID {
		return false
	}

	// Check if the issuer froze the trust line
	// The flag to check depends on which side the issuer is on
	// Reference: View.cpp L264: (issuer > account) ? lsfHighFreeze : lsfLowFreeze
	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil {
		return false
	}

	trustLine, err := sle.ParseRippleState(trustLineData)
	if err != nil {
		return false
	}

	issuerIsHigh := sle.CompareAccountIDs(issuerID, accountID) > 0
	if issuerIsHigh {
		// Issuer is HIGH → check lsfHighFreeze (set by HIGH account = issuer)
		return (trustLine.Flags & sle.LsfHighFreeze) != 0
	}
	// Issuer is LOW → check lsfLowFreeze (set by LOW account = issuer)
	return (trustLine.Flags & sle.LsfLowFreeze) != 0
}

// createTrustLineForCheckCash creates a trust line for the destination when
// CheckCashMakesTrustLine amendment is enabled.
// Reference: CashCheck.cpp L349-412, View.cpp trustCreate L1329-1445
func createTrustLineForCheckCash(ctx *tx.ApplyContext, destID, issuerID [20]byte, currency string) tx.Result {
	trustLineKey := keylet.Line(destID, issuerID, currency)

	destIsLow := sle.CompareAccountIDsForLine(destID, issuerID) < 0

	// Encode account addresses for trust line limits
	destAddress, err := sle.EncodeAccountID(destID)
	if err != nil {
		return tx.TefINTERNAL
	}
	issuerAddress, err := sle.EncodeAccountID(issuerID)
	if err != nil {
		return tx.TefINTERNAL
	}

	var lowAccountStr, highAccountStr string
	if destIsLow {
		lowAccountStr = destAddress
		highAccountStr = issuerAddress
	} else {
		lowAccountStr = issuerAddress
		highAccountStr = destAddress
	}

	// Read destination account for DefaultRipple check
	destKey := keylet.Account(destID)
	destData, err := ctx.View.Read(destKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	destAccount, err := sle.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Read issuer account for DefaultRipple check
	issuerKey := keylet.Account(issuerID)
	issuerData, err := ctx.View.Read(issuerKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	issuerAccount, err := sle.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TefINTERNAL
	}

	destDefaultRipple := (destAccount.Flags & sle.LsfDefaultRipple) != 0
	issuerDefaultRipple := (issuerAccount.Flags & sle.LsfDefaultRipple) != 0

	// Determine flags
	// Reference: trustCreate in View.cpp
	// Reserve flag for the destination's side (they are paying the reserve)
	// NoRipple: set on destination's side if destination lacks DefaultRipple,
	//           set on issuer's side if issuer lacks DefaultRipple
	var flags uint32
	if destIsLow {
		flags |= sle.LsfLowReserve
		if !destDefaultRipple {
			flags |= sle.LsfLowNoRipple
		}
		if !issuerDefaultRipple {
			flags |= sle.LsfHighNoRipple
		}
	} else {
		flags |= sle.LsfHighReserve
		if !destDefaultRipple {
			flags |= sle.LsfHighNoRipple
		}
		if !issuerDefaultRipple {
			flags |= sle.LsfLowNoRipple
		}
	}

	// Create trust line with zero balance and zero limits
	// LowLimit.Issuer = low account, HighLimit.Issuer = high account
	// Balance.Issuer = AccountOneAddress (special sentinel)
	zeroBalance := sle.NewIssuedAmountFromValue(0, -100, currency, sle.AccountOneAddress)
	lowLimit := sle.NewIssuedAmountFromValue(0, -100, currency, lowAccountStr)
	highLimit := sle.NewIssuedAmountFromValue(0, -100, currency, highAccountStr)

	newTrustLine := &sle.RippleState{
		Balance:   zeroBalance,
		LowLimit:  lowLimit,
		HighLimit: highLimit,
		Flags:     flags,
	}

	// Serialize and insert
	trustLineData, err := sle.SerializeRippleState(newTrustLine)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Insert(trustLineKey, trustLineData); err != nil {
		return tx.TefINTERNAL
	}

	// Update destination owner count
	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

// restoreTrustLineLimit restores the original trust line limit after flow.
// Reference: CashCheck.cpp scope_exit at L426-429
func restoreTrustLineLimit(ctx *tx.ApplyContext, destID, issuerID [20]byte, currency string, destLow bool, savedLimit sle.Amount) {
	trustLineKey := keylet.Line(destID, issuerID, currency)
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil {
		return
	}
	rs, err := sle.ParseRippleState(trustLineData)
	if err != nil {
		return
	}

	if destLow {
		rs.LowLimit = savedLimit
	} else {
		rs.HighLimit = savedLimit
	}

	updatedData, err := sle.SerializeRippleState(rs)
	if err != nil {
		return
	}
	ctx.View.Update(trustLineKey, updatedData)
}
