package addresscodec

import (
	"testing"

	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/stretchr/testify/require"
)

// TestSeedFromPassphrase tests seed generation from passphrases using rippled test vectors.
// These test vectors are from the rippled reference implementation.
func TestSeedFromPassphrase(t *testing.T) {
	testcases := []struct {
		name         string
		passphrase   string
		expectedSeed string
	}{
		{
			name:         "masterpassphrase - genesis account seed",
			passphrase:   "masterpassphrase",
			expectedSeed: "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		},
		{
			name:         "Non-Random Passphrase",
			passphrase:   "Non-Random Passphrase",
			expectedSeed: "snMKnVku798EnBwUfxeSD8953sLYA",
		},
		{
			name:         "cookies excitement hand public - BIP39 style passphrase",
			passphrase:   "cookies excitement hand public",
			expectedSeed: "sspUXGrmjQhq6mgc24jiRuevZiwKT",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate seed bytes from passphrase using SHA512-Half (first 16 bytes)
			seedHash := common.Sha512Half([]byte(tc.passphrase))
			seedBytes := seedHash[:16]

			// Encode the seed using secp256k1 algorithm
			encodedSeed, err := EncodeSeed(seedBytes, secp256k1.SECP256K1())
			require.NoError(t, err, "EncodeSeed should not return an error")
			require.Equal(t, tc.expectedSeed, encodedSeed, "Encoded seed should match expected value")
		})
	}
}

// TestSeedDecode tests decoding of base58 encoded seeds.
func TestSeedDecode(t *testing.T) {
	testcases := []struct {
		name         string
		seed         string
		expectError  bool
		errorMessage string
	}{
		{
			name:        "valid seed - masterpassphrase",
			seed:        "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
			expectError: false,
		},
		{
			name:        "valid seed - Non-Random Passphrase",
			seed:        "snMKnVku798EnBwUfxeSD8953sLYA",
			expectError: false,
		},
		{
			name:        "valid seed - cookies excitement hand public",
			seed:        "sspUXGrmjQhq6mgc24jiRuevZiwKT",
			expectError: false,
		},
		{
			name:         "invalid seed - empty string",
			seed:         "",
			expectError:  true,
			errorMessage: ErrInvalidSeed.Error(),
		},
		{
			name:         "invalid seed - too short (missing last char)",
			seed:         "sspUXGrmjQhq6mgc24jiRuevZiwK",
			expectError:  true,
			errorMessage: ErrInvalidSeed.Error(),
		},
		{
			name:         "invalid seed - too long (extra char)",
			seed:         "sspUXGrmjQhq6mgc24jiRuevZiwKTT",
			expectError:  true,
			errorMessage: ErrInvalidSeed.Error(),
		},
		{
			name:         "invalid seed - invalid character O (not in XRP alphabet)",
			seed:         "sspOXGrmjQhq6mgc24jiRuevZiwKT",
			expectError:  true,
			errorMessage: ErrInvalidSeed.Error(),
		},
		{
			name:         "invalid seed - invalid character / (not in XRP alphabet)",
			seed:         "ssp/XGrmjQhq6mgc24jiRuevZiwKT",
			expectError:  true,
			errorMessage: ErrInvalidSeed.Error(),
		},
		{
			name:         "invalid seed - invalid checksum",
			seed:         "snoPBrXtMeMyMHUVTgbuqAfg1SUTa",
			expectError:  true,
			errorMessage: ErrInvalidSeed.Error(),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := DecodeSeed(tc.seed)

			if tc.expectError {
				require.Error(t, err, "DecodeSeed should return an error for invalid seed")
				require.EqualError(t, err, tc.errorMessage, "Error message should match")
			} else {
				require.NoError(t, err, "DecodeSeed should not return an error for valid seed")
			}
		})
	}
}

// TestSeedRoundTrip tests that encoding and decoding seeds is reversible.
func TestSeedRoundTrip(t *testing.T) {
	testcases := []struct {
		name       string
		passphrase string
	}{
		{
			name:       "masterpassphrase",
			passphrase: "masterpassphrase",
		},
		{
			name:       "Non-Random Passphrase",
			passphrase: "Non-Random Passphrase",
		},
		{
			name:       "cookies excitement hand public",
			passphrase: "cookies excitement hand public",
		},
		{
			name:       "random test passphrase",
			passphrase: "this is a test passphrase for roundtrip",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate seed bytes from passphrase
			seedHash := common.Sha512Half([]byte(tc.passphrase))
			originalSeedBytes := seedHash[:16]

			// Encode the seed
			encodedSeed, err := EncodeSeed(originalSeedBytes, secp256k1.SECP256K1())
			require.NoError(t, err, "EncodeSeed should not return an error")

			// Decode the seed
			decodedSeedBytes, algo, err := DecodeSeed(encodedSeed)
			require.NoError(t, err, "DecodeSeed should not return an error")
			require.NotNil(t, algo, "Algorithm should not be nil")

			// Verify the seed bytes match
			require.Equal(t, originalSeedBytes, decodedSeedBytes, "Decoded seed should match original")
		})
	}
}

// TestSeedPrefixDetection tests that the correct algorithm is detected from encoded seeds.
func TestSeedPrefixDetection(t *testing.T) {
	testcases := []struct {
		name             string
		seed             string
		expectedAlgoType string
	}{
		{
			name:             "secp256k1 seed - starts with 's'",
			seed:             "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
			expectedAlgoType: "secp256k1",
		},
		{
			name:             "ed25519 seed - starts with 'sEd'",
			seed:             "sEdTzRkEgPoxDG1mJ6WkSucHWnMkm1H",
			expectedAlgoType: "ed25519",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			_, algo, err := DecodeSeed(tc.seed)
			require.NoError(t, err, "DecodeSeed should not return an error")
			require.NotNil(t, algo, "Algorithm should not be nil")

			// Check algorithm type by comparing with expected algorithm instances
			if tc.expectedAlgoType == "ed25519" {
				// ED25519 algorithm should match
				_, expectedAlgo := ed25519.ED25519(), ed25519.ED25519()
				require.Equal(t, expectedAlgo, algo, "Algorithm should be ED25519")
			} else {
				// SECP256K1 algorithm should match
				_, expectedAlgo := secp256k1.SECP256K1(), secp256k1.SECP256K1()
				require.Equal(t, expectedAlgo, algo, "Algorithm should be SECP256K1")
			}
		})
	}
}
