package amm

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// AMMBid places a bid on an AMM auction slot.
type AMMBid struct {
	tx.BaseTx

	// Asset identifies the first asset of the AMM (required)
	Asset tx.Asset `json:"Asset" xrpl:"Asset,asset"`

	// Asset2 identifies the second asset of the AMM (required)
	Asset2 tx.Asset `json:"Asset2" xrpl:"Asset2,asset"`

	// BidMin is the minimum bid amount (optional)
	BidMin *tx.Amount `json:"BidMin,omitempty" xrpl:"BidMin,omitempty,amount"`

	// BidMax is the maximum bid amount (optional)
	BidMax *tx.Amount `json:"BidMax,omitempty" xrpl:"BidMax,omitempty,amount"`

	// AuthAccounts are accounts to authorize for discounted trading (optional)
	AuthAccounts []AuthAccount `json:"AuthAccounts,omitempty" xrpl:"AuthAccounts,omitempty"`
}

// NewAMMBid creates a new AMMBid transaction
func NewAMMBid(account string, asset, asset2 tx.Asset) *AMMBid {
	return &AMMBid{
		BaseTx: *tx.NewBaseTx(tx.TypeAMMBid, account),
		Asset:  asset,
		Asset2: asset2,
	}
}

func (a *AMMBid) TxType() tx.Type {
	return tx.TypeAMMBid
}

// Reference: rippled AMMBid.cpp preflight
// GetFlagsMask adopts the engine FlagsMasker seam. AMMBid defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (a *AMMBid) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tfAMMBidMask
}

func (a *AMMBid) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Reference: rippled AMMBid.cpp preflight lines 48-53
	if err := validateAssetPair(a.Asset, a.Asset2); err != nil {
		return err
	}

	// Validate BidMin / BidMax if present. The error code
	// (temBAD_CURRENCY / temBAD_ISSUER / temBAD_AMOUNT) is propagated unchanged.
	// Reference: rippled AMMBid.cpp preflight lines 55-71
	if a.BidMin != nil {
		if err := validateAMMAmount(*a.BidMin); err != nil {
			return err
		}
	}
	if a.BidMax != nil {
		if err := validateAMMAmount(*a.BidMax); err != nil {
			return err
		}
	}

	// Max 4 auth accounts. The fixAMMv1_3-gated duplicate/self-authorization
	// check lives in PreflightRules (it needs amendment rules), still ahead of
	// any preclaim state check, matching rippled.
	// Reference: rippled AMMBid.cpp preflight lines 73-96
	if len(a.AuthAccounts) > auctionSlotMaxAuthAccounts {
		return ter.Errorf(ter.TemMALFORMED, "cannot have more than 4 AuthAccounts")
	}

	return nil
}

// PreflightRules performs AMMBid's amendment-gated preflight check: under
// fixAMMv1_3 an AuthAccounts entry that duplicates another or equals the
// submitting account is temMALFORMED. rippled runs this in preflight, before
// signature verification and before every preclaim state check (terNO_AMM,
// tecAMM_EMPTY, terNO_ACCOUNT), so it must not sink into Preclaim.
// Reference: rippled AMMBid.cpp preflight lines 81-95.
func (a *AMMBid) PreflightRules(rules *amendment.Rules) error {
	if len(a.AuthAccounts) == 0 || !rules.Enabled(amendment.FeatureFixAMMv1_3) {
		return nil
	}
	seen := make(map[string]bool)
	for _, authAcct := range a.AuthAccounts {
		acct := authAcct.AuthAccount.Account
		if acct == a.Common.Account || seen[acct] {
			return ter.Errorf(ter.TemMALFORMED, "duplicate or self AuthAccount")
		}
		seen[acct] = true
	}
	return nil
}

func (a *AMMBid) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

func (a *AMMBid) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// CheckExtraFeatures gates MPT pool assets on the MPTokensV2 amendment.
func (a *AMMBid) CheckExtraFeatures(rules *amendment.Rules) error {
	return requireMPTokensV2(rules, a.Asset.IsMPT() || a.Asset2.IsMPT())
}

// Preclaim validates the AMM, the bidder's LP holdings, and the bid bounds.
// Reference: rippled AMMBid.cpp preclaim (plus the fixAMMv1_3-gated AuthAccounts
// duplicate/self check that rippled performs in preflight).
func (a *AMMBid) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	amm, _, result := readAMM(view, a.Asset, a.Asset2)
	if result != ter.TesSUCCESS {
		return result
	}

	lptAMMBalance := amm.LPTokenBalance
	if lptAMMBalance.IsZero() {
		return ter.TecAMM_EMPTY
	}

	// Reference: rippled AMMBid.cpp preclaim lines 116-126
	for _, authAcct := range a.AuthAccounts {
		authAccountID, err := state.DecodeAccountID(authAcct.AuthAccount.Account)
		if err != nil {
			return ter.TerNO_ACCOUNT
		}
		if exists, _ := view.Exists(keylet.Account(authAccountID)); !exists {
			return ter.TerNO_ACCOUNT
		}
	}

	accountID, err := state.DecodeAccountID(a.Account)
	if err != nil {
		return ter.TecAMM_INVALID_TOKENS
	}
	lpTokens := ammLPHolds(view, amm, accountID)
	if lpTokens.IsZero() {
		return ter.TecAMM_INVALID_TOKENS
	}

	// BidMin / BidMax must be LP tokens, within the bidder's holdings and the
	// pool, and ordered. Reference: rippled AMMBid.cpp preclaim lines 137-172
	if a.BidMin != nil {
		if a.BidMin.Currency != lpTokens.Currency || a.BidMin.Issuer != lpTokens.Issuer {
			return ter.TemBAD_AMM_TOKENS
		}
		if isGreater(*a.BidMin, lpTokens) || isGreaterOrEqual(*a.BidMin, lptAMMBalance) {
			return ter.TecAMM_INVALID_TOKENS
		}
	}
	if a.BidMax != nil {
		if a.BidMax.Currency != lpTokens.Currency || a.BidMax.Issuer != lpTokens.Issuer {
			return ter.TemBAD_AMM_TOKENS
		}
		if isGreater(*a.BidMax, lpTokens) || isGreaterOrEqual(*a.BidMax, lptAMMBalance) {
			return ter.TecAMM_INVALID_TOKENS
		}
	}
	if a.BidMin != nil && a.BidMax != nil && isGreater(*a.BidMin, *a.BidMax) {
		return ter.TecAMM_INVALID_TOKENS
	}

	return ter.TesSUCCESS
}

// Reference: rippled AMMBid.cpp applyBid
func (a *AMMBid) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("amm bid apply",
		"account", a.Account,
		"asset", a.Asset,
		"asset2", a.Asset2,
		"bidMin", a.BidMin,
		"bidMax", a.BidMax,
	)

	accountID := ctx.AccountID
	math := newNumberMath(ctx)

	amm, ammKey, result := readAMM(ctx.View, a.Asset, a.Asset2)
	if result != ter.TesSUCCESS {
		return result
	}

	lptAMMBalance := amm.LPTokenBalance
	if lptAMMBalance.IsZero() {
		return ter.TecAMM_EMPTY
	}

	// Reference: rippled AMMBid.cpp preclaim line 129
	lpTokens := ammLPHolds(ctx.View, amm, accountID)
	if lpTokens.IsZero() {
		return ter.TecAMM_INVALID_TOKENS
	}

	// Compare against lpTokens.issue(), matching rippled exactly:
	//   bidMin->issue() != lpTokens.issue()
	// Reference: rippled AMMBid.cpp preclaim lines 137-160
	lptCurrency := lpTokens.Currency
	lptIssuer := lpTokens.Issuer

	bidMin := zeroAmount(tx.Asset{})
	bidMax := zeroAmount(tx.Asset{})

	if a.BidMin != nil {
		bidMin = *a.BidMin
	}
	if a.BidMax != nil {
		bidMax = *a.BidMax
	}

	tradingFee := getFee(math, amm.TradingFee)

	// Minimum slot price, evaluated left-to-right in Number space:
	// lptAMMBalance * tradingFee / auctionSlotMinFeeFraction.
	minSlotPriceNumber := math.div(
		math.fromAmount(lptAMMBalance).MulRounded(tradingFee, state.RoundToNearest),
		math.int(auctionSlotMinFeeFraction),
		state.RoundToNearest,
	)
	minSlotPrice := math.toAmount(minSlotPriceNumber, lptAMMBalance, state.RoundToNearest)

	discountedFee := amm.TradingFee / uint16(auctionSlotDiscountedFeeFraction)

	// Reference: rippled AMMBid.cpp:192 — view.info().parentCloseTime
	currentTime := ctx.Config.ParentCloseTime

	// Reference: rippled AMMBid.cpp lines 192-203 — fixInnerObjTemplate enforcement
	if amm.AuctionSlot == nil {
		if ctx.Rules().Enabled(amendment.FeatureFixInnerObjTemplate) {
			return ter.TefEXCEPTION
		}
		amm.AuctionSlot = &AuctionSlotData{
			AuthAccounts: make([][20]byte, 0),
			Price:        zeroAmount(tx.Asset{}),
		}
	}

	// Calculate time slot (0-19). rippled's ammAuctionTimeSlot only computes a
	// slot when Expiration >= TOTAL_TIME_SLOT_SECS, so the elapsed subtraction
	// below cannot underflow. Reference: rippled AMMCore.cpp:113-124.
	var timeSlot *int
	if amm.AuctionSlot.Expiration >= auctionSlotTotalTimeSecs && currentTime < amm.AuctionSlot.Expiration {
		elapsed := amm.AuctionSlot.Expiration - auctionSlotTotalTimeSecs
		if currentTime >= elapsed {
			slot := int((currentTime - elapsed) / auctionSlotIntervalDuration)
			if slot >= 0 && slot < auctionSlotTimeIntervals {
				timeSlot = &slot
			}
		}
	}

	validOwner := false
	if timeSlot != nil && *timeSlot < auctionSlotTimeIntervals-1 {
		var zeroAccount [20]byte
		if amm.AuctionSlot.Account != zeroAccount {
			ownerKey := keylet.Account(amm.AuctionSlot.Account)
			exists, _ := ctx.View.Exists(ownerKey)
			validOwner = exists
		}
	}

	var computedPrice tx.Amount
	fractionRemaining := math.zero()
	pricePurchased := amm.AuctionSlot.Price

	if !validOwner || timeSlot == nil {
		// Slot is unowned or expired - pay minimum price
		computedPrice = minSlotPrice
	} else {
		// Slot is owned - calculate price based on time interval
		// fractionUsed = (timeSlot + 1) / auctionSlotTimeIntervals
		slotNum := *timeSlot + 1
		fractionUsed := math.div(math.int(int64(slotNum)), math.int(auctionSlotTimeIntervals), state.RoundToNearest)
		fractionRemaining = math.subFromOne(fractionUsed, state.RoundToNearest)

		// price1p05 = pricePurchased * 1.05
		multiplier := math.number(105, -2, state.RoundToNearest)
		price1p05 := math.fromAmount(pricePurchased).MulRounded(multiplier, state.RoundToNearest)

		if *timeSlot == 0 {
			// First interval: price = pricePurchased * 1.05 + minSlotPrice
			computedPrice = math.toAmount(price1p05.AddRounded(minSlotPriceNumber, state.RoundToNearest), lptAMMBalance, state.RoundToNearest)
		} else {
			// Other intervals: price = pricePurchased * 1.05 * (1 - power(fractionUsed, 60)) + minSlotPrice
			// Reference: rippled AMMBid.cpp line 336
			fractionUsedPow60 := math.power(fractionUsed, 60, state.RoundToNearest)
			decayFactor := math.subFromOne(fractionUsedPow60, state.RoundToNearest)
			decayedPrice := price1p05.MulRounded(decayFactor, state.RoundToNearest)
			computedPrice = math.toAmount(decayedPrice.AddRounded(minSlotPriceNumber, state.RoundToNearest), lptAMMBalance, state.RoundToNearest)
		}
	}

	var payPrice tx.Amount
	hasBidMin := !bidMin.IsZero()
	hasBidMax := !bidMax.IsZero()

	if hasBidMin && hasBidMax {
		if isLessOrEqual(computedPrice, bidMax) {
			payPrice = maxAmount(computedPrice, bidMin)
		} else {
			ctx.Log.Debug("amm bid: not in range", "computedPrice", computedPrice, "bidMin", bidMin, "bidMax", bidMax)
			return ter.TecAMM_FAILED
		}
	} else if hasBidMin {
		payPrice = maxAmount(computedPrice, bidMin)
	} else if hasBidMax {
		if isLessOrEqual(computedPrice, bidMax) {
			payPrice = computedPrice
		} else {
			ctx.Log.Debug("amm bid: not in range", "computedPrice", computedPrice, "bidMax", bidMax)
			return ter.TecAMM_FAILED
		}
	} else {
		payPrice = computedPrice
	}

	if isGreater(payPrice, lpTokens) {
		return ter.TecAMM_INVALID_TOKENS
	}

	// Reference: rippled AMMBid.cpp:345-367
	var refund tx.Amount = zeroAmount(tx.Asset{})
	var burn tx.Amount = payPrice

	if validOwner && timeSlot != nil {
		// Refund previous owner: refund = fractionRemaining * pricePurchased
		refund = math.multiplyToAmount(math.fromAmount(pricePurchased), fractionRemaining, lptAMMBalance, state.RoundToNearest)
		if isGreater(refund, payPrice) {
			ctx.Log.Error("amm bid: refund exceeds payPrice", "refund", refund, "payPrice", payPrice)
			return ter.TefINTERNAL
		}
		burn, _ = payPrice.Sub(refund)

		// Transfer refund from bidder to previous owner via LP token trust lines.
		// Reference: rippled AMMBid.cpp:355-360 — accountSend(account_, previousOwner, refund)
		if !refund.IsZero() {
			refundWithIssue := state.NewIssuedAmountFromValue(
				refund.Mantissa(), refund.Exponent(), lptCurrency, lptIssuer)
			if r := transferLPTokens(ctx.View, accountID, amm.AuctionSlot.Account, amm.Account, refundWithIssue); r != ter.TesSUCCESS {
				return r
			}
		}
	}

	// Burn LP tokens: adjust, debit bidder's trust line, then reduce AMM LPTokenBalance.
	// Reference: rippled AMMBid.cpp updateSlot() lines 249-268
	saBurn := adjustLPTokens(lptAMMBalance, burn, false)
	if isGreaterOrEqual(saBurn, lptAMMBalance) {
		ctx.Log.Error("amm bid: LP token burn exceeds AMM balance", "burn", saBurn, "lptAMMBalance", lptAMMBalance)
		return ter.TecINTERNAL
	}
	if !saBurn.IsZero() {
		burnWithIssue := state.NewIssuedAmountFromValue(
			saBurn.Mantissa(), saBurn.Exponent(), lptCurrency, lptIssuer)
		if r := redeemLPTokens(ctx.View, accountID, amm.Account, burnWithIssue); r != ter.TesSUCCESS {
			return r
		}
	}
	newLPBalance, err := amm.LPTokenBalance.Sub(saBurn)
	if err != nil {
		return ter.TecINTERNAL
	}
	amm.LPTokenBalance = newLPBalance

	amm.AuctionSlot.Account = accountID
	amm.AuctionSlot.Expiration = currentTime + auctionSlotTotalTimeSecs
	amm.AuctionSlot.Price = payPrice
	amm.AuctionSlot.DiscountedFee = discountedFee

	if a.AuthAccounts != nil {
		amm.AuctionSlot.AuthAccountsPresent = true
		amm.AuctionSlot.AuthAccounts = make([][20]byte, 0, len(a.AuthAccounts))
		for _, authAccountEntry := range a.AuthAccounts {
			authAccountID, err := state.DecodeAccountID(authAccountEntry.AuthAccount.Account)
			if err == nil {
				amm.AuctionSlot.AuthAccounts = append(amm.AuctionSlot.AuthAccounts, authAccountID)
			}
		}
	} else {
		amm.AuctionSlot.AuthAccountsPresent = false
		amm.AuctionSlot.AuthAccounts = make([][20]byte, 0)
	}

	ammBytes, err := serializeAMMData(amm)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(ammKey, ammBytes); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// redeemLPTokens debits an account's LP token trust line, sending tokens back to the AMM (issuer).
// This is the LP token equivalent of rippled's redeemIOU().
// Reference: rippled Ledger/View.cpp redeemIOU()
func redeemLPTokens(view tx.LedgerView, accountID, ammAccountID [20]byte, amount tx.Amount) ter.Result {
	if amount.IsZero() {
		return ter.TesSUCCESS
	}
	return adjustLPTrustLine(view, accountID, ammAccountID, amount, false)
}

// transferLPTokens transfers LP tokens from one account to another via the AMM (issuer).
// This debits the sender's trust line and credits the receiver's trust line.
// Reference: rippled Ledger/View.cpp accountSend() → rippleCredit()
func transferLPTokens(view tx.LedgerView, from, to, ammAccountID [20]byte, amount tx.Amount) ter.Result {
	if amount.IsZero() || from == to {
		return ter.TesSUCCESS
	}
	// Debit sender → AMM (issuer)
	if r := adjustLPTrustLine(view, from, ammAccountID, amount, false); r != ter.TesSUCCESS {
		return r
	}
	// Credit AMM (issuer) → receiver
	return adjustLPTrustLine(view, to, ammAccountID, amount, true)
}

// adjustLPTrustLine modifies the LP token trust line balance between an account and the AMM.
// If isCredit is true, the account's balance increases; if false, it decreases.
// Reference: rippled Ledger/View.cpp rippleCredit()
func adjustLPTrustLine(view tx.LedgerView, accountID, ammAccountID [20]byte, amount tx.Amount, isCredit bool) ter.Result {
	trustLineKey := keylet.Line(accountID, ammAccountID, amount.Currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return ter.TecINTERNAL
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Determine if the LP account is the low account
	lpIsLow := keylet.IsLowAccount(accountID, ammAccountID)

	// Trust line balance convention:
	//   positive balance → low account holds tokens (low owes high)
	//   For LP tokens: AMM is the issuer, LP is the holder
	currentBalance := rs.Balance

	var newBalance tx.Amount
	if lpIsLow {
		// LP is low: positive = LP holds tokens
		if isCredit {
			newBalance, err = currentBalance.Add(amount)
		} else {
			newBalance, err = currentBalance.Sub(amount)
		}
	} else {
		// LP is high: negative = LP holds tokens (from low perspective)
		if isCredit {
			newBalance, err = currentBalance.Sub(amount)
		} else {
			newBalance, err = currentBalance.Add(amount)
		}
	}
	if err != nil {
		return ter.TefINTERNAL
	}

	rs.Balance = state.NewIssuedAmountFromValue(
		newBalance.Mantissa(), newBalance.Exponent(),
		rs.Balance.Currency, rs.Balance.Issuer,
	)

	rsBytes, err := state.SerializeRippleState(rs)
	if err != nil {
		return ter.TefINTERNAL
	}

	if err := view.Update(trustLineKey, rsBytes); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}
