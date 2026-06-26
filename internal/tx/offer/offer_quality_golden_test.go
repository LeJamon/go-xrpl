package offer

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

const goldenIssuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func fmtOfferAmt(a tx.Amount) string {
	if a.IsNative() {
		return fmt.Sprintf("XRP(%d)", a.Drops())
	}
	return fmt.Sprintf("IOU(m=%d,e=%d,neg=%v,zero=%v)", a.Mantissa(), a.Exponent(), a.IsNegative(), a.IsZero())
}

func safeOfferAmt(f func() tx.Amount) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("PANIC(%v)", r)
		}
	}()
	return fmtOfferAmt(f())
}

// offerGoldenRow exercises every offer mul/div round variant for one operand
// pair, in both native and IOU output modes and both rounding directions.
func offerGoldenRow(a, b tx.Amount) string {
	var s string
	for _, native := range []bool{false, true} {
		cur, iss := "USD", goldenIssuer
		if native {
			cur, iss = "", ""
		}
		for _, ru := range []bool{false, true} {
			s += fmt.Sprintf("|n=%v ru=%v D=%s DS=%s M=%s",
				native, ru,
				safeOfferAmt(func() tx.Amount { return offerDivRound(a, b, native, cur, iss, ru) }),
				safeOfferAmt(func() tx.Amount { return offerDivRoundStrict(a, b, native, cur, iss, ru) }),
				safeOfferAmt(func() tx.Amount { return offerMulRound(a, b, native, cur, iss, ru) }),
			)
		}
	}
	return s
}

func offerGoldenOperands() []tx.Amount {
	return []tx.Amount{
		tx.NewIssuedAmount(1000000000000000, 0, "USD", goldenIssuer),
		tx.NewIssuedAmount(3333333333333333, -2, "USD", goldenIssuer),
		tx.NewIssuedAmount(7000000000000000, -3, "USD", goldenIssuer),
		tx.NewIssuedAmount(-2500000000000000, -10, "USD", goldenIssuer),
		tx.NewXRPAmount(1000000),
		tx.NewXRPAmount(7),
		tx.NewXRPAmount(123456789),
	}
}

// TestOfferRoundGolden locks the byte-exact output of the offer-package
// native-aware mul/div round variants. These feed offer-crossing exchange-rate
// math, so the consolidation onto the shared state core must not drift.
func TestOfferRoundGolden(t *testing.T) {
	ops := offerGoldenOperands()
	pairs := [][2]int{
		{0, 1}, {1, 2}, {2, 3}, {3, 0}, {4, 5}, {5, 4}, {6, 4}, {0, 4}, {4, 0}, {2, 6},
	}
	want := offerRoundGoldens()
	if len(want) != 0 && len(want) != len(pairs) {
		t.Fatalf("golden count %d != pairs %d", len(want), len(pairs))
	}
	for i, p := range pairs {
		got := offerGoldenRow(ops[p[0]], ops[p[1]])
		if len(want) == 0 {
			t.Logf("GOLDEN[%d] = %q,", i, got)
			continue
		}
		if got != want[i] {
			t.Errorf("pair %v (%d):\n got=%s\nwant=%s", p, i, got, want[i])
		}
	}
	if len(want) == 0 {
		t.Fatal("goldens not yet captured")
	}
}

func offerRoundGoldens() []string {
	return []string{
		"|n=false ru=false D=IOU(m=3000000000000000,e=-14,neg=false,zero=false) DS=IOU(m=3000000000000000,e=-14,neg=false,zero=false) M=IOU(m=3333333333333333,e=13,neg=false,zero=false)|n=false ru=true D=IOU(m=3000000000000001,e=-14,neg=false,zero=false) DS=IOU(m=3000000000000001,e=-14,neg=false,zero=false) M=IOU(m=3333333333333333,e=13,neg=false,zero=false)|n=true ru=false D=XRP(30) DS=XRP(30) M=PANIC(Native currency amount out of range)|n=true ru=true D=XRP(30) DS=XRP(31) M=PANIC(Native currency amount out of range)",
		"|n=false ru=false D=IOU(m=4761904761904761,e=-15,neg=false,zero=false) DS=IOU(m=4761904761904761,e=-15,neg=false,zero=false) M=IOU(m=2333333333333333,e=11,neg=false,zero=false)|n=false ru=true D=IOU(m=4761904761904762,e=-15,neg=false,zero=false) DS=IOU(m=4761904761904762,e=-15,neg=false,zero=false) M=IOU(m=2333333333333334,e=11,neg=false,zero=false)|n=true ru=false D=XRP(4) DS=XRP(4) M=PANIC(Native currency amount out of range)|n=true ru=true D=XRP(5) DS=XRP(5) M=PANIC(Native currency amount out of range)",
		"|n=false ru=false D=IOU(m=-2800000000000000,e=-8,neg=true,zero=false) DS=IOU(m=-2800000000000000,e=-8,neg=true,zero=false) M=IOU(m=-1750000000000000,e=3,neg=true,zero=false)|n=false ru=true D=IOU(m=-2800000000000000,e=-8,neg=true,zero=false) DS=IOU(m=-2800000000000000,e=-8,neg=true,zero=false) M=IOU(m=-1750000000000000,e=3,neg=true,zero=false)|n=true ru=false D=XRP(-28000000) DS=XRP(-28000000) M=PANIC(Native currency amount out of range)|n=true ru=true D=XRP(-28000000) DS=XRP(-28000000) M=PANIC(Native currency amount out of range)",
		"|n=false ru=false D=IOU(m=-2500000000000000,e=-25,neg=true,zero=false) DS=IOU(m=-2500000000000000,e=-25,neg=true,zero=false) M=IOU(m=-2500000000000000,e=5,neg=true,zero=false)|n=false ru=true D=IOU(m=-2500000000000000,e=-25,neg=true,zero=false) DS=IOU(m=-2500000000000000,e=-25,neg=true,zero=false) M=IOU(m=-2500000000000000,e=5,neg=true,zero=false)|n=true ru=false D=XRP(0) DS=XRP(0) M=PANIC(Native currency amount out of range)|n=true ru=true D=XRP(0) DS=XRP(0) M=PANIC(Native currency amount out of range)",
		"|n=false ru=false D=IOU(m=1428571428571428,e=-10,neg=false,zero=false) DS=IOU(m=1428571428571428,e=-10,neg=false,zero=false) M=IOU(m=7000000000000000,e=-9,neg=false,zero=false)|n=false ru=true D=IOU(m=1428571428571429,e=-10,neg=false,zero=false) DS=IOU(m=1428571428571429,e=-10,neg=false,zero=false) M=IOU(m=7000000000000000,e=-9,neg=false,zero=false)|n=true ru=false D=XRP(142857) DS=XRP(142857) M=XRP(7000000)|n=true ru=true D=XRP(142858) DS=XRP(142858) M=XRP(7000000)",
		"|n=false ru=false D=IOU(m=7000000000000000,e=-21,neg=false,zero=false) DS=IOU(m=7000000000000000,e=-21,neg=false,zero=false) M=IOU(m=7000000000000000,e=-9,neg=false,zero=false)|n=false ru=true D=IOU(m=7000000000000000,e=-21,neg=false,zero=false) DS=IOU(m=7000000000000000,e=-21,neg=false,zero=false) M=IOU(m=7000000000000000,e=-9,neg=false,zero=false)|n=true ru=false D=XRP(0) DS=XRP(0) M=XRP(7000000)|n=true ru=true D=XRP(1) DS=XRP(1) M=XRP(7000000)",
		"|n=false ru=false D=IOU(m=1234567890000000,e=-13,neg=false,zero=false) DS=IOU(m=1234567890000000,e=-13,neg=false,zero=false) M=IOU(m=1234567890000000,e=-1,neg=false,zero=false)|n=false ru=true D=IOU(m=1234567890000000,e=-13,neg=false,zero=false) DS=IOU(m=1234567890000000,e=-13,neg=false,zero=false) M=IOU(m=1234567890000000,e=-1,neg=false,zero=false)|n=true ru=false D=XRP(123) DS=XRP(123) M=XRP(123456789000000)|n=true ru=true D=XRP(124) DS=XRP(124) M=XRP(123456789000001)",
		"|n=false ru=false D=IOU(m=1000000000000000,e=-6,neg=false,zero=false) DS=IOU(m=1000000000000000,e=-6,neg=false,zero=false) M=IOU(m=1000000000000000,e=6,neg=false,zero=false)|n=false ru=true D=IOU(m=1000000000000000,e=-6,neg=false,zero=false) DS=IOU(m=1000000000000000,e=-6,neg=false,zero=false) M=IOU(m=1000000000000000,e=6,neg=false,zero=false)|n=true ru=false D=XRP(1000000000) DS=XRP(1000000000) M=PANIC(Native currency amount out of range)|n=true ru=true D=XRP(1000000000) DS=XRP(1000000000) M=PANIC(Native currency amount out of range)",
		"|n=false ru=false D=IOU(m=1000000000000000,e=-24,neg=false,zero=false) DS=IOU(m=1000000000000000,e=-24,neg=false,zero=false) M=IOU(m=1000000000000000,e=6,neg=false,zero=false)|n=false ru=true D=IOU(m=1000000000000000,e=-24,neg=false,zero=false) DS=IOU(m=1000000000000000,e=-24,neg=false,zero=false) M=IOU(m=1000000000000000,e=6,neg=false,zero=false)|n=true ru=false D=XRP(0) DS=XRP(0) M=PANIC(Native currency amount out of range)|n=true ru=true D=XRP(1) DS=XRP(1) M=PANIC(Native currency amount out of range)",
		"|n=false ru=false D=IOU(m=5670000051597000,e=-11,neg=false,zero=false) DS=IOU(m=5670000051597000,e=-11,neg=false,zero=false) M=IOU(m=8641975230000000,e=5,neg=false,zero=false)|n=false ru=true D=IOU(m=5670000051597001,e=-11,neg=false,zero=false) DS=IOU(m=5670000051597001,e=-11,neg=false,zero=false) M=IOU(m=8641975230000000,e=5,neg=false,zero=false)|n=true ru=false D=XRP(56700) DS=XRP(56700) M=PANIC(Native currency amount out of range)|n=true ru=true D=XRP(56700) DS=XRP(56701) M=PANIC(Native currency amount out of range)",
	}
}
