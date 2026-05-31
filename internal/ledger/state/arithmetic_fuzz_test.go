package state

// Differential fuzzer for XRPL amount/Number arithmetic (issue #684).
//
// The oracle is an independent, exact big.Rat re-derivation of rippled's
// documented canonicalization: round the exact mathematical result to a
// 16-significant-digit mantissa using round-half-to-even (banker's rounding).
// This needs no rippled runtime, is deterministic, and is reproducible in CI.
//
// Same-sign addition and multiplication are byte-identical to the oracle:
// rippled's 16-digit BCD Guard with its sticky bit is provably equivalent to
// rounding the exact result half-to-even (Number.cpp). Division is within one
// ULP (rippled truncates the quotient at a 10^17 scaling before rounding).
// Subtraction with cancellation is within a few ULP: rippled renormalizes lost
// precision only while the mantissa is strictly below minMantissa
// (Number.cpp:320), so a multi-digit guard residue collapses to a single
// rounding step when cancellation lands exactly on minMantissa. This covers the
// class of bug behind #594 (Number->int64 via float64 truncation) and #601
// (codec normalize truncating instead of rounding half-to-even).

import (
	"fmt"
	"math"
	"math/big"
	"testing"
)

// --- independent big.Rat round-half-to-even oracle -------------------------

var (
	orcTen    = big.NewInt(10)
	orc1e15   = big.NewInt(1_000_000_000_000_000)
	orc1e16   = big.NewInt(10_000_000_000_000_000)
	orcRelTol = new(big.Rat).SetFrac(big.NewInt(1), big.NewInt(10_000_000_000_000)) // 1e-13
)

// orcPow10 returns 10^n for n >= 0.
func orcPow10(n int) *big.Int {
	return new(big.Int).Exp(orcTen, big.NewInt(int64(n)), nil)
}

// orcDivHalfEven returns round-half-to-even of a/b for a >= 0, b > 0.
func orcDivHalfEven(a, b *big.Int) *big.Int {
	q := new(big.Int)
	rem := new(big.Int)
	q.DivMod(a, b, rem) // q = floor(a/b), 0 <= rem < b
	twice := new(big.Int).Lsh(rem, 1)
	switch cmp := twice.Cmp(b); {
	case cmp > 0:
		q.Add(q, big.NewInt(1))
	case cmp == 0 && q.Bit(0) == 1:
		q.Add(q, big.NewInt(1))
	}
	return q
}

// orcValueRat returns the exact rational value mantissa * 10^exponent.
func orcValueRat(mantissa int64, exponent int) *big.Rat {
	if mantissa == 0 {
		return new(big.Rat)
	}
	m := new(big.Int).SetInt64(mantissa)
	if exponent >= 0 {
		return new(big.Rat).SetFrac(new(big.Int).Mul(m, orcPow10(exponent)), big.NewInt(1))
	}
	return new(big.Rat).SetFrac(m, orcPow10(-exponent))
}

// orcCanonical rounds the exact rational r to XRPL canonical form: a signed
// 16-digit mantissa in [10^15, 10^16) with an exponent, round-half-to-even.
// Zero maps to the canonical Number zero.
func orcCanonical(r *big.Rat) (mantissa int64, exponent int) {
	if r.Sign() == 0 {
		return 0, xrplNumZeroExponent
	}
	sign := int64(1)
	num := new(big.Int).Set(r.Num())
	if num.Sign() < 0 {
		sign = -1
		num.Neg(num)
	}
	den := new(big.Int).Set(r.Denom()) // always positive
	exp := orcExp16(num, den)
	m := orcScaleRound(num, den, exp)
	if m.Cmp(orc1e16) >= 0 { // rounding carried into the next decade
		m.Div(m, orcTen)
		exp++
	}
	return m.Int64() * sign, exp
}

// orcExp16 returns the exponent e such that 1e15 <= (num/den)/10^e < 1e16,
// chosen from the exact value rather than from a rounded mantissa so it stays
// correct at powers-of-ten boundaries (num, den > 0).
func orcExp16(num, den *big.Int) int {
	exp := len(num.String()) - len(den.String()) - 15
	for orcValueCmp(num, den, orc1e15, exp) < 0 {
		exp--
	}
	for orcValueCmp(num, den, orc1e16, exp) >= 0 {
		exp++
	}
	return exp
}

// orcValueCmp returns the sign of num/den - coef*10^exp for num, den, coef > 0.
func orcValueCmp(num, den, coef *big.Int, exp int) int {
	if exp >= 0 {
		rhs := new(big.Int).Mul(coef, orcPow10(exp))
		rhs.Mul(rhs, den)
		return num.Cmp(rhs)
	}
	lhs := new(big.Int).Mul(num, orcPow10(-exp))
	rhs := new(big.Int).Mul(coef, den)
	return lhs.Cmp(rhs)
}

// orcScaleRound returns round-half-to-even of (num/den)/10^exp for num, den > 0.
func orcScaleRound(num, den *big.Int, exp int) *big.Int {
	if exp >= 0 {
		return orcDivHalfEven(num, new(big.Int).Mul(den, orcPow10(exp)))
	}
	return orcDivHalfEven(new(big.Int).Mul(num, orcPow10(-exp)), den)
}

// orcRoundRatToInt rounds the exact rational r to an integer under mode.
func orcRoundRatToInt(r *big.Rat, mode RoundingMode) *big.Int {
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom()) // positive
	switch mode {
	case RoundTowardsZero:
		return new(big.Int).Quo(num, den) // truncate toward zero
	case RoundDownward:
		return new(big.Int).Div(num, den) // floor (den > 0)
	case RoundUpward:
		q := new(big.Int)
		rem := new(big.Int)
		q.DivMod(num, den, rem)
		if rem.Sign() != 0 {
			q.Add(q, big.NewInt(1))
		}
		return q
	default: // RoundToNearest: half-to-even
		sign := int64(1)
		a := new(big.Int).Set(num)
		if a.Sign() < 0 {
			sign = -1
			a.Neg(a)
		}
		q := orcDivHalfEven(a, den)
		if sign < 0 {
			q.Neg(q)
		}
		return q
	}
}

// --- helpers ---------------------------------------------------------------

func orcExp(e int32, lo, hi int) int {
	span := int32(hi - lo + 1)
	return int(((e%span)+span)%span) + lo
}

func orcMkNum(mantissa int64, exponent int) XRPLNumber {
	if mantissa == math.MinInt64 {
		mantissa++
	}
	return NewXRPLNumber(mantissa, exponent)
}

func orcStr(n XRPLNumber) string { return fmt.Sprintf("{%d,%d}", n.mantissa, n.exponent) }

// orcRun reports whether fn panicked.
func orcRun(fn func()) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	fn()
	return
}

// orcWithin fails t unless got differs from the exact value want by at most
// maxUlps units in the last place, measured at the coarser of the oracle
// exponent we and got's own exponent (the two representations may straddle a
// decade).
func orcWithin(t *testing.T, label string, got XRPLNumber, want *big.Rat, we, maxUlps int) {
	t.Helper()
	scale := we
	if got.exponent > scale {
		scale = got.exponent
	}
	diff := new(big.Rat).Sub(orcValueRat(got.mantissa, got.exponent), want)
	diff.Abs(diff)
	ulp := new(big.Rat)
	if scale >= 0 {
		ulp.SetInt(orcPow10(scale))
	} else {
		ulp.SetFrac(big.NewInt(1), orcPow10(-scale))
	}
	bound := new(big.Rat).Mul(ulp, new(big.Rat).SetInt64(int64(maxUlps)))
	if diff.Cmp(bound) > 0 {
		t.Fatalf("%s: %s not within %d ULP of exact %s (diff %s, ulp 1e%d)",
			label, orcStr(got), maxUlps, want.FloatString(40), diff.FloatString(40), scale)
	}
}

// --- fuzz targets ----------------------------------------------------------

// FuzzXRPLNumberArithmetic checks Add / Sub / Mul / Div against the exact
// big.Rat oracle: same-sign add and mul byte-identical, div within one ULP, and
// cancellation-subtraction within a few ULP (rippled's guard limit).
func FuzzXRPLNumberArithmetic(f *testing.F) {
	for _, s := range []struct {
		mA int64
		eA int32
		mB int64
		eB int32
		op uint8
	}{
		{15, -1, 25, -1, 0}, // 1.5 + 2.5 (tie boundary)
		{5_000_000_000_000_000, 0, 5_000_000_000_000_000, 0, 1}, // exact cancellation
		{1, 0, 3, 0, 3}, // 1/3, non-terminating
		{9_999_999_999_999_999, 0, 9_999_999_999_999_999, 0, 2}, // max * max
		{1_234_567_890_123_456, 200, 9_876_543_210_987_654, -200, 2},
		{1_000_000_000_000_000, 0, 7, 0, 3}, // 1e15 / 7
		{-12_345_678_901_234_567, 5, 98_765_432_109_876, -3, 0},
		{7, 0, 11, 0, 3},
		{1_000_000_000_000_000, 0, 1_000_000_000_000_000, 0, 1}, // -> 0
		{3_141_592_653_589_793, -15, 2_718_281_828_459_045, -15, 2},
		{9_999_999_999_999_995, 0, 1, 0, 0}, // carry across the mantissa boundary
	} {
		f.Add(s.mA, s.eA, s.mB, s.eB, s.op)
	}

	f.Fuzz(func(t *testing.T, mA int64, eA int32, mB int64, eB int32, op uint8) {
		a := orcMkNum(mA, orcExp(eA, -2048, 2048))
		b := orcMkNum(mB, orcExp(eB, -2048, 2048))
		kind := op % 4

		ra := orcValueRat(a.mantissa, a.exponent)
		rb := orcValueRat(b.mantissa, b.exponent)
		want := new(big.Rat)
		switch kind {
		case 0:
			want.Add(ra, rb)
		case 1:
			want.Sub(ra, rb)
		case 2:
			want.Mul(ra, rb)
		case 3:
			if rb.Sign() == 0 {
				if !orcRun(func() { a.Div(b) }) {
					t.Fatalf("Div(%s, %s) by zero did not panic", orcStr(a), orcStr(b))
				}
				return
			}
			want.Quo(ra, rb)
		}

		// Multiplication and same-sign addition are byte-identical to the exact
		// 16-digit round-half-to-even. Division differs by at most one ULP
		// (rippled truncates at a 10^17 scaling before rounding). Subtraction with
		// cancellation can differ by a few ULP: rippled renormalizes lost precision
		// only while the mantissa is strictly below minMantissa (Number.cpp:320),
		// so when cancellation lands it exactly on minMantissa a multi-digit guard
		// residue collapses to a single rounding step (bounded by ~5 ULP).
		cancellation := false
		switch kind {
		case 0:
			cancellation = ra.Sign() != 0 && rb.Sign() != 0 && ra.Sign() != rb.Sign()
		case 1:
			cancellation = ra.Sign() != 0 && rb.Sign() != 0 && ra.Sign() == rb.Sign()
		}
		tolerant := kind == 3 || cancellation

		wm, we := orcCanonical(want)
		overflow := want.Sign() != 0 && we > xrplNumMaxExponent
		underflow := want.Sign() != 0 && we < xrplNumMinExponent

		var got XRPLNumber
		panicked := orcRun(func() {
			switch kind {
			case 0:
				got = a.Add(b)
			case 1:
				got = a.Sub(b)
			case 2:
				got = a.Mul(b)
			case 3:
				got = a.Div(b)
			}
		})

		label := fmt.Sprintf("op %d on %s,%s", kind, orcStr(a), orcStr(b))
		switch {
		case want.Sign() == 0:
			if panicked || !got.IsZero() {
				t.Fatalf("%s: expected exact zero, got %s (panic=%v)", label, orcStr(got), panicked)
			}
		case overflow:
			if !panicked {
				t.Fatalf("%s: expected overflow panic (oracle exp %d), got %s", label, we, orcStr(got))
			}
		case panicked:
			t.Fatalf("%s: unexpected panic (oracle {%d,%d})", label, wm, we)
		case underflow:
			if !got.IsZero() {
				t.Fatalf("%s: expected underflow to zero, got %s", label, orcStr(got))
			}
		case tolerant:
			maxUlps := 8 // cancellation guard quirk; ~5 ULP worst case
			if kind == 3 {
				maxUlps = 1
			}
			orcWithin(t, label, got, want, we, maxUlps)
		default:
			if got.mantissa != wm || got.exponent != we {
				t.Fatalf("%s = {%d,%d}, oracle {%d,%d}", label, got.mantissa, got.exponent, wm, we)
			}
		}
	})
}

// FuzzXRPLNumberProperties checks algebraic invariants that hold regardless of
// any reference: normalize matches the oracle and is idempotent; identities,
// inverses, and commutativity hold.
func FuzzXRPLNumberProperties(f *testing.F) {
	for _, s := range []struct {
		mA int64
		eA int32
		mB int64
		eB int32
	}{
		{1_234_567_890_123_456, 3, 9_876_543_210_987_654, -7},
		{9_999_999_999_999_999, 0, 1, 0},
		{-5_000_000_000_000_000, 10, 5_000_000_000_000_000, 10},
		{12_345_678_901_234_565, 0, 7, 0}, // >16 significant digits -> normalize rounds
		{1, -30, 1, 30},
		{0, 0, 42, 0},
	} {
		f.Add(s.mA, s.eA, s.mB, s.eB)
	}

	f.Fuzz(func(t *testing.T, mA int64, eA int32, mB int64, eB int32) {
		expA := orcExp(eA, -2048, 2048)
		expB := orcExp(eB, -2048, 2048)

		// normalize matches the independent oracle (the #601 class).
		orcCheckNormalize(t, mA, expA)
		orcCheckNormalize(t, mB, expB)

		a := orcMkNum(mA, expA)
		b := orcMkNum(mB, expB)
		zero := xrplNumberZero()
		one := NewXRPLNumberFromInt(1)

		if !NewXRPLNumber(a.mantissa, a.exponent).Equal(a) {
			t.Fatalf("normalize not idempotent for %s", orcStr(a))
		}
		if !a.Negate().Negate().Equal(a) {
			t.Fatalf("double negate changed %s", orcStr(a))
		}
		if !a.Add(zero).Equal(a) || !zero.Add(a).Equal(a) {
			t.Fatalf("additive identity failed for %s", orcStr(a))
		}
		if !a.Add(a.Negate()).IsZero() {
			t.Fatalf("additive inverse failed for %s", orcStr(a))
		}
		if !a.Mul(one).Equal(a) || !one.Mul(a).Equal(a) {
			t.Fatalf("multiplicative identity failed for %s", orcStr(a))
		}
		if !a.Div(one).Equal(a) {
			t.Fatalf("a/1 != a for %s", orcStr(a))
		}

		// commutativity (overflow must be symmetric).
		var ab, ba XRPLNumber
		pAB := orcRun(func() { ab = a.Add(b) })
		pBA := orcRun(func() { ba = b.Add(a) })
		if pAB != pBA {
			t.Fatalf("add commutativity panic asymmetry for %s,%s", orcStr(a), orcStr(b))
		}
		if !pAB && !ab.Equal(ba) {
			t.Fatalf("add not commutative: %s+%s=%s but %s+%s=%s", orcStr(a), orcStr(b), orcStr(ab), orcStr(b), orcStr(a), orcStr(ba))
		}
		var mab, mba XRPLNumber
		pMAB := orcRun(func() { mab = a.Mul(b) })
		pMBA := orcRun(func() { mba = b.Mul(a) })
		if pMAB != pMBA {
			t.Fatalf("mul commutativity panic asymmetry for %s,%s", orcStr(a), orcStr(b))
		}
		if !pMAB && !mab.Equal(mba) {
			t.Fatalf("mul not commutative for %s,%s", orcStr(a), orcStr(b))
		}
	})
}

// orcCheckNormalize asserts NewXRPLNumber(mantissa, exponent) matches the
// independent oracle, honouring the overflow-panic and underflow-to-zero rules.
func orcCheckNormalize(t *testing.T, mantissa int64, exponent int) {
	t.Helper()
	if mantissa == math.MinInt64 {
		mantissa++
	}
	r := orcValueRat(mantissa, exponent)
	wm, we := orcCanonical(r)
	overflow := we > xrplNumMaxExponent
	underflow := r.Sign() != 0 && we < xrplNumMinExponent

	var n XRPLNumber
	panicked := orcRun(func() { n = NewXRPLNumber(mantissa, exponent) })

	switch {
	case overflow:
		if !panicked {
			t.Fatalf("normalize(%d,%d): expected overflow panic (oracle exp %d)", mantissa, exponent, we)
		}
	case panicked:
		t.Fatalf("normalize(%d,%d): unexpected panic (oracle {%d,%d})", mantissa, exponent, wm, we)
	case underflow:
		if !n.IsZero() {
			t.Fatalf("normalize(%d,%d): expected underflow to zero, got %s", mantissa, exponent, orcStr(n))
		}
	default:
		if n.mantissa != wm || n.exponent != we {
			t.Fatalf("normalize(%d,%d) = {%d,%d}, oracle {%d,%d}", mantissa, exponent, n.mantissa, n.exponent, wm, we)
		}
	}
}

// FuzzXRPLNumberToInt64 checks ToInt64WithMode against the oracle for every
// rounding mode — the conversion that #594 got wrong via float64 truncation.
func FuzzXRPLNumberToInt64(f *testing.F) {
	for _, s := range []struct {
		mant int64
		e    int32
	}{
		{15, -1},  // 1.5 -> round-half-even -> 2
		{25, -1},  // 2.5 -> 2 (even)
		{35, -1},  // 3.5 -> 4 (even)
		{-15, -1}, // -1.5 -> -2
		{-25, -1}, // -2.5 -> -2
		{5, 0},    // exact integer
		{0, 0},    // zero
		{12_345, -2},
		{9_999_999_999_999_999, -16}, // ~0.9999999999999999
		{1, 0},
		{-1, 0},
		{7_500_000_000_000_000, -16}, // 0.75
	} {
		f.Add(s.mant, s.e)
	}

	f.Fuzz(func(t *testing.T, mant int64, e int32) {
		n := orcMkNum(mant, orcExp(e, -40, 2))
		r := orcValueRat(n.mantissa, n.exponent)
		for _, mode := range []RoundingMode{RoundToNearest, RoundTowardsZero, RoundDownward, RoundUpward} {
			want := orcRoundRatToInt(r, mode)
			if !want.IsInt64() {
				t.Fatalf("oracle for %s mode %d out of int64 range: %s", orcStr(n), mode, want.String())
			}
			got := n.ToInt64WithMode(mode)
			if got != want.Int64() {
				t.Fatalf("ToInt64WithMode(%s, mode %d) = %d, oracle %d", orcStr(n), mode, got, want.Int64())
			}
		}
	})
}

// FuzzXRPLNumberRoot2 checks the square root: r >= 0 and r*r is within a tight
// relative tolerance of the input.
func FuzzXRPLNumberRoot2(f *testing.F) {
	for _, s := range []struct {
		mant int64
		e    int32
	}{
		{4, 0},
		{2, 0},
		{9, 0},
		{1, 0},
		{0, 0},
		{15_241_578_750_190_521, 0}, // 123456789^2
		{2, -1},                     // 0.2
		{1_000_000_000_000_000, 0},
		{9_999_999_999_999_999, 80},
	} {
		f.Add(s.mant, s.e)
	}

	f.Fuzz(func(t *testing.T, mant int64, e int32) {
		if mant == math.MinInt64 {
			mant++
		}
		if mant < 0 {
			mant = -mant
		}
		n := NewXRPLNumber(mant, orcExp(e, -2000, 2000))
		r := n.root2()
		if r.mantissa < 0 {
			t.Fatalf("root2(%s) = %s is negative", orcStr(n), orcStr(r))
		}
		if n.IsZero() {
			if !r.IsZero() {
				t.Fatalf("root2(0) = %s, want 0", orcStr(r))
			}
			return
		}
		sq := r.Mul(r)
		ratN := orcValueRat(n.mantissa, n.exponent)
		diff := new(big.Rat).Sub(orcValueRat(sq.mantissa, sq.exponent), ratN)
		diff.Abs(diff)
		tol := new(big.Rat).Mul(ratN, orcRelTol)
		if diff.Cmp(tol) > 0 {
			t.Fatalf("root2(%s) = %s; square %s exceeds tolerance (diff %s, tol %s)",
				orcStr(n), orcStr(r), orcStr(sq), diff.FloatString(30), tol.FloatString(30))
		}
	})
}

// FuzzIOUArithmetic checks the IOU production entrypoints (addIOUValues and
// Amount.Mul) with the Number switchover enabled against the same oracle,
// clamped to IOUAmount's narrower [-96, 80] exponent range. This guards the
// path real ledger amounts flow through.
func FuzzIOUArithmetic(f *testing.F) {
	for _, s := range []struct {
		mA int64
		eA int32
		mB int64
		eB int32
		op uint8
	}{
		{1_234_567_890_123_456, 0, 9_876_543_210_987_654, 0, 0},
		{5_000_000_000_000_000, -10, 5_000_000_000_000_000, -10, 1},
		{-1_000_000_000_000_000, 5, 1_000_000_000_000_000, 5, 0}, // cancels to zero
		{3_333_333_333_333_333, -15, 3, 0, 1},
		{9_999_999_999_999_999, 20, 9_999_999_999_999_999, 20, 1}, // mul near IOU ceiling
	} {
		f.Add(s.mA, s.eA, s.mB, s.eB, s.op)
	}

	f.Fuzz(func(t *testing.T, mA int64, eA int32, mB int64, eB int32, op uint8) {
		prev := GetNumberSwitchover()
		defer SetNumberSwitchover(prev)
		SetNumberSwitchover(true)

		if mA == math.MinInt64 {
			mA++
		}
		if mB == math.MinInt64 {
			mB++
		}
		// Use same-sign operands for addition so the result is byte-identical to
		// the oracle; rippled's cancellation guard quirk is covered (with a ULP
		// tolerance) by FuzzXRPLNumberArithmetic.
		if op%2 == 0 {
			if mA < 0 {
				mA = -mA
			}
			if mB < 0 {
				mB = -mB
			}
		}
		expA := orcExp(eA, -40, 25)
		expB := orcExp(eB, -40, 25)
		a := NewIOUAmountValue(mA, expA)
		b := NewIOUAmountValue(mB, expB)

		ra := orcValueRat(a.mantissa, a.exponent)
		rb := orcValueRat(b.mantissa, b.exponent)
		want := new(big.Rat)
		add := op%2 == 0
		if add {
			want.Add(ra, rb)
		} else {
			want.Mul(ra, rb)
		}
		wm, we := orcCanonical(want)
		overflow := want.Sign() != 0 && we > MaxExponent
		underflow := want.Sign() != 0 && we < MinExponent

		var got IOUAmountValue
		panicked := orcRun(func() {
			if add {
				got = addIOUValues(a, b)
			} else {
				ca := NewIssuedAmountFromValue(mA, expA, "USD", "rIssuer")
				cb := NewIssuedAmountFromValue(mB, expB, "USD", "rIssuer")
				got = ca.Mul(cb, false).IOU()
			}
		})

		switch {
		case want.Sign() == 0: // exact cancellation -> canonical IOU zero
			if !got.IsZero() {
				t.Fatalf("IOU op add=%v: expected zero, got {%d,%d}", add, got.mantissa, got.exponent)
			}
		case overflow:
			if !panicked {
				t.Fatalf("IOU op add=%v on {%d,%d},{%d,%d}: expected overflow panic (oracle exp %d)",
					add, a.mantissa, a.exponent, b.mantissa, b.exponent, we)
			}
		case panicked:
			t.Fatalf("IOU op add=%v on {%d,%d},{%d,%d}: unexpected panic (oracle {%d,%d})",
				add, a.mantissa, a.exponent, b.mantissa, b.exponent, wm, we)
		case underflow:
			if !got.IsZero() {
				t.Fatalf("IOU op add=%v: expected underflow to zero, got {%d,%d}", add, got.mantissa, got.exponent)
			}
		default:
			if got.mantissa != wm || got.exponent != we {
				t.Fatalf("IOU op add=%v on {%d,%d},{%d,%d} = {%d,%d}, oracle {%d,%d}",
					add, a.mantissa, a.exponent, b.mantissa, b.exponent, got.mantissa, got.exponent, wm, we)
			}
		}
	})
}
