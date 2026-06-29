package state

import (
	"fmt"
	"testing"
)

// goldenIssuer is an arbitrary valid issuer used only to give IOU results a
// stable currency/issuer; the rounding math never depends on its value.
const goldenIssuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func fmtRoundAmt(a Amount) string {
	if a.IsNative() {
		return fmt.Sprintf("XRP(%d)", a.Drops())
	}
	return fmt.Sprintf("IOU(m=%d,e=%d,neg=%v,zero=%v)", a.Mantissa(), a.Exponent(), a.IsNegative(), a.IsZero())
}

// safeAmt / safeInt capture both the value and any panic deterministically, so
// the golden also locks overflow/divide-by-zero panic boundaries.
func safeAmt(f func() Amount) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("PANIC(%v)", r)
		}
	}()
	return fmtRoundAmt(f())
}

func safeInt(f func() int64) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("PANIC(%v)", r)
		}
	}()
	return fmt.Sprintf("%d", f())
}

// roundGoldenRow exercises every mul/div round variant for one operand pair and
// both roundUp directions, returning a single deterministic signature string.
func roundGoldenRow(a, b Amount) string {
	const cur = "USD"
	var s string
	for _, ru := range []bool{false, true} {
		s += fmt.Sprintf("|ru=%v MS=%s M=%s D=%s DS=%s DN=%s MN=%s",
			ru,
			safeAmt(func() Amount { return MulRoundStrict(a, b, cur, goldenIssuer, ru) }),
			safeAmt(func() Amount { return MulRound(a, b, cur, goldenIssuer, ru) }),
			safeAmt(func() Amount { return DivRound(a, b, cur, goldenIssuer, ru) }),
			safeAmt(func() Amount { return DivRoundStrict(a, b, cur, goldenIssuer, ru) }),
			safeInt(func() int64 { return DivRoundNative(a, b, ru) }),
			safeInt(func() int64 { return MulRoundNative(a, b, ru) }),
		)
	}
	return s
}

func roundGoldenOperands() []Amount {
	return []Amount{
		NewIssuedAmountFromValue(1000000000000000, 0, "USD", goldenIssuer),    // 10^15 e0
		NewIssuedAmountFromValue(3333333333333333, -2, "USD", goldenIssuer),   // repeating
		NewIssuedAmountFromValue(7000000000000000, 5, "USD", goldenIssuer),    // large exp
		NewIssuedAmountFromValue(-2500000000000000, -10, "USD", goldenIssuer), // negative
		NewIssuedAmountFromValue(9999999999999999, 80, "USD", goldenIssuer),   // near max exp
		NewIssuedAmountFromValue(1000000000000001, -96, "USD", goldenIssuer),  // near min exp
		NewXRPAmountFromInt(1000000),                                          // 1 XRP
		NewXRPAmountFromInt(7),                                                // tiny native
		NewXRPAmountFromInt(-123456789),                                       // negative native
		NewXRPAmountFromInt(100000000000),                                     // large native
	}
}

// TestAmountRoundGolden locks the byte-exact output of every mul/div round
// variant across a grid of IOU/native, signed, and extreme-exponent operands.
// It guards the shared muldiv/canonicalize helpers against any behavioural
// drift — these feed consensus-relevant exchange-rate math.
func TestAmountRoundGolden(t *testing.T) {
	ops := roundGoldenOperands()
	pairs := [][2]int{
		{0, 1}, {1, 0}, {0, 2}, {2, 3}, {3, 1}, {4, 0}, {5, 0},
		{6, 7}, {7, 6}, {8, 6}, {9, 7}, {0, 6}, {6, 0}, {3, 8}, {2, 9},
	}
	want := amountRoundGoldens()
	if len(want) != 0 && len(want) != len(pairs) {
		t.Fatalf("golden count %d != pairs %d", len(want), len(pairs))
	}
	for i, p := range pairs {
		got := roundGoldenRow(ops[p[0]], ops[p[1]])
		if len(want) == 0 {
			t.Logf("GOLDEN[%d] = %q,", i, got)
			continue
		}
		if got != want[i] {
			t.Errorf("pair %v (%d):\n got=%s\nwant=%s", p, i, got, want[i])
		}
	}
	if len(want) == 0 {
		t.Fatal("goldens not yet captured; copy the GOLDEN lines above into amountRoundGoldens()")
	}
}

// amountRoundGoldens returns the captured byte-exact outputs, one signature
// string per operand pair. The IOU columns (MS/M/D/DS) match the pre-#894
// implementation exactly. The native columns (MN/DN) match it on every input
// reachable in a valid ledger; on operand pairs that cannot occur on chain —
// those whose native mul/div result would exceed cMaxNativeN (10^17 drops) —
// MulRoundNative/DivRoundNative panic ("Native currency amount out of range"),
// mirroring STAmount::canonicalize's range check rather than returning an
// out-of-range or int64-wrapped drops value. Native×native multiplies take
// rippled's mulRoundImpl fast path and panic ("Native value overflow") for a
// negative or oversized operand (pair [8 6]).
func amountRoundGoldens() []string {
	return []string{
		"|ru=false MS=IOU(m=3333333333333333,e=13,neg=false,zero=false) M=IOU(m=3333333333333333,e=13,neg=false,zero=false) D=IOU(m=3000000000000000,e=-14,neg=false,zero=false) DS=IOU(m=3000000000000000,e=-14,neg=false,zero=false) DN=30 MN=PANIC(Native currency amount out of range)|ru=true MS=IOU(m=3333333333333333,e=13,neg=false,zero=false) M=IOU(m=3333333333333333,e=13,neg=false,zero=false) D=IOU(m=3000000000000001,e=-14,neg=false,zero=false) DS=IOU(m=3000000000000001,e=-14,neg=false,zero=false) DN=30 MN=PANIC(Native currency amount out of range)",
		"|ru=false MS=IOU(m=3333333333333333,e=13,neg=false,zero=false) M=IOU(m=3333333333333333,e=13,neg=false,zero=false) D=IOU(m=3333333333333333,e=-17,neg=false,zero=false) DS=IOU(m=3333333333333333,e=-17,neg=false,zero=false) DN=0 MN=PANIC(Native currency amount out of range)|ru=true MS=IOU(m=3333333333333333,e=13,neg=false,zero=false) M=IOU(m=3333333333333333,e=13,neg=false,zero=false) D=IOU(m=3333333333333333,e=-17,neg=false,zero=false) DS=IOU(m=3333333333333333,e=-17,neg=false,zero=false) DN=1 MN=PANIC(Native currency amount out of range)",
		"|ru=false MS=IOU(m=7000000000000000,e=20,neg=false,zero=false) M=IOU(m=7000000000000000,e=20,neg=false,zero=false) D=IOU(m=1428571428571428,e=-21,neg=false,zero=false) DS=IOU(m=1428571428571428,e=-21,neg=false,zero=false) DN=0 MN=PANIC(Native currency amount out of range)|ru=true MS=IOU(m=7000000000000000,e=20,neg=false,zero=false) M=IOU(m=7000000000000000,e=20,neg=false,zero=false) D=IOU(m=1428571428571429,e=-21,neg=false,zero=false) DS=IOU(m=1428571428571429,e=-21,neg=false,zero=false) DN=1 MN=PANIC(Native currency amount out of range)",
		"|ru=false MS=IOU(m=-1750000000000000,e=11,neg=true,zero=false) M=IOU(m=-1750000000000000,e=11,neg=true,zero=false) D=IOU(m=-2800000000000000,e=0,neg=true,zero=false) DS=IOU(m=-2800000000000000,e=0,neg=true,zero=false) DN=-2800000000000001 MN=PANIC(Native currency amount out of range)|ru=true MS=IOU(m=-1750000000000000,e=11,neg=true,zero=false) M=IOU(m=-1750000000000000,e=11,neg=true,zero=false) D=IOU(m=-2800000000000000,e=0,neg=true,zero=false) DS=IOU(m=-2800000000000000,e=0,neg=true,zero=false) DN=-2800000000000000 MN=PANIC(Native currency amount out of range)",
		"|ru=false MS=IOU(m=-8333333333333333,e=3,neg=true,zero=false) M=IOU(m=-8333333333333333,e=3,neg=true,zero=false) D=IOU(m=-7500000000000001,e=-24,neg=true,zero=false) DS=IOU(m=-7500000000000001,e=-24,neg=true,zero=false) DN=0 MN=PANIC(Native currency amount out of range)|ru=true MS=IOU(m=-8333333333333332,e=3,neg=true,zero=false) M=IOU(m=-8333333333333332,e=3,neg=true,zero=false) D=IOU(m=-7500000000000000,e=-24,neg=true,zero=false) DS=IOU(m=-7500000000000000,e=-24,neg=true,zero=false) DN=0 MN=PANIC(Native currency amount out of range)",
		"|ru=false MS=PANIC(IOUAmount overflow) M=PANIC(IOUAmount overflow) D=IOU(m=9999999999999999,e=65,neg=false,zero=false) DS=IOU(m=9999999999999999,e=65,neg=false,zero=false) DN=PANIC(Native currency amount out of range) MN=PANIC(Native currency amount out of range)|ru=true MS=PANIC(IOUAmount overflow) M=PANIC(IOUAmount overflow) D=IOU(m=9999999999999999,e=65,neg=false,zero=false) DS=IOU(m=9999999999999999,e=65,neg=false,zero=false) DN=PANIC(Native currency amount out of range) MN=PANIC(Native currency amount out of range)",
		"|ru=false MS=IOU(m=1000000000000001,e=-81,neg=false,zero=false) M=IOU(m=1000000000000001,e=-81,neg=false,zero=false) D=IOU(m=0,e=-100,neg=false,zero=true) DS=IOU(m=0,e=-100,neg=false,zero=true) DN=0 MN=0|ru=true MS=IOU(m=1000000000000001,e=-81,neg=false,zero=false) M=IOU(m=1000000000000001,e=-81,neg=false,zero=false) D=IOU(m=1000000000000000,e=-96,neg=false,zero=false) DS=IOU(m=1000000000000000,e=-96,neg=false,zero=false) DN=1 MN=1",
		"|ru=false MS=IOU(m=7000000000000000,e=-9,neg=false,zero=false) M=IOU(m=7000000000000000,e=-9,neg=false,zero=false) D=IOU(m=1428571428571428,e=-10,neg=false,zero=false) DS=IOU(m=1428571428571428,e=-10,neg=false,zero=false) DN=142857 MN=7000000|ru=true MS=IOU(m=7000000000000000,e=-9,neg=false,zero=false) M=IOU(m=7000000000000000,e=-9,neg=false,zero=false) D=IOU(m=1428571428571429,e=-10,neg=false,zero=false) DS=IOU(m=1428571428571429,e=-10,neg=false,zero=false) DN=142858 MN=7000000",
		"|ru=false MS=IOU(m=7000000000000000,e=-9,neg=false,zero=false) M=IOU(m=7000000000000000,e=-9,neg=false,zero=false) D=IOU(m=7000000000000000,e=-21,neg=false,zero=false) DS=IOU(m=7000000000000000,e=-21,neg=false,zero=false) DN=0 MN=7000000|ru=true MS=IOU(m=7000000000000000,e=-9,neg=false,zero=false) M=IOU(m=7000000000000000,e=-9,neg=false,zero=false) D=IOU(m=7000000000000000,e=-21,neg=false,zero=false) DS=IOU(m=7000000000000000,e=-21,neg=false,zero=false) DN=1 MN=7000000",
		"|ru=false MS=IOU(m=-1234567890000000,e=-1,neg=true,zero=false) M=IOU(m=-1234567890000000,e=-1,neg=true,zero=false) D=IOU(m=-1234567890000000,e=-13,neg=true,zero=false) DS=IOU(m=-1234567890000000,e=-13,neg=true,zero=false) DN=-124 MN=PANIC(Native value overflow)|ru=true MS=IOU(m=-1234567890000000,e=-1,neg=true,zero=false) M=IOU(m=-1234567890000000,e=-1,neg=true,zero=false) D=IOU(m=-1234567890000000,e=-13,neg=true,zero=false) DS=IOU(m=-1234567890000000,e=-13,neg=true,zero=false) DN=-123 MN=PANIC(Native value overflow)",
		"|ru=false MS=IOU(m=7000000000000000,e=-4,neg=false,zero=false) M=IOU(m=7000000000000000,e=-4,neg=false,zero=false) D=IOU(m=1428571428571428,e=-5,neg=false,zero=false) DS=IOU(m=1428571428571428,e=-5,neg=false,zero=false) DN=14285714285 MN=700000000000|ru=true MS=IOU(m=7000000000000000,e=-4,neg=false,zero=false) M=IOU(m=7000000000000000,e=-4,neg=false,zero=false) D=IOU(m=1428571428571429,e=-5,neg=false,zero=false) DS=IOU(m=1428571428571429,e=-5,neg=false,zero=false) DN=14285714286 MN=700000000000",
		"|ru=false MS=IOU(m=1000000000000000,e=6,neg=false,zero=false) M=IOU(m=1000000000000000,e=6,neg=false,zero=false) D=IOU(m=1000000000000000,e=-6,neg=false,zero=false) DS=IOU(m=1000000000000000,e=-6,neg=false,zero=false) DN=1000000000 MN=PANIC(Native currency amount out of range)|ru=true MS=IOU(m=1000000000000000,e=6,neg=false,zero=false) M=IOU(m=1000000000000000,e=6,neg=false,zero=false) D=IOU(m=1000000000000000,e=-6,neg=false,zero=false) DS=IOU(m=1000000000000000,e=-6,neg=false,zero=false) DN=1000000000 MN=PANIC(Native currency amount out of range)",
		"|ru=false MS=IOU(m=1000000000000000,e=6,neg=false,zero=false) M=IOU(m=1000000000000000,e=6,neg=false,zero=false) D=IOU(m=1000000000000000,e=-24,neg=false,zero=false) DS=IOU(m=1000000000000000,e=-24,neg=false,zero=false) DN=0 MN=PANIC(Native currency amount out of range)|ru=true MS=IOU(m=1000000000000000,e=6,neg=false,zero=false) M=IOU(m=1000000000000000,e=6,neg=false,zero=false) D=IOU(m=1000000000000000,e=-24,neg=false,zero=false) DS=IOU(m=1000000000000000,e=-24,neg=false,zero=false) DN=1 MN=PANIC(Native currency amount out of range)",
		"|ru=false MS=IOU(m=3086419725000000,e=-2,neg=false,zero=false) M=IOU(m=3086419725000000,e=-2,neg=false,zero=false) D=IOU(m=2025000018427500,e=-18,neg=false,zero=false) DS=IOU(m=2025000018427500,e=-18,neg=false,zero=false) DN=0 MN=30864197250000|ru=true MS=IOU(m=3086419725000000,e=-2,neg=false,zero=false) M=IOU(m=3086419725000000,e=-2,neg=false,zero=false) D=IOU(m=2025000018427501,e=-18,neg=false,zero=false) DS=IOU(m=2025000018427501,e=-18,neg=false,zero=false) DN=1 MN=30864197250000",
		"|ru=false MS=IOU(m=7000000000000000,e=16,neg=false,zero=false) M=IOU(m=7000000000000000,e=16,neg=false,zero=false) D=IOU(m=7000000000000000,e=-6,neg=false,zero=false) DS=IOU(m=7000000000000000,e=-6,neg=false,zero=false) DN=7000000000 MN=PANIC(Native currency amount out of range)|ru=true MS=IOU(m=7000000000000000,e=16,neg=false,zero=false) M=IOU(m=7000000000000000,e=16,neg=false,zero=false) D=IOU(m=7000000000000000,e=-6,neg=false,zero=false) DS=IOU(m=7000000000000000,e=-6,neg=false,zero=false) DN=7000000000 MN=PANIC(Native currency amount out of range)",
	}
}
