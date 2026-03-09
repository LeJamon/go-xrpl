package state

import (
	"fmt"
	"math/big"
	"testing"
)

func TestXRPLNumberAddDebug(t *testing.T) {
	// Test: -1.0 + 0.03350000000000001
	// Expected with switchover: -0.9665000000333333 = {-9665000000333333, -16}
	// Expected without switchover: -0.966500000033334 = {-9665000000333340, -16}

	SetNumberSwitchover(true)
	defer SetNumberSwitchover(false)

	// 1. Test XRPLNumber.Add directly
	x := NewXRPLNumber(-1000000000000000, -15)
	y := NewXRPLNumber(3350000000000001, -17)
	result := x.Add(y)
	fmt.Printf("XRPLNumber.Add({-1000000000000000,-15}, {3350000000000001,-17}) = {%d, %d}\n", result.mantissa, result.exponent)

	// 2. Test addIOUValues
	a := NewIOUAmountValue(-1000000000000000, -15)
	b := NewIOUAmountValue(3350000000000001, -17)
	r := addIOUValues(a, b)
	fmt.Printf("addIOUValues({-1000000000000000,-15}, {3350000000000001,-17}) = {%d, %d}\n", r.mantissa, r.exponent)

	// 3. Test through Amount.Sub (1.0 - 0.0335...)
	amtA := NewIssuedAmountFromValue(1000000000000000, -15, "USD", "gateway")
	amtB := NewIssuedAmountFromValue(3350000000000001, -17, "USD", "gateway")
	result2, err := amtA.Sub(amtB)
	if err != nil {
		t.Fatalf("Error: %v", err)
	}
	iou2 := result2.IOU()
	fmt.Printf("Amount.Sub({1000000000000000,-15}, {3350000000000001,-17}) = {%d, %d} = %s\n", iou2.mantissa, iou2.exponent, result2.Value())

	// 4. Test: -1.0 + 0.0335... (as Amount.Add)
	amtC := NewIssuedAmountFromValue(-1000000000000000, -15, "USD", "gateway")
	result3, _ := amtC.Add(amtB)
	iou3 := result3.IOU()
	fmt.Printf("Amount.Add({-1000000000000000,-15}, {3350000000000001,-17}) = {%d, %d} = %s\n", iou3.mantissa, iou3.exponent, result3.Value())

	// 5. Check normalize with large mantissa
	n := NewXRPLNumber(334999999999999966, -19)
	fmt.Printf("NewXRPLNumber(334999999999999966, -19) = {%d, %d}\n", n.mantissa, n.exponent)

	// 6. MulRatio test: 3333333333333333e-17 * 1005/1000
	amtD := NewIssuedAmountFromValue(3333333333333333, -17, "USD", "gateway")
	mr := amtD.MulRatio(1005000000, 1000000000, true)
	iou4 := mr.IOU()
	fmt.Printf("MulRatio({3333333333333333,-17}, 1005000000, 1000000000, true) = {%d, %d} = %s\n", iou4.mantissa, iou4.exponent, mr.Value())

	// 7. What if we multiply differently? Test: 3333333333333333e-17 * 1005000000e-9
	na := NewXRPLNumber(3333333333333333, -17)
	nb := NewXRPLNumber(1005000000, -9)
	nmul := na.Mul(nb)
	fmt.Printf("XRPLNumber.Mul({3333333333333333,-17}, {1005000000,-9}) = {%d, %d}\n", nmul.mantissa, nmul.exponent)

	fmt.Printf("\nExpected with switchover: -0.9665000000333333 = {-9665000000333333, -16}\n")

	// 8. Detailed MulRatio trace
	fmt.Println("\n--- Detailed MulRatio trace ---")

	mant := int64(3333333333333333)
	num2 := uint64(1005000000)
	den2 := uint64(1000000000)

	bigMant := new(big.Int).SetInt64(mant)
	bigNum2 := new(big.Int).SetUint64(num2)
	bigDen2 := new(big.Int).SetUint64(den2)

	mul := new(big.Int).Mul(bigMant, bigNum2)
	fmt.Printf("mul = %s\n", mul.String())

	low := new(big.Int).Div(mul, bigDen2)
	rem := new(big.Int).Sub(mul, new(big.Int).Mul(low, bigDen2))
	fmt.Printf("low = %s, rem = %s\n", low.String(), rem.String())

	exponent := -17
	if rem.Sign() != 0 {
		roomToGrow := mulRatioFL64 - log10Ceil(low)
		fmt.Printf("log10Ceil(low) = %d, roomToGrow = %d\n", log10Ceil(low), roomToGrow)
		if roomToGrow > 0 {
			exponent -= roomToGrow
			scale := pow10Big(roomToGrow)
			low.Mul(low, scale)
			rem.Mul(rem, scale)
		}
		addRem := new(big.Int).Div(rem, bigDen2)
		low.Add(low, addRem)
		rem.Sub(rem, new(big.Int).Mul(addRem, bigDen2))
		fmt.Printf("after roomToGrow: low = %s, rem = %s, exp = %d\n", low.String(), rem.String(), exponent)
	}

	hasRem := rem.Sign() != 0
	mustShrink := log10Ceil(low) - mulRatioFL64
	fmt.Printf("mustShrink = %d, hasRem = %v\n", mustShrink, hasRem)
	if mustShrink > 0 {
		sav := new(big.Int).Set(low)
		exponent += mustShrink
		scale := pow10Big(mustShrink)
		low.Div(low, scale)
		if !hasRem {
			hasRem = new(big.Int).Sub(sav, new(big.Int).Mul(low, scale)).Sign() != 0
		}
		fmt.Printf("after shrink: low = %s, exp = %d, hasRem = %v\n", low.String(), exponent, hasRem)
	}

	resultMant := low.Int64()
	fmt.Printf("resultMant = %d, exp = %d\n", resultMant, exponent)
	resultAmt := NewIssuedAmountFromValue(resultMant, exponent, "USD", "gateway")
	fmt.Printf("after normalize: mantissa = %d, exp = %d\n", resultAmt.Mantissa(), resultAmt.Exponent())
	if hasRem {
		fmt.Printf("hasRem=true, roundUp=true, neg=false → mantissa + 1 = %d\n", resultAmt.Mantissa()+1)
	}
}
