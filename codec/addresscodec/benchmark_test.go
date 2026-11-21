package addresscodec

import "testing"

// nolint
func BenchmarkEncodeBase58(b *testing.B) {
	tt := []struct {
		description string
		input       []byte
	}{
		{
			description: "Benchmark XRP encode",
			input:       []byte("rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhmN"),
		},
	}

	for _, tc := range tt {
		b.Run(tc.description, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				EncodeBase58(tc.input)
			}
		})
	}
}

// nolint
func BenchmarkDecodeBase58(b *testing.B) {

	tt := []struct {
		description string
		input       string
	}{
		{
			description: "Benchmark XRP decode",
			input:       "s2Fku4vaPpFiqqXdAD3V5rYrSx5a9h9qvUJW3423akZSCeD",
		},
	}

	for _, tc := range tt {
		b.Run(tc.description, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				DecodeBase58(tc.input)
			}
		})
	}
}

// nolint
func BenchmarkEncodeClassicAddressFromPublicKeyHex(b *testing.B) {

	tt := []struct {
		description string
		input       string
		prefix      []byte
	}{
		{
			description: "Benchmark successful encode classic address",
			input:       "ED9434799226374926EDA3B54B1B461B4ABF7237962EAE18528FEA67595397FA32",
			prefix:      []byte{AccountAddressPrefix},
		},
		{
			description: "Benchmark unsuccessful encode classic address - invalid type prefix",
			input:       "ED9434799226374926EDA3B54B1B461B4ABF7237962EAE18528FEA67595397FA32",
			prefix:      []byte{0x00, 0x00},
		},
		{
			description: "Benchmark unsuccessful encode classic address - invalid public key",
			input:       "yurt",
			prefix:      []byte{AccountAddressPrefix},
		},
	}

	for _, tc := range tt {
		b.Run(tc.description, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				EncodeClassicAddressFromPublicKeyHex(tc.input)
			}
		})
	}

}

// nolint
func BenchmarkDecodeClassicAddressToAccountID(b *testing.B) {
	tt := []struct {
		description string
		input       string
	}{
		{
			description: "Benchmark decode classic address",
			input:       "rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhmN",
		},
	}

	for _, tc := range tt {
		b.Run(tc.description, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				DecodeClassicAddressToAccountID(tc.input)
			}
		})
	}
}

func BenchmarkEncodeAccountIDToClassicAddress(b *testing.B) {
	_, accountID, err := DecodeClassicAddressToAccountID("rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhmN")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := EncodeAccountIDToClassicAddress(accountID); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBase58CheckDecode(b *testing.B) {
	for _, in := range []string{
		"rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhmN",
		"n9MDGCfimuyCmKXUAMcR12rv39PE6PY5YfFpNs75ZjtY3UWt31td",
	} {
		b.Run(in[:1], func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Base58CheckDecode(in); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
