package handlers

import (
	"encoding/json"
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
	if got := standardDeviation.String(); got != "0.3027650354097492" {
		t.Fatalf("standard deviation = %s, want 0.3027650354097492", got)
	}
	if got := aggregatePriceMedian(prices).text(); got != "74.45" {
		t.Fatalf("median = %s, want 74.45", got)
	}

	trimmedMean, trimmedStandardDeviation := aggregatePriceStats(prices[2:8])
	if got := trimmedMean.text(); got != "74.45" {
		t.Fatalf("trimmed mean = %s, want 74.45", got)
	}
	if got := trimmedStandardDeviation.String(); got != "0.187082869338697" {
		t.Fatalf("trimmed standard deviation = %s, want 0.187082869338697", got)
	}
}
