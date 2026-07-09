package definitions

import (
	"fmt"
	"testing"
)

// nolint
func BenchmarkTypeCode(b *testing.B) {

	tt := []struct {
		input string
	}{
		{
			input: "Validation",
		},
		{
			input: "yurt",
		},
	}

	for _, test := range tt {
		b.Run(fmt.Sprintf("input_name_%v", test.input), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				definitions.TypeCode(test.input)
			}
		})
	}
}

// nolint
func BenchmarkFieldHeaderByName(b *testing.B) {
	tt := []struct {
		input string
	}{
		{
			input: "Generic",
		},
		{
			input: "yurt",
		},
	}

	for _, test := range tt {
		b.Run(fmt.Sprintf("input_name_%v", test.input), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				definitions.FieldHeaderByName(test.input)
			}
		})
	}
}

// func BenchmarkFieldNameByHeader(b *testing.B) {
// 	tt := []struct {
// 		input FieldHeader
// 	}{
// 		{
// 			input: FieldHeader{
// 				TypeCode:  []byte{1},
// 				FieldCode: []byte{1},
// 			},
// 		},
// 		{
// 			input: FieldHeader{
// 				TypeCode: []byte() 0000000000111,
// 				FieldCode: 00000000000000111,
// 			},
// 		},
// 	}

// 	for _, test := range tt {
// 		b.Run(fmt.Sprintf("input_name_%v", test.input), func(b *testing.B) {
// 			for i := 0; i < b.N; i++ {
// 				definitions.FieldNameByHeader(test.input)
// 			}
// 		})
// 	}
// }

// nolint
func BenchmarkFieldInstanceByName(b *testing.B) {
	tt := []struct {
		input string
	}{
		{
			input: "Generic",
		},
		{
			input: "yurt",
		},
	}

	for _, test := range tt {
		b.Run(fmt.Sprintf("input_name_%v", test.input), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				definitions.FieldInstanceByName(test.input)
			}
		})
	}
}

// nolint
func BenchmarkTransactionTypeCode(b *testing.B) {
	tt := []struct {
		input string
	}{
		{
			input: "Payment",
		},
		{
			input: "yurt",
		},
	}

	for _, test := range tt {
		b.Run(fmt.Sprintf("input_name_%v", test.input), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				definitions.TransactionTypeCode(test.input)
			}
		})
	}
}

// nolint
func BenchmarkTransactionTypeName(b *testing.B) {
	tt := []struct {
		input int32
	}{
		{
			input: 1,
		},
		{
			input: 999999999,
		},
	}

	for _, test := range tt {
		b.Run(fmt.Sprintf("input_code_%v", test.input), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				definitions.TransactionTypeName(test.input)
			}
		})
	}
}

// nolint
func BenchmarkTransactionResultName(b *testing.B) {
	tt := []struct {
		input int32
	}{
		{
			input: 100,
		},
		{
			input: 999999999,
		},
	}

	for _, test := range tt {
		b.Run(fmt.Sprintf("input_code_%v", test.input), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				definitions.TransactionResultName(test.input)
			}
		})
	}
}

// nolint
func BenchmarkTransactionResultCode(b *testing.B) {
	tt := []struct {
		input string
	}{
		{
			input: "tesSUCCESS",
		},
		{
			input: "yurt",
		},
	}

	for _, test := range tt {
		b.Run(fmt.Sprintf("input_name_%v", test.input), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				definitions.TransactionResultCode(test.input)
			}
		})
	}
}

// nolint
func BenchmarkLedgerEntryTypeCode(b *testing.B) {
	tt := []struct {
		input string
	}{
		{
			input: "AccountRoot",
		},
		{
			input: "yurt",
		},
	}

	for _, test := range tt {
		b.Run(fmt.Sprintf("input_name_%v", test.input), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				definitions.LedgerEntryTypeCode(test.input)
			}
		})
	}
}

// nolint
func BenchmarkLedgerEntryTypeName(b *testing.B) {
	tt := []struct {
		input int32
	}{
		{
			input: 100,
		},
		{
			input: 999999999,
		},
	}

	for _, test := range tt {
		b.Run(fmt.Sprintf("input_code_%v", test.input), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				definitions.LedgerEntryTypeName(test.input)
			}
		})
	}
}
