package offer

import (
	"math/big"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/keylet"
)

// Quality constants
const (
	maxTickSize uint8 = 15
)

// offerDivRound divides num by den using rippled's divRound (non-strict) algorithm
// with native-aware canonicalization. When native=true, uses canonicalizeRound for
// XRP drops; when native=false, uses IOU canonicalize.
// Reference: rippled STAmount.cpp divRoundImpl with canonicalizeRound + DontAffectNumberRoundMode
func offerDivRound(num, den tx.Amount, native bool, currency, issuer string, roundUp bool) tx.Amount {
	if den.IsZero() || num.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	numVal := num.Mantissa()
	numOff := num.Exponent()
	denVal := den.Mantissa()
	denOff := den.Exponent()

	if num.IsNative() {
		if numVal < 0 {
			numVal = -numVal
		}
		for numVal < state.MinMantissa {
			numVal *= 10
			numOff--
		}
	}
	if den.IsNative() {
		if denVal < 0 {
			denVal = -denVal
		}
		for denVal < state.MinMantissa {
			denVal *= 10
			denOff--
		}
	}

	resultNegative := num.IsNegative() != den.IsNegative()

	if numVal < 0 {
		numVal = -numVal
	}
	if denVal < 0 {
		denVal = -denVal
	}

	// muldiv_round: (numVal * 10^17 + rounding) / denVal
	tenTo17 := new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil)
	numerator := new(big.Int).Mul(big.NewInt(numVal), tenTo17)
	if resultNegative != roundUp {
		numerator.Add(numerator, new(big.Int).Sub(big.NewInt(denVal), big.NewInt(1)))
	}
	quotient := new(big.Int).Div(numerator, big.NewInt(denVal))
	amount := quotient.Uint64()
	offset := numOff - denOff - 17

	if resultNegative != roundUp {
		if native {
			// canonicalizeRound for native (XRP drops)
			drops := payment.CanonicalizeDrops(int64(amount), offset)
			// Fallback: if rounding up produced zero, return minimum positive (1 drop).
			// Reference: rippled STAmount.cpp divRoundImpl lines 1720-1726
			if drops == 0 && roundUp && !resultNegative {
				drops = 1
			}
			if resultNegative {
				drops = -drops
			}
			return tx.NewXRPAmount(drops)
		}
		// canonicalizeRound for IOU
		if amount > uint64(state.MaxMantissa) {
			for amount > 10*uint64(state.MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	} else if native {
		// No canonicalize needed, but still need to convert to drops
		drops := int64(amount)
		for offset > 0 {
			drops *= 10
			offset--
		}
		for offset < 0 {
			drops /= 10
			offset++
		}
		if resultNegative {
			drops = -drops
		}
		return tx.NewXRPAmount(drops)
	}

	// DontAffectNumberRoundMode: NO guard
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := state.NewIssuedAmountFromValue(mantissa, offset, currency, issuer)

	if roundUp && !resultNegative && result.IsZero() {
		if native {
			return tx.NewXRPAmount(1)
		}
		return state.NewIssuedAmountFromValue(state.MinMantissa, state.MinExponent, currency, issuer)
	}

	return result
}

// offerDivRoundStrict divides num by den using rippled's divRoundStrict algorithm
// with native-aware canonicalization.
// Reference: rippled STAmount.cpp divRoundImpl with canonicalizeRoundStrict + NumberRoundModeGuard
func offerDivRoundStrict(num, den tx.Amount, native bool, currency, issuer string, roundUp bool) tx.Amount {
	if den.IsZero() || num.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	numVal := num.Mantissa()
	numOff := num.Exponent()
	denVal := den.Mantissa()
	denOff := den.Exponent()

	if num.IsNative() {
		if numVal < 0 {
			numVal = -numVal
		}
		for numVal < state.MinMantissa {
			numVal *= 10
			numOff--
		}
	}
	if den.IsNative() {
		if denVal < 0 {
			denVal = -denVal
		}
		for denVal < state.MinMantissa {
			denVal *= 10
			denOff--
		}
	}

	resultNegative := num.IsNegative() != den.IsNegative()

	if numVal < 0 {
		numVal = -numVal
	}
	if denVal < 0 {
		denVal = -denVal
	}

	// muldiv_round: (numVal * 10^17 + rounding) / denVal
	tenTo17 := new(big.Int).SetUint64(100_000_000_000_000_000) // 10^17
	bigNum := new(big.Int).Mul(big.NewInt(numVal), tenTo17)
	bigDen := new(big.Int).SetInt64(denVal)
	if resultNegative != roundUp {
		bigNum.Add(bigNum, new(big.Int).Sub(bigDen, big.NewInt(1)))
	}
	bigResult := new(big.Int).Div(bigNum, bigDen)

	amount := bigResult.Uint64()
	offset := numOff - denOff - 17

	if resultNegative != roundUp {
		if native {
			// canonicalizeRoundStrict for native (XRP drops)
			drops := payment.CanonicalizeDropsStrict(int64(amount), offset, roundUp)
			// Fallback: if rounding up produced zero, return minimum positive (1 drop).
			// Reference: rippled STAmount.cpp divRoundImpl lines 1720-1726
			if drops == 0 && roundUp && !resultNegative {
				drops = 1
			}
			if resultNegative {
				drops = -drops
			}
			return tx.NewXRPAmount(drops)
		}
		// canonicalizeRoundStrict for IOU
		if amount > uint64(state.MaxMantissa) {
			for amount > 10*uint64(state.MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	} else if native {
		// No canonicalize needed (resultNegative == roundUp), just convert to drops
		drops := int64(amount)
		for offset > 0 {
			drops *= 10
			offset--
		}
		for offset < 0 {
			drops /= 10
			offset++
		}
		if resultNegative {
			drops = -drops
		}
		return tx.NewXRPAmount(drops)
	}

	// NumberRoundModeGuard with appropriate mode
	var mode state.RoundingMode
	if roundUp != resultNegative {
		mode = state.RoundUpward
	} else {
		mode = state.RoundDownward
	}
	guard := state.NewNumberRoundModeGuard(mode)
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := state.NewIssuedAmountFromValue(mantissa, offset, currency, issuer)
	guard.Release()

	if roundUp && !resultNegative && result.IsZero() {
		if native {
			return tx.NewXRPAmount(1)
		}
		return state.NewIssuedAmountFromValue(state.MinMantissa, state.MinExponent, currency, issuer)
	}

	return result
}

// offerMulRound multiplies v1 by v2 using rippled's mulRound (non-strict) algorithm
// with native-aware canonicalization.
// Reference: rippled STAmount.cpp mulRoundImpl with canonicalizeRound + DontAffectNumberRoundMode
func offerMulRound(v1, v2 tx.Amount, native bool, currency, issuer string, roundUp bool) tx.Amount {
	if v1.IsZero() || v2.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	value1 := v1.Mantissa()
	offset1 := v1.Exponent()
	value2 := v2.Mantissa()
	offset2 := v2.Exponent()

	if v1.IsNative() {
		if value1 < 0 {
			value1 = -value1
		}
		for value1 < state.MinMantissa {
			value1 *= 10
			offset1--
		}
	}
	if v2.IsNative() {
		if value2 < 0 {
			value2 = -value2
		}
		for value2 < state.MinMantissa {
			value2 *= 10
			offset2--
		}
	}

	resultNegative := v1.IsNegative() != v2.IsNegative()

	if value1 < 0 {
		value1 = -value1
	}
	if value2 < 0 {
		value2 = -value2
	}

	// muldiv_round: (value1 * value2 + rounding) / 10^14
	tenTo14 := new(big.Int).SetUint64(100_000_000_000_000)
	tenTo14m1 := new(big.Int).SetUint64(99_999_999_999_999)
	product := new(big.Int).Mul(big.NewInt(value1), big.NewInt(value2))
	if resultNegative != roundUp {
		product.Add(product, tenTo14m1)
	}
	product.Div(product, tenTo14)

	amount := product.Uint64()
	offset := offset1 + offset2 + 14

	if resultNegative != roundUp {
		if native {
			// canonicalizeRound for native (XRP drops)
			drops := payment.CanonicalizeDrops(int64(amount), offset)
			// Fallback: if rounding up produced zero, return minimum positive (1 drop).
			// Reference: rippled STAmount.cpp mulRoundImpl lines 1624-1630:
			//   if (roundUp && !resultNegative && !result) { amount = 1; offset = 0; }
			if drops == 0 && roundUp && !resultNegative {
				drops = 1
			}
			if resultNegative {
				drops = -drops
			}
			return tx.NewXRPAmount(drops)
		}
		// canonicalizeRound for IOU
		if amount > uint64(state.MaxMantissa) {
			for amount > 10*uint64(state.MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	} else if native {
		// No canonicalize needed (resultNegative == roundUp), just convert to drops
		drops := int64(amount)
		for offset > 0 {
			drops *= 10
			offset--
		}
		for offset < 0 {
			drops /= 10
			offset++
		}
		if resultNegative {
			drops = -drops
		}
		return tx.NewXRPAmount(drops)
	}

	// DontAffectNumberRoundMode: NO guard
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := state.NewIssuedAmountFromValue(mantissa, offset, currency, issuer)

	if roundUp && !resultNegative && result.IsZero() {
		if native {
			return tx.NewXRPAmount(1)
		}
		return state.NewIssuedAmountFromValue(state.MinMantissa, state.MinExponent, currency, issuer)
	}

	return result
}

// applyTickSize applies tick size rounding to offer amounts.
// Reference: rippled CreateOffer.cpp lines 643-685
func applyTickSize(view tx.LedgerView, takerPays, takerGets tx.Amount, bSell bool, rules *amendment.Rules) (tx.Amount, tx.Amount) {
	tickSize := maxTickSize

	if !takerPays.IsNative() {
		issuerTickSize := getTickSize(view, takerPays.Issuer)
		if issuerTickSize > 0 && issuerTickSize < tickSize {
			tickSize = issuerTickSize
		}
	}

	if !takerGets.IsNative() {
		issuerTickSize := getTickSize(view, takerGets.Issuer)
		if issuerTickSize > 0 && issuerTickSize < tickSize {
			tickSize = issuerTickSize
		}
	}

	// If no tick size applies, return unchanged
	if tickSize >= maxTickSize {
		return takerPays, takerGets
	}

	// Apply tick size rounding
	// Reference: lines 660-685
	quality := state.CalculateQuality(takerGets, takerPays)
	roundedQuality := roundToTickSize(quality, tickSize)

	if bSell {
		// Round TakerPays
		takerPays = multiplyByQuality(takerGets, roundedQuality, takerPays.Currency, takerPays.Issuer)
	} else {
		// Round TakerGets
		takerGets = divideByQuality(takerPays, roundedQuality, takerGets.Currency, takerGets.Issuer)
	}

	return takerPays, takerGets
}

// getTickSize returns the tick size for an issuer.
func getTickSize(view tx.LedgerView, issuerAddress string) uint8 {
	if issuerAddress == "" {
		return 0
	}

	issuerID, err := state.DecodeAccountID(issuerAddress)
	if err != nil {
		return 0
	}

	accountKey := keylet.Account(issuerID)
	data, err := view.Read(accountKey)
	if err != nil || data == nil {
		return 0
	}

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return 0
	}

	return account.TickSize
}

// roundToTickSize rounds a quality value to the specified tick size.
// Reference: rippled Quality.cpp round() function lines 182-212
// The tick size determines how many significant digits are kept in the mantissa.
// Quality is encoded as: (exponent << 56) | mantissa where mantissa is in [10^15, 10^16)
func roundToTickSize(quality uint64, tickSize uint8) uint64 {
	// If tick size is max or zero, no rounding needed
	if tickSize >= maxTickSize || tickSize == 0 {
		return quality
	}

	// Modulus for mantissa - determines rounding granularity
	// These are powers of 10 that determine rounding precision
	mod := []uint64{
		10000000000000000, // 0: 10^16 (no rounding)
		1000000000000000,  // 1: 10^15
		100000000000000,   // 2: 10^14
		10000000000000,    // 3: 10^13
		1000000000000,     // 4: 10^12
		100000000000,      // 5: 10^11
		10000000000,       // 6: 10^10
		1000000000,        // 7: 10^9
		100000000,         // 8: 10^8
		10000000,          // 9: 10^7
		1000000,           // 10: 10^6
		100000,            // 11: 10^5
		10000,             // 12: 10^4
		1000,              // 13: 10^3
		100,               // 14: 10^2
		10,                // 15: 10^1
		1,                 // 16: 10^0
	}

	// Extract exponent (top 8 bits) and mantissa (lower 56 bits)
	exponent := quality >> 56
	mantissa := quality & 0x00ffffffffffffff

	// Round up: add (mod-1) then truncate
	mantissa += mod[tickSize] - 1
	mantissa -= mantissa % mod[tickSize]

	// Reconstruct quality
	return (exponent << 56) | mantissa
}

// qualityToRate converts a quality value (encoded as (exponent << 56) | mantissa) to a big.Rat.
// Quality encoding: exponent is stored as (actual_exponent + 100) in the top 8 bits,
// mantissa is in the lower 56 bits and is in range [10^15, 10^16).
func qualityToRate(quality uint64) *big.Rat {
	if quality == 0 {
		return new(big.Rat).SetInt64(0)
	}

	// Extract exponent (top 8 bits) and mantissa (lower 56 bits)
	storedExponent := quality >> 56
	mantissa := quality & 0x00ffffffffffffff

	// Actual exponent = storedExponent - 100
	actualExponent := int(storedExponent) - 100

	// Rate = mantissa * 10^actualExponent
	rat := new(big.Rat).SetInt64(int64(mantissa))

	if actualExponent > 0 {
		// Multiply by 10^actualExponent
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(actualExponent)), nil)
		rat.Mul(rat, new(big.Rat).SetInt(scale))
	} else if actualExponent < 0 {
		// Divide by 10^(-actualExponent)
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-actualExponent)), nil)
		rat.Quo(rat, new(big.Rat).SetInt(scale))
	}

	return rat
}

// multiplyByQuality multiplies an amount by a quality rate.
// Reference: rippled uses mulRound to multiply amount by quality rate.
// The result type is determined by currency/issuer parameters.
func multiplyByQuality(amount tx.Amount, quality uint64, currency, issuer string) tx.Amount {
	if quality == 0 || amount.IsZero() {
		if currency == "" || currency == "XRP" {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	// Convert amount to big.Rat
	var amtRat *big.Rat
	if amount.IsNative() {
		amtRat = new(big.Rat).SetInt64(amount.Drops())
	} else {
		mantissa := amount.Mantissa()
		exponent := amount.Exponent()
		amtRat = new(big.Rat).SetInt64(mantissa)
		if exponent > 0 {
			scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
			amtRat.Mul(amtRat, new(big.Rat).SetInt(scale))
		} else if exponent < 0 {
			scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-exponent)), nil)
			amtRat.Quo(amtRat, new(big.Rat).SetInt(scale))
		}
	}

	rateRat := qualityToRate(quality)
	result := new(big.Rat).Mul(amtRat, rateRat)

	if currency == "" || currency == "XRP" {
		f, _ := result.Float64()
		return tx.NewXRPAmount(int64(f + 0.5)) // Round
	}

	return ratToIssuedAmount(result, currency, issuer, true)
}

// divideByQuality divides an amount by a quality rate.
// Reference: rippled uses divRound to divide amount by quality rate.
// The result type is determined by currency/issuer parameters.
func divideByQuality(amount tx.Amount, quality uint64, currency, issuer string) tx.Amount {
	if quality == 0 {
		// Division by zero - return zero
		if currency == "" || currency == "XRP" {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	if amount.IsZero() {
		if currency == "" || currency == "XRP" {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	// Convert amount to big.Rat
	var amtRat *big.Rat
	if amount.IsNative() {
		amtRat = new(big.Rat).SetInt64(amount.Drops())
	} else {
		mantissa := amount.Mantissa()
		exponent := amount.Exponent()
		amtRat = new(big.Rat).SetInt64(mantissa)
		if exponent > 0 {
			scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
			amtRat.Mul(amtRat, new(big.Rat).SetInt(scale))
		} else if exponent < 0 {
			scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-exponent)), nil)
			amtRat.Quo(amtRat, new(big.Rat).SetInt(scale))
		}
	}

	rateRat := qualityToRate(quality)
	result := new(big.Rat).Quo(amtRat, rateRat)

	if currency == "" || currency == "XRP" {
		f, _ := result.Float64()
		return tx.NewXRPAmount(int64(f + 0.5)) // Round
	}

	return ratToIssuedAmount(result, currency, issuer, true)
}

// ratToIssuedAmount converts a big.Rat to an IOU Amount with the given currency and issuer.
// The roundUp parameter controls rounding direction.
func ratToIssuedAmount(rat *big.Rat, currency, issuer string, roundUp bool) tx.Amount {
	if rat.Sign() == 0 {
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	// Handle sign
	negative := rat.Sign() < 0
	if negative {
		rat = new(big.Rat).Neg(rat)
	}

	// Normalize to mantissa in [10^15, 10^16)
	minMant := big.NewInt(1000000000000000)  // 10^15
	maxMant := big.NewInt(10000000000000000) // 10^16

	exponent := 0
	scaled := new(big.Rat).Set(rat)

	for {
		intPart := new(big.Int).Quo(scaled.Num(), scaled.Denom())
		if intPart.Cmp(maxMant) >= 0 {
			// Too large, divide by 10
			scaled.Quo(scaled, big.NewRat(10, 1))
			exponent++
		} else if intPart.Cmp(minMant) < 0 {
			// Too small, multiply by 10
			if scaled.Sign() == 0 {
				break
			}
			scaled.Mul(scaled, big.NewRat(10, 1))
			exponent--
		} else {
			break
		}
		// Safety limit
		if exponent > 80 || exponent < -96 {
			break
		}
	}

	intPart := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	remainder := new(big.Int).Mod(scaled.Num(), scaled.Denom())

	if roundUp && remainder.Sign() != 0 {
		intPart.Add(intPart, big.NewInt(1))
	}

	mantissa := intPart.Int64()
	if negative {
		mantissa = -mantissa
	}

	return tx.NewIssuedAmount(mantissa, exponent, currency, issuer)
}
