package types

// Differential fuzzer for STNumber canonicalization (issue #684).
//
// normalize() is checked against an independent big.Rat re-derivation of
// rippled's Number::normalize: round the discarded low-order digits
// half-to-even and clamp sub-normal results to canonical zero. This is exactly
// the class of bug behind #601 (truncating instead of rounding half-to-even and
// a missing underflow clamp).

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

var (
	orcTen  = big.NewInt(10)
	orc1e16 = new(big.Int).Add(maxMantissa, big.NewInt(1)) // 10^16
)

func orcPow10(n int) *big.Int {
	return new(big.Int).Exp(orcTen, big.NewInt(int64(n)), nil)
}

// orcDivHalfEven returns round-half-to-even of a/b for a >= 0, b > 0.
func orcDivHalfEven(a, b *big.Int) *big.Int {
	q := new(big.Int)
	rem := new(big.Int)
	q.DivMod(a, b, rem)
	twice := new(big.Int).Lsh(rem, 1)
	switch cmp := twice.Cmp(b); {
	case cmp > 0:
		q.Add(q, big.NewInt(1))
	case cmp == 0 && q.Bit(0) == 1:
		q.Add(q, big.NewInt(1))
	}
	return q
}

func orcExp(e int32, lo, hi int) int32 {
	span := int32(hi - lo + 1)
	return ((e%span)+span)%span + int32(lo)
}

// orcExp16 returns the exponent e such that 1e15 <= (num/den)/10^e < 1e16,
// chosen from the exact value (num, den > 0) so it is correct at boundaries.
func orcExp16(num, den *big.Int) int {
	exp := len(num.String()) - len(den.String()) - 15
	for orcValueCmp(num, den, minMantissa, exp) < 0 {
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

// orcNormalize is the independent oracle for normalize(): it canonicalizes the
// exact value mantissa*10^exponent to a 16-digit half-even mantissa, then
// applies rippled's overflow (error) and underflow (canonical zero) rules.
func orcNormalize(mantissa *big.Int, exponent int32) (mant *big.Int, exp int32, overflow bool) {
	if mantissa.Sign() == 0 {
		return big.NewInt(0), defaultZeroExp, false
	}
	sign := mantissa.Sign()
	abs := new(big.Int).Abs(mantissa)

	var num, den *big.Int
	if exponent >= 0 {
		num = new(big.Int).Mul(abs, orcPow10(int(exponent)))
		den = big.NewInt(1)
	} else {
		num = abs
		den = orcPow10(int(-exponent))
	}

	e := orcExp16(num, den)
	var m *big.Int
	if e >= 0 {
		m = orcDivHalfEven(num, new(big.Int).Mul(den, orcPow10(e)))
	} else {
		m = orcDivHalfEven(new(big.Int).Mul(num, orcPow10(-e)), den)
	}
	if m.Cmp(maxMantissa) > 0 { // rounding carried into the next decade
		m.Div(m, orcTen)
		e++
	}

	switch {
	case int64(e) > int64(maxExponent):
		return nil, 0, true
	case int64(e) < int64(minExponent):
		return big.NewInt(0), defaultZeroExp, false
	}
	if sign < 0 {
		m.Neg(m)
	}
	return m, int32(e), false
}

// FuzzNumberNormalize checks normalize() against the independent oracle for
// round-half-to-even, the underflow clamp, and the overflow error.
func FuzzNumberNormalize(f *testing.F) {
	for _, s := range []struct {
		mant string
		e    int32
	}{
		{"12345678901234575", 0},   // tie, odd -> round up
		{"12345678901234565", 0},   // tie, even -> stays
		{"12345678901234566", 0},   // above half
		{"12345678901234564", 0},   // below half
		{"99999999999999995", 0},   // round-up carries the exponent
		{"1234567890123456501", 0}, // multi-digit, above half
		{"1234567890123456500", 0}, // multi-digit, exactly half, even
		{"1234567890123457500", 0}, // multi-digit, exactly half, odd
		{"-12345678901234575", 0},  // sign preserved through rounding
		{"9999999999999999", 0},    // already canonical
		{"1000000000000000", 0},
		{"1", -32768},                // underflow at the exponent floor
		{"5000000000000000", -40000}, // exponent below minimum
		{"5", 40000},                 // scales up past the exponent ceiling
		{"3140000000000000", -15},
	} {
		f.Add(s.mant, s.e)
	}

	f.Fuzz(func(t *testing.T, mantStr string, eRaw int32) {
		if len(mantStr) == 0 || len(mantStr) > 60 {
			return
		}
		m, ok := new(big.Int).SetString(mantStr, 10)
		if !ok {
			return
		}
		e := orcExp(eRaw, -40000, 40000)

		gotM, gotE, gotErr := normalize(new(big.Int).Set(m), e)
		wantM, wantE, overflow := orcNormalize(m, e)

		if overflow {
			if gotErr != ErrNumberOverflow {
				t.Fatalf("normalize(%s, %d): expected ErrNumberOverflow, got ({%v,%d}, %v)", m, e, gotM, gotE, gotErr)
			}
			return
		}
		if gotErr != nil {
			t.Fatalf("normalize(%s, %d): unexpected error %v (oracle {%s,%d})", m, e, gotErr, wantM, wantE)
		}
		if gotM.Cmp(wantM) != 0 || gotE != wantE {
			t.Fatalf("normalize(%s, %d) = {%s, %d}, oracle {%s, %d}", m, e, gotM, gotE, wantM, wantE)
		}
	})
}

// FuzzNumberRoundTrip checks that the canonical serialized form is a fixed
// point of FromJSON∘ToJSON: the parse/normalize and render paths must agree.
func FuzzNumberRoundTrip(f *testing.F) {
	for _, s := range []string{
		"0", "3.14", "-3.14", "123", "-123",
		"1000000000000000", "12345678901234575",
		"1e-20", "1e-10", "-7.5", "0.0000001", "9999999999999999",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		n := &Number{}
		b1, err := n.FromJSON(s)
		if err != nil {
			return // not a valid Number string
		}
		if len(b1) != 12 {
			t.Fatalf("FromJSON(%q) returned %d bytes, want 12", s, len(b1))
		}

		p := serdes.NewBinaryParser(b1, definitions.Get())
		js, err := n.ToJSON(p)
		if err != nil {
			t.Fatalf("ToJSON after FromJSON(%q): %v", s, err)
		}
		str, ok := js.(string)
		if !ok {
			t.Fatalf("ToJSON returned %T, want string", js)
		}

		b2, err := n.FromJSON(str)
		if err != nil {
			t.Fatalf("re-encode FromJSON(%q rendered from %q): %v", str, s, err)
		}
		if !bytes.Equal(b1, b2) {
			t.Fatalf("round-trip not stable: %q -> %x -> %q -> %x", s, b1, str, b2)
		}
	})
}
