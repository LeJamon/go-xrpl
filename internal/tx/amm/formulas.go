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
func getFee(fee uint16) tx.Amount {
	if fee == 0 {
		return zeroIOU()
	}
	// fee / 100000 = fee * 10^-5
	// For normalized form: mantissa in [10^15, 10^16), so fee * 10^10 with exp -15
	mantissa := int64(fee) * 1e10
	return state.NewIssuedAmountFromValue(mantissa, -15, "", "")
}

// feeMult returns (1 - getFee(tfee)), i.e., (1 - fee).
// Reference: rippled AMMCore.h feeMult(): 1 - getFee(tfee)
func feeMult(tfee uint16) tx.Amount {
	return subFromOne(getFee(tfee))
}

// feeMultHalf returns (1 - getFee(tfee)/2), i.e., (1 - fee/2).
// Reference: rippled AMMCore.h feeMultHalf(): 1 - getFee(tfee) / 2
func feeMultHalf(tfee uint16) tx.Amount {
	fee := getFee(tfee)
	halfFee := numberDiv(fee, numAmount(2))
	return subFromOne(halfFee)
}

// adjustLPTokens adjusts LP tokens for precision loss when adding/subtracting
// from the AMM balance.
// Reference: rippled AMMHelpers.cpp adjustLPTokens()
func adjustLPTokens(lptAMMBalance, lpTokens tx.Amount, isDeposit bool) tx.Amount {
	const mode = state.RoundDownward

	lptBalIOU := toIOUForCalc(lptAMMBalance)
	lpTokIOU := toIOUForCalc(lpTokens)

	if isDeposit {
		// (lptAMMBalance + lpTokens) - lptAMMBalance
		sum, _ := lptBalIOU.AddRounded(lpTokIOU, mode)
		result, _ := sum.SubRounded(lptBalIOU, mode)
		return toSTAmountIssue(lptAMMBalance, result)
	}
	// (lpTokens - lptAMMBalance) + lptAMMBalance
	diff, _ := lpTokIOU.SubRounded(lptBalIOU, mode)
	result, _ := diff.AddRounded(lptBalIOU, mode)
	return toSTAmountIssue(lptAMMBalance, result)
}

// adjustAmountsByLPTokens is the post-computation adjustment pipeline.
// Reference: rippled AMMHelpers.cpp adjustAmountsByLPTokens()
// IMPORTANT: when fixAMMv1_3 is enabled, this returns the amounts unchanged.
func adjustAmountsByLPTokens(
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

	lpTokensActual := adjustLPTokens(lptAMMBalance, lpTokens, isDeposit)

	if lpTokensActual.IsZero() {
		var amount2Opt *tx.Amount
		if amount2 != nil {
			zero := zeroAmount(tx.Asset{Currency: (*amount2).Currency, Issuer: (*amount2).Issuer})
			amount2Opt = &zero
		}
		zero := zeroAmount(tx.Asset{Currency: amount.Currency, Issuer: amount.Issuer})
		return zero, amount2Opt, lpTokensActual
	}

	if toIOUForCalc(lpTokensActual).Compare(toIOUForCalc(lpTokens)) < 0 {
		// Equal trade
		if amount2 != nil {
			fr := numberDiv(toIOUForCalc(lpTokensActual), toIOUForCalc(lpTokens))
			amountActual := toSTAmountIssue(amount, toIOUForCalc(amount).Mul(fr, false))
			amount2Actual := toSTAmountIssue(*amount2, toIOUForCalc(*amount2).Mul(fr, false))
			if !fixAMMv1_1 {
				if toIOUForCalc(amountActual).Compare(toIOUForCalc(amount)) < 0 {
					// keep amountActual
				} else {
					amountActual = amount
				}
				if toIOUForCalc(amount2Actual).Compare(toIOUForCalc(*amount2)) < 0 {
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
			amountActual = ammAssetIn(amountBalance, lptAMMBalance, lpTokensActual, tfee, false)
		} else if !fixAMMv1_1 {
			amountActual = ammAssetOut(amountBalance, lptAMMBalance, lpTokens, tfee, false)
		} else {
			amountActual = ammAssetOut(amountBalance, lptAMMBalance, lpTokensActual, tfee, false)
		}
		if !fixAMMv1_1 {
			if toIOUForCalc(amountActual).Compare(toIOUForCalc(amount)) < 0 {
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
func getRoundedAsset(fixAMMv1_3 bool, balance, frac tx.Amount, isDeposit bool) tx.Amount {
	balIOU := toIOUForCalc(balance)
	fracIOU := toIOUForCalc(frac)
	if !fixAMMv1_3 {
		result := balIOU.Mul(fracIOU, false)
		return toSTAmountIssue(balance, result)
	}
	rm := getAssetRounding(isDeposit)
	return mulRoundForAsset(balIOU, fracIOU, rm, balance)
}

// getRoundedAssetCb rounds an AMM single deposit/withdrawal amount using callbacks.
// productCb receives the rounding mode under which it must evaluate its
// Number expression (rippled runs the callback inside a NumberRoundModeGuard).
// Reference: rippled AMMHelpers.cpp getRoundedAsset() (callback version)
func getRoundedAssetCb(fixAMMv1_3 bool, noRoundCb func() tx.Amount, balance tx.Amount, productCb func(state.RoundingMode) tx.Amount, isDeposit bool) tx.Amount {
	if !fixAMMv1_3 {
		result := noRoundCb()
		return toSTAmountIssue(balance, result)
	}
	rm := getAssetRounding(isDeposit)
	if isDeposit {
		// rippled multiplies under rm but evaluates the product callback in
		// the ambient (to_nearest) mode.
		return mulRoundForAsset(toIOUForCalc(balance), productCb(state.RoundToNearest), rm, balance)
	}
	result := productCb(rm)
	return toSTAmountIssueRounded(balance, result, rm)
}

// getRoundedLPTokens rounds LPTokens for equal deposit/withdrawal.
// Reference: rippled AMMHelpers.cpp getRoundedLPTokens() (simple version)
func getRoundedLPTokens(fixAMMv1_3 bool, balance, frac tx.Amount, isDeposit bool) tx.Amount {
	balIOU := toIOUForCalc(balance)
	fracIOU := toIOUForCalc(frac)
	if !fixAMMv1_3 {
		result := balIOU.Mul(fracIOU, false)
		return toSTAmountIssue(balance, result)
	}
	rm := getLPTokenRounding(isDeposit)
	tokens := multiplyWithRounding(balIOU, fracIOU, rm)
	return adjustLPTokens(balance, tokens, isDeposit)
}

// getRoundedLPTokensCb rounds LPTokens for single deposit/withdrawal using callbacks.
// productCb receives the rounding mode under which it must evaluate its
// Number expression (rippled runs the callback inside a NumberRoundModeGuard).
// Reference: rippled AMMHelpers.cpp getRoundedLPTokens() (callback version)
func getRoundedLPTokensCb(fixAMMv1_3 bool, noRoundCb func() tx.Amount, lptAMMBalance tx.Amount, productCb func(state.RoundingMode) tx.Amount, isDeposit bool) tx.Amount {
	lptBalIOU := toIOUForCalc(lptAMMBalance)
	if !fixAMMv1_3 {
		result := noRoundCb()
		return toSTAmountIssue(lptAMMBalance, result)
	}
	rm := getLPTokenRounding(isDeposit)
	var tokens tx.Amount
	if isDeposit {
		result := productCb(rm)
		tokens = toSTAmountIssue(lptAMMBalance, result)
	} else {
		// rippled multiplies under rm but evaluates the product callback in
		// the ambient (to_nearest) mode.
		tokens = multiplyWithRounding(lptBalIOU, productCb(state.RoundToNearest), rm)
	}
	return adjustLPTokens(lptAMMBalance, tokens, isDeposit)
}

// adjustAssetInByTokens adjusts deposit asset amount to factor in adjusted tokens.
// Reference: rippled AMMHelpers.cpp adjustAssetInByTokens()
func adjustAssetInByTokens(fixAMMv1_3 bool, balance, amount, lptAMMBalance, tokens tx.Amount, tfee uint16) (tx.Amount, tx.Amount) {
	if !fixAMMv1_3 {
		return tokens, amount
	}
	assetAdj := ammAssetIn(balance, lptAMMBalance, tokens, tfee, true)
	tokensAdj := tokens
	// Rounding didn't work the right way.
	if toIOUForCalc(assetAdj).Compare(toIOUForCalc(amount)) > 0 {
		diff, _ := toIOUForCalc(assetAdj).Sub(toIOUForCalc(amount))
		adjAmount, _ := toIOUForCalc(amount).Sub(diff)
		adjAmountFull := toSTAmountIssue(amount, adjAmount)
		t := lpTokensOut(balance, adjAmountFull, lptAMMBalance, tfee, true)
		tokensAdj = adjustLPTokens(lptAMMBalance, t, true)
		assetAdj = ammAssetIn(balance, lptAMMBalance, tokensAdj, tfee, true)
	}
	return tokensAdj, minAmountIOU(amount, assetAdj)
}

// adjustAssetOutByTokens adjusts withdrawal asset amount to factor in adjusted tokens.
// Reference: rippled AMMHelpers.cpp adjustAssetOutByTokens()
func adjustAssetOutByTokens(fixAMMv1_3 bool, balance, amount, lptAMMBalance, tokens tx.Amount, tfee uint16) (tx.Amount, tx.Amount) {
	if !fixAMMv1_3 {
		return tokens, amount
	}
	assetAdj := ammAssetOut(balance, lptAMMBalance, tokens, tfee, true)
	tokensAdj := tokens
	// Rounding didn't work the right way.
	if toIOUForCalc(assetAdj).Compare(toIOUForCalc(amount)) > 0 {
		diff, _ := toIOUForCalc(assetAdj).Sub(toIOUForCalc(amount))
		adjAmount, _ := toIOUForCalc(amount).Sub(diff)
		adjAmountFull := toSTAmountIssue(amount, adjAmount)
		t := calcLPTokensIn(balance, adjAmountFull, lptAMMBalance, tfee, true)
		tokensAdj = adjustLPTokens(lptAMMBalance, t, false)
		assetAdj = ammAssetOut(balance, lptAMMBalance, tokensAdj, tfee, true)
	}
	return tokensAdj, minAmountIOU(amount, assetAdj)
}

// adjustFracByTokens recalculates the fraction after token adjustment.
// Reference: rippled AMMHelpers.cpp adjustFracByTokens()
func adjustFracByTokens(fixAMMv1_3 bool, lptAMMBalance, tokens, frac tx.Amount) tx.Amount {
	if !fixAMMv1_3 {
		return frac
	}
	return numberDiv(toIOUForCalc(tokens), toIOUForCalc(lptAMMBalance))
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
func lpTokensOut(assetBalance, amountIn, lptBalance tx.Amount, tfee uint16, fixAMMv1_3 bool) tx.Amount {
	if assetBalance.IsZero() || lptBalance.IsZero() {
		return zeroIOU()
	}

	assetBalanceIOU := toIOUForCalc(assetBalance)
	amountInIOU := toIOUForCalc(amountIn)
	lptBalanceIOU := toIOUForCalc(lptBalance)

	f1 := feeMult(tfee)                    // 1 - fee
	f2 := numberDiv(feeMultHalf(tfee), f1) // (1 - fee/2) / (1 - fee)

	// r = asset1Deposit / asset1Balance
	r := numberDiv(amountInIOU, assetBalanceIOU)

	// c = root2(f2*f2 + r/f1) - f2
	f2f2 := f2.Mul(f2, false)
	rDivF1 := numberDiv(r, f1)
	inner, _ := f2f2.Add(rDivF1)
	if inner.IsNegative() {
		return zeroIOU()
	}
	sqrtInner := inner.Sqrt()
	c, _ := sqrtInner.Sub(f2)

	if !fixAMMv1_3 {
		// t = lptAMMBalance * (r - c) / (1 + c)
		rMinusC, _ := r.Sub(c)
		onePlusC := addToOne(c)
		t := numberDiv(lptBalanceIOU.Mul(rMinusC, false), onePlusC)
		return toSTAmountIssue(lptBalance, t)
	}

	// minimize tokens out
	rMinusC, _ := r.Sub(c)
	onePlusC := addToOne(c)
	frac := numberDiv(rMinusC, onePlusC)
	return mulRoundForAsset(lptBalanceIOU, frac, state.RoundDownward, lptBalance)
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
func ammAssetIn(assetBalance, lptBalance, lpTokensOutAmt tx.Amount, tfee uint16, fixAMMv1_3 bool) tx.Amount {
	if lptBalance.IsZero() {
		return zeroIOU()
	}

	assetBalanceIOU := toIOUForCalc(assetBalance)
	lptBalanceIOU := toIOUForCalc(lptBalance)
	lpTokensOutIOU := toIOUForCalc(lpTokensOutAmt)

	f1 := feeMult(tfee)
	f2 := numberDiv(feeMultHalf(tfee), f1)

	one := oneAmount()
	two := numAmount(2)

	// t1 = lpTokens / lptAMMBalance
	t1 := numberDiv(lpTokensOutIOU, lptBalanceIOU)
	// t2 = 1 + t1
	t2, _ := one.Add(t1)
	// d = f2 - t1/t2
	t1DivT2 := numberDiv(t1, t2)
	d, _ := f2.Sub(t1DivT2)

	// a = 1 / (t2 * t2)
	t2t2 := t2.Mul(t2, false)
	qa := numberDiv(one, t2t2)
	// b = 2*d/t2 - 1/f1
	twoD := two.Mul(d, false)
	twoDDivT2 := numberDiv(twoD, t2)
	oneOverF1 := numberDiv(one, f1)
	qb, _ := twoDDivT2.Sub(oneOverF1)
	// c = d*d - f2*f2
	dd := d.Mul(d, false)
	f2f2 := f2.Mul(f2, false)
	qc, _ := dd.Sub(f2f2)

	if !fixAMMv1_3 {
		frac := solveQuadraticEq(qa, qb, qc)
		result := assetBalanceIOU.Mul(frac, false)
		return toSTAmountIssue(assetBalance, result)
	}

	// maximize deposit
	frac := solveQuadraticEq(qa, qb, qc)
	return mulRoundForAsset(assetBalanceIOU, frac, state.RoundUpward, assetBalance)
}

// ammAssetOut calculates the asset amount received for burning LP tokens (Equation 8).
// Reference: rippled AMMHelpers.cpp ammAssetOut()
//
//	f = getFee(tfee)
//	t1 = lpTokens / lptAMMBalance
//	if !fixAMMv1_3: b = assetBalance * (t1*t1 - t1*(2-f)) / (t1*f - 1)
//	else:           frac = (t1*t1 - t1*(2-f)) / (t1*f - 1); multiply(assetBalance, frac, downward)
func ammAssetOut(assetBalance, lptBalance, lpTokensIn tx.Amount, tfee uint16, fixAMMv1_3 bool) tx.Amount {
	if lptBalance.IsZero() {
		return zeroIOU()
	}

	assetBalanceIOU := toIOUForCalc(assetBalance)
	lptBalanceIOU := toIOUForCalc(lptBalance)
	lpTokensInIOU := toIOUForCalc(lpTokensIn)

	f := getFee(tfee)
	one := oneAmount()
	two := numAmount(2)

	// t1 = lpTokens / lptAMMBalance
	t1 := numberDiv(lpTokensInIOU, lptBalanceIOU)

	// t1*t1
	t1t1 := t1.Mul(t1, false)
	// (2 - f)
	twoMinusF, _ := two.Sub(f)
	// t1 * (2 - f)
	t1TimesTwo := t1.Mul(twoMinusF, false)
	// numerator = t1*t1 - t1*(2-f)
	numerator, _ := t1t1.Sub(t1TimesTwo)
	// t1*f
	t1f := t1.Mul(f, false)
	// denominator = t1*f - 1
	denominator, _ := t1f.Sub(one)

	if !fixAMMv1_3 {
		result := numberDiv(assetBalanceIOU.Mul(numerator, false), denominator)
		return toSTAmountIssue(assetBalance, result)
	}

	// minimize withdraw
	frac := numberDiv(numerator, denominator)
	return mulRoundForAsset(assetBalanceIOU, frac, state.RoundDownward, assetBalance)
}

// calcLPTokensIn calculates LP tokens needed for a single-asset withdrawal amount (Equation 7).
// Reference: rippled AMMHelpers.cpp lpTokensIn()
//
//	fr = asset1Withdraw / asset1Balance
//	f1 = getFee(tfee)   // fee (NOT feeMult!)
//	c = fr * f1 + 2 - f1
//	if !fixAMMv1_3: t = lptAMMBalance * (c - root2(c*c - 4*fr)) / 2
//	else:           frac = (c - root2(c*c - 4*fr)) / 2; multiply(lptAMMBalance, frac, upward)
func calcLPTokensIn(assetBalance, amountOut, lptBalance tx.Amount, tfee uint16, fixAMMv1_3 bool) tx.Amount {
	if assetBalance.IsZero() || lptBalance.IsZero() {
		return zeroIOU()
	}

	assetBalanceIOU := toIOUForCalc(assetBalance)
	amountOutIOU := toIOUForCalc(amountOut)
	lptBalanceIOU := toIOUForCalc(lptBalance)

	two := numAmount(2)
	four := numAmount(4)

	// fr = asset1Withdraw / asset1Balance
	fr := numberDiv(amountOutIOU, assetBalanceIOU)
	// f1 = getFee(tfee) -- this is the fee, NOT feeMult
	f1 := getFee(tfee)
	// c = fr * f1 + 2 - f1
	frTimesF1 := fr.Mul(f1, false)
	twoMinusF1, _ := two.Sub(f1)
	c, _ := frTimesF1.Add(twoMinusF1)

	// discriminant = c*c - 4*fr
	cc := c.Mul(c, false)
	fourFr := four.Mul(fr, false)
	disc, _ := cc.Sub(fourFr)
	// If discriminant is negative (withdrawal > pool balance), return zero.
	// In rippled, root2() throws std::overflow_error which propagates to
	// the engine catch handler. Here we return zero so the caller can
	// produce the appropriate TER code.
	if disc.IsNegative() {
		return zeroIOU()
	}
	sqrtDisc := disc.Sqrt()

	// (c - sqrt(c*c - 4*fr)) / 2
	cMinusSqrt, _ := c.Sub(sqrtDisc)
	halfResult := numberDiv(cMinusSqrt, two)

	if !fixAMMv1_3 {
		result := lptBalanceIOU.Mul(halfResult, false)
		return toSTAmountIssue(lptBalance, result)
	}

	// maximize tokens in
	return mulRoundForAsset(lptBalanceIOU, halfResult, state.RoundUpward, lptBalance)
}

// initializeFeeAuctionVote initializes the vote slots and auction slot for an AMM.
// This is called when creating an AMM or when depositing into an empty AMM.
// Reference: rippled AMMUtils.cpp initializeFeeAuctionVote lines 340-384
func initializeFeeAuctionVote(amm *AMMData, accountID [20]byte, lptCurrency string, ammAccountAddr string, tfee uint16, parentCloseTime uint32) {
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

	// Initialize auction slot
	amm.AuctionSlot = &AuctionSlotData{
		Account:       accountID,
		Expiration:    expiration,
		Price:         zeroAmount(tx.Asset{Currency: lptCurrency, Issuer: ammAccountAddr}),
		DiscountedFee: discountedFee,
		AuthAccounts:  make([][20]byte, 0),
	}
}

// verifyAndAdjustLPTokenBalance adjusts the AMM SLE's LPTokenBalance when
// the last LP's trust line balance differs from it due to rounding.
// Reference: rippled AMMUtils.cpp verifyAndAdjustLPTokenBalance (lines 468-494)
func verifyAndAdjustLPTokenBalance(view tx.LedgerView, ammKey keylet.Keylet, lpTokens tx.Amount, amm *AMMData, lpAccountID [20]byte) ter.Result {
	lptCurrency := GenerateAMMLPTCurrency(amm.Asset.Currency, amm.Asset2.Currency)
	onlyLP, res := isOnlyLiquidityProvider(view, lptCurrency, amm.Account, lpAccountID)
	if res != ter.TesSUCCESS {
		return res
	}
	if onlyLP {
		// Number{1, -3} = 0.001 tolerance
		tolerance := state.NewIssuedAmountFromValue(1, -3, "", "")
		if withinRelativeDistance(lpTokens, amm.LPTokenBalance, tolerance) {
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
