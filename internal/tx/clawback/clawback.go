package clawback

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/ledger/entry"
)

// Clawback errors
var (
	ErrClawbackAmountRequired  = tx.Errorf(tx.TemBAD_AMOUNT, "Amount is required")
	ErrClawbackAmountNotToken  = tx.Errorf(tx.TemBAD_AMOUNT, "cannot claw back XRP")
	ErrClawbackAmountNotPos    = tx.Errorf(tx.TemBAD_AMOUNT, "Amount must be positive")
	ErrClawbackHolderWithToken = tx.Errorf(tx.TemMALFORMED, "Holder field cannot be present for token clawback")
	ErrClawbackHolderRequired  = tx.Errorf(tx.TemMALFORMED, "Holder is required for MPToken clawback")
	ErrClawbackHolderIsSelf    = tx.Errorf(tx.TemMALFORMED, "Holder cannot be the same as issuer")
)

// Clawback claws back tokens from a trust line or MPToken.
// Reference: rippled Clawback.cpp
type Clawback struct {
	tx.BaseTx

	// Amount is the amount to claw back (required)
	// For IOU clawback, the issuer field specifies the holder
	Amount state.Amount `json:"Amount" xrpl:"Amount,amount"`

	// Holder is the MPToken holder (optional, for MPToken clawback only)
	Holder string `json:"Holder,omitempty" xrpl:"Holder,omitempty"`

	// MPTokenIssuanceID is the issuance ID for MPToken clawback (required when Holder is set)
	MPTokenIssuanceID string `json:"MPTokenIssuanceID,omitempty" xrpl:"MPTokenIssuanceID,omitempty"`
}

// NewClawback creates a new Clawback transaction for IOU tokens
func NewClawback(account string, amount state.Amount) *Clawback {
	return &Clawback{
		BaseTx: *tx.NewBaseTx(tx.TypeClawback, account),
		Amount: amount,
	}
}

// NewMPTokenClawback creates a new Clawback transaction for MPTokens
func NewMPTokenClawback(account, holder, issuanceID string, amount state.Amount) *Clawback {
	return &Clawback{
		BaseTx:            *tx.NewBaseTx(tx.TypeClawback, account),
		Amount:            amount,
		Holder:            holder,
		MPTokenIssuanceID: issuanceID,
	}
}

func (c *Clawback) TxType() tx.Type {
	return tx.TypeClawback
}

// Reference: rippled Clawback.cpp preflight()
func (c *Clawback) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	// Reference: rippled Clawback.cpp:87-88
	if err := tx.CheckFlags(c.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	// Amount is required and must be positive
	if c.Amount.IsZero() {
		return ErrClawbackAmountRequired
	}

	// Amount must be positive (not negative or zero)
	if c.Amount.Signum() <= 0 {
		return ErrClawbackAmountNotPos
	}

	// Cannot claw back XRP
	if c.Amount.IsNative() {
		return ErrClawbackAmountNotToken
	}

	// Determine if this is IOU or MPToken clawback based on Amount type.
	// Reference: rippled Clawback.cpp:90-94 dispatches via std::visit on Amount's asset type.
	if c.Amount.IsMPT() {
		// MPToken clawback (preflightHelper<MPTIssue>)
		// Reference: rippled Clawback.cpp:54-76
		// Holder is required for MPT clawback
		if c.Holder == "" {
			return ErrClawbackHolderRequired
		}
		// Holder cannot be same as issuer
		if c.Holder == c.Account {
			return ErrClawbackHolderIsSelf
		}
	} else {
		// IOU clawback (preflightHelper<Issue>)
		// Reference: rippled Clawback.cpp:37-51
		// Holder must not be present for IOU clawback
		if c.Holder != "" {
			return ErrClawbackHolderWithToken
		}

		// For IOU, the issuer field in Amount specifies the holder
		// The transaction account must be the issuer of the currency
		holder := c.Amount.Issuer

		// Issuer cannot claw back from themselves
		if holder == c.Account {
			return ErrClawbackAmountNotPos // temBAD_AMOUNT per rippled
		}
	}

	return nil
}

// Reference: rippled Clawback.cpp preclaim() + applyHelper<Issue>() / applyHelper<MPTIssue>()
func (c *Clawback) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("clawback apply",
		"account", c.Account,
		"amount", c.Amount,
		"holder", c.Holder,
	)

	if c.Amount.IsMPT() {
		return c.applyMPT(ctx)
	}
	return c.applyIOU(ctx)
}

// applyMPT handles MPToken clawback when Amount is an MPT type.
// Reference: rippled Clawback.cpp preclaimHelper<MPTIssue>() + applyHelper<MPTIssue>()
func (c *Clawback) applyMPT(ctx *tx.ApplyContext) tx.Result {
	// Get the MPT issuance ID: prefer the legacy field, fall back to Amount's embedded ID
	mptIDHex := c.MPTokenIssuanceID
	if mptIDHex == "" {
		mptIDHex = c.Amount.MPTIssuanceID()
	}

	// Parse MPTokenIssuanceID
	var mptID [24]byte
	issuanceIDBytes, err := hex.DecodeString(mptIDHex)
	if err != nil || len(issuanceIDBytes) != 24 {
		// If the ID is invalid/empty, the issuance won't be found
		return tx.TecOBJECT_NOT_FOUND
	}
	copy(mptID[:], issuanceIDBytes)

	// Look up the issuance
	issuanceKey := keylet.MPTIssuance(mptID)
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Issuance must have CanClawback flag
	if issuance.Flags&entry.LsfMPTCanClawback == 0 {
		return tx.TecNO_PERMISSION
	}

	// Caller must be the issuer
	if issuance.Issuer != ctx.AccountID {
		return tx.TecNO_PERMISSION
	}

	// Decode holder account
	holderID, err := state.DecodeAccountID(c.Holder)
	if err != nil {
		return tx.TecNO_DST
	}

	// Look up holder's MPToken
	tokenKey := keylet.MPToken(issuanceKey.Key, holderID)
	tokenRaw, err := ctx.View.Read(tokenKey)
	if err != nil || tokenRaw == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Holder must have a positive balance
	if token.MPTAmount == 0 {
		return tx.TecINSUFFICIENT_FUNDS
	}

	// Extract requested amount as uint64 from the IOU-style Amount
	requested := amountToUint64(c.Amount)
	if requested == 0 {
		return tx.TecINSUFFICIENT_FUNDS
	}

	// Compute actual clawback amount = min(balance, requested)
	actual := token.MPTAmount
	if requested < actual {
		actual = requested
	}

	token.MPTAmount -= actual

	if issuance.OutstandingAmount >= actual {
		issuance.OutstandingAmount -= actual
	} else {
		issuance.OutstandingAmount = 0
	}

	updatedToken, err := state.SerializeMPToken(token)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(tokenKey, updatedToken); err != nil {
		return tx.TefINTERNAL
	}

	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedIssuance); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// amountToUint64 converts an Amount to a uint64 integer value.
// Prefers the raw MPT int64 value when available to avoid IOU normalization precision loss.
func amountToUint64(a state.Amount) uint64 {
	if raw, ok := a.MPTRaw(); ok {
		if raw <= 0 {
			return 0
		}
		return uint64(raw)
	}
	mantissa := a.Mantissa()
	if mantissa <= 0 {
		return 0
	}
	exp := a.Exponent()
	result := uint64(mantissa)
	for exp > 0 {
		result *= 10
		exp--
	}
	for exp < 0 {
		result /= 10
		exp++
	}
	return result
}

// applyIOU handles IOU token clawback (original path).
// Reference: rippled Clawback.cpp preclaim() + applyHelper<Issue>()
func (c *Clawback) applyIOU(ctx *tx.ApplyContext) tx.Result {
	// --- Preclaim checks ---

	// 1. Decode holder from Amount.Issuer
	holderID, err := state.DecodeAccountID(c.Amount.Issuer)
	if err != nil {
		return tx.TecNO_TARGET
	}

	// 2. Read holder's account — terNO_ACCOUNT if missing
	// Reference: rippled Clawback.cpp:206-208
	holderAccountKey := keylet.Account(holderID)
	holderAccountData, err := ctx.View.Read(holderAccountKey)
	if err != nil || holderAccountData == nil {
		return tx.TerNO_ACCOUNT
	}
	holderAccount, err := state.ParseAccountRoot(holderAccountData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// 3. Check issuer flags (ctx.Account is the issuer)
	// Reference: rippled Clawback.cpp preclaimHelper<Issue>() lines 117-123
	// AllowTrustLineClawback must be set, NoFreeze must NOT be set
	if (ctx.Account.Flags & state.LsfAllowTrustLineClawback) == 0 {
		return tx.TecNO_PERMISSION
	}
	if (ctx.Account.Flags & state.LsfNoFreeze) != 0 {
		return tx.TecNO_PERMISSION
	}

	// 4. Read trust line
	// Reference: rippled Clawback.cpp:125-128
	trustKey := keylet.Line(holderID, ctx.AccountID, c.Amount.Currency)
	trustData, err := ctx.View.Read(trustKey)
	if err != nil || trustData == nil {
		return tx.TecNO_LINE
	}
	rs, err := state.ParseRippleState(trustData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// 5. Balance direction check
	// Reference: rippled Clawback.cpp:132-138
	// Balance is from LOW account's perspective:
	//   Positive: HIGH owes LOW (HIGH is the issuer)
	//   Negative: LOW owes HIGH (LOW is the issuer)
	// If balance > 0, issuer must be HIGH (issuer > holder)
	// If balance < 0, issuer must be LOW (issuer < holder)
	issuerIsLow := state.CompareAccountIDs(ctx.AccountID, holderID) < 0
	if rs.Balance.Signum() > 0 && issuerIsLow {
		return tx.TecNO_PERMISSION
	}
	if rs.Balance.Signum() < 0 && !issuerIsLow {
		return tx.TecNO_PERMISSION
	}

	// 6. Check holder has funds (accountHolds equivalent, ignoring freeze)
	// Reference: rippled Clawback.cpp:149-156
	// Get balance from holder's perspective
	holderIsLow := !issuerIsLow
	var holderBalance state.Amount
	if holderIsLow {
		holderBalance = rs.Balance
	} else {
		holderBalance = rs.Balance.Negate()
	}
	if holderBalance.Signum() <= 0 {
		return tx.TecINSUFFICIENT_FUNDS
	}

	// --- Apply ---
	// Reference: rippled Clawback.cpp applyHelper<Issue>() lines 230-259

	// 7. Compute actual claw amount = min(holderBalance, clawAmount)
	// Set the issuer field to the actual issuer (matching rippled line 239)
	clawAmount := c.Amount
	clawAmount.Issuer = ctx.Account.Account

	var actualAmount state.Amount
	if holderBalance.Compare(clawAmount) < 0 {
		actualAmount = holderBalance
	} else {
		actualAmount = clawAmount
	}

	// 8. Transfer from holder to issuer (rippleCredit equivalent)
	// Reference: rippled View.cpp rippleCredit()
	// If holder is LOW: holder pays issuer (HIGH) → balance decreases
	// If holder is HIGH: holder pays issuer (LOW) → balance increases
	if holderIsLow {
		rs.Balance, _ = rs.Balance.Sub(actualAmount)
	} else {
		rs.Balance, _ = rs.Balance.Add(actualAmount)
	}

	// 9. Check if trust line should be deleted (default state)
	// Reference: rippled View.cpp rippleCredit() default state check
	// Same pattern as trustset.go lines 514-570
	var lowDefRipple, highDefRipple bool
	if issuerIsLow {
		lowDefRipple = (ctx.Account.Flags & state.LsfDefaultRipple) != 0
		highDefRipple = (holderAccount.Flags & state.LsfDefaultRipple) != 0
	} else {
		lowDefRipple = (holderAccount.Flags & state.LsfDefaultRipple) != 0
		highDefRipple = (ctx.Account.Flags & state.LsfDefaultRipple) != 0
	}

	bLowReserveSet := rs.LowQualityIn != 0 || rs.LowQualityOut != 0 ||
		((rs.Flags&state.LsfLowNoRipple) == 0) != lowDefRipple ||
		(rs.Flags&state.LsfLowFreeze) != 0 || !rs.LowLimit.IsZero() ||
		rs.Balance.Signum() > 0

	bHighReserveSet := rs.HighQualityIn != 0 || rs.HighQualityOut != 0 ||
		((rs.Flags&state.LsfHighNoRipple) == 0) != highDefRipple ||
		(rs.Flags&state.LsfHighFreeze) != 0 || !rs.HighLimit.IsZero() ||
		rs.Balance.Signum() < 0

	bDefault := !bLowReserveSet && !bHighReserveSet

	if bDefault && rs.Balance.IsZero() {
		// Remove from both owner directories before erasing
		var lowAccountID, highAccountID [20]byte
		if issuerIsLow {
			lowAccountID = ctx.AccountID
			highAccountID = holderID
		} else {
			lowAccountID = holderID
			highAccountID = ctx.AccountID
		}
		lowDirKey := keylet.OwnerDir(lowAccountID)
		state.DirRemove(ctx.View, lowDirKey, rs.LowNode, trustKey.Key, false)
		highDirKey := keylet.OwnerDir(highAccountID)
		state.DirRemove(ctx.View, highDirKey, rs.HighNode, trustKey.Key, false)

		if err := ctx.View.Erase(trustKey); err != nil {
			return tx.TefINTERNAL
		}

		// Decrement OwnerCount for both sides
		if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
		if holderAccount.OwnerCount > 0 {
			holderAccount.OwnerCount--
		}

		if result := ctx.UpdateAccountRoot(holderID, holderAccount); result != tx.TesSUCCESS {
			return result
		}
	} else {
		// Update reserve flags
		if bLowReserveSet && (rs.Flags&state.LsfLowReserve) == 0 {
			rs.Flags |= state.LsfLowReserve
		} else if !bLowReserveSet && (rs.Flags&state.LsfLowReserve) != 0 {
			rs.Flags &^= state.LsfLowReserve
		}
		if bHighReserveSet && (rs.Flags&state.LsfHighReserve) == 0 {
			rs.Flags |= state.LsfHighReserve
		} else if !bHighReserveSet && (rs.Flags&state.LsfHighReserve) != 0 {
			rs.Flags &^= state.LsfHighReserve
		}

		updatedData, serErr := state.SerializeRippleState(rs)
		if serErr != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(trustKey, updatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	return tx.TesSUCCESS
}

// ClawbackAmount returns the Amount field for use by the ValidClawback invariant checker.
// Implements tx.clawbackAmountProvider.
func (c *Clawback) ClawbackAmount() tx.Amount {
	return c.Amount
}

func (c *Clawback) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

// Only require FeatureMPTokensV1 when the Amount actually holds an MPT issue,
// matching rippled's dispatch which checks the Amount type, not the Holder field.
// Reference: rippled Clawback.cpp:90-94 dispatches preflightHelper<MPTIssue>
// which contains the featureMPTokensV1 check.
func (c *Clawback) RequiredAmendments() [][32]byte {
	if c.Amount.IsMPT() {
		return [][32]byte{amendment.FeatureClawback, amendment.FeatureMPTokensV1}
	}
	return [][32]byte{amendment.FeatureClawback}
}
