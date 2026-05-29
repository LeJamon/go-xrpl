package amm

import (
	"fmt"
	"math/big"

	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// calculateLPTokens calculates initial LP token balance as sqrt(amount1 * amount2).
// Uses tx.Amount arithmetic for precision with IOU values.
// LP tokens are always IOU (never XRP), so we ensure the result is IOU.
// Note: rippled uses XRP in drops (not XRP units) for this calculation.
// So sqrt(10000 XRP * 10000 USD) = sqrt(10000000000 drops * 10000) = sqrt(10^14) = 10^7 = 10,000,000 LP tokens
// Reference: rippled AMMHelpers.cpp ammLPTokens() — with fixAMMv1_3, rounds DOWN
// to maintain AMM invariant: sqrt(asset1 * asset2) >= LPTokensBalance
func calculateLPTokens(amount1, amount2 tx.Amount, fixV1_3 ...bool) tx.Amount {
	if amount1.IsZero() || amount2.IsZero() {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}

	// With fixAMMv1_3 enabled, set rounding mode to downward to maintain
	// the AMM invariant: sqrt(asset1 * asset2) >= LPTokensBalance
	// Reference: rippled AMMHelpers.cpp ammLPTokens() line 31-33
	roundDown := len(fixV1_3) > 0 && fixV1_3[0]
	if roundDown {
		g := state.NewNumberRoundModeGuard(state.RoundDownward)
		defer g.Release()
	}

	// Convert amounts to IOU representation for consistent calculation
	// IMPORTANT: XRP uses drops directly (NOT converted to XRP units)
	// This matches rippled behavior where sqrt(drops * IOU) gives LP tokens
	var iou1, iou2 tx.Amount

	if amount1.IsNative() {
		// XRP: use drops directly as the value
		// e.g., 10,000 XRP = 10,000,000,000 drops, represented as mantissa=1e15, exp=-5
		drops := amount1.Drops()
		mantissa := drops
		exp := 0 // Start with exponent 0 for the raw drops value
		// Normalize mantissa to [1e15, 1e16)
		for mantissa >= 1e16 {
			mantissa /= 10
			exp++
		}
		for mantissa > 0 && mantissa < 1e15 {
			mantissa *= 10
			exp--
		}
		iou1 = state.NewIssuedAmountFromValue(mantissa, exp, "", "")
	} else {
		iou1 = state.NewIssuedAmountFromValue(amount1.Mantissa(), amount1.Exponent(), "", "")
	}

	if amount2.IsNative() {
		drops := amount2.Drops()
		mantissa := drops
		exp := 0
		for mantissa >= 1e16 {
			mantissa /= 10
			exp++
		}
		for mantissa > 0 && mantissa < 1e15 {
			mantissa *= 10
			exp--
		}
		iou2 = state.NewIssuedAmountFromValue(mantissa, exp, "", "")
	} else {
		iou2 = state.NewIssuedAmountFromValue(amount2.Mantissa(), amount2.Exponent(), "", "")
	}

	// product = iou1 * iou2
	product := iou1.Mul(iou2, false)
	// result = sqrt(product)
	return product.Sqrt()
}

// GenerateAMMLPTCurrency generates the LP token currency code from two asset currencies.
// The LP token currency is 0x03 prefix + first 19 bytes of sha512Half(min(c1,c2), max(c1,c2)).
// Reference: rippled AMMCore.cpp ammLPTCurrency()
func GenerateAMMLPTCurrency(currency1, currency2 string) string {
	c1 := state.GetCurrencyBytes(currency1)
	c2 := state.GetCurrencyBytes(currency2)

	// Sort currencies lexicographically (std::minmax in rippled)
	minC, maxC := c1, c2
	for i := 0; i < 20; i++ {
		if c1[i] < c2[i] {
			break
		} else if c1[i] > c2[i] {
			minC, maxC = c2, c1
			break
		}
	}

	// sha512Half(minC, maxC)
	hash := common.Sha512Half(minC[:], maxC[:])

	// AMM LPToken currency: 0x03 + first 19 bytes of hash
	var lptCurrency [20]byte
	lptCurrency[0] = 0x03
	copy(lptCurrency[1:], hash[:19])

	return fmt.Sprintf("%X", lptCurrency)
}

// numberPower returns f^n using exponentiation by squaring.
// Reference: rippled Number.cpp power(Number const& f, unsigned n)
func numberPower(f tx.Amount, n int) tx.Amount {
	if n == 0 {
		return oneAmount()
	}
	if n == 1 {
		return f
	}
	r := numberPower(f, n/2)
	r = r.Mul(r, false)
	if n%2 != 0 {
		r = r.Mul(f, false)
	}
	return r
}

// oneAmount returns the Amount value 1.0 as an IOU for arithmetic.
func oneAmount() tx.Amount {
	return state.NewIssuedAmountFromValue(1e15, -15, "", "")
}

// numAmount returns a Number-like Amount from an integer.
func numAmount(n int64) tx.Amount {
	return toIOUForCalc(state.NewXRPAmountFromInt(n))
}

// subFromOne calculates (1 - x) where x is a fractional Amount
func subFromOne(x tx.Amount) tx.Amount {
	one := oneAmount()
	result, _ := one.Sub(x)
	return result
}

// addToOne calculates (1 + x) where x is a fractional Amount
func addToOne(x tx.Amount) tx.Amount {
	one := oneAmount()
	result, _ := one.Add(x)
	return result
}

// numberDiv performs Number-based division: n / d.
// This matches rippled's Number::operator/= which uses Guard-based rounding,
// unlike Amount.Div() which uses STAmount::divide() (muldiv + 5).
// All AMM formula divisions must use this function because rippled's AMM code
// operates entirely in Number space.
func numberDiv(n, d tx.Amount) tx.Amount {
	if d.IsZero() {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}
	if n.IsZero() {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}
	nNum := state.NewXRPLNumber(n.Mantissa(), n.Exponent())
	dNum := state.NewXRPLNumber(d.Mantissa(), d.Exponent())
	result := nNum.Div(dNum)
	iou := result.ToIOUAmountValue()
	return state.NewIssuedAmountFromValue(iou.Mantissa(), iou.Exponent(), n.Currency, n.Issuer)
}

// numberDivToInt64 computes n / d in Number space and converts the quotient to
// int64 using round-half-to-even, matching rippled's
// static_cast<std::int64_t>(Number{...} / ...) (default to_nearest rounding).
// Unlike numberDiv followed by Float64()+integer cast, this avoids both the
// float64 precision loss and the truncation-toward-zero of a float cast.
func numberDivToInt64(n, d tx.Amount) int64 {
	if d.IsZero() || n.IsZero() {
		return 0
	}
	nNum := state.NewXRPLNumber(n.Mantissa(), n.Exponent())
	dNum := state.NewXRPLNumber(d.Mantissa(), d.Exponent())
	return nNum.Div(dNum).ToInt64WithMode(state.RoundToNearest)
}

// stAmountDiv performs STAmount-style division: n / d.
// This matches rippled's divide(STAmount, STAmount, Issue) which uses
// muldiv with +5 rounding, unlike Number division (Guard-based).
// Used specifically in equalDepositTokens and equalWithdrawTokens where
// rippled divides STAmount values (not Number values).
// Reference: rippled STAmount.cpp divide() line 1294
func stAmountDiv(n, d tx.Amount) tx.Amount {
	if d.IsZero() {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}
	if n.IsZero() {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}
	// roundUp=false matches rippled's divide() default rounding behavior
	// for AMM proportional deposit/withdraw fraction calculations.
	result := n.Div(d, false)
	return state.NewIssuedAmountFromValue(result.Mantissa(), result.Exponent(), n.Currency, n.Issuer)
}

// solveQuadraticEq solves the positive root of quadratic equation:
//
//	x = (-b + sqrt(b*b - 4*a*c)) / (2*a)
//
// Reference: rippled AMMHelpers.cpp solveQuadraticEq()
func solveQuadraticEq(a, b, c tx.Amount) tx.Amount {
	two := numAmount(2)
	four := numAmount(4)
	// discriminant = b*b - 4*a*c
	bb := b.Mul(b, false)
	fourAC := four.Mul(a, false).Mul(c, false)
	disc, _ := bb.Sub(fourAC)
	// Guard against negative discriminant (no real root)
	if disc.IsNegative() {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}
	sqrtDisc := disc.Sqrt()
	// (-b + sqrtDisc) / (2*a)
	negB := b.Negate()
	numerator, _ := negB.Add(sqrtDisc)
	denominator := two.Mul(a, false)
	return numberDiv(numerator, denominator)
}

// multiplyWithRounding multiplies an amount by a fractional Number
// using an explicit rounding mode.
// Reference: rippled AMMHelpers.cpp multiply(amount, frac, rm)
func multiplyWithRounding(amount, frac tx.Amount, rm state.RoundingMode) tx.Amount {
	g := state.NewNumberRoundModeGuard(rm)
	defer g.Release()
	result := amount.Mul(frac, rm == state.RoundUpward)
	return toSTAmount(amount, result)
}

// toSTAmount converts a result back to the same type as the original amount.
// For XRP amounts: converts IOU-space result back to drops.
// For IOU amounts: preserves currency/issuer.
func toSTAmount(original, result tx.Amount) tx.Amount {
	if original.IsNative() {
		drops := iouToDrops(result)
		return state.NewXRPAmountFromInt(drops)
	}
	return state.NewIssuedAmountFromValue(result.Mantissa(), result.Exponent(),
		original.Currency, original.Issuer)
}

// toSTAmountIssue converts a result to the issue of the given amount.
// For XRP, uses to_nearest rounding (matching rippled's toSTAmount default).
// Reference: rippled AmountConversions.h toSTAmount(issue, number, mode=getround())
func toSTAmountIssue(amt tx.Amount, result tx.Amount) tx.Amount {
	if amt.IsNative() {
		g := state.NewNumberRoundModeGuard(state.RoundToNearest)
		drops := iouToDropsRounded(result)
		g.Release()
		return state.NewXRPAmountFromInt(drops)
	}
	return state.NewIssuedAmountFromValue(result.Mantissa(), result.Exponent(),
		amt.Currency, amt.Issuer)
}

// toSTAmountIssueRounded converts a result to the issue of the given amount,
// using the current global rounding mode for XRP drops conversion.
// Must be called while the appropriate NumberRoundModeGuard is active.
// Reference: rippled's Number::operator rep() rounding behavior.
func toSTAmountIssueRounded(amt tx.Amount, result tx.Amount) tx.Amount {
	if amt.IsNative() {
		return state.NewXRPAmountFromInt(iouToDropsRounded(result))
	}
	return state.NewIssuedAmountFromValue(result.Mantissa(), result.Exponent(),
		amt.Currency, amt.Issuer)
}

// mulRoundForAsset multiplies amount * frac with rounding mode rm and returns
// the result typed to match asset (XRP drops or IOU).
// This is needed because toIOUForCalc strips the native flag from XRP amounts,
// so the original asset is passed separately to preserve the return type.
// Reference: rippled AMMHelpers.cpp multiply(amount, frac, rm) + toSTAmount(issue, ...)
func mulRoundForAsset(amount, frac tx.Amount, rm state.RoundingMode, asset tx.Amount) tx.Amount {
	if asset.IsNative() {
		// For XRP: raw multiplication → single rounding step during drops conversion.
		// Matches rippled's Number{v1, v2, unchecked{}} + Number::operator rep().
		// rippled does NOT normalize the product before converting to drops.
		return state.NewXRPAmountFromInt(multiplyRawToDrops(amount, frac, rm))
	}
	// For IOU: rounding mode active during multiplication normalization
	g := state.NewNumberRoundModeGuard(rm)
	defer g.Release()
	result := amount.Mul(frac, rm == state.RoundUpward)
	return state.NewIssuedAmountFromValue(result.Mantissa(), result.Exponent(),
		asset.Currency, asset.Issuer)
}

// multiplyRawToDrops multiplies two IOU-format amounts and converts the
// product to XRP drops, matching rippled's two-step rounding:
//
//	Step 1: Number::operator*= — normalize product to [10^15, 10^16) with Guard
//	Step 2: Number::operator rep() — convert normalized Number to integer drops with Guard
//
// Uses big.Int to avoid overflow (two 16-digit mantissas → 32-digit product).
func multiplyRawToDrops(a, b tx.Amount, rm state.RoundingMode) int64 {
	m1 := a.Mantissa()
	e1 := a.Exponent()
	m2 := b.Mantissa()
	e2 := b.Exponent()

	if m1 == 0 || m2 == 0 {
		return 0
	}

	neg := (m1 < 0) != (m2 < 0)
	if m1 < 0 {
		m1 = -m1
	}
	if m2 < 0 {
		m2 = -m2
	}

	// Raw product using big.Int (up to 32 digits)
	product := new(big.Int).Mul(big.NewInt(m1), big.NewInt(m2))
	exp := e1 + e2

	ten := big.NewInt(10)
	mod := new(big.Int)

	// Step 1: Normalize to [10^15, 10^16) — matches rippled Number::operator*=
	// Track guard digits: lastDigit = most significant discarded digit,
	// hasRemainder = any earlier discarded digit was non-zero.
	maxMantissa := big.NewInt(9_999_999_999_999_999) // 10^16 - 1
	var guardDigit1 int64
	hasRemainder1 := false

	for product.Cmp(maxMantissa) > 0 {
		if guardDigit1 != 0 {
			hasRemainder1 = true
		}
		product.DivMod(product, ten, mod)
		guardDigit1 = mod.Int64()
		exp++
	}

	// Apply rounding from normalization
	mantissa := product.Int64()
	mantissa = applyGuardRound(mantissa, guardDigit1, hasRemainder1, neg, rm)
	if mantissa > 9_999_999_999_999_999 {
		// Carry overflow: rippled does xm /= 10; ++xe (exact, no guard)
		mantissa /= 10
		exp++
	}

	// Step 2: Convert to drops — matches rippled Number::operator rep()
	drops := mantissa
	var guardDigit2 int64
	hasRemainder2 := false

	for exp < 0 {
		if guardDigit2 != 0 {
			hasRemainder2 = true
		}
		guardDigit2 = drops % 10
		drops /= 10
		exp++
	}
	for exp > 0 {
		drops *= 10
		exp--
	}

	// Apply rounding from drops conversion
	drops = applyGuardRound(drops, guardDigit2, hasRemainder2, neg, rm)

	if neg {
		drops = -drops
	}
	return drops
}

// applyGuardRound applies rippled-style Guard rounding to a mantissa.
// guardDigit is the most significant discarded digit (0-9).
// hasRemainder indicates whether any earlier discarded digit was non-zero.
// Reference: rippled Number.cpp Guard::round() + caller rounding logic.
func applyGuardRound(mantissa, guardDigit int64, hasRemainder, neg bool, rm state.RoundingMode) int64 {
	anyDiscarded := guardDigit != 0 || hasRemainder

	switch rm {
	case state.RoundUpward:
		if !neg && anyDiscarded {
			mantissa++
		}
	case state.RoundDownward:
		if neg && anyDiscarded {
			mantissa++
		}
	case state.RoundToNearest:
		if guardDigit > 5 {
			mantissa++
		} else if guardDigit == 5 {
			if hasRemainder {
				// > 0.5: round up
				mantissa++
			} else if mantissa%2 != 0 {
				// exactly 0.5: banker's round to even
				mantissa++
			}
		}
	case state.RoundTowardsZero:
		// truncate — no rounding
	}
	return mantissa
}

// toIOUForCalc converts an Amount to IOU representation for precise calculations.
// This is necessary because XRP/XRP division uses integer arithmetic and loses precision.
// For example, 1000 XRP / 10000 XRP = 0 with integer division, but should be 0.1 as a fraction.
func toIOUForCalc(amt tx.Amount) tx.Amount {
	if !amt.IsNative() {
		// Drop the currency/issuer tag: AMM math operates in rippled's
		// unitless Number space, where amounts of any asset are freely
		// combined. The real issue is reapplied at the STAmount boundary
		// (toSTAmount* / mulRoundForAsset).
		amt.Currency = ""
		amt.Issuer = ""
		return amt
	}
	// Convert XRP drops to IOU representation
	drops := amt.Drops()
	if drops == 0 {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}
	// Normalize mantissa to [10^15, 10^16) range
	mantissa := drops
	exp := 0
	for mantissa >= 1e16 {
		mantissa /= 10
		exp++
	}
	for mantissa > 0 && mantissa < 1e15 {
		mantissa *= 10
		exp--
	}
	return state.NewIssuedAmountFromValue(mantissa, exp, "", "")
}

// ToIOUForCalcExported is an exported wrapper around toIOUForCalc for test use.
func ToIOUForCalcExported(amt tx.Amount) tx.Amount {
	return toIOUForCalc(amt)
}

// iouToDrops converts an IOU representation back to XRP drops.
// This is the reverse of toIOUForCalc for XRP amounts.
// Uses truncation (floor towards zero) — suitable for non-rounded contexts.
func iouToDrops(amt tx.Amount) int64 {
	if amt.IsNative() {
		return amt.Drops()
	}
	// Convert IOU mantissa/exponent to drops
	mantissa := amt.Mantissa()
	exp := amt.Exponent()
	// Result = mantissa * 10^exp
	for exp > 0 {
		mantissa *= 10
		exp--
	}
	for exp < 0 {
		mantissa /= 10
		exp++
	}
	return mantissa
}

// iouToDropsRounded converts an IOU representation back to XRP drops,
// using the current global rounding mode with full Guard-style digit tracking.
// Reference: rippled Number::operator rep() — accumulates ALL discarded digits
// into a Guard, then rounds using the most significant discarded digit plus
// a sticky bit for any earlier non-zero digits.
func iouToDropsRounded(amt tx.Amount) int64 {
	if amt.IsNative() {
		return amt.Drops()
	}
	mantissa := amt.Mantissa()
	exp := amt.Exponent()
	if mantissa == 0 {
		return 0
	}

	neg := mantissa < 0
	if neg {
		mantissa = -mantissa
	}

	// Scale up (no precision loss)
	for exp > 0 {
		mantissa *= 10
		exp--
	}

	// Scale down with Guard-style tracking:
	// guardDigit = most significant discarded digit (the last one removed)
	// hasRemainder = any earlier discarded digit was non-zero
	var guardDigit int64
	hasRemainder := false

	for exp < 0 {
		if guardDigit != 0 {
			hasRemainder = true
		}
		guardDigit = mantissa % 10
		mantissa /= 10
		exp++
	}

	// Apply rounding using accumulated guard info
	mode := state.GetNumberRound()
	mantissa = applyGuardRound(mantissa, guardDigit, hasRemainder, neg, mode)

	if neg {
		mantissa = -mantissa
	}
	return mantissa
}
