package types

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

const benchIssuer = "rf1BiGeXwwQoi8Z2ueFYTEXSwuJYfV2Jpn"

var amountBenchCases = []struct {
	name  string
	value any
}{
	{"XRP", "123456789"},
	{"XRPFee", "10"},
	{"XRPBalance", "1000000000"},
	{"XRPMax", "100000000000000000"},
	{"XRPNegative", "-125000708"},
	{"IOU3", map[string]any{"value": "1234.5678", "currency": "USD", "issuer": benchIssuer}},
	{"IOUHex", map[string]any{
		"value":    "-0.0000000123",
		"currency": "0158415500000000C1F76FF6ECB0BAC600000000",
		"issuer":   benchIssuer,
	}},
	{"MPT", map[string]any{
		"value":           "9223372036854775807",
		"mpt_issuance_id": "00000002430427B80BD2D09D36B70B969E12801065F22308",
	}},
	{"MPTSmall", map[string]any{
		"value":           "100",
		"mpt_issuance_id": "00000002430427B80BD2D09D36B70B969E12801065F22308",
	}},
	{"MPTNegative", map[string]any{
		"value":           "-1000000",
		"mpt_issuance_id": "00000002430427B80BD2D09D36B70B969E12801065F22308",
	}},
}

func BenchmarkAmountFromJSON(b *testing.B) {
	for _, bc := range amountBenchCases {
		b.Run(bc.name, func(b *testing.B) {
			a := &Amount{}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := a.FromJSON(bc.value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAmountToJSON(b *testing.B) {
	for _, bc := range amountBenchCases {
		b.Run(bc.name, func(b *testing.B) {
			a := &Amount{}
			wire, err := a.FromJSON(bc.value)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := a.ToJSON(serdes.NewBinaryParser(wire, nil)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEncodeCurrencyCode(b *testing.B) {
	for _, code := range []string{"USD", "0158415500000000C1F76FF6ECB0BAC600000000"} {
		b.Run(code, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := encodeCurrencyCode(code); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecodeCurrencyCode(b *testing.B) {
	usd, err := encodeCurrencyCode("USD")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := decodeCurrencyCode(usd); err != nil {
			b.Fatal(err)
		}
	}
}
