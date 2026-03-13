package addresscodec

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Rippled Seed_test.cpp Test Vectors
// These test vectors are extracted from the rippled reference implementation
// to ensure compatibility with the official XRPL protocol.
// =============================================================================

// TestRippledSeedEncodingVectors tests seed generation from passphrases using
// exact test vectors from rippled's Seed_test.cpp.
func TestRippledSeedEncodingVectors(t *testing.T) {
	testcases := []struct {
		name         string
		passphrase   string
		expectedSeed string
	}{
		{
			name:         "masterpassphrase - genesis account seed (rippled Seed_test.cpp)",
			passphrase:   "masterpassphrase",
			expectedSeed: "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		},
		{
			name:         "Non-Random Passphrase (rippled Seed_test.cpp)",
			passphrase:   "Non-Random Passphrase",
			expectedSeed: "snMKnVku798EnBwUfxeSD8953sLYA",
		},
		{
			name:         "cookies excitement hand public - BIP39 style (rippled Seed_test.cpp)",
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
			require.Equal(t, tc.expectedSeed, encodedSeed, "Encoded seed should match rippled test vector")
		})
	}
}

// TestRippledInvalidSeedVectors tests that invalid seeds are properly rejected
// using test vectors from rippled's Seed_test.cpp.
func TestRippledInvalidSeedVectors(t *testing.T) {
	testcases := []struct {
		name        string
		seed        string
		expectError bool
		description string
	}{
		{
			name:        "empty string should fail",
			seed:        "",
			expectError: true,
			description: "Empty string is not a valid seed",
		},
		{
			name:        "too short - missing last char (rippled Seed_test.cpp)",
			seed:        "sspUXGrmjQhq6mgc24jiRuevZiwK",
			expectError: true,
			description: "Seed is too short by one character",
		},
		{
			name:        "too long - extra char (rippled Seed_test.cpp)",
			seed:        "sspUXGrmjQhq6mgc24jiRuevZiwKTT",
			expectError: true,
			description: "Seed has an extra character",
		},
		{
			name:        "invalid char O - not in XRP base58 alphabet (rippled Seed_test.cpp)",
			seed:        "sspOXGrmjQhq6mgc24jiRuevZiwKT",
			expectError: true,
			description: "Character 'O' is not in the XRP Ledger base58 alphabet",
		},
		{
			name:        "invalid char / - not in XRP base58 alphabet (rippled Seed_test.cpp)",
			seed:        "ssp/XGrmjQhq6mgc24jiRuevZiwKT",
			expectError: true,
			description: "Character '/' is not in the XRP Ledger base58 alphabet",
		},
		{
			name:        "valid seed should succeed",
			seed:        "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
			expectError: false,
			description: "Valid masterpassphrase seed",
		},
		{
			name:        "invalid checksum should fail",
			seed:        "snoPBrXtMeMyMHUVTgbuqAfg1SUTa",
			expectError: true,
			description: "Last character changed causes checksum failure",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := DecodeSeed(tc.seed)

			if tc.expectError {
				require.Error(t, err, "DecodeSeed should return an error for: %s", tc.description)
			} else {
				require.NoError(t, err, "DecodeSeed should not return an error for: %s", tc.description)
			}
		})
	}
}

// TestRippledSecp256k1KeyDerivation tests the complete key derivation chain for
// secp256k1 using rippled test vectors from "masterpassphrase".
//
// From rippled Seed_test.cpp:
// - Account Address: rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh
// - Account Public:  aBQG8RQAzjs1eTKFEAQXr2gS4utcDiEC9wmi7pfUPTi27VCahwgw
// - Account Secret:  p9JfM6HHi64m6mvB6v5k7G2b1cXzGmYiCNJf6GHPKvFTWdeRVjh
// - Node Public:     n94a1u4jAz288pZLtw6yFWVbi89YamiC6JBXPVUj5zmExe5fTVg9
// - Node Private:    pnen77YEeUd4fFKG7iycBWcwKpTaeFRkW2WFostaATy1DSupwXe
// - Node ID:         7E59C17D50F5959C7B158FEC95C8F815BF653DC8
//
// Note: DeriveKeypair derives ACCOUNT keys, not NODE/ROOT keys.
func TestRippledSecp256k1KeyDerivation(t *testing.T) {
	// Test vectors from rippled Seed_test.cpp
	expected := struct {
		seed           string
		accountAddress string
		accountPublic  string
		accountSecret  string
		nodePublic     string
		nodePrivate    string
		nodeID         string
	}{
		seed:           "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		accountAddress: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		accountPublic:  "aBQG8RQAzjs1eTKFEAQXr2gS4utcDiEC9wmi7pfUPTi27VCahwgw",
		accountSecret:  "p9JfM6HHi64m6mvB6v5k7G2b1cXzGmYiCNJf6GHPKvFTWdeRVjh",
		nodePublic:     "n94a1u4jAz288pZLtw6yFWVbi89YamiC6JBXPVUj5zmExe5fTVg9",
		nodePrivate:    "pnen77YEeUd4fFKG7iycBWcwKpTaeFRkW2WFostaATy1DSupwXe",
		nodeID:         "7E59C17D50F5959C7B158FEC95C8F815BF653DC8",
	}

	// Generate seed bytes from masterpassphrase
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := seedHash[:16]

	t.Run("seed encoding", func(t *testing.T) {
		encodedSeed, err := EncodeSeed(seedBytes, secp256k1.SECP256K1())
		require.NoError(t, err)
		require.Equal(t, expected.seed, encodedSeed, "Seed encoding should match rippled test vector")
	})

	t.Run("account keypair derivation", func(t *testing.T) {
		// Make a copy since DeriveKeypair may modify the input
		seedCopy := make([]byte, len(seedBytes))
		copy(seedCopy, seedBytes)

		privKeyHex, pubKeyHex, err := secp256k1.SECP256K1().DeriveKeypair(seedCopy, false)
		require.NoError(t, err)

		// Verify private key format (secp256k1 has 00 prefix)
		require.True(t, strings.HasPrefix(privKeyHex, "00"),
			"secp256k1 private key should have 00 prefix")

		// Verify account address derivation
		accountAddress, err := EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
		require.NoError(t, err)
		require.Equal(t, expected.accountAddress, accountAddress,
			"Account address should match rippled test vector")

		// Verify account public key encoding
		pubKeyBytes, err := hex.DecodeString(pubKeyHex)
		require.NoError(t, err)
		accountPublic, err := EncodeAccountPublicKey(pubKeyBytes)
		require.NoError(t, err)
		require.Equal(t, expected.accountPublic, accountPublic,
			"Account public key should match rippled test vector")

		// Verify account secret key encoding
		// Account secret uses prefix 0x22 for 32-byte private key
		privKeyBytesWithPrefix, err := hex.DecodeString(privKeyHex)
		require.NoError(t, err)
		// Remove the 00 prefix from the private key
		privKeyBytes := privKeyBytesWithPrefix[1:]
		require.Len(t, privKeyBytes, PrivateKeyLength, "Private key should be 32 bytes")

		accountSecret := Base58CheckEncode(privKeyBytes, AccountSecretKeyPrefix)
		require.Equal(t, expected.accountSecret, accountSecret,
			"Account secret key should match rippled test vector")
	})

	// Note: Node public and node private keys require root/generator key derivation
	// which is different from account key derivation. The current DeriveKeypair
	// derives account keys (with the additional derivation step), not node keys.
	// Therefore, we skip node key tests for secp256k1 as they require separate
	// implementation of root key derivation.
}

// TestRippledED25519KeyDerivation tests the complete key derivation chain for
// ED25519 using rippled test vectors from "masterpassphrase".
//
// From rippled Seed_test.cpp:
// - Account Address: rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf
// - Account Public:  aKGheSBjmCsKJVuLNKRAKpZXT6wpk2FCuEZAXJupXgdAxX5THCqR
// - Account Secret:  pwDQjwEhbUBmPuEjFpEG75bFhv2obkCB7NxQsfFxM7xGHBMVPu9
// - Node Public:     nHUeeJCSY2dM71oxM8Cgjouf5ekTuev2mwDpc374aLMxzDLXNmjf
// - Node Private:    paKv46LztLqK3GaKz1rG2nQGN6M4JLyRtxFBYFTw4wAVHtGys36
// - Node ID:         AA066C988C712815CC37AF71472B7CBBBD4E2A0A
//
// Note: For ED25519, node and account keys are the same (different encoding prefixes).
func TestRippledED25519KeyDerivation(t *testing.T) {
	// Test vectors from rippled Seed_test.cpp
	expected := struct {
		accountAddress string
		accountPublic  string
		accountSecret  string
		nodePublic     string
		nodePrivate    string
		nodeID         string
	}{
		accountAddress: "rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf",
		accountPublic:  "aKGheSBjmCsKJVuLNKRAKpZXT6wpk2FCuEZAXJupXgdAxX5THCqR",
		accountSecret:  "pwDQjwEhbUBmPuEjFpEG75bFhv2obkCB7NxQsfFxM7xGHBMVPu9",
		nodePublic:     "nHUeeJCSY2dM71oxM8Cgjouf5ekTuev2mwDpc374aLMxzDLXNmjf",
		nodePrivate:    "paKv46LztLqK3GaKz1rG2nQGN6M4JLyRtxFBYFTw4wAVHtGys36",
		nodeID:         "AA066C988C712815CC37AF71472B7CBBBD4E2A0A",
	}

	// Generate seed bytes from masterpassphrase
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := seedHash[:16]

	// Derive ED25519 keypair
	privKeyHex, pubKeyHex, err := ed25519.ED25519().DeriveKeypair(seedBytes, false)
	require.NoError(t, err)

	t.Run("public key format", func(t *testing.T) {
		require.True(t, strings.HasPrefix(pubKeyHex, "ED"),
			"ED25519 public key should have ED prefix")
	})

	t.Run("private key format", func(t *testing.T) {
		require.True(t, strings.HasPrefix(privKeyHex, "ED"),
			"ED25519 private key should have ED prefix")
	})

	t.Run("account address derivation", func(t *testing.T) {
		accountAddress, err := EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
		require.NoError(t, err)
		require.Equal(t, expected.accountAddress, accountAddress,
			"Account address should match rippled test vector")
	})

	t.Run("account public key encoding", func(t *testing.T) {
		pubKeyBytes, err := hex.DecodeString(pubKeyHex)
		require.NoError(t, err)
		accountPublic, err := EncodeAccountPublicKey(pubKeyBytes)
		require.NoError(t, err)
		require.Equal(t, expected.accountPublic, accountPublic,
			"Account public key should match rippled test vector")
	})

	t.Run("node public key encoding", func(t *testing.T) {
		pubKeyBytes, err := hex.DecodeString(pubKeyHex)
		require.NoError(t, err)
		nodePublic, err := EncodeNodePublicKey(pubKeyBytes)
		require.NoError(t, err)
		require.Equal(t, expected.nodePublic, nodePublic,
			"Node public key should match rippled test vector")
	})

	t.Run("node ID derivation", func(t *testing.T) {
		pubKeyBytes, err := hex.DecodeString(pubKeyHex)
		require.NoError(t, err)
		nodeIDBytes := Sha256RipeMD160(pubKeyBytes)
		nodeID := strings.ToUpper(hex.EncodeToString(nodeIDBytes))
		require.Equal(t, expected.nodeID, nodeID,
			"Node ID should match rippled test vector")
	})

	t.Run("account secret key encoding", func(t *testing.T) {
		// ED25519 private key has ED prefix; we need to remove it for encoding
		privKeyBytes, err := hex.DecodeString(privKeyHex)
		require.NoError(t, err)
		// Remove the ED prefix (first byte)
		privKeyBytesNoPrefix := privKeyBytes[1:]
		require.Len(t, privKeyBytesNoPrefix, PrivateKeyLength, "Private key should be 32 bytes")

		accountSecret := Base58CheckEncode(privKeyBytesNoPrefix, AccountSecretKeyPrefix)
		require.Equal(t, expected.accountSecret, accountSecret,
			"Account secret key should match rippled test vector")
	})

	t.Run("node private key encoding", func(t *testing.T) {
		// ED25519 private key has ED prefix; we need to remove it for encoding
		privKeyBytes, err := hex.DecodeString(privKeyHex)
		require.NoError(t, err)
		// Remove the ED prefix (first byte)
		privKeyBytesNoPrefix := privKeyBytes[1:]
		require.Len(t, privKeyBytesNoPrefix, PrivateKeyLength, "Private key should be 32 bytes")

		nodePrivate := Base58CheckEncode(privKeyBytesNoPrefix, NodePrivateKeyPrefix)
		require.Equal(t, expected.nodePrivate, nodePrivate,
			"Node private key should match rippled test vector")
	})
}

// TestRippledSeedRoundTrip tests that all seed test vectors can be encoded and
// decoded without data loss.
func TestRippledSeedRoundTrip(t *testing.T) {
	testcases := []struct {
		name       string
		passphrase string
	}{
		{name: "masterpassphrase", passphrase: "masterpassphrase"},
		{name: "Non-Random Passphrase", passphrase: "Non-Random Passphrase"},
		{name: "cookies excitement hand public", passphrase: "cookies excitement hand public"},
	}

	for _, tc := range testcases {
		t.Run(tc.name+" secp256k1", func(t *testing.T) {
			// Generate original seed bytes
			seedHash := common.Sha512Half([]byte(tc.passphrase))
			originalSeedBytes := seedHash[:16]

			// Encode
			encodedSeed, err := EncodeSeed(originalSeedBytes, secp256k1.SECP256K1())
			require.NoError(t, err)

			// Decode
			decodedSeedBytes, algo, err := DecodeSeed(encodedSeed)
			require.NoError(t, err)
			require.Equal(t, secp256k1.SECP256K1(), algo)
			require.Equal(t, originalSeedBytes, decodedSeedBytes,
				"Decoded seed should match original")
		})

		t.Run(tc.name+" ed25519", func(t *testing.T) {
			// Generate original seed bytes
			seedHash := common.Sha512Half([]byte(tc.passphrase))
			originalSeedBytes := seedHash[:16]

			// Encode
			encodedSeed, err := EncodeSeed(originalSeedBytes, ed25519.ED25519())
			require.NoError(t, err)

			// Decode
			decodedSeedBytes, algo, err := DecodeSeed(encodedSeed)
			require.NoError(t, err)
			require.Equal(t, ed25519.ED25519(), algo)
			require.Equal(t, originalSeedBytes, decodedSeedBytes,
				"Decoded seed should match original")
		})
	}
}

// TestRippledAddressValidation tests that addresses derived from rippled test
// vectors pass validation.
func TestRippledAddressValidation(t *testing.T) {
	testcases := []struct {
		name    string
		address string
		valid   bool
	}{
		{
			name:    "secp256k1 masterpassphrase address",
			address: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			valid:   true,
		},
		{
			name:    "ed25519 masterpassphrase address",
			address: "rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf",
			valid:   true,
		},
		{
			name:    "invalid address - wrong checksum",
			address: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTi",
			valid:   false,
		},
		{
			name:    "invalid address - invalid character O",
			address: "rOOOOJAWyB4rj91VRWn96DkukG4bwdtyTh",
			valid:   false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			isValid := IsValidClassicAddress(tc.address)
			require.Equal(t, tc.valid, isValid,
				"Address validation should match expected result")
		})
	}
}

// TestRippledPublicKeyEncoding tests the encoding of public keys with different
// prefixes matches rippled test vectors.
func TestRippledPublicKeyEncoding(t *testing.T) {
	// Generate keys from masterpassphrase for testing
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := seedHash[:16]

	t.Run("secp256k1 public key encoding", func(t *testing.T) {
		seedCopy := make([]byte, len(seedBytes))
		copy(seedCopy, seedBytes)

		_, pubKeyHex, err := secp256k1.SECP256K1().DeriveKeypair(seedCopy, false)
		require.NoError(t, err)

		pubKeyBytes, err := hex.DecodeString(pubKeyHex)
		require.NoError(t, err)

		// Account public key (prefix 0x23, starts with 'a')
		accountPubKey, err := EncodeAccountPublicKey(pubKeyBytes)
		require.NoError(t, err)
		require.Equal(t, "aBQG8RQAzjs1eTKFEAQXr2gS4utcDiEC9wmi7pfUPTi27VCahwgw", accountPubKey)
		require.True(t, strings.HasPrefix(accountPubKey, "a"),
			"Account public key should start with 'a'")

		// Node public key (prefix 0x1C, starts with 'n')
		nodePubKey, err := EncodeNodePublicKey(pubKeyBytes)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(nodePubKey, "n"),
			"Node public key should start with 'n'")
	})

	t.Run("ed25519 public key encoding", func(t *testing.T) {
		_, pubKeyHex, err := ed25519.ED25519().DeriveKeypair(seedBytes, false)
		require.NoError(t, err)

		pubKeyBytes, err := hex.DecodeString(pubKeyHex)
		require.NoError(t, err)

		// Account public key
		accountPubKey, err := EncodeAccountPublicKey(pubKeyBytes)
		require.NoError(t, err)
		require.Equal(t, "aKGheSBjmCsKJVuLNKRAKpZXT6wpk2FCuEZAXJupXgdAxX5THCqR", accountPubKey)

		// Node public key
		nodePubKey, err := EncodeNodePublicKey(pubKeyBytes)
		require.NoError(t, err)
		require.Equal(t, "nHUeeJCSY2dM71oxM8Cgjouf5ekTuev2mwDpc374aLMxzDLXNmjf", nodePubKey)
	})
}

// TestRippledPrivateKeyEncoding tests the encoding of private keys with
// different prefixes matches rippled test vectors.
func TestRippledPrivateKeyEncoding(t *testing.T) {
	// Generate keys from masterpassphrase for testing
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := seedHash[:16]

	t.Run("secp256k1 private key encoding", func(t *testing.T) {
		seedCopy := make([]byte, len(seedBytes))
		copy(seedCopy, seedBytes)

		privKeyHex, _, err := secp256k1.SECP256K1().DeriveKeypair(seedCopy, false)
		require.NoError(t, err)

		privKeyBytes, err := hex.DecodeString(privKeyHex)
		require.NoError(t, err)

		// Remove the 00 prefix for secp256k1
		privKeyBytesNoPrefix := privKeyBytes[1:]
		require.Len(t, privKeyBytesNoPrefix, 32, "Private key should be 32 bytes")

		// Account secret key (prefix 0x22, starts with 'p')
		accountSecret := Base58CheckEncode(privKeyBytesNoPrefix, AccountSecretKeyPrefix)
		require.Equal(t, "p9JfM6HHi64m6mvB6v5k7G2b1cXzGmYiCNJf6GHPKvFTWdeRVjh", accountSecret)
		require.True(t, strings.HasPrefix(accountSecret, "p"),
			"Account secret key should start with 'p'")
	})

	t.Run("ed25519 private key encoding", func(t *testing.T) {
		privKeyHex, _, err := ed25519.ED25519().DeriveKeypair(seedBytes, false)
		require.NoError(t, err)

		privKeyBytes, err := hex.DecodeString(privKeyHex)
		require.NoError(t, err)

		// Remove the ED prefix for ed25519
		privKeyBytesNoPrefix := privKeyBytes[1:]
		require.Len(t, privKeyBytesNoPrefix, 32, "Private key should be 32 bytes")

		// Account secret key (prefix 0x22)
		accountSecret := Base58CheckEncode(privKeyBytesNoPrefix, AccountSecretKeyPrefix)
		require.Equal(t, "pwDQjwEhbUBmPuEjFpEG75bFhv2obkCB7NxQsfFxM7xGHBMVPu9", accountSecret)

		// Node private key (prefix 0x20)
		nodePrivate := Base58CheckEncode(privKeyBytesNoPrefix, NodePrivateKeyPrefix)
		require.Equal(t, "paKv46LztLqK3GaKz1rG2nQGN6M4JLyRtxFBYFTw4wAVHtGys36", nodePrivate)
	})
}

// TestRippledNodeIDDerivation tests that node ID derivation matches rippled
// test vectors. Node ID is SHA256-RIPEMD160 of the public key.
func TestRippledNodeIDDerivation(t *testing.T) {
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := seedHash[:16]

	t.Run("ed25519 node ID", func(t *testing.T) {
		_, pubKeyHex, err := ed25519.ED25519().DeriveKeypair(seedBytes, false)
		require.NoError(t, err)

		pubKeyBytes, err := hex.DecodeString(pubKeyHex)
		require.NoError(t, err)

		nodeIDBytes := Sha256RipeMD160(pubKeyBytes)
		nodeID := strings.ToUpper(hex.EncodeToString(nodeIDBytes))

		require.Equal(t, "AA066C988C712815CC37AF71472B7CBBBD4E2A0A", nodeID,
			"ED25519 node ID should match rippled test vector")
		require.Len(t, nodeID, 40, "Node ID should be 20 bytes (40 hex chars)")
	})

	// Note: secp256k1 node ID from rippled uses the ROOT public key, not the
	// account public key. The expected value "7E59C17D50F5959C7B158FEC95C8F815BF653DC8"
	// cannot be tested here without implementing root key derivation.
}

// TestRippledKeyDerivationDeterminism verifies that key derivation is
// deterministic - same seed always produces same keys.
func TestRippledKeyDerivationDeterminism(t *testing.T) {
	passphrase := "masterpassphrase"
	seedHash := common.Sha512Half([]byte(passphrase))
	baseSeedBytes := seedHash[:16]

	iterations := 10

	t.Run("secp256k1 determinism", func(t *testing.T) {
		var firstPriv, firstPub string

		for i := 0; i < iterations; i++ {
			// Make a copy because DeriveKeypair may modify input
			seed := make([]byte, len(baseSeedBytes))
			copy(seed, baseSeedBytes)

			priv, pub, err := secp256k1.SECP256K1().DeriveKeypair(seed, false)
			require.NoError(t, err)

			if i == 0 {
				firstPriv = priv
				firstPub = pub
			} else {
				require.Equal(t, firstPriv, priv,
					"Private key should be deterministic (iteration %d)", i)
				require.Equal(t, firstPub, pub,
					"Public key should be deterministic (iteration %d)", i)
			}
		}
	})

	t.Run("ed25519 determinism", func(t *testing.T) {
		var firstPriv, firstPub string

		for i := 0; i < iterations; i++ {
			seed := make([]byte, len(baseSeedBytes))
			copy(seed, baseSeedBytes)

			priv, pub, err := ed25519.ED25519().DeriveKeypair(seed, false)
			require.NoError(t, err)

			if i == 0 {
				firstPriv = priv
				firstPub = pub
			} else {
				require.Equal(t, firstPriv, priv,
					"Private key should be deterministic (iteration %d)", i)
				require.Equal(t, firstPub, pub,
					"Public key should be deterministic (iteration %d)", i)
			}
		}
	})
}

// TestRippledBase58Alphabet verifies that base58 encoding uses the correct
// XRP Ledger alphabet (which excludes 0, O, I, l to avoid confusion).
func TestRippledBase58Alphabet(t *testing.T) {
	// The XRP Ledger base58 alphabet is:
	// rpshnaf39wBUDNEGHJKLM4PQRST7VWXYZ2bcdeCg65jkm8oFqi1tuvAxyz
	// Note: It excludes 0, O, I, l

	testcases := []struct {
		name      string
		seed      string
		shouldErr bool
	}{
		{
			name:      "valid seed - no excluded chars",
			seed:      "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
			shouldErr: false,
		},
		{
			name:      "invalid - contains 0",
			seed:      "sn0PBrXtMeMyMHUVTgbuqAfg1SUTb",
			shouldErr: true,
		},
		{
			name:      "invalid - contains O",
			seed:      "snOPBrXtMeMyMHUVTgbuqAfg1SUTb",
			shouldErr: true,
		},
		{
			name:      "invalid - contains I",
			seed:      "snIPBrXtMeMyMHUVTgbuqAfg1SUTb",
			shouldErr: true,
		},
		{
			name:      "invalid - contains l",
			seed:      "snlPBrXtMeMyMHUVTgbuqAfg1SUTb",
			shouldErr: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := DecodeSeed(tc.seed)
			if tc.shouldErr {
				require.Error(t, err, "Should error for invalid base58 character")
			} else {
				require.NoError(t, err, "Should succeed for valid base58 characters")
			}
		})
	}
}

// TestRippledSeedPrefixDetection tests that seed decoding correctly identifies
// the cryptographic algorithm from the encoded prefix.
func TestRippledSeedPrefixDetection(t *testing.T) {
	testcases := []struct {
		name         string
		seed         string
		expectedAlgo string
		description  string
	}{
		{
			name:         "secp256k1 seed starts with 's' (not 'sEd')",
			seed:         "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
			expectedAlgo: "secp256k1",
			description:  "Prefix 0x21 encodes to seeds starting with 's'",
		},
		{
			name:         "ed25519 seed starts with 'sEd'",
			seed:         "sEdTzRkEgPoxDG1mJ6WkSucHWnMkm1H",
			expectedAlgo: "ed25519",
			description:  "Prefix [0x01, 0xe1, 0x4b] encodes to seeds starting with 'sEd'",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			_, algo, err := DecodeSeed(tc.seed)
			require.NoError(t, err, tc.description)
			require.NotNil(t, algo)

			if tc.expectedAlgo == "ed25519" {
				require.Equal(t, ed25519.ED25519(), algo,
					"Should detect ED25519 algorithm")
			} else {
				require.Equal(t, secp256k1.SECP256K1(), algo,
					"Should detect SECP256K1 algorithm")
			}
		})
	}
}

// TestRippledValidatorKeypairError tests that validator keypair derivation
// returns appropriate errors (as it's not fully supported).
func TestRippledValidatorKeypairError(t *testing.T) {
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := seedHash[:16]

	t.Run("ed25519 validator derivation", func(t *testing.T) {
		_, _, err := ed25519.ED25519().DeriveKeypair(seedBytes, true)
		require.Error(t, err, "Validator keypair derivation should return error for ed25519")
	})
}
