package offer

import (
	"math/big"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// Quality constants
const (
	maxTickSize uint8 = 15
)

// offerNativeDrops finalizes a muldiv-round magnitude as an XRP-drops Amount,
// delegating to the shared state native-round tail.
func offerNativeDrops(amount uint64, offset int, resultNegative, roundUp, addSlop, strict bool) tx.Amount {
	return tx.NewXRPAmount(state.NativeRoundDrops(amount, offset, resultNegative, roundUp, addSlop, strict))
}

// offerDivRound divides num by den using rippled's divRound (non-strict)
// algorithm with native-aware canonicalization. The muldiv and IOU-overflow
// canonicalize core is shared with the state package; the native (XRP-drops)
// path and the zero-returns-zero contract are offer-layer specifics.
func offerDivRound(num, den tx.Amount, native bool, currency, issuer string, roundUp bool) tx.Amount {
	if den.IsZero() || num.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}
	numVal, numOff := state.PrepareMulDivOperand(num)
	denVal, denOff := state.PrepareMulDivOperand(den)
	resultNegative := num.IsNegative() != den.IsNegative()
	addSlop := resultNegative != roundUp

	amount := state.DivMantissas(numVal, denVal, addSlop)
	offset := numOff - denOff - 17
	if native {
		return offerNativeDrops(amount, offset, resultNegative, roundUp, addSlop, false)
	}
	if addSlop {
		amount, offset = state.CanonicalizeRoundIOUOverflow(amount, offset)
	}
	return state.FinalizeRoundIOU(amount, offset, resultNegative, roundUp, currency, issuer, 0, false)
}

// offerDivRoundStrict divides num by den using rippled's divRoundStrict
// algorithm with native-aware canonicalization.
func offerDivRoundStrict(num, den tx.Amount, native bool, currency, issuer string, roundUp bool) tx.Amount {
	if den.IsZero() || num.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}
	numVal, numOff := state.PrepareMulDivOperand(num)
	denVal, denOff := state.PrepareMulDivOperand(den)
	resultNegative := num.IsNegative() != den.IsNegative()
	addSlop := resultNegative != roundUp

	amount := state.DivMantissas(numVal, denVal, addSlop)
	offset := numOff - denOff - 17
	if native {
		return offerNativeDrops(amount, offset, resultNegative, roundUp, addSlop, true)
	}
	if addSlop {
		amount, offset = state.CanonicalizeRoundIOUOverflow(amount, offset)
	}
	mode := state.RoundDownward
	if roundUp != resultNegative {
		mode = state.RoundUpward
	}
	return state.FinalizeRoundIOU(amount, offset, resultNegative, roundUp, currency, issuer, mode, true)
}

// offerMulRound multiplies v1 by v2 using rippled's mulRound (non-strict)
// algorithm with native-aware canonicalization.
func offerMulRound(v1, v2 tx.Amount, native bool, currency, issuer string, roundUp bool) tx.Amount {
	if v1.IsZero() || v2.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}
	value1, offset1 := state.PrepareMulDivOperand(v1)
	value2, offset2 := state.PrepareMulDivOperand(v2)
	resultNegative := v1.IsNegative() != v2.IsNegative()
	addSlop := resultNegative != roundUp

	amount := state.MulMantissas(value1, value2, addSlop)
	offset := offset1 + offset2 + 14
	if native {
		return offerNativeDrops(amount, offset, resultNegative, roundUp, addSlop, false)
	}
	if addSlop {
		amount, offset = state.CanonicalizeRoundIOUOverflow(amount, offset)
	}
	return state.FinalizeRoundIOU(amount, offset, resultNegative, roundUp, currency, issuer, 0, false)
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

// rateAmountFromQuality decodes a quality code (exponent<<56 | mantissa) into the
// IOU rate Amount rippled's Quality::rate() yields: an issue-less value carrying the
// rate magnitude. Used as the divisor/multiplicand for tick-size amount rounding.
func rateAmountFromQuality(quality uint64) tx.Amount {
	mantissa := int64(quality & 0x00ffffffffffffff)
	exponent := int(quality>>56) - 100
	return tx.NewIssuedAmount(mantissa, exponent, "", "")
}

// multiplyByQuality multiplies an amount by a quality rate, reproducing rippled's
// multiply(amount, rate, asset). The result type is determined by currency/issuer
// parameters.
func multiplyByQuality(amount tx.Amount, quality uint64, currency, issuer string) tx.Amount {
	native := currency == "" || currency == "XRP"
	if quality == 0 || amount.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	if native {
		// rippled multiply() with a native result asset (post-fixUniversalNumber)
		// rounds in two stages: the product is first rounded to a 16-significant-
		// digit Number, then that Number is converted to drops. Reproducing it
		// with a single exact round can round a tied-up product down at the 17th
		// digit the opposite way (the placed offer's XRP leg ends up one drop
		// high). Compute it as Number{amount} * Number{rate} → drops.
		rate := rateAmountFromQuality(quality)
		prod := state.NewXRPLNumber(amount.Mantissa(), amount.Exponent()).
			Mul(state.NewXRPLNumber(rate.Mantissa(), rate.Exponent()))
		return tx.NewXRPAmount(prod.ToInt64WithMode(state.RoundToNearest))
	}

	// Convert amount to big.Rat
	mantissa := amount.Mantissa()
	exponent := amount.Exponent()
	amtRat := new(big.Rat).SetInt64(mantissa)
	if exponent > 0 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
		amtRat.Mul(amtRat, new(big.Rat).SetInt(scale))
	} else if exponent < 0 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-exponent)), nil)
		amtRat.Quo(amtRat, new(big.Rat).SetInt(scale))
	}

	rateRat := qualityToRate(quality)
	result := new(big.Rat).Mul(amtRat, rateRat)

	return ratToIssuedAmount(result, currency, issuer)
}

// divideByQuality divides an amount by a quality rate, reproducing rippled's
// divide(amount, rate, asset): muldiv(amount, 10^17, rate) + 5 canonicalized to
// nearest (ties to even). This is the same core GetRate uses, so the offer's
// tick-rounded amount and the quality recomputed from it stay consistent.
// The result type is determined by currency/issuer parameters.
func divideByQuality(amount tx.Amount, quality uint64, currency, issuer string) tx.Amount {
	native := currency == "" || currency == "XRP"
	if quality == 0 || amount.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	rate := rateAmountFromQuality(quality)
	numVal, numOff := state.PrepareMulDivOperand(amount)
	denVal, denOff := state.PrepareMulDivOperand(rate)
	resultNegative := amount.IsNegative() != rate.IsNegative()

	mantissa := state.DivMantissas(numVal, denVal, false) + 5
	offset := numOff - denOff - 17
	if native {
		return offerNativeDrops(mantissa, offset, resultNegative, false, false, false)
	}
	return state.FinalizeRoundIOU(mantissa, offset, resultNegative, false, currency, issuer, state.RoundToNearest, true)
}

// ratToIssuedAmount converts a big.Rat to an IOU Amount with the given currency and
// issuer, rounding the mantissa to nearest (ties to even) — the rounding rippled's
// multiply()/divide() apply through Number under fixUniversalNumber.
func ratToIssuedAmount(rat *big.Rat, currency, issuer string) tx.Amount {
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

	// Round half to even on the discarded fraction.
	twice := new(big.Int).Lsh(remainder, 1)
	if cmp := twice.Cmp(scaled.Denom()); cmp > 0 || (cmp == 0 && intPart.Bit(0) == 1) {
		intPart.Add(intPart, big.NewInt(1))
		if intPart.Cmp(maxMant) >= 0 {
			intPart.Quo(intPart, big.NewInt(10))
			exponent++
		}
	}

	mantissa := intPart.Int64()
	if negative {
		mantissa = -mantissa
	}

	return tx.NewIssuedAmount(mantissa, exponent, currency, issuer)
}
