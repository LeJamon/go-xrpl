//revive:disable:var-naming
package types

import (
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// Number represents the XRPL Number type (also known as STNumber in JS).
// It is encoded as 12 bytes: 8-byte signed mantissa + 4-byte signed exponent, both big-endian.
type Number struct{}

// Constants for the large mantissa range used by every serialized STNumber.
var (
	minMantissa, _    = new(big.Int).SetString("1000000000000000000", 10) // 10^18
	maxMantissa, _    = new(big.Int).SetString("9999999999999999999", 10) // 10^19 - 1
	maxWireMantissa   = big.NewInt(math.MaxInt64)
	minExponent       = int32(-32768)
	maxExponent       = int32(32768)
	defaultZeroExp    = int32(-2147483648) // 0x80000000
	ErrInvalidNumber  = errors.New("invalid Number string")
	ErrNumberOverflow = errors.New("mantissa and exponent are too large")
)

// numberRangeLog is log10 of the normalized mantissa minimum. STNumber is only
// used by Vault, LoanBroker, and Loan, whose amendments install the large range.
const numberRangeLog = 18

// numberRegex matches decimal/float/scientific number strings.
// Pattern: optional sign, integer part, optional decimal, optional exponent
var numberRegex = regexp.MustCompile(`^([-+]?)([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)

// FromJSON converts a JSON value (string) into a serialized 12-byte slice.
func (n *Number) FromJSON(value any) ([]byte, error) {
	s, ok := value.(string)
	if !ok {
		return nil, ErrInvalidNumber
	}

	mantissa, exponent, err := parseAndNormalize(s)
	if err != nil {
		return nil, err
	}
	wireMantissa, wireExponent, err := numberExternal(mantissa, exponent)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, 12)
	writeInt64BE(buf, wireMantissa, 0)
	writeInt32BE(buf, wireExponent, 8)

	return buf, nil
}

// ToJSON takes a BinaryParser and converts the serialized byte data back to a JSON string.
func (n *Number) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	b, err := p.ReadBytes(12)
	if err != nil {
		return nil, err
	}

	// rippled deserializes STNumber as `Number{mantissa, exponent}`
	// (STNumber.cpp:49), whose constructor runs Number::normalize
	// (Number.cpp:178): it canonicalizes the representation and throws when the
	// value falls outside the mantissa/exponent range. Mirror that here so the
	// decoder accepts exactly the blobs the encoder can produce — without it an
	// out-of-range blob decodes to a string that Encode then rejects.
	bigMantissa, exponent, err := normalize(big.NewInt(readInt64BE(b, 0)), readInt32BE(b, 8))
	if err != nil {
		return nil, err
	}

	return formatNumberText(bigMantissa, exponent, numberRangeLog), nil
}

// parseAndNormalize extracts mantissa, exponent from a string and normalizes them.
func parseAndNormalize(s string) (*big.Int, int32, error) {
	match := numberRegex.FindStringSubmatch(s)
	if match == nil {
		return nil, 0, ErrInvalidNumber
	}

	sign := match[1]
	intPart := match[2]
	fracPart := match[3]
	expPart := match[4]

	// Remove leading zeros (unless entire intPart is zeros)
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}

	mantissaStr := intPart
	exponent := int32(0)

	if fracPart != "" {
		mantissaStr += fracPart
		exponent -= int32(len(fracPart))
	}

	if expPart != "" {
		expVal, err := strconv.ParseInt(expPart, 10, 64)
		if err != nil {
			return nil, 0, ErrInvalidNumber
		}
		exponent += int32(expVal)
	}

	mantissa := new(big.Int)
	mantissa.SetString(mantissaStr, 10)

	if sign == "-" {
		mantissa.Neg(mantissa)
	}

	// Check for zero
	if mantissa.Sign() == 0 {
		return big.NewInt(0), defaultZeroExp, nil
	}

	// Normalize
	mantissa, exponent, err := normalize(mantissa, exponent)
	if err != nil {
		return nil, 0, err
	}

	return mantissa, exponent, nil
}

// normalize adjusts mantissa and exponent to the large XRPL Number range. The
// extra maxWireMantissa step mirrors Number's signed external representation:
// values above int64 are rounded one decimal place before serialization.
func normalize(mantissa *big.Int, exponent int32) (*big.Int, int32, error) {
	if mantissa.Sign() == 0 {
		return big.NewInt(0), defaultZeroExp, nil
	}

	isNegative := mantissa.Sign() < 0
	m := new(big.Int).Abs(mantissa)
	ten := big.NewInt(10)

	// Scale up if too small.
	for m.Cmp(minMantissa) < 0 && exponent > minExponent {
		exponent--
		m.Mul(m, ten)
	}

	// Scale down if too large, accumulating the discarded low-order digits so
	// the result can be rounded half-to-even (rippled's Guard). dropped holds
	// the value of the discarded digits and scale holds 10^(digits dropped).
	dropped := new(big.Int)
	scale := big.NewInt(1)
	rem := new(big.Int)
	dropDigit := func() {
		exponent++
		m.DivMod(m, ten, rem)
		dropped.Add(dropped, new(big.Int).Mul(rem, scale))
		scale.Mul(scale, ten)
	}
	for m.Cmp(maxMantissa) > 0 {
		if exponent >= maxExponent {
			return nil, 0, ErrNumberOverflow
		}
		dropDigit()
	}

	// Underflow clamps to canonical zero.
	if exponent < minExponent || m.Cmp(minMantissa) < 0 {
		return big.NewInt(0), defaultZeroExp, nil
	}

	// Large normalized values may exceed int64 internally, but the serialized
	// mantissa may not. Drop once more so rounding occurs in the wire range.
	if m.Cmp(maxWireMantissa) > 0 {
		if exponent >= maxExponent {
			return nil, 0, ErrNumberOverflow
		}
		dropDigit()
	}

	shouldRoundUp := func() bool {
		if scale.Cmp(big.NewInt(1)) == 0 {
			return false
		}
		half := new(big.Int).Div(new(big.Int).Set(scale), big.NewInt(2))
		cmp := dropped.Cmp(half)
		return cmp > 0 || (cmp == 0 && m.Bit(0) == 1)
	}
	if shouldRoundUp() {
		if m.Cmp(maxMantissa) < 0 && m.Cmp(maxWireMantissa) < 0 {
			m.Add(m, big.NewInt(1))
		} else {
			// fixCleanup3_2_0 cusp behavior: dividing first preserves the digit
			// that made an increment unsafe, then rounds the shorter mantissa.
			if exponent >= maxExponent {
				return nil, 0, ErrNumberOverflow
			}
			dropDigit()
			if shouldRoundUp() {
				m.Add(m, big.NewInt(1))
			}
		}
	}

	if m.Cmp(minMantissa) < 0 {
		m.Mul(m, ten)
		exponent--
	}
	if exponent < minExponent {
		return big.NewInt(0), defaultZeroExp, nil
	}

	if exponent > maxExponent {
		return nil, 0, ErrNumberOverflow
	}

	if isNegative {
		m.Neg(m)
	}

	return m, exponent, nil
}

// numberExternal returns Number's signed, on-wire mantissa and exponent. A
// large internal mantissa above int64 is guaranteed to end in zero and is
// exposed by rippled as mantissa/10 with exponent+1.
func numberExternal(mantissa *big.Int, exponent int32) (int64, int32, error) {
	if mantissa.Sign() == 0 {
		return 0, defaultZeroExp, nil
	}
	negative := mantissa.Sign() < 0
	m := new(big.Int).Abs(mantissa)
	if m.Cmp(maxWireMantissa) > 0 {
		q, r := new(big.Int), new(big.Int)
		q.QuoRem(m, big.NewInt(10), r)
		if r.Sign() != 0 || q.Cmp(maxWireMantissa) > 0 || exponent >= maxExponent {
			return 0, 0, ErrNumberOverflow
		}
		m = q
		exponent++
	}
	if negative {
		m.Neg(m)
	}
	return m.Int64(), exponent, nil
}

// formatNumberText renders a normalized (mantissa, exponent) as rippled's
// to_string(Number) does, for a given rangeLog. It uses scientific notation
// outside a threshold band and decimal within it; in the scientific branch the
// mantissa's trailing zeros are shifted into the exponent (a live 3.1.0
// formatting change), so e.g. 1000000000000000e-45 renders as 1e-30. The sign
// is emitted separately.
func formatNumberText(mantissa *big.Int, exponent int32, rangeLog int) string {
	if mantissa.Sign() == 0 {
		return "0"
	}
	negative := mantissa.Sign() < 0
	m := new(big.Int).Abs(mantissa)
	L := int32(rangeLog)

	if exponent != 0 && (exponent < -(L+10) || exponent > -(L-10)) {
		ten := big.NewInt(10)
		q := new(big.Int)
		r := new(big.Int)
		for m.Sign() != 0 && exponent < maxExponent {
			q.QuoRem(m, ten, r)
			if r.Sign() != 0 {
				break
			}
			m.Set(q)
			exponent++
		}
		s := m.String() + "e" + strconv.Itoa(int(exponent))
		if negative {
			return "-" + s
		}
		return s
	}

	padPrefix := rangeLog + 12
	padSuffix := rangeLog + 8
	rawValue := strings.Repeat("0", padPrefix) + m.String() + strings.Repeat("0", padSuffix)
	offset := int(exponent) + padPrefix + rangeLog + 1

	integerPart := strings.TrimLeft(rawValue[:offset], "0")
	if integerPart == "" {
		integerPart = "0"
	}
	fractionPart := strings.TrimRight(rawValue[offset:], "0")

	result := integerPart
	if fractionPart != "" {
		result += "." + fractionPart
	}
	if negative {
		result = "-" + result
	}
	return result
}

// Helper functions for big-endian signed integer I/O

func writeInt64BE(buf []byte, v int64, offset int) {
	binary.BigEndian.PutUint64(buf[offset:], uint64(v))
}

func writeInt32BE(buf []byte, v int32, offset int) {
	binary.BigEndian.PutUint32(buf[offset:], uint32(v))
}

func readInt64BE(buf []byte, offset int) int64 {
	return int64(binary.BigEndian.Uint64(buf[offset:]))
}

func readInt32BE(buf []byte, offset int) int32 {
	return int32(binary.BigEndian.Uint32(buf[offset:]))
}
