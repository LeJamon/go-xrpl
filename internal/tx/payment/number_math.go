package payment

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

type numberMath struct {
	ctx state.NumberContext
}

func numberMathForRules(rules *amendment.Rules) numberMath {
	return numberMath{ctx: tx.NumberContextForRules(rules)}
}

func legacyNumberMath() numberMath {
	return numberMath{ctx: state.NewNumberContext(state.MantissaScaleSmall)}
}

func (m numberMath) int(value int64) state.XRPLNumber {
	return m.ctx.Int(value)
}

func (m numberMath) number(mantissa int64, exponent int, mode state.RoundingMode) state.XRPLNumber {
	return m.ctx.Number(mantissa, exponent, mode)
}

func (m numberMath) fromAmount(amount tx.Amount, mode state.RoundingMode) state.XRPLNumber {
	return m.ctx.FromAmount(amount, mode)
}

func (m numberMath) toAmount(number state.XRPLNumber, prototype tx.Amount, mode state.RoundingMode) tx.Amount {
	return m.ctx.ToAmount(number, prototype, mode)
}

func (m numberMath) toAmountWithNativeRounding(
	number state.XRPLNumber,
	prototype tx.Amount,
	nativeMode, ambientMode state.RoundingMode,
) tx.Amount {
	return m.ctx.ToAmountWithNativeRounding(number, prototype, nativeMode, ambientMode)
}

func (m numberMath) zero() state.XRPLNumber { return m.int(0) }
func (m numberMath) one() state.XRPLNumber  { return m.int(1) }
