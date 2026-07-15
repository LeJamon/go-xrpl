package offer

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
)

// maxNativeDrops is rippled STAmount::cMaxNativeN — the isLegalNet ceiling on a
// native (XRP) amount's magnitude.
const maxNativeDrops uint64 = 100_000_000_000_000_000

// isLegalNetAmount mirrors rippled STAmount isLegalNet: a native amount's
// magnitude may not exceed cMaxNativeN; non-native amounts are always legal.
// Zero amounts are legal here and are rejected later by the temBAD_OFFER check.
func isLegalNetAmount(amt tx.Amount) bool {
	if !amt.IsNative() {
		return true
	}
	d := amt.Drops()
	if d < 0 {
		d = -d
	}
	return uint64(d) <= maxNativeDrops
}

// isAmountZeroOrNegative checks if an amount is zero or negative.
func isAmountZeroOrNegative(amt tx.Amount) bool {
	return amt.IsZero() || amt.IsNegative()
}

// isAmountNegative checks if an amount is strictly negative.
func isAmountNegative(amt tx.Amount) bool {
	return amt.IsNegative()
}

// zeroAmount returns a zero amount matching the type/issue of the given amount.
func zeroAmount(amt tx.Amount) tx.Amount {
	if amt.IsNative() {
		return tx.NewXRPAmount(0)
	}
	if amt.IsMPT() {
		return newMPTAmountLike(amt, 0)
	}
	return tx.NewIssuedAmount(0, -100, amt.Currency, amt.Issuer)
}

// subtractAmounts subtracts b from a.
// a - b = result
func subtractAmounts(a, b tx.Amount) tx.Amount {
	result, err := a.Sub(b)
	if err != nil {
		return zeroAmount(a)
	}

	if result.IsNegative() {
		return zeroAmount(a)
	}

	return result
}

func newMPTAmountLike(amount tx.Amount, value int64) tx.Amount {
	id, err := mptutil.DecodeID(amount.MPTIssuanceID())
	if err != nil {
		return tx.Amount{}
	}
	return state.NewMPTAmountWithIssuanceID(
		value,
		state.EncodeAccountIDSafe(mptutil.Issuer(id)),
		mptutil.EncodeID(id),
	)
}
