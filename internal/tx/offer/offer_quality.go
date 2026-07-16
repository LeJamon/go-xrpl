package offer

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// Quality constants
const (
	// maxTickSize is the "no rounding" sentinel (rippled Quality::maxTickSize),
	// not the maximum valid TickSize field value (15): using 15 skips rounding
	// for TickSize-15 issuers (#1224).
	maxTickSize uint8 = 16
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

func offerMulRoundLike(v1, v2, resultAsset tx.Amount, roundUp bool) tx.Amount {
	if !resultAsset.IsMPT() {
		return offerMulRound(v1, v2, resultAsset.IsNative(), resultAsset.Currency, resultAsset.Issuer, roundUp)
	}
	return newMPTAmountLike(resultAsset, state.MulRoundMPT(v1, v2, roundUp))
}

func offerDivRoundStrictLike(num, den, resultAsset tx.Amount, roundUp bool) tx.Amount {
	if !resultAsset.IsMPT() {
		return offerDivRoundStrict(num, den, resultAsset.IsNative(), resultAsset.Currency, resultAsset.Issuer, roundUp)
	}
	return newMPTAmountLike(resultAsset, state.DivRoundMPTStrict(num, den, roundUp))
}

// applyTickSize applies tick size rounding to offer amounts.
// Reference: rippled CreateOffer.cpp lines 643-685
func applyTickSize(view tx.LedgerView, takerPays, takerGets tx.Amount, bSell bool, rules *amendment.Rules, numberContexts ...state.NumberContext) (tx.Amount, tx.Amount) {
	tickSize := maxTickSize

	if !takerPays.IsNative() && !takerPays.IsMPT() {
		issuerTickSize := getTickSize(view, takerPays.Issuer)
		if issuerTickSize > 0 && issuerTickSize < tickSize {
			tickSize = issuerTickSize
		}
	}

	if !takerGets.IsNative() && !takerGets.IsMPT() {
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
		if !takerPays.IsMPT() {
			takerPays = multiplyByQuality(takerGets, roundedQuality, takerPays.Currency, takerPays.Issuer, rules, numberContexts...)
		}
	} else if !takerGets.IsMPT() {
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
func multiplyByQuality(amount tx.Amount, quality uint64, currency, issuer string, rules *amendment.Rules, numberContexts ...state.NumberContext) tx.Amount {
	native := currency == "" || currency == "XRP"
	if quality == 0 || amount.IsZero() {
		if native {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, currency, issuer)
	}

	if rules != nil && rules.Enabled(amendment.FeatureFixUniversalNumber) {
		rate := rateAmountFromQuality(quality)
		numberContext := tx.NumberContextForRules(rules)
		if len(numberContexts) > 0 {
			numberContext = numberContexts[0]
		}
		prod := numberContext.FromAmount(amount, state.RoundToNearest).
			Mul(numberContext.FromAmount(rate, state.RoundToNearest))
		prototype := tx.NewIssuedAmount(0, -100, currency, issuer)
		if native {
			prototype = tx.NewXRPAmount(0)
		}
		return numberContext.ToAmount(prod, prototype, state.RoundToNearest)
	}

	rate := rateAmountFromQuality(quality)
	value1, offset1 := state.PrepareMulDivOperand(amount)
	value2, offset2 := state.PrepareMulDivOperand(rate)
	resultNegative := amount.IsNegative() != rate.IsNegative()
	mantissa := state.MulMantissas(value1, value2, false) + 7
	offset := offset1 + offset2 + 14
	if native {
		return offerNativeDrops(mantissa, offset, resultNegative, false, false, false)
	}
	return state.FinalizeRoundIOU(
		mantissa,
		offset,
		resultNegative,
		false,
		currency,
		issuer,
		state.RoundToNearest,
		true,
	)
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
