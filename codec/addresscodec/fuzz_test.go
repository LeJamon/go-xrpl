package addresscodec

import (
	"bytes"
	"testing"
)

// FuzzBase58CheckDecode fuzzes Base58CheckDecode with arbitrary strings.
// On success, it verifies the round-trip: re-encoding the decoded bytes
// must produce the original input string.
func FuzzBase58CheckDecode(f *testing.F) {
	// Valid addresses and seeds
	f.Add("rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhmN")
	f.Add("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	f.Add("shKMVJjV52uudwfS7HzzaiwmZqVeP")
	f.Add("sEdTzRkEgPoxDG1mJ6WkSucHWnMkm1H")
	f.Add("r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59")
	// Edge cases
	f.Add("")
	f.Add("r")
	f.Add("rr")
	f.Add("rrr")
	f.Add("rrrr")
	// Invalid characters not in XRP base58 alphabet
	f.Add("O")
	f.Add("0")
	f.Add("I")
	f.Add("l")
	// Short strings
	f.Add("abc")
	f.Add("x")
	f.Add("xx")
	f.Add("xxx")
	f.Add("xxxx")
	// Bad checksum
	f.Add("3MNQE1Y")
	// X-addresses
	f.Add("X7AcgcsBL6XDcUb289X4mJ8djcdyKaB5hJDWMArnXr61cqZ")
	f.Add("T719a5UwUCnEs54UsxG9CJYYDhwmFCqkr7wxCcNcfZ6p5GZ")

	f.Fuzz(func(t *testing.T, input string) {
		decoded, err := Base58CheckDecode(input)
		if err != nil {
			return
		}

		// Round-trip: re-encode decoded bytes (which include the prefix)
		// with no additional prefix. This should reproduce the original string.
		reEncoded := Base58CheckEncode(decoded)
		if reEncoded != input {
			t.Errorf("round-trip failed: Base58CheckEncode(Base58CheckDecode(%q)) = %q", input, reEncoded)
		}
	})
}

// FuzzDecodeClassicAddress fuzzes DecodeClassicAddressToAccountID with arbitrary strings.
// On success, it verifies that EncodeAccountIDToClassicAddress round-trips to the same address.
func FuzzDecodeClassicAddress(f *testing.F) {
	// Valid classic addresses
	f.Add("rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhmN")
	f.Add("r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59")
	f.Add("rJKhsipKHooQbtS3v5Jro6N5Q7TMNPkoAs")
	// Edge cases
	f.Add("")
	f.Add("r")
	f.Add("rr")
	// Invalid: too short
	f.Add("rDTX")
	// Truncated valid address
	f.Add("rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhm")
	// Non-XRP alphabet characters
	f.Add("0DTXlQ7ZKZVKz33zJbHjgVShjsBnqMBhmN")
	// Completely invalid
	f.Add("davidschwartz")
	f.Add("yurt")

	f.Fuzz(func(t *testing.T, addr string) {
		_, accountID, err := DecodeClassicAddressToAccountID(addr)
		if err != nil {
			return
		}

		roundTripped, err := EncodeAccountIDToClassicAddress(accountID)
		if err != nil {
			t.Fatalf("EncodeAccountIDToClassicAddress failed on successfully decoded accountID: %v", err)
		}

		if roundTripped != addr {
			t.Errorf("round-trip failed: Encode(Decode(%q)) = %q", addr, roundTripped)
		}
	})
}

// FuzzDecodeSeed fuzzes DecodeSeed with arbitrary strings.
// On success, it verifies that EncodeSeed round-trips using the detected algorithm.
func FuzzDecodeSeed(f *testing.F) {
	// Valid secp256k1 seeds
	f.Add("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	f.Add("shPSkLzQNWfyXjZ7bbwgCky6twagA")
	f.Add("shKMVJjV52uudwfS7HzzaiwmZqVeP")
	f.Add("snMKnVku798EnBwUfxeSD8953sLYA")
	f.Add("sspUXGrmjQhq6mgc24jiRuevZiwKT")
	// Valid ed25519 seeds
	f.Add("sEdTzRkEgPoxDG1mJ6WkSucHWnMkm1H")
	f.Add("sEdTvLVDRVJsrUyBiCPTHDs46GUKQAr")
	// Bad checksum
	f.Add("snoPBrXtMeMyMHUVTgbuqAfg1SUTa")
	f.Add("sEdTzRkEgPoxDG1mJ6WkSucHWnMkm1D")
	// Edge cases
	f.Add("")
	f.Add("s")
	f.Add("sEd")
	f.Add("yurt")
	// Truncated valid seeds
	f.Add("snoPBrXtMeMyMHUVTgbuqAfg1SUT")
	f.Add("sspUXGrmjQhq6mgc24jiRuevZiwK")
	// Invalid alphabet characters
	f.Add("sspOXGrmjQhq6mgc24jiRuevZiwKT")
	f.Add("ssp/XGrmjQhq6mgc24jiRuevZiwKT")

	f.Fuzz(func(t *testing.T, seed string) {
		entropy, algoType, err := DecodeSeed(seed)
		if err != nil {
			return
		}

		reEncoded, err := EncodeSeed(entropy, algoType)
		if err != nil {
			t.Fatalf("EncodeSeed failed on successfully decoded seed: %v", err)
		}

		if reEncoded != seed {
			t.Errorf("round-trip failed: EncodeSeed(DecodeSeed(%q)) = %q", seed, reEncoded)
		}
	})
}

// FuzzDecodeXAddress fuzzes DecodeXAddressWithTagPresence with arbitrary strings.
// On success, it verifies that EncodeXAddress round-trips to the same x-address.
func FuzzDecodeXAddress(f *testing.F) {
	// Mainnet no tag
	f.Add("X7AcgcsBL6XDcUb289X4mJ8djcdyKaB5hJDWMArnXr61cqZ")
	// Mainnet with tag=22
	f.Add("X7AcgcsBL6XDcUb289X4mJ8djcdyKaGxLBw6rACm2heBxVn")
	// Testnet no tag
	f.Add("T719a5UwUCnEs54UsxG9CJYYDhwmFCqkr7wxCcNcfZ6p5GZ")
	// Testnet with tag=22
	f.Add("T719a5UwUCnEs54UsxG9CJYYDhwmFCvzHM39KcuJw6gp2gS")
	zero := uint32(0)
	zeroTagAddress, err := ClassicAddressToXAddress("r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59", &zero, Mainnet)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(zeroTagAddress)
	// Edge cases
	f.Add("")
	f.Add("X")
	f.Add("T")
	f.Add("invalid")
	// Truncated valid x-address
	f.Add("X7AcgcsBL6XDcUb289X4mJ8djcdyKaB5hJDWMArnXr61cq")

	f.Fuzz(func(t *testing.T, addr string) {
		accountID, tag, network, err := DecodeXAddressWithTagPresence(addr)
		if err != nil {
			return
		}

		reEncoded, err := EncodeXAddress(accountID, tag, network)
		if err != nil {
			t.Fatalf("EncodeXAddress failed on successfully decoded x-address: %v", err)
		}

		if reEncoded != addr {
			t.Errorf("round-trip failed: EncodeXAddress(DecodeXAddressWithTagPresence(%q)) = %q", addr, reEncoded)
		}
	})
}

// FuzzBase58RoundTrip fuzzes the Base58 encode/decode cycle with arbitrary byte slices.
// EncodeBase58 → DecodeBase58 should reproduce the original input.
func FuzzBase58RoundTrip(f *testing.F) {
	f.Add([]byte{0x01, 0x02, 0x03})
	f.Add([]byte{})
	f.Add([]byte{0x00, 0x00, 0x01})
	f.Add([]byte{0x00})
	f.Add([]byte{0x00, 0x00, 0x00})
	f.Add([]byte{0xff, 0xff, 0xff})
	f.Add([]byte{0x00, 0x00, 0x00, 0x00, 0x01})
	// A realistic payload (20 bytes like an accountID)
	f.Add([]byte{0x88, 0xa5, 0xa5, 0x7c, 0x82, 0x9f, 0x40, 0xf2, 0x5e, 0xa8,
		0x33, 0x85, 0xbb, 0xde, 0x6c, 0x3d, 0x8b, 0x4c, 0xa0, 0x82})

	f.Fuzz(func(t *testing.T, input []byte) {
		encoded := EncodeBase58(input)
		decoded := DecodeBase58(encoded)

		if !bytes.Equal(decoded, input) {
			t.Errorf("round-trip failed:\n  input:   %x\n  encoded: %s\n  decoded: %x", input, encoded, decoded)
		}
	})
}

// FuzzDecodeNodePublicKey fuzzes DecodeNodePublicKey with arbitrary strings.
// On success, it verifies that EncodeNodePublicKey round-trips to the same key.
func FuzzDecodeNodePublicKey(f *testing.F) {
	f.Add("n9MDGCfimuyCmKXUAMcR12rv39PE6PY5YfFpNs75ZjtY3UWt31td")
	f.Add("n9")
	f.Add("")
	f.Add("invalid")
	f.Add("rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhmN")
	f.Add("aKEt5wr2oXW5H55Z4m94ioKb1Drmj42UWoQDvFJZ5LaxPv126G9d")
	f.Add("n9MDGCfimuyCmKXUAMcR12rv39PE6PY5YfFpNs75ZjtY3UWt31t")
	f.Add("yurt")

	f.Fuzz(func(t *testing.T, key string) {
		decoded, err := DecodeNodePublicKey(key)
		if err != nil {
			return
		}

		reEncoded, err := EncodeNodePublicKey(decoded)
		if err != nil {
			t.Fatalf("EncodeNodePublicKey failed on successfully decoded key: %v", err)
		}

		if reEncoded != key {
			t.Errorf("round-trip failed: EncodeNodePublicKey(DecodeNodePublicKey(%q)) = %q", key, reEncoded)
		}
	})
}
