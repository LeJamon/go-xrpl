package drops

import (
	"strconv"
)

// XRPAmount is a signed amount of drops (1 XRP = 1e6 drops). The XRPL
// protocol caps a single XRPAmount at 10^17 drops, well within int64 range.
type XRPAmount int64

// DropsPerXRP is the number of drops in one XRP (1 XRP = 1,000,000 drops).
const DropsPerXRP XRPAmount = 1_000_000

// MaxDrops is the protocol-level maximum positive value of an XRPAmount.
// Mirrors rippled's `SYSTEM_CURRENCY_PARTS * 100_000_000_000`.
const MaxDrops XRPAmount = 100_000_000_000_000_000

// NewXRPAmount returns an XRPAmount for the given number of drops.
func NewXRPAmount(drops int64) XRPAmount {
	return XRPAmount(drops)
}

// Drops returns the amount as an integer number of drops.
func (x XRPAmount) Drops() int64 {
	return int64(x)
}

// DecimalXRP returns the amount expressed in XRP (drops divided by 1e6).
func (x XRPAmount) DecimalXRP() float64 {
	return float64(x) / float64(DropsPerXRP)
}

// Add returns x+other with plain int64 arithmetic, mirroring rippled's
// XRPAmount::operator+. Overflow wraps silently, as in rippled.
func (x XRPAmount) Add(other XRPAmount) XRPAmount {
	return XRPAmount(int64(x) + int64(other))
}

// Sub returns x-other with plain int64 arithmetic, mirroring rippled's
// XRPAmount::operator-. Overflow wraps silently, as in rippled.
func (x XRPAmount) Sub(other XRPAmount) XRPAmount {
	return XRPAmount(int64(x) - int64(other))
}

// Mul returns x*factor with plain int64 arithmetic, mirroring rippled's
// XRPAmount::operator*. Overflow wraps silently, as in rippled.
func (x XRPAmount) Mul(factor int64) XRPAmount {
	return XRPAmount(int64(x) * factor)
}

// IsPositive reports whether the amount is greater than zero.
func (x XRPAmount) IsPositive() bool {
	return x > 0
}

// IsZero reports whether the amount is exactly zero.
func (x XRPAmount) IsZero() bool {
	return x == 0
}

func (x XRPAmount) String() string {
	return strconv.FormatInt(int64(x), 10)
}
