package state

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"regexp"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/keylet"
)

// AmountFromJSON parses an RPC-parameter amount (destination_amount,
// send_max, ...) the way rippled's amountFromJson does. Accepted forms:
//
//   - a JSON string: either a bare number, or up to three segments split
//     on any of "\t\n\r ,/" as value / currency / issuer
//   - a JSON object: {currency, issuer, value} or {mpt_issuance_id, value}
//   - a JSON array: [value, currency, issuer]
//   - a bare JSON number (32-bit only — rippled's Json reader classifies
//     anything larger, fractional, or exponent-form as Real, which the
//     parser rejects as "invalid amount type")
//
// Any error means the input is malformed; callers map it to their
// method-specific error code, mirroring amountFromJsonNoThrow.
func AmountFromJSON(raw json.RawMessage) (amt Amount, err error) {
	// IOU normalization panics on exponent overflow the way rippled's
	// canonicalize throws; surface it as a parse error like
	// amountFromJsonNoThrow catching the exception.
	defer func() {
		if r := recover(); r != nil {
			amt = Amount{}
			err = fmt.Errorf("%v", r)
		}
	}()

	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return Amount{}, fmt.Errorf("invalid amount json: %w", err)
	}

	var (
		value         any
		currencyField any
		issuerField   any
		isMPT         bool
		isObject      bool
		bareNumber    bool
	)

	switch jv := v.(type) {
	case nil:
		return Amount{}, errors.New("XRP may not be specified with a null Json value")
	case map[string]any:
		isObject = true
		_, hasMPTID := jv["mpt_issuance_id"]
		_, hasCurrency := jv["currency"]
		_, hasIssuer := jv["issuer"]
		if hasMPTID && (hasCurrency || hasIssuer) {
			return Amount{}, errors.New("Invalid Asset's Json specification")
		}
		if !hasMPTID && !hasCurrency {
			return Amount{}, errors.New("Invalid Asset's Json specification")
		}
		value = jv["value"]
		if hasMPTID {
			isMPT = true
			currencyField = jv["mpt_issuance_id"]
		} else {
			currencyField = jv["currency"]
			issuerField = jv["issuer"]
		}
	case []any:
		value = json.Number("0")
		if len(jv) > 0 {
			value = jv[0]
		}
		if len(jv) > 1 {
			currencyField = jv[1]
		}
		if len(jv) > 2 {
			issuerField = jv[2]
		}
	case string:
		elements := splitAmountString(jv)
		if len(elements) > 3 {
			return Amount{}, errors.New("invalid amount string")
		}
		value = elements[0]
		if len(elements) > 1 {
			currencyField = elements[1]
		}
		if len(elements) > 2 {
			issuerField = elements[2]
		}
	case json.Number:
		value = jv
		bareNumber = true
	default:
		value = jv
	}

	currencyStr, currencyIsString := currencyField.(string)
	native := !currencyIsString || currencyStr == "" || currencyStr == "XRP"

	var (
		mptID     string
		currency  string
		issuer    string
		accountID [20]byte
	)
	if native {
		if isObject {
			return Amount{}, errors.New("XRP may not be specified as an object")
		}
	} else if isMPT {
		// 192 bits: sequence (32) + issuer account (160).
		decoded, hexErr := hex.DecodeString(currencyStr)
		if hexErr != nil || len(decoded) != 24 {
			return Amount{}, errors.New("invalid MPTokenIssuanceID")
		}
		mptID = currencyStr
	} else {
		cur, curErr := currencyFromJSONString(currencyStr)
		if curErr != nil {
			return Amount{}, curErr
		}
		issuerStr, issuerIsString := issuerField.(string)
		if !issuerIsString {
			return Amount{}, errors.New("invalid issuer")
		}
		id, issErr := issuerFromJSONString(issuerStr)
		if issErr != nil {
			return Amount{}, issErr
		}
		// An all-zero 160-bit code is the native currency; only the hex
		// form can reach here with it.
		if cur == ([20]byte{}) {
			return Amount{}, errors.New("invalid issuer")
		}
		currency = currencyStr
		accountID = id
		issuer = EncodeAccountIDSafe(accountID)
	}

	parts, err := amountValueParts(value, bareNumber, native || isMPT)
	if err != nil {
		return Amount{}, err
	}

	if native {
		drops, rangeErr := integralAmount(parts, maxNativeAmount, "Native currency amount out of range")
		if rangeErr != nil {
			return Amount{}, rangeErr
		}
		return NewXRPAmountFromInt(drops), nil
	}
	if isMPT {
		units, rangeErr := integralAmount(parts, maxMPTAmount, "MPT amount out of range")
		if rangeErr != nil {
			return Amount{}, rangeErr
		}
		return NewMPTAmountWithIssuanceID(units, "", mptID), nil
	}

	mantissa, exponent := reduceIOUMantissa(parts.mantissa, parts.exponent)
	if parts.negative {
		mantissa = -mantissa
	}
	return NewIssuedAmountFromValue(mantissa, exponent, currency, issuer), nil
}

// rippled STAmount.h: cMaxNativeN (10^17 drops) and maxMPTokenAmount
// (math.MaxInt64).
const (
	maxNativeAmount = uint64(100_000_000_000_000_000)
	maxMPTAmount    = uint64(math.MaxInt64)
)

// nativeExponentLimit / mptExponentLimit mirror the canonicalize guards
// log10(cMaxNativeN) == 17 and log10(maxMPTokenAmount) ~ 18.96.
const (
	nativeExponentLimit = 17
	mptExponentLimit    = 18
)

// amountNumberParts is rippled's NumberParts: an unsigned mantissa with a
// decimal exponent and explicit sign.
type amountNumberParts struct {
	mantissa uint64
	exponent int
	negative bool
}

// amountNumberRe is rippled partsFromString's anchored number grammar:
// optional sign, no leading zeroes, optional fraction, optional exponent.
var amountNumberRe = regexp.MustCompile(
	`^([-+]?)(0|[1-9][0-9]*)(\.([0-9]+))?([eE]([+-]?)([0-9]+))?$`)

// amountPartsFromString ports rippled's partsFromString.
func amountPartsFromString(number string) (amountNumberParts, error) {
	m := amountNumberRe.FindStringSubmatch(number)
	if m == nil {
		return amountNumberParts{}, fmt.Errorf("'%s' is not a number", number)
	}

	parts := amountNumberParts{negative: m[1] == "-"}

	digits := m[2]
	if m[4] != "" {
		digits += m[4]
		parts.exponent = -len(m[4])
	}
	mantissa, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return amountNumberParts{}, fmt.Errorf("'%s' is not a number", number)
	}
	parts.mantissa = mantissa

	if m[7] != "" {
		exp, err := strconv.Atoi(m[7])
		if err != nil {
			return amountNumberParts{}, fmt.Errorf("'%s' is not a number", number)
		}
		if m[6] == "-" {
			parts.exponent -= exp
		} else {
			parts.exponent += exp
		}
	}

	return parts, nil
}

// amountValueParts interprets the value member the way amountFromJson does:
// 32-bit ints directly, strings through the number grammar, anything else
// (null, bool, fractional/oversized numbers, nested containers) rejected.
// integral reports whether the asset is XRP or MPT, which may not use a
// fractional representation.
func amountValueParts(value any, bareNumber, integral bool) (amountNumberParts, error) {
	switch val := value.(type) {
	case json.Number:
		s := val.String()
		if strings.ContainsAny(s, ".eE") {
			// rippled's Json reader classifies these as Real.
			return amountNumberParts{}, errors.New("invalid amount type")
		}
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return amountNumberParts{}, errors.New("invalid amount type")
		}
		switch {
		case i >= math.MinInt32 && i <= math.MaxInt32:
			parts := amountNumberParts{}
			if i < 0 {
				parts.negative = true
				parts.mantissa = uint64(-i)
			} else {
				parts.mantissa = uint64(i)
			}
			return parts, nil
		case i > 0 && i <= math.MaxUint32:
			// rippled reads the UInt branch off the enclosing value
			// (v.asUInt(), not value.asUInt()), so a uint-range number
			// inside an object or array throws a Json conversion error.
			if !bareNumber {
				return amountNumberParts{}, errors.New("Value is not convertible to UInt.")
			}
			return amountNumberParts{mantissa: uint64(i)}, nil
		default:
			// Beyond 32 bits the Json reader stores a Real.
			return amountNumberParts{}, errors.New("invalid amount type")
		}
	case string:
		parts, err := amountPartsFromString(val)
		if err != nil {
			return amountNumberParts{}, err
		}
		if integral && parts.exponent < 0 {
			return amountNumberParts{}, errors.New("XRP and MPT must be specified as integral amount.")
		}
		return parts, nil
	default:
		return amountNumberParts{}, errors.New("invalid amount type")
	}
}

// integralAmount scales mantissa by 10^exponent for XRP/MPT amounts,
// enforcing rippled's canonicalize range guards.
func integralAmount(parts amountNumberParts, maxValue uint64, rangeMsg string) (int64, error) {
	if parts.mantissa == 0 {
		return 0, nil
	}
	limit := nativeExponentLimit
	if maxValue == maxMPTAmount {
		limit = mptExponentLimit
	}
	if parts.exponent > limit {
		return 0, errors.New(rangeMsg)
	}
	value := parts.mantissa
	for i := 0; i < parts.exponent; i++ {
		hi, lo := bits.Mul64(value, 10)
		if hi != 0 || lo > maxValue {
			return 0, errors.New(rangeMsg)
		}
		value = lo
	}
	if value > maxValue {
		return 0, errors.New(rangeMsg)
	}
	out := int64(value)
	if parts.negative {
		out = -out
	}
	return out, nil
}

// reduceIOUMantissa brings a uint64 mantissa into int64 range with a single
// round-half-to-even step so the subsequent normalization never has to
// round twice. Values at or below MaxMantissa pass through untouched.
func reduceIOUMantissa(mantissa uint64, exponent int) (int64, int) {
	if mantissa <= uint64(MaxMantissa) {
		return int64(mantissa), exponent
	}
	divisor := uint64(1)
	for mantissa/divisor > uint64(MaxMantissa) {
		divisor *= 10
		exponent++
	}
	q, r := mantissa/divisor, mantissa%divisor
	switch {
	case r*2 > divisor:
		q++
	case r*2 == divisor && q%2 == 1:
		q++
	}
	return int64(q), exponent
}

// amountStringSeparators is the separator set rippled splits string
// amounts on.
const amountStringSeparators = "\t\n\r ,/"

// splitAmountString splits on every separator occurrence without
// compressing adjacent separators, like boost::split.
func splitAmountString(s string) []string {
	elements := []string{}
	var cur strings.Builder
	for _, r := range s {
		if strings.ContainsRune(amountStringSeparators, r) {
			elements = append(elements, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	return append(elements, cur.String())
}

// currencyFromJSONString ports to_currency for the non-native path: a
// three-character ISO-like code or a 160-bit hex code. The caller has already
// handled the native ("" / "XRP") case, so this validates the form through
// keylet and encodes with the canonical encoder.
func currencyFromJSONString(code string) ([20]byte, error) {
	if !keylet.IsValidCurrencyCode(code) {
		return [20]byte{}, errors.New("invalid currency")
	}
	return keylet.CurrencyBytes(code), nil
}

// issuerFromJSONString ports to_issuer: a 160-bit hex account or a base58
// classic address.
func issuerFromJSONString(issuer string) ([20]byte, error) {
	if len(issuer) == 40 {
		decoded, err := hex.DecodeString(issuer)
		if err == nil {
			var id [20]byte
			copy(id[:], decoded)
			return id, nil
		}
	}
	id, err := DecodeAccountID(issuer)
	if err != nil {
		return [20]byte{}, errors.New("invalid issuer")
	}
	return id, nil
}
