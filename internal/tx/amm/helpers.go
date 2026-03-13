package amm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// validateAMMAmount validates an AMM amount
func validateAMMAmount(amt tx.Amount) error {
	if amt.IsZero() {
		return errors.New("amount must be positive")
	}
	if amt.IsNegative() {
		return errors.New("amount must be positive")
	}
	return nil
}

// validateAMMAmountWithPair validates an AMM amount including optional asset pair matching.
// If pair is provided, the amount's issue must match either asset.
// If validZero is true, zero amounts are allowed.
// Returns:
// - "temBAD_AMM_TOKENS" if amount's issue doesn't match the asset pair
// - "temBAD_AMOUNT" if amount is negative or zero (when validZero is false)
// - "" on success
func validateAMMAmountWithPair(amt tx.Amount, asset1, asset2 *tx.Asset, validZero bool) string {
	// Check if amount's issue matches either asset in the pair
	if asset1 != nil && asset2 != nil {
		if !matchesAsset(&amt, *asset1) && !matchesAsset(&amt, *asset2) {
			return "temBAD_AMM_TOKENS"
		}
	}

	// Check amount value
	if amt.IsNegative() {
		return "temBAD_AMOUNT"
	}
	if !validZero && amt.IsZero() {
		return "temBAD_AMOUNT"
	}

	return ""
}

// ammErrCodeToResult maps a string error code from validateAMMAmountWithPair
// to its corresponding tx.Result constant.
func ammErrCodeToResult(code string) tx.Result {
	switch code {
	case "temBAD_AMM_TOKENS":
		return tx.TemBAD_AMM_TOKENS
	case "temBAD_AMOUNT":
		return tx.TemBAD_AMOUNT
	default:
		return tx.TemMALFORMED
	}
}

// matchesAssetByIssue checks if two Assets represent the same issue.
// Handles XRP being represented as either "" or "XRP" for currency.
func matchesAssetByIssue(a, b tx.Asset) bool {
	aIsXRP := a.Currency == "" || a.Currency == "XRP"
	bIsXRP := b.Currency == "" || b.Currency == "XRP"
	if aIsXRP && bIsXRP {
		return true
	}
	return a.Currency == b.Currency && a.Issuer == b.Issuer
}

// matchesAsset checks if an Amount matches an Asset
// Handles XRP being represented as either "" or "XRP" for currency
func matchesAsset(amt *tx.Amount, asset tx.Asset) bool {
	if amt == nil {
		return false
	}
	// Check if both are XRP (currency empty or "XRP", no issuer)
	amtIsXRP := amt.IsNative() || amt.Currency == "" || amt.Currency == "XRP"
	assetIsXRP := asset.Currency == "" || asset.Currency == "XRP"
	if amtIsXRP && assetIsXRP {
		return true
	}
	// For IOUs, compare currency and issuer
	return amt.Currency == asset.Currency && amt.Issuer == asset.Issuer
}

// zeroAmount returns a zero amount for the given asset
func zeroAmount(asset tx.Asset) tx.Amount {
	if asset.Currency == "" || asset.Currency == "XRP" {
		return state.NewXRPAmountFromInt(0)
	}
	return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
}

// ammAccountCache maps AMM keylet keys to their pseudo-account IDs.
// Populated during AMMCreate and used by ComputeAMMAccountAddress for test helpers.
var ammAccountCache = make(map[[32]byte][20]byte)

// CacheAMMAccount stores an AMM keylet → accountID mapping for later lookup.
func CacheAMMAccount(ammKeyletKey [32]byte, accountID [20]byte) {
	ammAccountCache[ammKeyletKey] = accountID
}

// ClearAMMAccountCache removes all cached AMM account mappings.
// Call this from test env_reset or between test runs.
func ClearAMMAccountCache() {
	ammAccountCache = make(map[[32]byte][20]byte)
}

// ComputeAMMAccountAddress returns the AMM pseudo-account address for the given asset pair.
// It first checks the cache (populated by AMMCreate), then falls back to the AMM keylet
// first-20-bytes derivation (which is incorrect but backwards-compatible for old tests).
// Exported for use in test helpers.
func ComputeAMMAccountAddress(asset1, asset2 tx.Asset) string {
	ammKey := computeAMMKeylet(asset1, asset2)
	if cachedID, ok := ammAccountCache[ammKey.Key]; ok {
		addr, _ := encodeAccountID(cachedID)
		return addr
	}
	// Fallback: first 20 bytes of keylet hash (incorrect but backwards-compatible)
	var accountID [20]byte
	copy(accountID[:], ammKey.Key[:20])
	addr, _ := encodeAccountID(accountID)
	return addr
}

// ComputeAMMKeylet computes the AMM keylet from the asset pair.
// Exported for use in test helpers.
func ComputeAMMKeylet(asset1, asset2 tx.Asset) keylet.Keylet {
	return computeAMMKeylet(asset1, asset2)
}

// PseudoAccountAddress derives the AMM pseudo-account ID for the given keylet key.
// Exported for use in test helpers (e.g., PseudoAccount collision tests).
func PseudoAccountAddress(view tx.LedgerView, parentHash [32]byte, key [32]byte) [20]byte {
	return pseudoAccountAddress(view, parentHash, key)
}

// computeAMMKeylet computes the AMM keylet from the asset pair.
func computeAMMKeylet(asset1, asset2 tx.Asset) keylet.Keylet {
	issuer1 := getIssuerBytes(asset1.Issuer)
	currency1 := state.GetCurrencyBytes(asset1.Currency)
	issuer2 := getIssuerBytes(asset2.Issuer)
	currency2 := state.GetCurrencyBytes(asset2.Currency)

	return keylet.AMM(issuer1, currency1, issuer2, currency2)
}

// getIssuerBytes converts an issuer address string to a 20-byte account ID.
func getIssuerBytes(issuer string) [20]byte {
	if issuer == "" {
		return [20]byte{}
	}
	id, _ := state.DecodeAccountID(issuer)
	return id
}

// maxPseudoAccountAttempts is the number of candidate addresses to try.
// Reference: rippled View.cpp pseudoAccountAddress: maxAccountAttempts = 256
const maxPseudoAccountAttempts = 256

// pseudoAccountAddress derives the AMM pseudo-account ID.
// It tries up to 256 candidate addresses derived from sha512Half(i, parentHash, pseudoOwnerKey),
// then SHA256-RIPEMD160, and returns the first one not already occupied in the ledger.
// Returns the zero AccountID if all 256 slots are taken.
// Reference: rippled View.cpp pseudoAccountAddress (line 1067-1081)
func pseudoAccountAddress(view tx.LedgerView, parentHash [32]byte, pseudoOwnerKey [32]byte) [20]byte {
	for i := uint16(0); i < maxPseudoAccountAttempts; i++ {
		// sha512Half(i, parentHash, pseudoOwnerKey)
		iBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(iBytes, i)
		hash := common.Sha512Half(iBytes, parentHash[:], pseudoOwnerKey[:])

		// ripesha_hasher: SHA256 then RIPEMD160
		accountID := sha256Ripemd160(hash[:])

		// Check if account exists
		acctKey := keylet.Account(accountID)
		if exists, _ := view.Exists(acctKey); !exists {
			return accountID
		}
	}
	return [20]byte{} // All slots taken
}

// sha256Ripemd160 computes SHA256(data) then RIPEMD160 of the result, returning a 20-byte AccountID.
func sha256Ripemd160(data []byte) [20]byte {
	result := addresscodec.Sha256RipeMD160(data)
	var id [20]byte
	copy(id[:], result)
	return id
}

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

// compareAccountIDs compares two account IDs lexicographically.
func compareAccountIDs(a, b [20]byte) int {
	return state.CompareAccountIDs(a, b)
}

// encodeAccountID encodes a 20-byte account ID to an XRPL address string.
func encodeAccountID(accountID [20]byte) (string, error) {
	return state.EncodeAccountID(accountID)
}

// EncodeAccountID converts a 20-byte account ID to an r-address string.
// Exported for use in test helpers.
func EncodeAccountID(accountID [20]byte) (string, error) {
	return state.EncodeAccountID(accountID)
}

// getFee converts a trading fee in basis points (0-1000) to a fractional Amount.
// 1000 basis points = 1% = 0.01
// Returns fee as an IOU Amount for precise arithmetic.
// Reference: rippled AMMCore.h getFee(): Number{tfee} / AUCTION_SLOT_FEE_SCALE_FACTOR
func getFee(fee uint16) tx.Amount {
	if fee == 0 {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
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
// Reference: rippled's toSTAmount(issue, number)
func toSTAmountIssue(amt tx.Amount, result tx.Amount) tx.Amount {
	if amt.IsNative() {
		return state.NewXRPAmountFromInt(iouToDrops(result))
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
//   Step 1: Number::operator*= — normalize product to [10^15, 10^16) with Guard
//   Step 2: Number::operator rep() — convert normalized Number to integer drops with Guard
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

// adjustLPTokens adjusts LP tokens for precision loss when adding/subtracting
// from the AMM balance.
// Reference: rippled AMMHelpers.cpp adjustLPTokens()
func adjustLPTokens(lptAMMBalance, lpTokens tx.Amount, isDeposit bool) tx.Amount {
	g := state.NewNumberRoundModeGuard(state.RoundDownward)
	defer g.Release()

	lptBalIOU := toIOUForCalc(lptAMMBalance)
	lpTokIOU := toIOUForCalc(lpTokens)

	if isDeposit {
		// (lptAMMBalance + lpTokens) - lptAMMBalance
		sum, _ := lptBalIOU.Add(lpTokIOU)
		result, _ := sum.Sub(lptBalIOU)
		return toSTAmountIssue(lpTokens, result)
	}
	// (lpTokens - lptAMMBalance) + lptAMMBalance
	diff, _ := lpTokIOU.Sub(lptBalIOU)
	result, _ := diff.Add(lptBalIOU)
	return toSTAmountIssue(lpTokens, result)
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
// Reference: rippled AMMHelpers.cpp getRoundedAsset() (callback version)
func getRoundedAssetCb(fixAMMv1_3 bool, noRoundCb func() tx.Amount, balance tx.Amount, productCb func() tx.Amount, isDeposit bool) tx.Amount {
	if !fixAMMv1_3 {
		result := noRoundCb()
		return toSTAmountIssue(balance, result)
	}
	rm := getAssetRounding(isDeposit)
	if isDeposit {
		return mulRoundForAsset(toIOUForCalc(balance), productCb(), rm, balance)
	}
	g := state.NewNumberRoundModeGuard(rm)
	defer g.Release()
	result := productCb()
	return toSTAmountIssueRounded(balance, result)
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
// Reference: rippled AMMHelpers.cpp getRoundedLPTokens() (callback version)
func getRoundedLPTokensCb(fixAMMv1_3 bool, noRoundCb func() tx.Amount, lptAMMBalance tx.Amount, productCb func() tx.Amount, isDeposit bool) tx.Amount {
	lptBalIOU := toIOUForCalc(lptAMMBalance)
	if !fixAMMv1_3 {
		result := noRoundCb()
		return toSTAmountIssue(lptAMMBalance, result)
	}
	rm := getLPTokenRounding(isDeposit)
	var tokens tx.Amount
	if isDeposit {
		g := state.NewNumberRoundModeGuard(rm)
		result := productCb()
		tokens = toSTAmountIssue(lptAMMBalance, result)
		g.Release()
	} else {
		tokens = multiplyWithRounding(lptBalIOU, productCb(), rm)
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

// minAmountIOU returns the smaller of two amounts compared in IOU space.
func minAmountIOU(a, b tx.Amount) tx.Amount {
	if toIOUForCalc(a).Compare(toIOUForCalc(b)) < 0 {
		return a
	}
	return b
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
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}

	assetBalanceIOU := toIOUForCalc(assetBalance)
	amountInIOU := toIOUForCalc(amountIn)
	lptBalanceIOU := toIOUForCalc(lptBalance)

	f1 := feeMult(tfee)                       // 1 - fee
	f2 := numberDiv(feeMultHalf(tfee), f1) // (1 - fee/2) / (1 - fee)

	// r = asset1Deposit / asset1Balance
	r := numberDiv(amountInIOU, assetBalanceIOU)

	// c = root2(f2*f2 + r/f1) - f2
	f2f2 := f2.Mul(f2, false)
	rDivF1 := numberDiv(r, f1)
	inner, _ := f2f2.Add(rDivF1)
	if inner.IsNegative() {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
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
	return multiplyWithRounding(lptBalanceIOU, frac, state.RoundDownward)
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
		return state.NewIssuedAmountFromValue(0, -100, "", "")
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
		return state.NewIssuedAmountFromValue(0, -100, "", "")
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

// AMMAssetOutExported is the exported wrapper for ammAssetOut, used by tests.
// It computes the asset amount received for burning LP tokens without fixAMMv1_3.
func AMMAssetOutExported(assetBalance, lptBalance, lpTokens tx.Amount, tfee uint16) tx.Amount {
	return ammAssetOut(assetBalance, lptBalance, lpTokens, tfee, false)
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
		return state.NewIssuedAmountFromValue(0, -100, "", "")
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
		return state.NewIssuedAmountFromValue(0, -100, "", "")
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
	return multiplyWithRounding(lptBalanceIOU, halfResult, state.RoundUpward)
}

// proportionalAmount calculates balance * (numerator / denominator) using Amount arithmetic.
// This replaces float64 fraction calculations like: balance * (tokens / totalTokens)
func proportionalAmount(balance, numerator, denominator tx.Amount) tx.Amount {
	if denominator.IsZero() {
		return zeroAmount(tx.Asset{Currency: balance.Currency, Issuer: balance.Issuer})
	}
	// fraction = numerator / denominator
	balIOU := toIOUForCalc(balance)
	numIOU := toIOUForCalc(numerator)
	denIOU := toIOUForCalc(denominator)
	fraction := numberDiv(numIOU, denIOU)
	// result = balance * fraction
	result := balIOU.Mul(fraction, false)
	return toSTAmountIssue(balance, result)
}

// toIOUForCalc converts an Amount to IOU representation for precise calculations.
// This is necessary because XRP/XRP division uses integer arithmetic and loses precision.
// For example, 1000 XRP / 10000 XRP = 0 with integer division, but should be 0.1 as a fraction.
func toIOUForCalc(amt tx.Amount) tx.Amount {
	if !amt.IsNative() {
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

// minAmount returns the smaller of two amounts.
// Assumes both amounts are of the same type (both XRP or same IOU).
func minAmount(a, b tx.Amount) tx.Amount {
	if a.Compare(b) < 0 {
		return a
	}
	return b
}

// maxAmount returns the larger of two amounts.
// Assumes both amounts are of the same type (both XRP or same IOU).
func maxAmount(a, b tx.Amount) tx.Amount {
	if a.Compare(b) > 0 {
		return a
	}
	return b
}

// isGreater returns true if a > b
func isGreater(a, b tx.Amount) bool {
	return a.Compare(b) > 0
}

// isLessOrEqual returns true if a <= b
func isLessOrEqual(a, b tx.Amount) bool {
	return a.Compare(b) <= 0
}

// amountFromXRPDrops creates an XRP Amount from drops (for balance tracking).
func amountFromXRPDrops(drops uint64) tx.Amount {
	return state.NewXRPAmountFromInt(int64(drops))
}

// serializeAmount serializes a tx.Amount to binary.
// Format: 1 byte type (0=XRP, 1=IOU), then:
//   - XRP: 8 bytes int64 drops
//   - IOU: 8 bytes int64 mantissa + 4 bytes int32 exponent
func serializeAmount(amt tx.Amount) []byte {
	if amt.IsNative() {
		buf := make([]byte, 9) // 1 type + 8 drops
		buf[0] = 0             // XRP type
		binary.BigEndian.PutUint64(buf[1:9], uint64(amt.Drops()))
		return buf
	}
	buf := make([]byte, 13) // 1 type + 8 mantissa + 4 exponent
	buf[0] = 1              // IOU type
	binary.BigEndian.PutUint64(buf[1:9], uint64(amt.Mantissa()))
	binary.BigEndian.PutUint32(buf[9:13], uint32(amt.Exponent()+128)) // offset exponent to avoid negative
	return buf
}

// deserializeAmount deserializes a tx.Amount from binary.
// Returns the Amount and bytes consumed.
func deserializeAmount(data []byte) (tx.Amount, int) {
	if len(data) < 1 {
		return state.NewXRPAmountFromInt(0), 0
	}
	amtType := data[0]
	if amtType == 0 {
		// XRP
		if len(data) < 9 {
			return state.NewXRPAmountFromInt(0), 0
		}
		drops := int64(binary.BigEndian.Uint64(data[1:9]))
		return state.NewXRPAmountFromInt(drops), 9
	}
	// IOU
	if len(data) < 13 {
		return state.NewIssuedAmountFromValue(0, -100, "", ""), 0
	}
	mantissa := int64(binary.BigEndian.Uint64(data[1:9]))
	exponent := int(binary.BigEndian.Uint32(data[9:13])) - 128 // reverse offset
	return state.NewIssuedAmountFromValue(mantissa, exponent, "", ""), 13
}

// serializeIssue serializes an Issue (currency + issuer) to binary.
// Format: 20 bytes currency + 20 bytes issuer = 40 bytes total
func serializeIssue(asset tx.Asset) []byte {
	buf := make([]byte, 40)
	// Currency (20 bytes)
	currency := state.GetCurrencyBytes(asset.Currency)
	copy(buf[0:20], currency[:])
	// Issuer (20 bytes)
	issuer := getIssuerBytes(asset.Issuer)
	copy(buf[20:40], issuer[:])
	return buf
}

// deserializeIssue deserializes an Issue from binary.
// Returns the Asset and bytes consumed (always 40).
func deserializeIssue(data []byte) (tx.Asset, int) {
	if len(data) < 40 {
		return tx.Asset{}, 0
	}
	// Currency (20 bytes)
	var currencyBytes [20]byte
	copy(currencyBytes[:], data[0:20])
	currency := state.GetCurrencyString(currencyBytes)

	// Issuer (20 bytes)
	var issuerBytes [20]byte
	copy(issuerBytes[:], data[20:40])
	issuer := ""
	if issuerBytes != [20]byte{} {
		issuer, _ = state.EncodeAccountID(issuerBytes)
	}

	return tx.Asset{Currency: currency, Issuer: issuer}, 40
}

// ParseAMMData deserializes an AMM ledger entry from binary data.
// Exported for use by TrustSet to check LP token balance.
func ParseAMMData(data []byte) (*AMMData, error) {
	return parseAMMData(data)
}

// parseAMMData deserializes an AMM ledger entry from binary data.
// This matches the rippled AMM ledger entry format exactly.
// Reference: rippled include/xrpl/protocol/detail/ledger_entries.macro ltAMM
func parseAMMData(data []byte) (*AMMData, error) {
	// Minimum size: Account(20) + Asset(40) + Asset2(40) + TradingFee(2) + OwnerNode(8) = 110 bytes
	if len(data) < 110 {
		return nil, fmt.Errorf("AMM data too short: %d bytes", len(data))
	}

	amm := &AMMData{}
	offset := 0

	// Account (20 bytes)
	copy(amm.Account[:], data[offset:offset+20])
	offset += 20

	// Asset (40 bytes: currency + issuer)
	amm.Asset, _ = deserializeIssue(data[offset:])
	offset += 40

	// Asset2 (40 bytes: currency + issuer)
	amm.Asset2, _ = deserializeIssue(data[offset:])
	offset += 40

	// TradingFee (2 bytes)
	amm.TradingFee = binary.BigEndian.Uint16(data[offset : offset+2])
	offset += 2

	// OwnerNode (8 bytes)
	amm.OwnerNode = binary.BigEndian.Uint64(data[offset : offset+8])
	offset += 8

	// LPTokenBalance (variable: 9 or 13 bytes)
	if offset >= len(data) {
		return amm, nil
	}
	amt, consumed := deserializeAmount(data[offset:])
	amm.LPTokenBalance = amt
	offset += consumed

	// VoteSlots count (1 byte)
	if offset+1 > len(data) {
		amm.VoteSlots = make([]VoteSlotData, 0)
		return amm, nil
	}
	voteCount := int(data[offset])
	offset++

	amm.VoteSlots = make([]VoteSlotData, 0, voteCount)
	for i := 0; i < voteCount && offset+26 <= len(data); i++ {
		var slot VoteSlotData
		copy(slot.Account[:], data[offset:offset+20])
		offset += 20
		slot.TradingFee = binary.BigEndian.Uint16(data[offset : offset+2])
		offset += 2
		slot.VoteWeight = binary.BigEndian.Uint32(data[offset : offset+4])
		offset += 4
		amm.VoteSlots = append(amm.VoteSlots, slot)
	}

	// AuctionSlot (optional)
	if offset+1 > len(data) {
		return amm, nil
	}
	hasAuctionSlot := data[offset]
	offset++

	if hasAuctionSlot != 0 && offset+24 <= len(data) {
		slot := &AuctionSlotData{
			AuthAccounts: make([][20]byte, 0),
		}
		copy(slot.Account[:], data[offset:offset+20])
		offset += 20
		slot.Expiration = binary.BigEndian.Uint32(data[offset : offset+4])
		offset += 4

		// DiscountedFee (2 bytes)
		if offset+2 <= len(data) {
			slot.DiscountedFee = binary.BigEndian.Uint16(data[offset : offset+2])
			offset += 2
		}

		// Price (variable: 9 or 13 bytes)
		if offset < len(data) {
			price, consumed := deserializeAmount(data[offset:])
			slot.Price = price
			offset += consumed
		}

		// Auth accounts count
		if offset+1 <= len(data) {
			authCount := int(data[offset])
			offset++
			for i := 0; i < authCount && offset+20 <= len(data); i++ {
				var authID [20]byte
				copy(authID[:], data[offset:offset+20])
				offset += 20
				slot.AuthAccounts = append(slot.AuthAccounts, authID)
			}
		}
		amm.AuctionSlot = slot
	}

	return amm, nil
}

// serializeAMMData serializes an AMMData entry to binary.
// This matches the rippled AMM ledger entry format exactly.
// Reference: rippled include/xrpl/protocol/detail/ledger_entries.macro ltAMM
// IMPORTANT: Asset balances are NOT stored - they are read from AccountRoot/trustlines.
func serializeAMMData(amm *AMMData) ([]byte, error) {
	// Pre-serialize amounts to get their sizes
	lptBalanceBytes := serializeAmount(amm.LPTokenBalance)

	// Calculate size
	// Account(20) + Asset(40) + Asset2(40) + TradingFee(2) + OwnerNode(8) + LPTokenBalance(variable)
	size := 20 + 40 + 40 + 2 + 8 + len(lptBalanceBytes)
	size += 1                       // voteCount
	size += len(amm.VoteSlots) * 26 // Each vote slot: 20 + 2 + 4
	size += 1                       // hasAuctionSlot flag
	if amm.AuctionSlot != nil {
		priceBytes := serializeAmount(amm.AuctionSlot.Price)
		// Account(20) + Expiration(4) + DiscountedFee(2) + Price(variable) + authCount(1)
		size += 20 + 4 + 2 + len(priceBytes) + 1
		size += len(amm.AuctionSlot.AuthAccounts) * 20
	}

	data := make([]byte, size)
	offset := 0

	// Account (20 bytes)
	copy(data[offset:offset+20], amm.Account[:])
	offset += 20

	// Asset (40 bytes: currency + issuer)
	assetBytes := serializeIssue(amm.Asset)
	copy(data[offset:offset+40], assetBytes)
	offset += 40

	// Asset2 (40 bytes: currency + issuer)
	asset2Bytes := serializeIssue(amm.Asset2)
	copy(data[offset:offset+40], asset2Bytes)
	offset += 40

	// TradingFee (2 bytes)
	binary.BigEndian.PutUint16(data[offset:offset+2], amm.TradingFee)
	offset += 2

	// OwnerNode (8 bytes)
	binary.BigEndian.PutUint64(data[offset:offset+8], amm.OwnerNode)
	offset += 8

	// LPTokenBalance (using serializeAmount for proper Amount serialization)
	copy(data[offset:], lptBalanceBytes)
	offset += len(lptBalanceBytes)

	// VoteSlots
	data[offset] = byte(len(amm.VoteSlots))
	offset++

	for _, slot := range amm.VoteSlots {
		copy(data[offset:offset+20], slot.Account[:])
		offset += 20
		binary.BigEndian.PutUint16(data[offset:offset+2], slot.TradingFee)
		offset += 2
		binary.BigEndian.PutUint32(data[offset:offset+4], slot.VoteWeight)
		offset += 4
	}

	// AuctionSlot
	if amm.AuctionSlot != nil {
		data[offset] = 1
		offset++
		copy(data[offset:offset+20], amm.AuctionSlot.Account[:])
		offset += 20
		binary.BigEndian.PutUint32(data[offset:offset+4], amm.AuctionSlot.Expiration)
		offset += 4
		// DiscountedFee (2 bytes)
		binary.BigEndian.PutUint16(data[offset:offset+2], amm.AuctionSlot.DiscountedFee)
		offset += 2
		// Price (using serializeAmount for proper Amount serialization)
		priceBytes := serializeAmount(amm.AuctionSlot.Price)
		copy(data[offset:], priceBytes)
		offset += len(priceBytes)
		data[offset] = byte(len(amm.AuctionSlot.AuthAccounts))
		offset++
		for _, authID := range amm.AuctionSlot.AuthAccounts {
			copy(data[offset:offset+20], authID[:])
			offset += 20
		}
	} else {
		data[offset] = 0
		offset++
	}

	return data[:offset], nil
}

// createOrUpdateAMMTrustline creates or updates a trust line for an AMM asset.
// This creates the trustline between the AMM account and the asset issuer,
// following rippled's trustCreate logic.
// Reference: rippled View.cpp trustCreate lines 1329-1445
func createOrUpdateAMMTrustline(ammAccountID [20]byte, asset tx.Asset, amount tx.Amount, view tx.LedgerView) error {
	// XRP doesn't need a trustline
	if asset.Currency == "" || asset.Currency == "XRP" {
		return nil
	}

	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return err
	}

	// Get trustline keylet
	trustLineKey := keylet.Line(ammAccountID, issuerID, asset.Currency)

	// Check if trustline already exists
	exists, err := view.Exists(trustLineKey)
	if err != nil {
		return err
	}

	if exists {
		// Trustline exists - update the balance
		// Reference: rippled rippleCreditIOU lines 1668-1748
		data, err := view.Read(trustLineKey)
		if err != nil {
			return err
		}

		rs, err := state.ParseRippleState(data)
		if err != nil {
			return err
		}

		// Determine if AMM is low or high account
		ammIsLow := keylet.IsLowAccount(ammAccountID, issuerID)

		// Update balance - positive balance means low owes high
		// AMM is receiving tokens from issuer (or being credited), so:
		// If AMM is low: balance should increase (AMM holds more)
		// If AMM is high: balance should decrease (AMM holds more, from their perspective)
		currentBalance := rs.Balance
		var newBalance tx.Amount

		if ammIsLow {
			// AMM is low - positive balance means AMM holds tokens
			newBalance, err = currentBalance.Add(amount)
			if err != nil {
				return err
			}
		} else {
			// AMM is high - negative balance means AMM holds tokens
			newBalance, err = currentBalance.Sub(amount)
			if err != nil {
				return err
			}
		}

		// Update balance preserving currency/issuer
		rs.Balance = state.NewIssuedAmountFromValue(
			newBalance.Mantissa(),
			newBalance.Exponent(),
			rs.Balance.Currency,
			rs.Balance.Issuer,
		)

		// Ensure lsfAMMNode flag is set (for AMM-owned trustlines)
		rs.Flags |= state.LsfAMMNode

		// Serialize and update
		rsBytes, err := state.SerializeRippleState(rs)
		if err != nil {
			return err
		}

		return view.Update(trustLineKey, rsBytes)
	}

	// Trustline doesn't exist - create it
	// Reference: rippled trustCreate lines 1347-1445

	// Determine low/high account ordering
	var lowAccountID, highAccountID [20]byte
	ammIsLow := keylet.IsLowAccount(ammAccountID, issuerID)
	if ammIsLow {
		lowAccountID = ammAccountID
		highAccountID = issuerID
	} else {
		lowAccountID = issuerID
		highAccountID = ammAccountID
	}

	lowAccountStr, _ := state.EncodeAccountID(lowAccountID)
	highAccountStr, _ := state.EncodeAccountID(highAccountID)

	// Create the RippleState entry
	// For AMM trustlines:
	// - Balance represents how much the low account "owes" the high account
	// - If AMM is low, positive balance = AMM holds tokens
	// - If AMM is high, negative balance = AMM holds tokens
	// - Balance issuer is always ACCOUNT_ONE (no account)
	var balance tx.Amount
	if ammIsLow {
		// AMM is low - positive balance
		balance = state.NewIssuedAmountFromValue(
			amount.Mantissa(),
			amount.Exponent(),
			asset.Currency,
			state.AccountOneAddress,
		)
	} else {
		// AMM is high - negative balance
		negated := amount.Negate()
		balance = state.NewIssuedAmountFromValue(
			negated.Mantissa(),
			negated.Exponent(),
			asset.Currency,
			state.AccountOneAddress,
		)
	}

	// Create RippleState
	// Reference: rippled trustCreate - limits are set based on who set the limit
	// For AMM trustlines, the limits are 0 on both sides (AMM doesn't set limits)
	rs := &state.RippleState{
		Balance:  balance,
		LowLimit: state.NewIssuedAmountFromValue(0, -100, asset.Currency, lowAccountStr),
		HighLimit: state.NewIssuedAmountFromValue(0, -100, asset.Currency, highAccountStr),
		Flags:    0,
		LowNode:  0,
		HighNode: 0,
	}

	// Set reserve flag for the side that is NOT the issuer
	// Reference: rippled trustCreate line 1409
	// For AMM, the AMM account should have reserve set
	if ammIsLow {
		rs.Flags |= state.LsfLowReserve
	} else {
		rs.Flags |= state.LsfHighReserve
	}

	// Set lsfAMMNode flag - this identifies it as an AMM-owned trustline
	// Reference: rippled AMMCreate.cpp line 297-306
	rs.Flags |= state.LsfAMMNode

	// Insert into low account's owner directory
	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDirResult, err := state.DirInsert(view, lowDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return err
	}

	// Insert into high account's owner directory
	highDirKey := keylet.OwnerDir(highAccountID)
	highDirResult, err := state.DirInsert(view, highDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return err
	}

	// Set deletion hints (page numbers where the trustline is stored)
	rs.LowNode = lowDirResult.Page
	rs.HighNode = highDirResult.Page

	// Serialize and insert the trustline
	rsBytes, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	return view.Insert(trustLineKey, rsBytes)
}

// updateTrustlineBalanceInView updates the balance of a trust line for IOU transfers.
// This reads the trust line, modifies the balance, and writes it back.
// delta is the amount to add (positive) or subtract (negative) from the account's perspective.
func updateTrustlineBalanceInView(accountID [20]byte, issuerID [20]byte, currency string, delta tx.Amount, view tx.LedgerView) error {
	// Get trust line keylet
	lineKey := keylet.Line(accountID, issuerID, currency)

	// Check if trust line exists
	exists, err := view.Exists(lineKey)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("trust line does not exist")
	}

	// Read trust line data
	data, err := view.Read(lineKey)
	if err != nil {
		return err
	}

	// Parse trust line
	rs, err := state.ParseRippleState(data)
	if err != nil {
		return err
	}

	// Determine if account is low or high in the trust line
	// Balance convention: positive means low owes high
	isLow := keylet.IsLowAccount(accountID, issuerID)

	// Get current balance from holder's perspective
	currentBalance := rs.Balance
	if !isLow {
		currentBalance = currentBalance.Negate()
	}

	// Apply delta (positive = receiving, negative = sending)
	newBalance, err := currentBalance.Add(delta)
	if err != nil {
		return err
	}

	// Convert back to RippleState balance convention
	if !isLow {
		newBalance = newBalance.Negate()
	}

	// Update the balance - preserve currency and issuer from original
	rs.Balance = state.NewIssuedAmountFromValue(
		newBalance.Mantissa(),
		newBalance.Exponent(),
		rs.Balance.Currency,
		rs.Balance.Issuer,
	)

	// Serialize and write back
	serialized, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	return view.Update(lineKey, serialized)
}

// createLPTokenTrustline creates or updates a trust line for LP tokens.
// This creates the trustline between the depositor and the AMM account (LP token issuer).
// Reference: rippled View.cpp trustCreate
func createLPTokenTrustline(accountID [20]byte, lptAsset tx.Asset, amount tx.Amount, view tx.LedgerView) error {
	// LP token issuer is the AMM account
	ammAccountID, err := state.DecodeAccountID(lptAsset.Issuer)
	if err != nil {
		return err
	}

	// Get trustline keylet
	trustLineKey := keylet.Line(accountID, ammAccountID, lptAsset.Currency)

	// Check if trustline already exists
	exists, err := view.Exists(trustLineKey)
	if err != nil {
		return err
	}

	if exists {
		// Trustline exists - update the balance
		data, err := view.Read(trustLineKey)
		if err != nil {
			return err
		}

		rs, err := state.ParseRippleState(data)
		if err != nil {
			return err
		}

		// Determine if holder is low or high account
		holderIsLow := keylet.IsLowAccount(accountID, ammAccountID)

		// Update balance - holder is receiving LP tokens
		currentBalance := rs.Balance
		var newBalance tx.Amount

		if holderIsLow {
			// Holder is low - positive balance means holder holds tokens
			newBalance, err = currentBalance.Add(amount)
			if err != nil {
				return err
			}
		} else {
			// Holder is high - negative balance means holder holds tokens
			newBalance, err = currentBalance.Sub(amount)
			if err != nil {
				return err
			}
		}

		// Update balance preserving currency/issuer
		rs.Balance = state.NewIssuedAmountFromValue(
			newBalance.Mantissa(),
			newBalance.Exponent(),
			rs.Balance.Currency,
			rs.Balance.Issuer,
		)

		// Serialize and update
		rsBytes, err := state.SerializeRippleState(rs)
		if err != nil {
			return err
		}

		return view.Update(trustLineKey, rsBytes)
	}

	// Trustline doesn't exist - create it

	// Determine low/high account ordering
	var lowAccountID, highAccountID [20]byte
	holderIsLow := keylet.IsLowAccount(accountID, ammAccountID)
	if holderIsLow {
		lowAccountID = accountID
		highAccountID = ammAccountID
	} else {
		lowAccountID = ammAccountID
		highAccountID = accountID
	}

	lowAccountStr, _ := state.EncodeAccountID(lowAccountID)
	highAccountStr, _ := state.EncodeAccountID(highAccountID)

	// Create balance - holder receives LP tokens
	var balance tx.Amount
	if holderIsLow {
		// Holder is low - positive balance
		balance = state.NewIssuedAmountFromValue(
			amount.Mantissa(),
			amount.Exponent(),
			lptAsset.Currency,
			state.AccountOneAddress,
		)
	} else {
		// Holder is high - negative balance
		negated := amount.Negate()
		balance = state.NewIssuedAmountFromValue(
			negated.Mantissa(),
			negated.Exponent(),
			lptAsset.Currency,
			state.AccountOneAddress,
		)
	}

	// Create RippleState
	// For LP token trustlines, the holder side gets reserve, AMM side doesn't
	rs := &state.RippleState{
		Balance:   balance,
		LowLimit:  state.NewIssuedAmountFromValue(0, -100, lptAsset.Currency, lowAccountStr),
		HighLimit: state.NewIssuedAmountFromValue(0, -100, lptAsset.Currency, highAccountStr),
		Flags:     0,
		LowNode:   0,
		HighNode:  0,
	}

	// Set reserve flag and NoRipple flags matching rippled's trustCreate + issueIOU.
	// Reference: rippled View.cpp trustCreate (lines 1415-1432) and issueIOU (line 2228-2240).
	// When creating a trust line, each side gets NoRipple set if that account
	// does NOT have the lsfDefaultRipple flag set.
	holderHasDefaultRipple := false
	if holderAccountData, readErr := view.Read(keylet.Account(accountID)); readErr == nil && holderAccountData != nil {
		if holderAcct, parseErr := state.ParseAccountRoot(holderAccountData); parseErr == nil {
			holderHasDefaultRipple = (holderAcct.Flags & state.LsfDefaultRipple) != 0
		}
	}
	ammHasDefaultRipple := false
	if ammAccountData, readErr := view.Read(keylet.Account(ammAccountID)); readErr == nil && ammAccountData != nil {
		if ammAcct, parseErr := state.ParseAccountRoot(ammAccountData); parseErr == nil {
			ammHasDefaultRipple = (ammAcct.Flags & state.LsfDefaultRipple) != 0
		}
	}

	if holderIsLow {
		rs.Flags |= state.LsfLowReserve
		if !holderHasDefaultRipple {
			rs.Flags |= state.LsfLowNoRipple
		}
		if !ammHasDefaultRipple {
			rs.Flags |= state.LsfHighNoRipple
		}
	} else {
		rs.Flags |= state.LsfHighReserve
		if !holderHasDefaultRipple {
			rs.Flags |= state.LsfHighNoRipple
		}
		if !ammHasDefaultRipple {
			rs.Flags |= state.LsfLowNoRipple
		}
	}

	// Insert into low account's owner directory
	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDirResult, err := state.DirInsert(view, lowDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return err
	}

	// Insert into high account's owner directory
	highDirKey := keylet.OwnerDir(highAccountID)
	highDirResult, err := state.DirInsert(view, highDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return err
	}

	// Set deletion hints
	rs.LowNode = lowDirResult.Page
	rs.HighNode = highDirResult.Page

	// Serialize and insert
	rsBytes, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	return view.Insert(trustLineKey, rsBytes)
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
			VoteWeight: uint32(VOTE_WEIGHT_SCALE_FACTOR),
		},
	}

	// Set trading fee
	amm.TradingFee = tfee

	// Calculate discounted fee
	discountedFee := uint16(0)
	if tfee > 0 {
		discountedFee = tfee / uint16(AUCTION_SLOT_DISCOUNTED_FEE_FRACTION)
	}

	// Calculate expiration: parentCloseTime + TOTAL_TIME_SLOT_SECS (24 hours)
	expiration := parentCloseTime + uint32(TOTAL_TIME_SLOT_SECS)

	// Initialize auction slot
	amm.AuctionSlot = &AuctionSlotData{
		Account:       accountID,
		Expiration:    expiration,
		Price:         zeroAmount(tx.Asset{Currency: lptCurrency, Issuer: ammAccountAddr}),
		DiscountedFee: discountedFee,
		AuthAccounts:  make([][20]byte, 0),
	}
}

// ammAccountHolds returns the amount held by the AMM account for a specific issue.
// For XRP: reads from the AMM account's AccountRoot.Balance
// For IOU: reads from the trustline between AMM account and issuer
// Reference: rippled AMMUtils.cpp ammAccountHolds
func ammAccountHolds(view tx.LedgerView, ammAccountID [20]byte, asset tx.Asset) tx.Amount {
	if asset.Currency == "" || asset.Currency == "XRP" {
		// XRP: read from AccountRoot
		accountKey := keylet.Account(ammAccountID)
		data, err := view.Read(accountKey)
		if err != nil || data == nil {
			return state.NewXRPAmountFromInt(0)
		}
		account, err := state.ParseAccountRoot(data)
		if err != nil {
			return state.NewXRPAmountFromInt(0)
		}
		return state.NewXRPAmountFromInt(int64(account.Balance))
	}

	// IOU: read from trustline
	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	trustLineKey := keylet.Line(ammAccountID, issuerID, asset.Currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	// Determine balance based on canonical ordering
	// Balance is stored from low account's perspective (positive = low owes high)
	// For AMM: if AMM is low, positive balance means AMM holds tokens
	ammIsLow := state.CompareAccountIDsForLine(ammAccountID, issuerID) < 0
	balance := rs.Balance
	if !ammIsLow {
		balance = balance.Negate()
	}

	// Return absolute balance with proper currency/issuer
	if balance.Signum() <= 0 {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	return state.NewIssuedAmountFromValue(balance.Mantissa(), balance.Exponent(), asset.Currency, asset.Issuer)
}

// ammPoolHolds returns the balances of both assets in the AMM pool.
// Reference: rippled AMMUtils.cpp ammPoolHolds
func ammPoolHolds(view tx.LedgerView, ammAccountID [20]byte, asset1, asset2 tx.Asset, fhZeroIfFrozen bool) (tx.Amount, tx.Amount) {
	// Get balance of first asset
	balance1 := ammAccountHolds(view, ammAccountID, asset1)

	// Get balance of second asset
	balance2 := ammAccountHolds(view, ammAccountID, asset2)

	// Check for frozen assets if requested
	if fhZeroIfFrozen {
		if tx.IsGlobalFrozen(view, asset1.Issuer) || tx.IsIndividualFrozen(view, ammAccountID, asset1) {
			balance1 = zeroAmount(asset1)
		}
		if tx.IsGlobalFrozen(view, asset2.Issuer) || tx.IsIndividualFrozen(view, ammAccountID, asset2) {
			balance2 = zeroAmount(asset2)
		}
	}

	return balance1, balance2
}

// AMMHolds returns the pool balances and LP token balance for an AMM.
// This is the main function to get current AMM state.
// Reference: rippled AMMUtils.cpp ammHolds
func AMMHolds(view tx.LedgerView, amm *AMMData, fhZeroIfFrozen bool) (asset1Balance, asset2Balance, lptBalance tx.Amount) {
	// Get pool balances from actual state
	asset1Balance, asset2Balance = ammPoolHolds(view, amm.Account, amm.Asset, amm.Asset2, fhZeroIfFrozen)

	// LP token balance is stored in the AMM entry
	lptBalance = amm.LPTokenBalance

	return asset1Balance, asset2Balance, lptBalance
}

// IsAMMEmpty returns true if the AMM has no LP tokens outstanding.
// An empty AMM can be deleted or reinitialized on deposit.
// Reference: rippled checks lpTokens == 0 for empty AMM
func IsAMMEmpty(amm *AMMData) bool {
	return amm.LPTokenBalance.IsZero()
}

// ammLPHolds returns the LP token balance held by an account for an AMM.
// LP tokens are stored in a trustline between the LP account and the AMM account.
// Reference: rippled AMMUtils.cpp ammLPHolds lines 113-160
func ammLPHolds(view tx.LedgerView, amm *AMMData, lpAccountID [20]byte) tx.Amount {
	// LP token currency is derived from the two asset currencies
	lptCurrency := GenerateAMMLPTCurrency(amm.Asset.Currency, amm.Asset2.Currency)
	ammAccountID := amm.Account

	// Read the trustline between LP account and AMM account
	trustLineKey := keylet.Line(lpAccountID, ammAccountID, lptCurrency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		// No trustline = no LP tokens held
		ammAccountAddr, _ := state.EncodeAccountID(ammAccountID)
		return state.NewIssuedAmountFromValue(0, -100, lptCurrency, ammAccountAddr)
	}

	// Parse the trustline
	rs, err := state.ParseRippleState(data)
	if err != nil {
		ammAccountAddr, _ := state.EncodeAccountID(ammAccountID)
		return state.NewIssuedAmountFromValue(0, -100, lptCurrency, ammAccountAddr)
	}

	// Determine balance based on canonical ordering
	// Balance is stored from low account's perspective (positive = low owes high)
	// For LP tokens: if LP is low, positive balance means LP holds tokens
	lpIsLow := state.CompareAccountIDsForLine(lpAccountID, ammAccountID) < 0
	balance := rs.Balance
	if !lpIsLow {
		balance = balance.Negate()
	}

	// Return balance with proper issuer (AMM account)
	ammAccountAddr, _ := state.EncodeAccountID(ammAccountID)
	if balance.Signum() <= 0 {
		return state.NewIssuedAmountFromValue(0, -100, lptCurrency, ammAccountAddr)
	}

	return state.NewIssuedAmountFromValue(balance.Mantissa(), balance.Exponent(), lptCurrency, ammAccountAddr)
}

// isOnlyLiquidityProvider checks if the given account is the sole LP in the AMM.
// Simplified approach: if the LP's token balance equals the AMM's total LP token
// balance (within tolerance), they must be the only LP.
// Reference: rippled AMMUtils.cpp isOnlyLiquidityProvider (lines 386-466)
func isOnlyLiquidityProvider(lpTokens tx.Amount, lptBalance tx.Amount) bool {
	lpIOU := toIOUForCalc(lpTokens)
	totalIOU := toIOUForCalc(lptBalance)
	// If LP holds all tokens, they are the only provider.
	// Use withinRelativeDistance to handle rounding differences.
	tolerance := state.NewIssuedAmountFromValue(1, -3, "", "") // 0.001
	return withinRelativeDistance(lpIOU, totalIOU, tolerance)
}

// withinRelativeDistance checks if two amounts are within relative distance dist.
// Returns true if calc == req, or (max - min) / max < dist.
// Reference: rippled AMMHelpers.h withinRelativeDistance
func withinRelativeDistance(calc, req, dist tx.Amount) bool {
	calcIOU := toIOUForCalc(calc)
	reqIOU := toIOUForCalc(req)

	if calcIOU.Compare(reqIOU) == 0 {
		return true
	}

	var minAmt, maxAmt tx.Amount
	if calcIOU.Compare(reqIOU) < 0 {
		minAmt = calcIOU
		maxAmt = reqIOU
	} else {
		minAmt = reqIOU
		maxAmt = calcIOU
	}

	diff, _ := maxAmt.Sub(minAmt)
	ratio := numberDiv(diff, maxAmt)
	return ratio.Compare(dist) < 0
}

// verifyAndAdjustLPTokenBalance adjusts the AMM SLE's LPTokenBalance when
// the last LP's trust line balance differs from it due to rounding.
// Reference: rippled AMMUtils.cpp verifyAndAdjustLPTokenBalance (lines 468-494)
func verifyAndAdjustLPTokenBalance(lpTokens tx.Amount, amm *AMMData) tx.Result {
	if isOnlyLiquidityProvider(lpTokens, amm.LPTokenBalance) {
		// Number{1, -3} = 0.001 tolerance
		tolerance := state.NewIssuedAmountFromValue(1, -3, "", "")
		if withinRelativeDistance(lpTokens, amm.LPTokenBalance, tolerance) {
			amm.LPTokenBalance = lpTokens
		} else {
			return tx.TecAMM_INVALID_TOKENS
		}
	}

	return tx.TesSUCCESS
}
