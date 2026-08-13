package payment

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

func TestPaymentNumberMathUsesRulesScale(t *testing.T) {
	legacyRules := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	fixedRules := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureFixCleanup3_2_0,
	})

	require.Equal(t, state.MantissaScaleLargeLegacy, numberMathForRules(legacyRules).ctx.Scale())
	require.Equal(t, state.MantissaScaleLarge320, numberMathForRules(fixedRules).ctx.Scale())
	require.Equal(t, state.MantissaScaleSmall, numberMathForRules(amendment.EmptyRules()).ctx.Scale())
}

func TestSolveQuadraticEqRetainsRulesScale(t *testing.T) {
	small := legacyNumberMath()
	large := numberMath{ctx: state.NewNumberContext(state.MantissaScaleLarge, true)}

	coefficients := func(m numberMath) (state.XRPLNumber, state.XRPLNumber, state.XRPLNumber) {
		three := m.int(3)
		return m.int(2).Div(three), m.int(1).Div(three), m.int(-1).Div(three)
	}

	a, b, c := coefficients(small)
	smallRoot := solveQuadraticEq(small, a, b, c)
	a, b, c = coefficients(large)
	largeRoot := solveQuadraticEq(large, a, b, c)

	require.Equal(t, state.MantissaScaleSmall, smallRoot.MantissaScale())
	require.Equal(t, state.MantissaScaleLarge, largeRoot.MantissaScale())
	require.Equal(t, "0.5000000000000002", smallRoot.String())
	require.Equal(t, "0.5000000000000000002", largeRoot.String())
	require.False(t, smallRoot.Equal(largeRoot))
}

func TestAMMQualityFunctionKeepsNumberContext(t *testing.T) {
	m := numberMath{ctx: state.NewNumberContext(state.MantissaScaleLarge, true)}
	poolGets := state.NewXRPAmountFromInt(10_000_000)
	poolPays := tx.NewIssuedAmount(10_000_000_000_000_00, -11, "USD", "issuer")

	qf := newAMMQualityFunction(m, poolGets, poolPays, 0)
	require.NotNil(t, qf)
	require.Equal(t, state.MantissaScaleLarge, qf.math.ctx.Scale())
	require.Equal(t, state.MantissaScaleLarge, qf.m.MantissaScale())
	require.Equal(t, state.MantissaScaleLarge, qf.b.MantissaScale())
}
