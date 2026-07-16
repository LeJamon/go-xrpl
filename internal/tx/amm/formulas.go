package amm

import (
	"slices"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// getAccountTradingFee returns the trading fee for an account interacting with
// an AMM, discounted if the account holds the auction slot or is one of its
// authorized accounts.
func getAccountTradingFee(amm *AMMData, accountID [20]byte, parentCloseTime uint32) uint16 {
	if amm.AuctionSlot != nil {
		// Check if auction slot is not expired
		if parentCloseTime < amm.AuctionSlot.Expiration {
			// Check if account is the auction slot holder
			if amm.AuctionSlot.Account == accountID {
				return amm.AuctionSlot.DiscountedFee
			}
			// Check authorized accounts
			if slices.Contains(amm.AuctionSlot.AuthAccounts, accountID) {
				return amm.AuctionSlot.DiscountedFee
			}
		}
	}
	return amm.TradingFee
}

// getFee converts a trading fee in basis points to a fractional IOU Amount:
// fee / voteWeightScaleFactor (e.g. 1000 basis points = 1% = 0.01).
func getFee(math numberMath, fee uint16) state.XRPLNumber {
	return math.number(int64(fee), -5, state.RoundToNearest)
}

// feeMult returns (1 - getFee(tfee)), i.e., (1 - fee).
// Reference: rippled AMMCore.h feeMult(): 1 - getFee(tfee)
func feeMult(math numberMath, tfee uint16) state.XRPLNumber {
	return math.subFromOne(getFee(math, tfee), state.RoundToNearest)
}

// feeMultHalf returns (1 - getFee(tfee)/2), i.e., (1 - fee/2).
// Reference: rippled AMMCore.h feeMultHalf(): 1 - getFee(tfee) / 2
func feeMultHalf(math numberMath, tfee uint16) state.XRPLNumber {
	halfFee := math.div(getFee(math, tfee), math.int(2), state.RoundToNearest)
	return math.subFromOne(halfFee, state.RoundToNearest)
}

// adjustLPTokens adjusts LP tokens for precision loss when adding/subtracting
// from the AMM balance.
// Reference: rippled AMMHelpers.cpp adjustLPTokens()
func adjustLPTokens(math numberMath, lptAMMBalance, lpTokens tx.Amount, isDeposit bool) tx.Amount {
	const mode = state.RoundDownward

	if isDeposit {
		return math.subAmounts(math.addAmounts(lptAMMBalance, lpTokens, mode), lptAMMBalance, mode)
	}
	return math.addAmounts(math.subAmounts(lpTokens, lptAMMBalance, mode), lptAMMBalance, mode)
}

// adjustAmountsByLPTokens is the post-computation adjustment pipeline.
// Reference: rippled AMMHelpers.cpp adjustAmountsByLPTokens()
// IMPORTANT: when fixAMMv1_3 is enabled, this returns the amounts unchanged.
func adjustAmountsByLPTokens(
	math numberMath,
	amountBalance, amount tx.Amount,
	amount2 *tx.Amount,
	lptAMMBalance, lpTokens tx.Amount,
	tfee uint16,
	isDeposit bool,
	fixAMMv1_3 bool,
	fixAMMv1_1 bool,
) (tx.Amount, *tx.Amount, tx.Amount) {
	// AMMv1_3 amendment adjusts tokens and amounts in deposit/withdraw formulas directly
	if fixAMMv1_3 {
		return amount, amount2, lpTokens
	}

	lpTokensActual := adjustLPTokens(math, lptAMMBalance, lpTokens, isDeposit)

	if lpTokensActual.IsZero() {
		var amount2Opt *tx.Amount
		if amount2 != nil {
			zero := zeroAmount(amountAsset(*amount2))
			amount2Opt = &zero
		}
		zero := zeroAmount(amountAsset(amount))
		return zero, amount2Opt, lpTokensActual
	}

	if math.fromAmount(lpTokensActual).Cmp(math.fromAmount(lpTokens)) < 0 {
		// Equal trade
		if amount2 != nil {
			fr := math.div(math.fromAmount(lpTokensActual), math.fromAmount(lpTokens), state.RoundToNearest)
			amountActual := math.multiplyToAmount(math.fromAmount(amount), fr, amount, state.RoundToNearest)
			amount2Actual := math.multiplyToAmount(math.fromAmount(*amount2), fr, *amount2, state.RoundToNearest)
			if !fixAMMv1_1 {
				if math.fromAmount(amountActual).Cmp(math.fromAmount(amount)) < 0 {
					// keep amountActual
				} else {
					amountActual = amount
				}
				if math.fromAmount(amount2Actual).Cmp(math.fromAmount(*amount2)) < 0 {
					// keep amount2Actual
				} else {
					amount2Actual = *amount2
				}
			}
			return amountActual, &amount2Actual, lpTokensActual
		}

		// Single trade
		var amountActual tx.Amount
		if isDeposit {
			amountActual = ammAssetIn(math, amountBalance, lptAMMBalance, lpTokensActual, tfee, false)
		} else if !fixAMMv1_1 {
			amountActual = ammAssetOut(math, amountBalance, lptAMMBalance, lpTokens, tfee, false)
		} else {
			amountActual = ammAssetOut(math, amountBalance, lptAMMBalance, lpTokensActual, tfee, false)
		}
		if !fixAMMv1_1 {
			if math.fromAmount(amountActual).Cmp(math.fromAmount(amount)) < 0 {
				return amountActual, nil, lpTokensActual
			}
			return amount, nil, lpTokensActual
		}
		return amountActual, nil, lpTokensActual
	}

	return amount, amount2, lpTokensActual
}

// getRoundedAsset rounds an AMM equal deposit/withdrawal amount.
// For simple signature: balance * frac
// Reference: rippled AMMHelpers.h getRoundedAsset() (template version)
func getRoundedAsset(math numberMath, fixAMMv1_3 bool, balance tx.Amount, frac state.XRPLNumber, isDeposit bool) tx.Amount {
	if !fixAMMv1_3 {
		return math.multiplyToAmount(math.fromAmount(balance), frac, balance, state.RoundToNearest)
	}
	rm := getAssetRounding(isDeposit)
	return math.multiplyToAmount(math.fromAmountRounded(balance, rm), frac, balance, rm)
}

// getRoundedAssetCb rounds an AMM single deposit/withdrawal amount using callbacks.
// productCb receives the rounding mode under which it must evaluate its
// Number expression (rippled runs the callback inside a NumberRoundModeGuard).
// Reference: rippled AMMHelpers.cpp getRoundedAsset() (callback version)
func getRoundedAssetCb(math numberMath, fixAMMv1_3 bool, noRoundCb func() state.XRPLNumber, balance tx.Amount, productCb func(state.RoundingMode) state.XRPLNumber, isDeposit bool) tx.Amount {
	if !fixAMMv1_3 {
		return math.toAmount(noRoundCb(), balance, state.RoundToNearest)
	}
	rm := getAssetRounding(isDeposit)
	if isDeposit {
		return math.multiplyToAmount(math.fromAmountRounded(balance, rm), productCb(state.RoundToNearest), balance, rm)
	}
	return math.toAmount(productCb(rm), balance, rm)
}

// getRoundedLPTokens rounds LPTokens for equal deposit/withdrawal.
// Reference: rippled AMMHelpers.cpp getRoundedLPTokens() (simple version)
func getRoundedLPTokens(math numberMath, fixAMMv1_3 bool, balance tx.Amount, frac state.XRPLNumber, isDeposit bool) tx.Amount {
	if !fixAMMv1_3 {
		return math.multiplyToAmount(math.fromAmount(balance), frac, balance, state.RoundToNearest)
	}
	rm := getLPTokenRounding(isDeposit)
	tokens := math.multiplyToAmount(math.fromAmountRounded(balance, rm), frac, balance, rm)
	return adjustLPTokens(math, balance, tokens, isDeposit)
}

// getRoundedLPTokensCb rounds LPTokens for single deposit/withdrawal using callbacks.
// productCb receives the rounding mode under which it must evaluate its
// Number expression (rippled runs the callback inside a NumberRoundModeGuard).
// Reference: rippled AMMHelpers.cpp getRoundedLPTokens() (callback version)
func getRoundedLPTokensCb(math numberMath, fixAMMv1_3 bool, noRoundCb func() state.XRPLNumber, lptAMMBalance tx.Amount, productCb func(state.RoundingMode) state.XRPLNumber, isDeposit bool) tx.Amount {
	if !fixAMMv1_3 {
		return math.toAmount(noRoundCb(), lptAMMBalance, state.RoundToNearest)
	}
	rm := getLPTokenRounding(isDeposit)
	var tokens tx.Amount
	if isDeposit {
		tokens = math.toAmount(productCb(rm), lptAMMBalance, rm)
	} else {
		tokens = math.multiplyToAmount(math.fromAmountRounded(lptAMMBalance, rm), productCb(state.RoundToNearest), lptAMMBalance, rm)
	}
	return adjustLPTokens(math, lptAMMBalance, tokens, isDeposit)
}

// adjustAssetInByTokens adjusts deposit asset amount to factor in adjusted tokens.
// Reference: rippled AMMHelpers.cpp adjustAssetInByTokens()
func adjustAssetInByTokens(math numberMath, fixAMMv1_3 bool, balance, amount, lptAMMBalance, tokens tx.Amount, tfee uint16) (tx.Amount, tx.Amount) {
	if !fixAMMv1_3 {
		return tokens, amount
	}
	assetAdj := ammAssetIn(math, balance, lptAMMBalance, tokens, tfee, true)
	tokensAdj := tokens
	// Rounding didn't work the right way.
	if math.fromAmount(assetAdj).Cmp(math.fromAmount(amount)) > 0 {
		diff := math.subAmounts(assetAdj, amount, state.RoundToNearest)
		adjAmountFull := math.subAmounts(amount, diff, state.RoundToNearest)
		t := lpTokensOut(math, balance, adjAmountFull, lptAMMBalance, tfee, true)
		tokensAdj = adjustLPTokens(math, lptAMMBalance, t, true)
		assetAdj = ammAssetIn(math, balance, lptAMMBalance, tokensAdj, tfee, true)
	}
	return tokensAdj, minAmountIOU(amount, assetAdj)
}

// adjustAssetOutByTokens adjusts withdrawal asset amount to factor in adjusted tokens.
// Reference: rippled AMMHelpers.cpp adjustAssetOutByTokens()
func adjustAssetOutByTokens(math numberMath, fixAMMv1_3 bool, balance, amount, lptAMMBalance, tokens tx.Amount, tfee uint16) (tx.Amount, tx.Amount) {
	if !fixAMMv1_3 {
		return tokens, amount
	}
	assetAdj := ammAssetOut(math, balance, lptAMMBalance, tokens, tfee, true)
	tokensAdj := tokens
	// Rounding didn't work the right way.
	if math.fromAmount(assetAdj).Cmp(math.fromAmount(amount)) > 0 {
		diff := math.subAmounts(assetAdj, amount, state.RoundToNearest)
		adjAmountFull := math.subAmounts(amount, diff, state.RoundToNearest)
		t := calcLPTokensIn(math, balance, adjAmountFull, lptAMMBalance, tfee, true)
		tokensAdj = adjustLPTokens(math, lptAMMBalance, t, false)
		assetAdj = ammAssetOut(math, balance, lptAMMBalance, tokensAdj, tfee, true)
	}
	return tokensAdj, minAmountIOU(amount, assetAdj)
}

// adjustFracByTokens recalculates the fraction after token adjustment.
// Reference: rippled AMMHelpers.cpp adjustFracByTokens()
func adjustFracByTokens(math numberMath, fixAMMv1_3 bool, lptAMMBalance, tokens tx.Amount, frac state.XRPLNumber) state.XRPLNumber {
	if !fixAMMv1_3 {
		return frac
	}
	return math.div(math.fromAmount(tokens), math.fromAmount(lptAMMBalance), state.RoundToNearest)
}

// getAssetRounding returns the rounding mode for asset amounts.
// Deposit: upward (maximize deposit), Withdraw: downward (minimize withdrawal)
// Reference: rippled AMMHelpers.h detail::getAssetRounding()
func getAssetRounding(isDeposit bool) state.RoundingMode {
	if isDeposit {
		return state.RoundUpward
	}
	return state.RoundDownward
}

// getLPTokenRounding returns the rounding mode for LP token amounts.
// Deposit: downward (minimize tokens out), Withdraw: upward (maximize tokens in)
// Reference: rippled AMMHelpers.h detail::getLPTokenRounding()
func getLPTokenRounding(isDeposit bool) state.RoundingMode {
	if isDeposit {
		return state.RoundDownward
	}
	return state.RoundUpward
}

// lpTokensOut calculates LP tokens issued for a single-asset deposit (Equation 3).
// Reference: rippled AMMHelpers.cpp lpTokensOut()
//
//	f1 = feeMult(tfee)           // 1 - fee
//	f2 = feeMultHalf(tfee) / f1  // (1 - fee/2) / (1 - fee)
//	r = asset1Deposit / asset1Balance
//	c = root2(f2*f2 + r/f1) - f2
//	if !fixAMMv1_3: t = lptAMMBalance * (r - c) / (1 + c)
//	else:           frac = (r-c)/(1+c); multiply(lptAMMBalance, frac, downward)
func lpTokensOut(math numberMath, assetBalance, amountIn, lptBalance tx.Amount, tfee uint16, fixAMMv1_3 bool) tx.Amount {
	if assetBalance.IsZero() || lptBalance.IsZero() {
		return zeroIOU()
	}
	const mode = state.RoundToNearest
	assetBalanceNumber := math.fromAmount(assetBalance)
	lptBalanceNumber := math.fromAmount(lptBalance)
	f1 := feeMult(math, tfee)
	f2 := math.div(feeMultHalf(math, tfee), f1, mode)
	r := math.div(math.fromAmount(amountIn), assetBalanceNumber, mode)
	inner := f2.MulRounded(f2, mode).AddRounded(math.div(r, f1, mode), mode)
	if inner.Signum() < 0 {
		return zeroIOU()
	}
	c := inner.Root2Rounded(mode).AddRounded(f2.Negate(), mode)
	rMinusC := r.AddRounded(c.Negate(), mode)
	onePlusC := math.addToOne(c, mode)

	if !fixAMMv1_3 {
		t := math.div(lptBalanceNumber.MulRounded(rMinusC, mode), onePlusC, mode)
		return math.toAmount(t, lptBalance, mode)
	}
	frac := math.div(rMinusC, onePlusC, mode)
	return math.multiplyToAmount(
		math.fromAmountRounded(lptBalance, state.RoundDownward),
		frac,
		lptBalance,
		state.RoundDownward,
	)
}

// ammAssetIn calculates the asset amount needed for a specified LP token output (Equation 4).
// Reference: rippled AMMHelpers.cpp ammAssetIn()
//
//	f1 = feeMult(tfee); f2 = feeMultHalf(tfee) / f1
//	t1 = lpTokens / lptAMMBalance; t2 = 1 + t1
//	d = f2 - t1/t2
//	a = 1/(t2*t2); b = 2*d/t2 - 1/f1; c = d*d - f2*f2
//	if !fixAMMv1_3: toSTAmount(asset1Balance * solveQuadraticEq(a, b, c))
//	else:           frac = solveQuadraticEq(a,b,c); multiply(asset1Balance, frac, upward)
func ammAssetIn(math numberMath, assetBalance, lptBalance, lpTokensOutAmt tx.Amount, tfee uint16, fixAMMv1_3 bool) tx.Amount {
	if lptBalance.IsZero() {
		return zeroIOU()
	}
	const mode = state.RoundToNearest
	f1 := feeMult(math, tfee)
	f2 := math.div(feeMultHalf(math, tfee), f1, mode)
	t1 := math.div(math.fromAmountRounded(lpTokensOutAmt, mode), math.fromAmountRounded(lptBalance, mode), mode)
	t2 := math.addToOne(t1, mode)
	d := f2.AddRounded(math.div(t1, t2, mode).Negate(), mode)
	qa := math.div(math.one(), t2.MulRounded(t2, mode), mode)
	qb := math.div(math.int(2).MulRounded(d, mode), t2, mode).
		AddRounded(math.div(math.one(), f1, mode).Negate(), mode)
	qc := d.MulRounded(d, mode).AddRounded(f2.MulRounded(f2, mode).Negate(), mode)
	frac := math.solveQuadraticEq(qa, qb, qc, mode)
	if !fixAMMv1_3 {
		return math.multiplyToAmount(math.fromAmount(assetBalance), frac, assetBalance, mode)
	}
	return math.multiplyToAmount(
		math.fromAmountRounded(assetBalance, state.RoundUpward),
		frac,
		assetBalance,
		state.RoundUpward,
	)
}

// ammAssetOut calculates the asset amount received for burning LP tokens (Equation 8).
// Reference: rippled AMMHelpers.cpp ammAssetOut()
//
//	f = getFee(tfee)
//	t1 = lpTokens / lptAMMBalance
//	if !fixAMMv1_3: b = assetBalance * (t1*t1 - t1*(2-f)) / (t1*f - 1)
//	else:           frac = (t1*t1 - t1*(2-f)) / (t1*f - 1); multiply(assetBalance, frac, downward)
func ammAssetOut(math numberMath, assetBalance, lptBalance, lpTokensIn tx.Amount, tfee uint16, fixAMMv1_3 bool) tx.Amount {
	if lptBalance.IsZero() {
		return zeroIOU()
	}
	const mode = state.RoundToNearest
	f := getFee(math, tfee)
	t1 := math.div(math.fromAmountRounded(lpTokensIn, mode), math.fromAmountRounded(lptBalance, mode), mode)
	twoMinusF := math.int(2).AddRounded(f.Negate(), mode)
	numerator := t1.MulRounded(t1, mode).
		AddRounded(t1.MulRounded(twoMinusF, mode).Negate(), mode)
	denominator := t1.MulRounded(f, mode).AddRounded(math.one().Negate(), mode)
	frac := math.div(numerator, denominator, mode)
	if !fixAMMv1_3 {
		return math.multiplyToAmount(math.fromAmount(assetBalance), frac, assetBalance, mode)
	}
	return math.multiplyToAmount(
		math.fromAmountRounded(assetBalance, state.RoundDownward),
		frac,
		assetBalance,
		state.RoundDownward,
	)
}

// calcLPTokensIn calculates LP tokens needed for a single-asset withdrawal amount (Equation 7).
// Reference: rippled AMMHelpers.cpp lpTokensIn()
//
//	fr = asset1Withdraw / asset1Balance
//	f1 = getFee(tfee)   // fee (NOT feeMult!)
//	c = fr * f1 + 2 - f1
//	if !fixAMMv1_3: t = lptAMMBalance * (c - root2(c*c - 4*fr)) / 2
//	else:           frac = (c - root2(c*c - 4*fr)) / 2; multiply(lptAMMBalance, frac, upward)
func calcLPTokensIn(math numberMath, assetBalance, amountOut, lptBalance tx.Amount, tfee uint16, fixAMMv1_3 bool) tx.Amount {
	if assetBalance.IsZero() || lptBalance.IsZero() {
		return zeroIOU()
	}
	const mode = state.RoundToNearest
	two := math.int(2)
	fr := math.div(math.fromAmountRounded(amountOut, mode), math.fromAmountRounded(assetBalance, mode), mode)
	f1 := getFee(math, tfee)
	c := fr.MulRounded(f1, mode).AddRounded(two.AddRounded(f1.Negate(), mode), mode)
	disc := c.MulRounded(c, mode).
		AddRounded(math.int(4).MulRounded(fr, mode).Negate(), mode)
	if disc.Signum() < 0 {
		return zeroIOU()
	}
	halfResult := math.div(c.AddRounded(disc.Root2Rounded(mode).Negate(), mode), two, mode)
	if !fixAMMv1_3 {
		return math.multiplyToAmount(math.fromAmount(lptBalance), halfResult, lptBalance, mode)
	}
	return math.multiplyToAmount(
		math.fromAmountRounded(lptBalance, state.RoundUpward),
		halfResult,
		lptBalance,
		state.RoundUpward,
	)
}

// initializeFeeAuctionVote initializes the vote slots and auction slot for an AMM.
// This is called when creating an AMM or when depositing into an empty AMM.
// Reference: rippled AMMUtils.cpp initializeFeeAuctionVote lines 340-384
func initializeFeeAuctionVote(amm *AMMData, accountID [20]byte, lptCurrency string, ammAccountAddr string, tfee uint16, parentCloseTime uint32, clearStaleAuthAccounts bool) {
	// Clear existing vote slots and add creator's vote
	amm.VoteSlots = []VoteSlotData{
		{
			Account:    accountID,
			TradingFee: tfee,
			VoteWeight: uint32(voteWeightScaleFactor),
		},
	}

	// Set trading fee
	amm.TradingFee = tfee

	// Calculate discounted fee
	discountedFee := uint16(0)
	if tfee > 0 {
		discountedFee = tfee / uint16(auctionSlotDiscountedFeeFraction)
	}

	// Expiration is one full time slot (24 hours) after the parent close.
	expiration := parentCloseTime + uint32(totalTimeSlotSecs)

	// Pre-fixCleanup3_2_0, rippled peeks and mutates the existing auction slot in
	// place, so a prior holder's AuthAccounts survive an empty-AMM re-init. The
	// amendment clears them. Preserve them when the amendment is off.
	authAccounts := make([][20]byte, 0)
	authAccountsPresent := false
	if !clearStaleAuthAccounts && amm.AuctionSlot != nil && amm.AuctionSlot.AuthAccountsPresent {
		authAccounts = amm.AuctionSlot.AuthAccounts
		authAccountsPresent = true
	}

	// Initialize auction slot
	amm.AuctionSlot = &AuctionSlotData{
		Account:             accountID,
		Expiration:          expiration,
		Price:               zeroAmount(tx.Asset{Currency: lptCurrency, Issuer: ammAccountAddr}),
		DiscountedFee:       discountedFee,
		AuthAccounts:        authAccounts,
		AuthAccountsPresent: authAccountsPresent,
	}
}

// verifyAndAdjustLPTokenBalance adjusts the AMM SLE's LPTokenBalance when
// the last LP's trust line balance differs from it due to rounding.
// Reference: rippled AMMUtils.cpp verifyAndAdjustLPTokenBalance (lines 468-494)
func verifyAndAdjustLPTokenBalance(math numberMath, view tx.LedgerView, ammKey keylet.Keylet, lpTokens tx.Amount, amm *AMMData, lpAccountID [20]byte) ter.Result {
	lptCurrency := GenerateAMMLPTCurrencyForAssets(amm.Asset, amm.Asset2)
	onlyLP, res := isOnlyLiquidityProvider(view, lptCurrency, amm.Account, lpAccountID)
	if res != ter.TesSUCCESS {
		return res
	}
	if onlyLP {
		tolerance := math.number(1, -3, state.RoundToNearest)
		if withinRelativeDistance(math, lpTokens, amm.LPTokenBalance, tolerance) {
			amm.LPTokenBalance = lpTokens
			// Persist so a deletion's DeletedNode records the reconciled
			// LPTokenBalance, not the stale one (1 ULP ledger fork otherwise).
			ammBytes, err := serializeAMMData(amm)
			if err != nil {
				return ter.TefINTERNAL
			}
			if err := view.Update(ammKey, ammBytes); err != nil {
				return ter.TefINTERNAL
			}
		} else {
			return ter.TecAMM_INVALID_TOKENS
		}
	}

	return ter.TesSUCCESS
}
