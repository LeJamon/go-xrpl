package payment

import (
	"fmt"
	"math/big"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

// EitherAmount holds either an XRP, IOU, or MPT amount, allowing
// unified handling in the flow algorithm regardless of currency type.
type EitherAmount struct {
	IsNative bool
	IsMPT    bool

	// XRP holds the amount in drops (only valid if IsNative is true)
	XRP int64

	// IOU holds the IOU amount (only valid if IsNative is false)
	IOU tx.Amount

	// MPT holds an integral MPT amount and its issuance ID.
	MPT   int64
	MPTID [24]byte
}

func NewXRPEitherAmount(drops int64) EitherAmount {
	return EitherAmount{
		IsNative: true,
		XRP:      drops,
	}
}

func NewIOUEitherAmount(amount tx.Amount) EitherAmount {
	return EitherAmount{
		IsNative: false,
		IOU:      amount,
	}
}

func NewMPTEitherAmount(value int64, issuanceID [24]byte) EitherAmount {
	return EitherAmount{
		IsMPT: true,
		MPT:   value,
		MPTID: issuanceID,
	}
}

func ZeroXRPEitherAmount() EitherAmount {
	return EitherAmount{
		IsNative: true,
		XRP:      0,
	}
}

func ZeroIOUEitherAmount(currency, issuer string) EitherAmount {
	return EitherAmount{
		IsNative: false,
		IOU:      tx.NewIssuedAmount(0, -100, currency, issuer),
	}
}

func ZeroMPTEitherAmount(issuanceID [24]byte) EitherAmount {
	return NewMPTEitherAmount(0, issuanceID)
}

func (e EitherAmount) IsZero() bool {
	if e.IsNative {
		return e.XRP == 0
	}
	if e.IsMPT {
		return e.MPT == 0
	}
	return e.IOU.IsZero()
}

func (e EitherAmount) IsNegative() bool {
	if e.IsNative {
		return e.XRP < 0
	}
	if e.IsMPT {
		return e.MPT < 0
	}
	return e.IOU.IsNegative()
}

// Add adds two EitherAmounts (must be same type - both XRP or both IOU)
func (e EitherAmount) Add(other EitherAmount) EitherAmount {
	return e.AddWithNumberContext(
		other,
		state.NewNumberContext(state.MantissaScaleSmall, false),
	)
}

func (e EitherAmount) AddWithNumberContext(
	other EitherAmount,
	numberContext state.NumberContext,
) EitherAmount {
	if e.IsNative {
		return NewXRPEitherAmount(e.XRP + other.XRP)
	}
	if e.IsMPT {
		return NewMPTEitherAmount(e.MPT+other.MPT, e.MPTID)
	}
	result, _ := e.IOU.AddWithNumberContext(other.IOU, numberContext, state.RoundToNearest)
	return NewIOUEitherAmount(result)
}

// Sub subtracts other from e (must be same type)
func (e EitherAmount) Sub(other EitherAmount) EitherAmount {
	return e.SubWithNumberContext(
		other,
		state.NewNumberContext(state.MantissaScaleSmall, false),
	)
}

func (e EitherAmount) SubWithNumberContext(
	other EitherAmount,
	numberContext state.NumberContext,
) EitherAmount {
	if e.IsNative {
		return NewXRPEitherAmount(e.XRP - other.XRP)
	}
	if e.IsMPT {
		return NewMPTEitherAmount(e.MPT-other.MPT, e.MPTID)
	}
	result, _ := e.IOU.SubWithNumberContext(other.IOU, numberContext, state.RoundToNearest)
	return NewIOUEitherAmount(result)
}

func (e EitherAmount) Compare(other EitherAmount) int {
	cmp, err := e.CompareChecked(other)
	if err != nil {
		panic(err)
	}
	return cmp
}

func (e EitherAmount) CompareChecked(other EitherAmount) (int, error) {
	if e.IsNative != other.IsNative || e.IsMPT != other.IsMPT {
		return 0, fmt.Errorf("temBAD_AMOUNT: cannot compare amounts with different assets")
	}
	if e.IsNative {
		if e.XRP < other.XRP {
			return -1, nil
		}
		if e.XRP > other.XRP {
			return 1, nil
		}
		return 0, nil
	}
	if e.IsMPT {
		if e.MPTID != other.MPTID {
			return 0, fmt.Errorf("temBAD_AMOUNT: cannot compare different MPT issuances")
		}
		if e.MPT < other.MPT {
			return -1, nil
		}
		if e.MPT > other.MPT {
			return 1, nil
		}
		return 0, nil
	}
	return e.IOU.CompareChecked(other.IOU)
}

func (e EitherAmount) Min(other EitherAmount) EitherAmount {
	if e.Compare(other) <= 0 {
		return e
	}
	return other
}

func (e EitherAmount) Max(other EitherAmount) EitherAmount {
	if e.Compare(other) >= 0 {
		return e
	}
	return other
}

func ToEitherAmount(amt tx.Amount) EitherAmount {
	if amt.IsNative() {
		return NewXRPEitherAmount(amt.Drops())
	}
	if amt.IsMPT() {
		var id [24]byte
		if decoded, ok := decodeMPTID(amt.MPTIssuanceID()); ok {
			id = decoded
		}
		value, _ := amt.MPTRaw()
		return NewMPTEitherAmount(value, id)
	}
	return NewIOUEitherAmount(amt)
}

func FromEitherAmount(e EitherAmount) tx.Amount {
	if e.IsNative {
		return tx.NewXRPAmount(e.XRP)
	}
	if e.IsMPT {
		return newMPTAmount(e.MPT, e.MPTID)
	}
	return e.IOU
}

func MulRatio(amt EitherAmount, num, den uint32, roundUp bool) EitherAmount {
	return MulRatioWithNumberContext(
		amt,
		num,
		den,
		roundUp,
		state.NewNumberContext(state.MantissaScaleSmall, false),
	)
}

func MulRatioWithNumberContext(
	amt EitherAmount,
	num, den uint32,
	roundUp bool,
	numberContext state.NumberContext,
) EitherAmount {
	if den == 0 {
		panic("division by zero")
	}

	if amt.IsNative {
		xrpAmt := tx.NewXRPAmount(amt.XRP)
		result := xrpAmt.MulRatio(num, den, roundUp)
		return NewXRPEitherAmount(result.Drops())
	}
	if amt.IsMPT {
		return NewMPTEitherAmount(mptMulRatio(amt.MPT, num, den, roundUp), amt.MPTID)
	}

	return NewIOUEitherAmount(
		amt.IOU.MulRatioWithNumberContext(num, den, roundUp, numberContext),
	)
}

func mptMulRatio(amount int64, num, den uint32, roundUp bool) int64 {
	if den == 0 {
		panic("division by zero")
	}
	if amount == 0 {
		return amount
	}

	numerator := new(big.Int).Mul(big.NewInt(amount), new(big.Int).SetUint64(uint64(num)))
	denominator := new(big.Int).SetUint64(uint64(den))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if remainder.Sign() != 0 {
		switch {
		case amount > 0 && roundUp:
			quotient.Add(quotient, big.NewInt(1))
		case amount < 0 && !roundUp:
			quotient.Sub(quotient, big.NewInt(1))
		}
	}
	if !quotient.IsInt64() {
		panic("MPT mulRatio overflow")
	}
	return quotient.Int64()
}

func toNumberAmount(amt EitherAmount) tx.Amount {
	if amt.IsNative {
		return tx.NewIssuedAmount(amt.XRP, 0, "", "")
	}
	if amt.IsMPT {
		return tx.NewIssuedAmount(amt.MPT, 0, "", "")
	}
	value := amt.IOU.IOU()
	return tx.NewIssuedAmount(value.Mantissa(), value.Exponent(), "", "")
}
