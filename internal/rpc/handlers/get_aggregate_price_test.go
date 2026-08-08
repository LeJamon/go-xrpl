package handlers

import (
	"encoding/json"
	"math"
	"testing"
)

func TestAggregatePriceValidUIntCategories(t *testing.T) {
	valid := []struct {
		input string
		want  uint32
	}{
		{input: `0`, want: 0},
		{input: `-0`, want: 0},
		{input: `4294967295`, want: 4294967295},
		{input: `"42"`, want: 42},
		{input: `"+42"`, want: 42},
	}
	for _, test := range valid {
		got, err := parseUintParam(json.RawMessage(test.input))
		if err != nil {
			t.Fatalf("parseUintParam(%s): %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("parseUintParam(%s) = %d, want %d", test.input, got, test.want)
		}
	}

	for _, input := range []string{
		`1.0`, `1e0`, `-1`, `4294967296`, `""`, `"1.0"`, `"-0"`, `null`, `true`,
	} {
		if _, err := parseUintParam(json.RawMessage(input)); err == nil {
			t.Fatalf("parseUintParam(%s) succeeded", input)
		}
	}
}

func TestAggregatePriceSTAmountAndNumberStats(t *testing.T) {
	prices := make([]aggregatePricePoint, 0, 10)
	for value := int64(740); value < 750; value++ {
		prices = append(prices, aggregatePricePoint{price: newAggregatePriceAmount(value, -1)})
	}

	mean, standardDeviation := aggregatePriceStats(prices)
	if got := mean.text(); got != "74.45" {
		t.Fatalf("mean = %s, want 74.45", got)
	}
	if got := standardDeviation.String(); got != "0.3027650354097491666" {
		t.Fatalf("standard deviation = %s, want 0.3027650354097491666", got)
	}
	if got := aggregatePriceMedian(prices).text(); got != "74.45" {
		t.Fatalf("median = %s, want 74.45", got)
	}

	trimmedMean, trimmedStandardDeviation := aggregatePriceStats(prices[2:8])
	if got := trimmedMean.text(); got != "74.45" {
		t.Fatalf("trimmed mean = %s, want 74.45", got)
	}
	if got := trimmedStandardDeviation.String(); got != "0.1870828693386970693" {
		t.Fatalf("trimmed standard deviation = %s, want 0.1870828693386970693", got)
	}
}

func TestAggregatePriceUnsignedBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		price    uint64
		scale    int
		want     string
		wantSign int
	}{
		{name: "max int64", price: math.MaxInt64, want: "9223372036854776e3", wantSign: 1},
		{name: "max int64 plus one", price: uint64(math.MaxInt64) + 1, want: "9223372036854776e3", wantSign: 1},
		{name: "max uint64", price: math.MaxUint64, want: "1844674407370955e4", wantSign: 1},
		{name: "minimum STAmount exponent", price: math.MaxUint64, scale: 100, want: "1844674407370955e-96", wantSign: 1},
		{name: "below minimum STAmount exponent", price: math.MaxUint64, scale: 101, want: "0", wantSign: 0},
		{name: "max scale underflows STAmount", price: math.MaxUint64, scale: 255, want: "0", wantSign: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			amount := newAggregatePriceAmountUnsigned(test.price, -test.scale)
			if got := amount.text(); got != test.want {
				t.Fatalf("price = %s, want %s", got, test.want)
			}
			if got := amount.number.Signum(); got != test.wantSign {
				t.Fatalf("sign = %d, want %d", got, test.wantSign)
			}
		})
	}
}

func TestAggregatePriceUnsignedStats(t *testing.T) {
	prices := []aggregatePricePoint{
		{price: newAggregatePriceAmountUnsigned(math.MaxInt64, 0)},
		{price: newAggregatePriceAmountUnsigned(uint64(math.MaxInt64)+1, 0)},
		{price: newAggregatePriceAmountUnsigned(math.MaxUint64, 0)},
		{price: newAggregatePriceAmountUnsigned(math.MaxUint64, 0)},
	}

	mean, standardDeviation := aggregatePriceStats(prices)
	if got := mean.text(); got != "1383505805528216e4" {
		t.Fatalf("mean = %s, want 1383505805528216e4", got)
	}
	if got := standardDeviation.String(); got != "5325116328314170655" {
		t.Fatalf("standard deviation = %s, want 5325116328314170655", got)
	}
	if got := aggregatePriceMedian(prices).text(); got != "1383505805528217e4" {
		t.Fatalf("median = %s, want 1383505805528217e4", got)
	}

	trimmedMean, trimmedStandardDeviation := aggregatePriceStats(prices[1:3])
	if got := trimmedMean.text(); got != "1383505805528217e4" {
		t.Fatalf("trimmed mean = %s, want 1383505805528217e4", got)
	}
	if got := trimmedStandardDeviation.String(); got != "6521908912666389825" {
		t.Fatalf("trimmed standard deviation = %s, want 6521908912666389825", got)
	}
}
