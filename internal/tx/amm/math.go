package amm

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/keylet"
)

// numberMath is the immutable Number environment for one transaction. All AMM
// arithmetic stays in XRPLNumber space until a ledger Amount boundary.
type numberMath struct {
	ctx state.NumberContext
}

func newNumberMath(ctx *tx.ApplyContext) numberMath {
	return numberMath{ctx: ctx.NumberContext()}
}

func (m numberMath) fromAmount(amount tx.Amount) state.XRPLNumber {
	return m.ctx.FromAmount(amount, state.RoundToNearest)
}

func (m numberMath) fromAmountRounded(amount tx.Amount, mode state.RoundingMode) state.XRPLNumber {
	return m.ctx.FromAmount(amount, mode)
}

func (m numberMath) int(value int64) state.XRPLNumber {
	return m.ctx.FromInt(value, state.RoundToNearest)
}

func (m numberMath) number(mantissa int64, exponent int, mode state.RoundingMode) state.XRPLNumber {
	return m.ctx.New(mantissa, exponent, mode)
}

func (m numberMath) toAmount(number state.XRPLNumber, prototype tx.Amount, mode state.RoundingMode) tx.Amount {
	return m.ctx.ToAmount(number, prototype, mode)
}

func (m numberMath) zero() state.XRPLNumber { return m.int(0) }
func (m numberMath) one() state.XRPLNumber  { return m.int(1) }

// calculateLPTokens calculates the initial LP token balance in Number space.
func (m numberMath) calculateLPTokens(amount1, amount2 tx.Amount, fixV1_3 bool) state.XRPLNumber {
	if amount1.IsZero() || amount2.IsZero() {
		return m.zero()
	}
	mode := state.RoundToNearest
	if fixV1_3 {
		mode = state.RoundDownward
	}
	product := m.fromAmountRounded(amount1, mode).MulRounded(m.fromAmountRounded(amount2, mode), mode)
	return product.Root2Rounded(mode)
}

// GenerateAMMLPTCurrency generates the LP token currency code from two asset currencies.
func GenerateAMMLPTCurrency(currency1, currency2 string) string {
	return GenerateAMMLPTCurrencyForAssets(tx.Asset{Currency: currency1}, tx.Asset{Currency: currency2})
}

func GenerateAMMLPTCurrencyForAssets(asset1, asset2 tx.Asset) string {
	minAsset, maxAsset := asset1, asset2
	if !assetLessEqual(asset1, asset2) {
		minAsset, maxAsset = asset2, asset1
	}
	minSeed := ammLPTSeed(minAsset)
	maxSeed := ammLPTSeed(maxAsset)
	hash := sha512half.Sum(minSeed, maxSeed)
	var lptCurrency [20]byte
	lptCurrency[0] = 0x03
	copy(lptCurrency[1:], hash[:19])
	return fmt.Sprintf("%X", lptCurrency)
}

func ammLPTSeed(asset tx.Asset) []byte {
	if asset.IsMPT() {
		id, _ := mptutil.DecodeID(asset.MPTIssuanceID)
		return id[:]
	}
	currency := keylet.CurrencyBytes(asset.Currency)
	return currency[:]
}

// power returns f^n using exponentiation by squaring.
func (m numberMath) power(f state.XRPLNumber, n int, mode state.RoundingMode) state.XRPLNumber {
	if n == 0 {
		return m.one()
	}
	if n == 1 {
		return f
	}
	r := m.power(f, n/2, mode)
	r = r.MulRounded(r, mode)
	if n%2 != 0 {
		r = r.MulRounded(f, mode)
	}
	return r
}

func (m numberMath) subFromOne(x state.XRPLNumber, mode state.RoundingMode) state.XRPLNumber {
	return m.one().AddRounded(x.Negate(), mode)
}

func (m numberMath) addToOne(x state.XRPLNumber, mode state.RoundingMode) state.XRPLNumber {
	return m.one().AddRounded(x, mode)
}

func (m numberMath) div(n, d state.XRPLNumber, mode state.RoundingMode) state.XRPLNumber {
	if n.IsZero() || d.IsZero() {
		return m.zero()
	}
	return n.DivRounded(d, mode)
}

func (m numberMath) divToInt64(n, d state.XRPLNumber) int64 {
	return m.div(n, d, state.RoundToNearest).ToInt64WithMode(state.RoundToNearest)
}

// stAmountDiv intentionally remains STAmount arithmetic: rippled uses divide
// rather than Number division for proportional equal deposit/withdraw fractions.
func (m numberMath) stAmountDiv(n, d tx.Amount) state.XRPLNumber {
	if n.IsZero() || d.IsZero() {
		return m.zero()
	}
	numberPrototype := zeroIOU()
	numerator := m.toAmount(m.fromAmount(n), numberPrototype, state.RoundToNearest)
	denominator := m.toAmount(m.fromAmount(d), numberPrototype, state.RoundToNearest)
	return m.fromAmount(numerator.Div(denominator, false))
}

func (m numberMath) solveQuadraticEq(a, b, c state.XRPLNumber, mode state.RoundingMode) state.XRPLNumber {
	two := m.int(2)
	four := m.int(4)
	bb := b.MulRounded(b, mode)
	fourAC := four.MulRounded(a, mode).MulRounded(c, mode)
	disc := bb.AddRounded(fourAC.Negate(), mode)
	if disc.Signum() < 0 {
		return m.zero()
	}
	sqrtDisc := disc.Root2Rounded(mode)
	numerator := b.Negate().AddRounded(sqrtDisc, mode)
	return m.div(numerator, two.MulRounded(a, mode), mode)
}

func (m numberMath) multiplyToAmount(
	amount, frac state.XRPLNumber,
	prototype tx.Amount,
	mode state.RoundingMode,
) tx.Amount {
	return m.toAmount(amount.MulRounded(frac, mode), prototype, mode)
}
