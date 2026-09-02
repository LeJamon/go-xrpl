// Package decimal parses and formats exact decimal values used by the binary codec.
package decimal

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// ErrInvalid reports a decimal that does not match the supported grammar or ranges.
var ErrInvalid = errors.New("invalid decimal")

// Parts is the normalized representation of a parsed decimal value.
type Parts struct {
	Mantissa    uint64
	Exponent    int32
	RawMantissa uint64
	RawExponent int32
	Negative    bool
	Precision   int
}

// Parse converts an anchored decimal string into normalized parts.
func Parse(value string) (Parts, error) {
	if value == "" {
		return Parts{}, ErrInvalid
	}

	i := 0
	negative := false
	if value[i] == '+' || value[i] == '-' {
		negative = value[i] == '-'
		i++
		if i == len(value) {
			return Parts{}, ErrInvalid
		}
	}

	if value[i] < '0' || value[i] > '9' {
		return Parts{}, ErrInvalid
	}
	if value[i] == '0' {
		i++
		if i < len(value) && value[i] >= '0' && value[i] <= '9' {
			return Parts{}, ErrInvalid
		}
	} else {
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
	}

	fractionDigits := 0
	if i < len(value) && value[i] == '.' {
		i++
		fractionStart := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == fractionStart {
			return Parts{}, ErrInvalid
		}
		fractionDigits = i - fractionStart
	}

	explicitExponent := int64(0)
	if i < len(value) && (value[i] == 'e' || value[i] == 'E') {
		i++
		exponentNegative := false
		if i < len(value) && (value[i] == '+' || value[i] == '-') {
			exponentNegative = value[i] == '-'
			i++
		}
		if i == len(value) || value[i] < '0' || value[i] > '9' {
			return Parts{}, ErrInvalid
		}
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			digit := int64(value[i] - '0')
			if explicitExponent > (math.MaxInt32-digit)/10 {
				return Parts{}, ErrInvalid
			}
			explicitExponent = explicitExponent*10 + digit
			i++
		}
		if exponentNegative {
			explicitExponent = -explicitExponent
		}
	}
	if i != len(value) {
		return Parts{}, ErrInvalid
	}

	var mantissa uint64
	for i = 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			if value[i] == 'e' || value[i] == 'E' {
				break
			}
			continue
		}
		digit := uint64(value[i] - '0')
		if mantissa > (math.MaxUint64-digit)/10 {
			return Parts{}, ErrInvalid
		}
		mantissa = mantissa*10 + digit
	}

	exponent := explicitExponent - int64(fractionDigits)
	if exponent < math.MinInt32 || exponent > math.MaxInt32 {
		return Parts{}, ErrInvalid
	}
	if mantissa == 0 {
		return Parts{Negative: negative}, nil
	}
	rawMantissa := mantissa
	rawExponent := int32(exponent)
	for mantissa%10 == 0 {
		mantissa /= 10
		exponent++
		if exponent > math.MaxInt32 {
			return Parts{}, ErrInvalid
		}
	}

	return Parts{
		Mantissa:    mantissa,
		Exponent:    int32(exponent),
		RawMantissa: rawMantissa,
		RawExponent: rawExponent,
		Negative:    negative,
		Precision:   decimalDigits(mantissa),
	}, nil
}

// Format returns the exact fixed-point representation of decimal parts.
func Format(mantissa uint64, exponent int, negative bool) string {
	if mantissa == 0 {
		return "0"
	}

	digits := strconv.FormatUint(mantissa, 10)
	var value string
	switch point := len(digits) + exponent; {
	case exponent >= 0:
		value = digits + strings.Repeat("0", exponent)
	case point > 0:
		value = digits[:point] + "." + digits[point:]
	default:
		value = "0." + strings.Repeat("0", -point) + digits
	}
	if strings.Contains(value, ".") {
		value = strings.TrimRight(value, "0")
		value = strings.TrimSuffix(value, ".")
	}
	if negative {
		return "-" + value
	}
	return value
}

func decimalDigits(value uint64) int {
	digits := 1
	for value >= 10 {
		value /= 10
		digits++
	}
	return digits
}
