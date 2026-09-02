package clawback

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// Clawback errors
var (
	ErrClawbackAmountNotPos    = ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
	ErrClawbackHolderWithToken = ter.Errorf(ter.TemMALFORMED, "Holder field cannot be present for token clawback")
	ErrClawbackHolderRequired  = ter.Errorf(ter.TemMALFORMED, "Holder is required for MPToken clawback")
	ErrClawbackHolderIsSelf    = ter.Errorf(ter.TemMALFORMED, "Holder cannot be the same as issuer")
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
}

// NewClawback creates a new Clawback transaction for IOU tokens
func NewClawback(account string, amount state.Amount) *Clawback {
	return &Clawback{
		BaseTx: *tx.NewBaseTx(tx.TypeClawback, account),
		Amount: amount,
	}
}

// NewMPTokenClawback creates a new Clawback transaction for MPTokens
func NewMPTokenClawback(account, holder string, amount state.Amount) *Clawback {
	return &Clawback{
		BaseTx: *tx.NewBaseTx(tx.TypeClawback, account),
		Amount: amount,
		Holder: holder,
	}
}

func (c *Clawback) TxType() tx.Type {
	return tx.TypeClawback
}

// GetFlagsMask adopts the engine FlagsMasker seam. Clawback defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (c *Clawback) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// Validate holds Clawback's rules-independent preflight: the base fields only.
// The flags mask is enforced by the engine at preflight0 (GetFlagsMask). The
// amount/holder body is amendment-dependent (the MPT arm gates on
// featureMPTokensV1) and lives in PreflightRules so its per-arm order matches
// rippled.
// Reference: rippled Clawback.cpp getFlagsMask + preflight().
func (c *Clawback) Validate() error {
	return c.BaseTx.Validate()
}

// PreflightRules is Clawback's preflight body, which rippled dispatches by the
// Amount's asset type (std::visit). A native-XRP Amount dispatches to the Issue
// arm, so — matching rippled — the holder-shape check is evaluated before the
// amount's XRP/zero/negative rejection. The MPT arm leads with the
// featureMPTokensV1 temDISABLED gate (rippled makes it the first line of
// preflightHelper<MPTIssue>, i.e. a per-type preflight check, not a macro gate),
// then the holder-shape temMALFORMED checks, then the amount temBAD_AMOUNT check.
// Reference: rippled Clawback.cpp preflightHelper<Issue>/<MPTIssue>.
func (c *Clawback) PreflightRules(rules *amendment.Rules) error {
	hasHolder := c.FieldPresent("Holder", c.Holder != "")
	if c.Amount.IsMPT() {
		if !rules.Enabled(amendment.FeatureMPTokensV1) {
			return ter.Errorf(ter.TemDISABLED, "MPToken clawback requires MPTokensV1")
		}
		if !hasHolder {
			return ErrClawbackHolderRequired
		}
		if c.Account == c.Holder {
			return ErrClawbackHolderIsSelf
		}
		// maxMPTokenAmount is int64 max, unreachable via a parseable Amount, so
		// only the non-positive bound is observable here.
		if c.Amount.Signum() <= 0 {
			return ErrClawbackAmountNotPos
		}
		return nil
	}
	// Issue arm — IOU, and native XRP, which std::visit routes here too.
	if hasHolder {
		return ErrClawbackHolderWithToken
	}
	// rippled: issuer == holder || isXRP(amount) || amount <= 0 -> temBAD_AMOUNT.
	// The holder is the Amount's issuer field; the issuer is the tx Account.
	if c.Amount.Issuer == c.Account || c.Amount.IsNative() || c.Amount.Signum() <= 0 {
		return ErrClawbackAmountNotPos
	}
	return nil
}

// Reference: rippled Clawback.cpp preclaim() + applyHelper<Issue>() / applyHelper<MPTIssue>()
func (c *Clawback) Apply(ctx *tx.ApplyContext) ter.Result {
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
func (c *Clawback) applyMPT(ctx *tx.ApplyContext) ter.Result {
	// Read the holder's AccountRoot and reject a pseudo-account / AMM holder
	// before the per-issue preclaim checks, mirroring rippled's preclaim order.
	// Reference: rippled Clawback.cpp:202-216
	holderID, err := tx.DecodeAccountIDField(c.Holder, c.FieldPresent("Holder", c.Holder != ""))
	if err != nil {
		return ter.TecNO_DST
	}
	holderAccountData, err := ctx.View.Read(keylet.Account(holderID))
	if err != nil || holderAccountData == nil {
		return ter.TerNO_ACCOUNT
	}
	holderAccount, err := state.ParseAccountRoot(holderAccountData)
	if err != nil {
		return ter.TefINTERNAL
	}
	if result := clawbackHolderGuard(ctx, holderAccount); result != ter.TesSUCCESS {
		return result
	}

	var mptID [24]byte
	issuanceIDBytes, err := hex.DecodeString(c.Amount.MPTIssuanceID())
	if err != nil || len(issuanceIDBytes) != 24 {
		// If the ID is invalid/empty, the issuance won't be found
		return ter.TecOBJECT_NOT_FOUND
	}
	copy(mptID[:], issuanceIDBytes)

	// Look up the issuance
	issuanceKey := keylet.MPTIssuance(mptID)
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Issuance must have CanClawback flag
	if issuance.Flags&entry.LsfMPTCanClawback == 0 {
		return ter.TecNO_PERMISSION
	}

	// Caller must be the issuer
	if issuance.Issuer != ctx.AccountID {
		return ter.TecNO_PERMISSION
	}

	// Look up holder's MPToken
	tokenKey := keylet.MPToken(issuanceKey.Key, holderID)
	tokenRaw, err := ctx.View.Read(tokenKey)
	if err != nil || tokenRaw == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenRaw)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Holder must have a positive balance
	if token.MPTAmount == 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}

	// Extract requested amount as uint64 from the IOU-style Amount
	requested := amountToUint64(c.Amount)
	if requested == 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}

	// Compute actual clawback amount = min(balance, requested)
	actual := min(requested, token.MPTAmount)
	if issuance.OutstandingAmount < actual {
		return ter.TecINTERNAL
	}

	token.MPTAmount -= actual
	issuance.OutstandingAmount -= actual

	updatedToken, err := state.SerializeMPToken(token)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(tokenKey, updatedToken); err != nil {
		return ter.TefINTERNAL
	}

	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedIssuance); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
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

// clawbackHolderGuard rejects clawing back from a pseudo-account or AMM holder.
// Reference: rippled Clawback.cpp:210-216. When featureSingleAssetVault is
// enabled the pseudo-account check subsumes the sfAMMID check (an AMM account is
// itself a pseudo-account), so the order is preserved.
func clawbackHolderGuard(ctx *tx.ApplyContext, holder *state.AccountRoot) ter.Result {
	if ctx.Rules().Enabled(amendment.FeatureSingleAssetVault) && holder.IsPseudoAccount() {
		return ter.TecPSEUDO_ACCOUNT
	}
	if holder.HasAMMID() {
		return ter.TecAMM_ACCOUNT
	}
	return ter.TesSUCCESS
}

// applyIOU handles IOU token clawback (original path).
// Reference: rippled Clawback.cpp preclaim() + applyHelper<Issue>()
func (c *Clawback) applyIOU(ctx *tx.ApplyContext) ter.Result {
	// --- Preclaim checks ---

	// 1. Decode holder from Amount.Issuer
	holderID, err := state.DecodeAccountID(c.Amount.Issuer)
	if err != nil {
		return ter.TecNO_TARGET
	}

	// 2. Read holder's account — terNO_ACCOUNT if missing
	// Reference: rippled Clawback.cpp:206-208
	holderAccountKey := keylet.Account(holderID)
	holderAccountData, err := ctx.View.Read(holderAccountKey)
	if err != nil || holderAccountData == nil {
		return ter.TerNO_ACCOUNT
	}
	holderAccount, err := state.ParseAccountRoot(holderAccountData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// 3. Reject a pseudo-account / AMM holder.
	// Reference: rippled Clawback.cpp:210-216, evaluated before the per-issue
	// preclaim checks.
	if result := clawbackHolderGuard(ctx, holderAccount); result != ter.TesSUCCESS {
		return result
	}

	// 4. Check issuer flags (ctx.Account is the issuer)
	// Reference: rippled Clawback.cpp preclaimHelper<Issue>() lines 117-123
	// AllowTrustLineClawback must be set, NoFreeze must NOT be set
	if (ctx.Account.Flags & state.LsfAllowTrustLineClawback) == 0 {
		return ter.TecNO_PERMISSION
	}
	if (ctx.Account.Flags & state.LsfNoFreeze) != 0 {
		return ter.TecNO_PERMISSION
	}

	// 5. Read trust line
	// Reference: rippled Clawback.cpp:125-128
	trustKey := keylet.Line(holderID, ctx.AccountID, c.Amount.Currency)
	trustData, err := ctx.View.Read(trustKey)
	if err != nil || trustData == nil {
		return ter.TecNO_LINE
	}
	rs, err := state.ParseRippleState(trustData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// 6. Balance direction check
	// Reference: rippled Clawback.cpp:132-138
	// Balance is from LOW account's perspective:
	//   Positive: HIGH owes LOW (HIGH is the issuer)
	//   Negative: LOW owes HIGH (LOW is the issuer)
	// If balance > 0, issuer must be HIGH (issuer > holder)
	// If balance < 0, issuer must be LOW (issuer < holder)
	issuerIsLow := state.CompareAccountIDs(ctx.AccountID, holderID) < 0
	if rs.Balance.Signum() > 0 && issuerIsLow {
		return ter.TecNO_PERMISSION
	}
	if rs.Balance.Signum() < 0 && !issuerIsLow {
		return ter.TecNO_PERMISSION
	}

	// 7. Check holder has funds (accountHolds equivalent, ignoring freeze)
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
		return ter.TecINSUFFICIENT_FUNDS
	}

	// --- Apply ---
	// Reference: rippled Clawback.cpp applyHelper<Issue>() lines 230-259

	// 8. Compute actual claw amount = min(holderBalance, clawAmount)
	// Set the issuer field to the actual issuer (matching rippled line 239)
	clawAmount := c.Amount
	clawAmount.Issuer = ctx.Account.Account

	var actualAmount state.Amount
	if holderBalance.Compare(clawAmount) < 0 {
		actualAmount = holderBalance
	} else {
		actualAmount = clawAmount
	}

	// 9. Transfer from holder to issuer (rippleCredit equivalent)
	// Reference: rippled View.cpp rippleCredit()
	// If holder is LOW: holder pays issuer (HIGH) → balance decreases
	// If holder is HIGH: holder pays issuer (LOW) → balance increases
	if holderIsLow {
		rs.Balance, err = rs.Balance.SubWithNumberContext(
			actualAmount,
			ctx.NumberContext(),
			state.RoundToNearest,
		)
	} else {
		rs.Balance, err = rs.Balance.AddWithNumberContext(
			actualAmount,
			ctx.NumberContext(),
			state.RoundToNearest,
		)
	}
	if err != nil {
		return ter.TefINTERNAL
	}

	// 10. Check if trust line should be deleted (default state)
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
		if res, err := state.DirRemove(ctx.View, lowDirKey, rs.LowNode, trustKey.Key, false); err != nil || !res.Success {
			return ter.TefBAD_LEDGER
		}
		highDirKey := keylet.OwnerDir(highAccountID)
		if res, err := state.DirRemove(ctx.View, highDirKey, rs.HighNode, trustKey.Key, false); err != nil || !res.Success {
			return ter.TefBAD_LEDGER
		}

		if err := ctx.View.Erase(trustKey); err != nil {
			return ter.TefINTERNAL
		}

		issuerSponsor, holderSponsor := rs.HighSponsor, rs.LowSponsor
		issuerReserved, holderReserved := rs.Flags&state.LsfHighReserve != 0, rs.Flags&state.LsfLowReserve != 0
		if issuerIsLow {
			issuerSponsor, holderSponsor = rs.LowSponsor, rs.HighSponsor
			issuerReserved, holderReserved = rs.Flags&state.LsfLowReserve != 0, rs.Flags&state.LsfHighReserve != 0
		}
		if issuerReserved {
			if err := tx.DecreaseOwnerCount(ctx.View, ctx.Account, issuerSponsor, 1); err != nil {
				return ctx.Internal("Clawback.IssuerOwnerCount", err)
			}
		}
		if latest, readErr := tx.ReadAccountRoot(ctx.View, holderID); readErr != nil || latest == nil {
			return ter.TefINTERNAL
		} else {
			holderAccount.SponsoringOwnerCount = latest.SponsoringOwnerCount
		}
		if holderReserved {
			if err := tx.DecreaseOwnerCount(ctx.View, holderAccount, holderSponsor, 1); err != nil {
				return ctx.Internal("Clawback.HolderOwnerCount", err)
			}
			ctx.SyncSenderSponsorCounts(holderSponsor)
		}

		if result := ctx.UpdateAccountRoot(holderID, holderAccount); result != ter.TesSUCCESS {
			return result
		}
	} else {
		lowID, highID := holderID, ctx.AccountID
		if issuerIsLow {
			lowID, highID = ctx.AccountID, holderID
		}
		updateReserve := func(ownerID [20]byte, flag uint32, desired bool, sponsorAddress string) (string, ter.Result) {
			reserved := rs.Flags&flag != 0
			if reserved == desired {
				return sponsorAddress, ter.TesSUCCESS
			}
			if !desired {
				if result := tx.DecreaseOwnerCountFor(ctx, ownerID, sponsorAddress, 1); result != ter.TesSUCCESS {
					return sponsorAddress, result
				}
				rs.Flags &^= flag
				return "", ter.TesSUCCESS
			}

			if ownerID == ctx.AccountID {
				newSponsor, result := tx.IncreaseOwnerCount(ctx, ownerID, ctx.Account, 1)
				if result != ter.TesSUCCESS {
					return sponsorAddress, result
				}
				sponsorAddress = newSponsor
			} else if err := tx.AdjustOwnerCount(ctx.View, ownerID, 1); err != nil {
				return sponsorAddress, ter.TefINTERNAL
			}
			rs.Flags |= flag
			return sponsorAddress, ter.TesSUCCESS
		}

		var result ter.Result
		rs.LowSponsor, result = updateReserve(lowID, state.LsfLowReserve, bLowReserveSet, rs.LowSponsor)
		if result != ter.TesSUCCESS {
			return result
		}
		rs.HighSponsor, result = updateReserve(highID, state.LsfHighReserve, bHighReserveSet, rs.HighSponsor)
		if result != ter.TesSUCCESS {
			return result
		}

		updatedData, serErr := state.SerializeRippleState(rs)
		if serErr != nil {
			return ter.TefINTERNAL
		}
		if err := ctx.View.Update(trustKey, updatedData); err != nil {
			return ter.TefINTERNAL
		}
	}

	return ter.TesSUCCESS
}

// ClawbackAmount returns the Amount field for use by the ValidClawback invariant checker.
// Implements tx.clawbackAmountProvider.
func (c *Clawback) ClawbackAmount() tx.Amount {
	return c.Amount
}

func (c *Clawback) ClawbackHolder() string {
	return c.Holder
}

func (c *Clawback) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

// RequiredAmendments leaves Clawback unconditional. MPTokensV1 remains a
// preflight-arm gate for MPT clawbacks.
func (c *Clawback) RequiredAmendments() [][32]byte {
	return nil
}
