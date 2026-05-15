package drops

import (
	"errors"
	"math"
	"math/bits"
	"strconv"
)

// XRPAmount is a signed amount of drops (1 XRP = 1e6 drops). The XRPL
// protocol caps a single XRPAmount at 10^17 drops, well within int64 range.
type XRPAmount int64

const DropsPerXRP XRPAmount = 1_000_000

// MaxDrops is the protocol-level maximum positive value of an XRPAmount.
// Mirrors rippled's `SYSTEM_CURRENCY_PARTS * 100_000_000_000`.
const MaxDrops XRPAmount = 100_000_000_000_000_000

// ErrXRPAmountOverflow is returned when a checked operation would push the
// result outside [-MaxDrops, MaxDrops]. Unchecked arithmetic (Add/Sub/Mul)
// panics with this error wrapped in a string.
var ErrXRPAmountOverflow = errors.New("XRPAmount overflow")

// ErrInvalidDecimalXRP is returned by FromDecimalXRP for NaN/Inf inputs.
var ErrInvalidDecimalXRP = errors.New("invalid decimal XRP value (NaN or Inf)")

func NewXRPAmount(drops int64) XRPAmount {
	return XRPAmount(drops)
}

// FromDecimalXRP converts a decimal XRP value to drops. It returns an error
// rather than producing a silently truncated amount for NaN/Inf input.
func FromDecimalXRP(xrp float64) (XRPAmount, error) {
	if math.IsNaN(xrp) || math.IsInf(xrp, 0) {
		return 0, ErrInvalidDecimalXRP
	}
	scaled := xrp * float64(DropsPerXRP)
	if scaled > float64(math.MaxInt64) || scaled < float64(math.MinInt64) {
		return 0, ErrXRPAmountOverflow
	}
	return XRPAmount(scaled), nil
}

func (x XRPAmount) Drops() int64 {
	return int64(x)
}

func (x XRPAmount) DecimalXRP() float64 {
	return float64(x) / float64(DropsPerXRP)
}

// Add returns x+other. It panics on int64 overflow.
func (x XRPAmount) Add(other XRPAmount) XRPAmount {
	out, err := x.AddChecked(other)
	if err != nil {
		panic("drops: " + err.Error())
	}
	return out
}

// AddChecked returns x+other and an error on int64 overflow.
func (x XRPAmount) AddChecked(other XRPAmount) (XRPAmount, error) {
	r := int64(x) + int64(other)
	if (int64(other) > 0 && r < int64(x)) || (int64(other) < 0 && r > int64(x)) {
		return 0, ErrXRPAmountOverflow
	}
	return XRPAmount(r), nil
}

// Sub returns x-other. It panics on int64 overflow.
func (x XRPAmount) Sub(other XRPAmount) XRPAmount {
	out, err := x.SubChecked(other)
	if err != nil {
		panic("drops: " + err.Error())
	}
	return out
}

// SubChecked returns x-other and an error on int64 overflow.
func (x XRPAmount) SubChecked(other XRPAmount) (XRPAmount, error) {
	r := int64(x) - int64(other)
	if (int64(other) > 0 && r > int64(x)) || (int64(other) < 0 && r < int64(x)) {
		return 0, ErrXRPAmountOverflow
	}
	return XRPAmount(r), nil
}

// Mul returns x*factor. It panics on int64 overflow rather than silently
// wrapping. Callers that can tolerate the overflow as an error should use
// MulChecked.
func (x XRPAmount) Mul(factor int64) XRPAmount {
	out, err := x.MulChecked(factor)
	if err != nil {
		panic("drops: " + err.Error())
	}
	return out
}

// MulChecked returns x*factor and an error on int64 overflow.
func (x XRPAmount) MulChecked(factor int64) (XRPAmount, error) {
	if factor == 0 || int64(x) == 0 {
		return 0, nil
	}
	// Convert to absolute uint64 multiplication, then range-check.
	var sign int = 1
	a := int64(x)
	b := factor
	if a < 0 {
		sign = -sign
		a = -a
	}
	if b < 0 {
		sign = -sign
		b = -b
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 {
		return 0, ErrXRPAmountOverflow
	}
	if sign > 0 {
		if lo > math.MaxInt64 {
			return 0, ErrXRPAmountOverflow
		}
		return XRPAmount(lo), nil
	}
	// Negative result: -MinInt64 == MaxInt64+1 is allowed.
	if lo > uint64(math.MaxInt64)+1 {
		return 0, ErrXRPAmountOverflow
	}
	return XRPAmount(-int64(lo)), nil
}

func (x XRPAmount) IsPositive() bool {
	return x > 0
}

func (x XRPAmount) IsZero() bool {
	return x == 0
}

func (x XRPAmount) String() string {
	return strconv.FormatInt(int64(x), 10)
}
