//revive:disable:var-naming
package types

import (
	"encoding/binary"
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// Number represents the XRPL Number type (also known as STNumber in JS).
// It is encoded as 12 bytes: 8-byte signed mantissa + 4-byte signed exponent, both big-endian.
type Number struct{}

// Constants for mantissa and exponent normalization per XRPL Number spec.
var (
	minMantissa       = big.NewInt(1000000000000000) // 10^15
	maxMantissa       = big.NewInt(9999999999999999) // 10^16 - 1
	minExponent       = int32(-32768)
	maxExponent       = int32(32768)
	defaultZeroExp    = int32(-2147483648) // 0x80000000
	ErrInvalidNumber  = errors.New("invalid Number string")
	ErrNumberOverflow = errors.New("mantissa and exponent are too large")
)

// numberRangeLog is log10 of the normalized mantissa minimum. It drives
// to_string's scientific-vs-decimal threshold and padding. STNumber serializes
// and normalizes at the small scale (rangeLog 15) on the current chain; the
// large scale (rangeLog 18) is threaded in when SingleAssetVault /
// LendingProtocol activate (Number/codec Phase B). formatNumberText is faithful
// for either value.
const numberRangeLog = 15

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

	buf := make([]byte, 12)
	writeInt64BE(buf, mantissa.Int64(), 0)
	writeInt32BE(buf, exponent, 8)

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

// normalize adjusts mantissa and exponent to XRPL constraints, mirroring
// rippled's Number::normalize (Number.cpp): it rounds the discarded low-order
// digits half-to-even and clamps to canonical zero on underflow.
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
	for m.Cmp(maxMantissa) > 0 {
		if exponent >= maxExponent {
			return nil, 0, ErrNumberOverflow
		}
		exponent++
		m.DivMod(m, ten, rem)
		dropped.Add(dropped, new(big.Int).Mul(rem, scale))
		scale.Mul(scale, ten)
	}

	// Underflow clamps to canonical zero.
	if exponent < minExponent || m.Cmp(minMantissa) < 0 {
		return big.NewInt(0), defaultZeroExp, nil
	}

	// Round half-to-even on the discarded digits. When any digit was dropped
	// scale is 10^k with k>=1, so half (scale/2) is exact.
	if scale.Cmp(big.NewInt(1)) > 0 {
		half := new(big.Int).Div(scale, big.NewInt(2))
		if cmp := dropped.Cmp(half); cmp > 0 || (cmp == 0 && m.Bit(0) == 1) {
			m.Add(m, big.NewInt(1))
			if m.Cmp(maxMantissa) > 0 {
				m.Div(m, ten)
				exponent++
			}
		}
	}

	if exponent > maxExponent {
		return nil, 0, ErrNumberOverflow
	}

	if isNegative {
		m.Neg(m)
	}

	return m, exponent, nil
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
