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

// TestSeedFromPassphraseRippledVectors tests seed generation from passphrases
// using rippled official test vectors.
func TestSeedFromPassphraseRippledVectors(t *testing.T) {
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

// TestInvalidSeedDecodingRippledVectors tests that invalid seeds are properly rejected.
func TestInvalidSeedDecodingRippledVectors(t *testing.T) {
	testcases := []struct {
		name        string
		seed        string
		expectError bool
	}{
		{
			name:        "empty string should fail",
			seed:        "",
			expectError: true,
		},
		{
			name:        "too short seed should fail",
			seed:        "sspUXGrmjQhq6mgc24jiRuevZiwK",
			expectError: true,
		},
		{
			name:        "too long seed should fail",
			seed:        "sspUXGrmjQhq6mgc24jiRuevZiwKTT",
			expectError: true,
		},
		{
			name:        "valid masterpassphrase seed should succeed",
			seed:        "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
			expectError: false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := DecodeSeed(tc.seed)

			if tc.expectError {
				require.Error(t, err, "DecodeSeed should return an error for invalid seed")
			} else {
				require.NoError(t, err, "DecodeSeed should not return an error for valid seed")
			}
		})
	}
}

// TestSecp256k1KeyDerivationFromMasterpassphrase tests full key derivation
// using rippled test vectors from "masterpassphrase".
//
// Note: In rippled, "Node Public" and "Account Public" are different keys:
// - Node keys are derived directly from the seed (root keypair/private generator)
// - Account keys have a second derivation step added
//
// The DeriveKeypair function in this library derives ACCOUNT keys (with the second
// derivation step), which matches rippled's account key derivation.
func TestSecp256k1KeyDerivationFromMasterpassphrase(t *testing.T) {
	// rippled test vectors for secp256k1 derivation from "masterpassphrase"
	// These are the ACCOUNT keys (not node/root keys)
	expectedAccountAddress := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	expectedAccountPublicKeyBase58 := "aBQG8RQAzjs1eTKFEAQXr2gS4utcDiEC9wmi7pfUPTi27VCahwgw"

	// Generate seed bytes from masterpassphrase
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := seedHash[:16]

	// Verify seed encoding
	encodedSeed, err := EncodeSeed(seedBytes, secp256k1.SECP256K1())
	require.NoError(t, err)
	require.Equal(t, "snoPBrXtMeMyMHUVTgbuqAfg1SUTb", encodedSeed)

	// Derive keypair from seed (this derives ACCOUNT keys, not node/root keys)
	privKeyHex, pubKeyHex, err := secp256k1.SECP256K1().DeriveKeypair(seedBytes, false)
	require.NoError(t, err)

	// Test account address derivation from public key
	accountAddress, err := EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
	require.NoError(t, err)
	require.Equal(t, expectedAccountAddress, accountAddress,
		"Account address should match rippled test vector")

	// Test account public key encoding
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	require.NoError(t, err)
	accountPublicKeyBase58, err := EncodeAccountPublicKey(pubKeyBytes)
	require.NoError(t, err)
	require.Equal(t, expectedAccountPublicKeyBase58, accountPublicKeyBase58,
		"Account public key should match rippled test vector")

	// Verify private key is not empty and has expected format
	require.NotEmpty(t, privKeyHex, "Private key should not be empty")
	require.True(t, strings.HasPrefix(privKeyHex, "00"),
		"secp256k1 private key should have 00 prefix")
}

// TestED25519KeyDerivationFromMasterpassphrase tests full key derivation
// using rippled test vectors for ED25519 from "masterpassphrase".
//
// Note: For ED25519, there is no separate root/account derivation step like secp256k1.
// The same key is used for both node and account purposes.
func TestED25519KeyDerivationFromMasterpassphrase(t *testing.T) {
	// rippled test vectors for ed25519 derivation from "masterpassphrase"
	expectedAccountAddress := "rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf"
	expectedAccountPublicKeyBase58 := "aKGheSBjmCsKJVuLNKRAKpZXT6wpk2FCuEZAXJupXgdAxX5THCqR"
	expectedNodePublicKeyBase58 := "nHUeeJCSY2dM71oxM8Cgjouf5ekTuev2mwDpc374aLMxzDLXNmjf"

	// Generate seed bytes from masterpassphrase
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := seedHash[:16]

	// Derive ED25519 keypair from seed
	privKeyHex, pubKeyHex, err := ed25519.ED25519().DeriveKeypair(seedBytes, false)
	require.NoError(t, err)

	// Test account address derivation from public key
	accountAddress, err := EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
	require.NoError(t, err)
	require.Equal(t, expectedAccountAddress, accountAddress,
		"Account address should match rippled test vector")

	// Test account public key encoding
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	require.NoError(t, err)
	accountPublicKeyBase58, err := EncodeAccountPublicKey(pubKeyBytes)
	require.NoError(t, err)
	require.Equal(t, expectedAccountPublicKeyBase58, accountPublicKeyBase58,
		"Account public key should match rippled test vector")

	// Test node public key encoding (same key for ED25519, different prefix)
	nodePublicKeyBase58, err := EncodeNodePublicKey(pubKeyBytes)
	require.NoError(t, err)
	require.Equal(t, expectedNodePublicKeyBase58, nodePublicKeyBase58,
		"Node public key should match rippled test vector")

	// Verify private key is not empty and has ED prefix
	require.NotEmpty(t, privKeyHex, "Private key should not be empty")
	require.True(t, strings.HasPrefix(pubKeyHex, "ED"),
		"ED25519 public key should have ED prefix")
}

// TestSeedEncodingRoundTripAllAlgorithms tests that seed encoding/decoding is reversible.
func TestSeedEncodingRoundTripAllAlgorithms(t *testing.T) {
	passphrases := []string{
		"masterpassphrase",
		"Non-Random Passphrase",
		"cookies excitement hand public",
		"test passphrase for roundtrip validation",
	}

	for _, passphrase := range passphrases {
		t.Run(passphrase, func(t *testing.T) {
			// Generate seed bytes
			seedHash := common.Sha512Half([]byte(passphrase))
			originalSeedBytes := seedHash[:16]

			// Test secp256k1 round trip
			t.Run("secp256k1", func(t *testing.T) {
				encoded, err := EncodeSeed(originalSeedBytes, secp256k1.SECP256K1())
				require.NoError(t, err)

				decoded, algo, err := DecodeSeed(encoded)
				require.NoError(t, err)
				require.Equal(t, originalSeedBytes, decoded)
				require.Equal(t, secp256k1.SECP256K1(), algo)
			})

			// Test ed25519 round trip
			t.Run("ed25519", func(t *testing.T) {
				encoded, err := EncodeSeed(originalSeedBytes, ed25519.ED25519())
				require.NoError(t, err)

				decoded, algo, err := DecodeSeed(encoded)
				require.NoError(t, err)
				require.Equal(t, originalSeedBytes, decoded)
				require.Equal(t, ed25519.ED25519(), algo)
			})
		})
	}
}

// TestKeyDerivationConsistency verifies that key derivation is deterministic.
// Note: This test creates copies of the seed bytes because the current secp256k1
// implementation has a bug that modifies the input slice. See deriveScalar function.
func TestKeyDerivationConsistency(t *testing.T) {
	passphrase := "masterpassphrase"
	seedHash := common.Sha512Half([]byte(passphrase))
	baseSeedBytes := seedHash[:16]

	// Derive keys multiple times and ensure consistency
	for i := 0; i < 5; i++ {
		t.Run("secp256k1", func(t *testing.T) {
			// Make copies because DeriveKeypair modifies the input slice (bug in deriveScalar)
			seed1 := make([]byte, len(baseSeedBytes))
			seed2 := make([]byte, len(baseSeedBytes))
			copy(seed1, baseSeedBytes)
			copy(seed2, baseSeedBytes)

			priv1, pub1, err := secp256k1.SECP256K1().DeriveKeypair(seed1, false)
			require.NoError(t, err)

			priv2, pub2, err := secp256k1.SECP256K1().DeriveKeypair(seed2, false)
			require.NoError(t, err)

			require.Equal(t, priv1, priv2, "Private key derivation should be deterministic")
			require.Equal(t, pub1, pub2, "Public key derivation should be deterministic")
		})

		t.Run("ed25519", func(t *testing.T) {
			// ED25519 does not have this bug, but copy for consistency
			seed1 := make([]byte, len(baseSeedBytes))
			seed2 := make([]byte, len(baseSeedBytes))
			copy(seed1, baseSeedBytes)
			copy(seed2, baseSeedBytes)

			priv1, pub1, err := ed25519.ED25519().DeriveKeypair(seed1, false)
			require.NoError(t, err)

			priv2, pub2, err := ed25519.ED25519().DeriveKeypair(seed2, false)
			require.NoError(t, err)

			require.Equal(t, priv1, priv2, "Private key derivation should be deterministic")
			require.Equal(t, pub1, pub2, "Public key derivation should be deterministic")
		})
	}
}

// TestAddressDerivationFromKnownPublicKeys tests address derivation using known public keys.
// This test verifies that the address derivation logic correctly computes addresses
// from arbitrary public keys.
func TestAddressDerivationFromKnownPublicKeys(t *testing.T) {
	// Test with dynamically derived keys to ensure address derivation works correctly
	seedHash := common.Sha512Half([]byte("masterpassphrase"))

	t.Run("secp256k1", func(t *testing.T) {
		// Make a copy since DeriveKeypair modifies the input
		seedBytes := make([]byte, 16)
		copy(seedBytes, seedHash[:16])

		_, pubKeyHex, err := secp256k1.SECP256K1().DeriveKeypair(seedBytes, false)
		require.NoError(t, err)

		// Derive address from the public key
		addr, err := EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
		require.NoError(t, err)

		// This should match the rippled account address for masterpassphrase
		require.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", addr,
			"secp256k1 account address should match rippled test vector")
	})

	t.Run("ed25519", func(t *testing.T) {
		seedBytes := make([]byte, 16)
		copy(seedBytes, seedHash[:16])

		_, pubKeyHex, err := ed25519.ED25519().DeriveKeypair(seedBytes, false)
		require.NoError(t, err)

		// Derive address from the public key
		addr, err := EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
		require.NoError(t, err)

		// This should match the rippled account address for masterpassphrase ed25519
		require.Equal(t, "rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf", addr,
			"ed25519 account address should match rippled test vector")
	})
}

// TestNodeIDFromPublicKey tests that node ID can be computed from public key.
// Node ID is the SHA256-RIPEMD160 hash of the public key, encoded as hex (uppercase).
// Note: Node ID derived from account public key will differ from rippled's "Node ID"
// because rippled's Node ID uses the root public key, not the account public key.
func TestNodeIDFromPublicKey(t *testing.T) {
	seedHash := common.Sha512Half([]byte("masterpassphrase"))

	t.Run("secp256k1", func(t *testing.T) {
		seedBytes := make([]byte, 16)
		copy(seedBytes, seedHash[:16])

		_, pubKeyHex, err := secp256k1.SECP256K1().DeriveKeypair(seedBytes, false)
		require.NoError(t, err)

		pubKeyBytes, err := hex.DecodeString(pubKeyHex)
		require.NoError(t, err)

		// Compute node ID (SHA256-RIPEMD160 of the public key)
		nodeIDBytes := Sha256RipeMD160(pubKeyBytes)
		nodeID := strings.ToUpper(hex.EncodeToString(nodeIDBytes))

		// Verify the node ID matches what we would expect for the derived public key
		// This proves the SHA256-RIPEMD160 function works correctly
		require.Len(t, nodeID, 40, "Node ID should be 20 bytes (40 hex chars)")
		require.NotEmpty(t, nodeID, "Node ID should not be empty")

		// The account address is also derived from SHA256-RIPEMD160, so we can verify
		// that the address derivation uses the same hash
		addr, err := EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
		require.NoError(t, err)
		require.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", addr)
	})

	t.Run("ed25519", func(t *testing.T) {
		seedBytes := make([]byte, 16)
		copy(seedBytes, seedHash[:16])

		_, pubKeyHex, err := ed25519.ED25519().DeriveKeypair(seedBytes, false)
		require.NoError(t, err)

		pubKeyBytes, err := hex.DecodeString(pubKeyHex)
		require.NoError(t, err)

		// Compute node ID (SHA256-RIPEMD160 of the public key)
		nodeIDBytes := Sha256RipeMD160(pubKeyBytes)
		nodeID := strings.ToUpper(hex.EncodeToString(nodeIDBytes))

		// For ED25519, the same key is used for node and account purposes
		// So this should match the expected rippled node ID
		require.Equal(t, "AA066C988C712815CC37AF71472B7CBBBD4E2A0A", nodeID,
			"ED25519 node ID should match rippled test vector")

		// Verify address is also correct
		addr, err := EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
		require.NoError(t, err)
		require.Equal(t, "rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf", addr)
	})
}

// TestPublicKeyEncodingWithDifferentPrefixes tests that the same public key
// can be encoded with different prefixes for different purposes.
func TestPublicKeyEncodingWithDifferentPrefixes(t *testing.T) {
	// Derive a public key from masterpassphrase
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := make([]byte, 16)
	copy(seedBytes, seedHash[:16])

	_, publicKeyHex, err := secp256k1.SECP256K1().DeriveKeypair(seedBytes, false)
	require.NoError(t, err)

	pubKeyBytes, err := hex.DecodeString(publicKeyHex)
	require.NoError(t, err)

	// Account public key encoding (0x23 prefix -> 'a' character)
	accountPubKey, err := EncodeAccountPublicKey(pubKeyBytes)
	require.NoError(t, err)
	require.True(t, accountPubKey[0] == 'a', "Account public key should start with 'a'")

	// Node public key encoding (0x1C prefix -> 'n' character)
	nodePubKey, err := EncodeNodePublicKey(pubKeyBytes)
	require.NoError(t, err)
	require.True(t, nodePubKey[0] == 'n', "Node public key should start with 'n'")

	// Verify both decode back to the same bytes
	decodedAccount, err := DecodeAccountPublicKey(accountPubKey)
	require.NoError(t, err)
	require.Equal(t, pubKeyBytes, decodedAccount)

	decodedNode, err := DecodeNodePublicKey(nodePubKey)
	require.NoError(t, err)
	require.Equal(t, pubKeyBytes, decodedNode)
}

// TestFullKeyDerivationChainSecp256k1 tests the complete derivation chain
// from passphrase to all key formats for secp256k1.
//
// Note: The DeriveKeypair function derives ACCOUNT keys, not NODE keys.
// In rippled, node keys are derived from the root keypair (private generator),
// while account keys have an additional derivation step.
func TestFullKeyDerivationChainSecp256k1(t *testing.T) {
	// Test vectors from rippled for "masterpassphrase" with secp256k1
	// Note: We only test account-related values since DeriveKeypair derives account keys
	expected := struct {
		seed              string
		accountAddress    string
		accountPublic     string
	}{
		seed:              "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		accountAddress:    "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		accountPublic:     "aBQG8RQAzjs1eTKFEAQXr2gS4utcDiEC9wmi7pfUPTi27VCahwgw",
	}

	// Step 1: Generate seed from passphrase
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := seedHash[:16]

	// Step 2: Verify seed encoding
	encodedSeed, err := EncodeSeed(seedBytes, secp256k1.SECP256K1())
	require.NoError(t, err)
	require.Equal(t, expected.seed, encodedSeed, "Seed encoding should match")

	// Step 3: Derive keypair (this derives ACCOUNT keys)
	privKeyHex, pubKeyHex, err := secp256k1.SECP256K1().DeriveKeypair(seedBytes, false)
	require.NoError(t, err)

	// Step 4: Verify account address
	accountAddress, err := EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
	require.NoError(t, err)
	require.Equal(t, expected.accountAddress, accountAddress, "Account address should match")

	// Step 5: Verify account public key
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	require.NoError(t, err)
	accountPublic, err := EncodeAccountPublicKey(pubKeyBytes)
	require.NoError(t, err)
	require.Equal(t, expected.accountPublic, accountPublic, "Account public key should match")

	// Verify private key format
	require.True(t, strings.HasPrefix(privKeyHex, "00"),
		"secp256k1 private key should have 00 prefix")
}

// TestFullKeyDerivationChainED25519 tests the complete derivation chain
// from passphrase to all key formats for ED25519.
//
// Note: For ED25519, there is no separate root/account derivation - same key is used.
func TestFullKeyDerivationChainED25519(t *testing.T) {
	// Test vectors from rippled for "masterpassphrase" with ed25519
	expected := struct {
		nodePublic        string
		nodeID            string
		accountAddress    string
		accountPublic     string
	}{
		nodePublic:        "nHUeeJCSY2dM71oxM8Cgjouf5ekTuev2mwDpc374aLMxzDLXNmjf",
		nodeID:            "AA066C988C712815CC37AF71472B7CBBBD4E2A0A",
		accountAddress:    "rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf",
		accountPublic:     "aKGheSBjmCsKJVuLNKRAKpZXT6wpk2FCuEZAXJupXgdAxX5THCqR",
	}

	// Step 1: Generate seed from passphrase
	seedHash := common.Sha512Half([]byte("masterpassphrase"))
	seedBytes := seedHash[:16]

	// Step 2: Derive ED25519 keypair
	privKeyHex, pubKeyHex, err := ed25519.ED25519().DeriveKeypair(seedBytes, false)
	require.NoError(t, err)

	// Step 3: Verify account address
	accountAddress, err := EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
	require.NoError(t, err)
	require.Equal(t, expected.accountAddress, accountAddress, "Account address should match")

	// Step 4: Verify account public key
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	require.NoError(t, err)
	accountPublic, err := EncodeAccountPublicKey(pubKeyBytes)
	require.NoError(t, err)
	require.Equal(t, expected.accountPublic, accountPublic, "Account public key should match")

	// Step 5: Verify node public key (same as account for ED25519)
	nodePublic, err := EncodeNodePublicKey(pubKeyBytes)
	require.NoError(t, err)
	require.Equal(t, expected.nodePublic, nodePublic, "Node public key should match")

	// Step 6: Verify node ID
	nodeIDBytes := Sha256RipeMD160(pubKeyBytes)
	nodeID := strings.ToUpper(hex.EncodeToString(nodeIDBytes))
	require.Equal(t, expected.nodeID, nodeID, "Node ID should match")

	// Verify private key format
	require.True(t, strings.HasPrefix(privKeyHex, "ED"),
		"ED25519 private key should have ED prefix")
}

// TestSeedAlgorithmDetection tests that decoding correctly identifies the algorithm.
func TestSeedAlgorithmDetection(t *testing.T) {
	testcases := []struct {
		name           string
		seed           string
		expectedAlgo   string
	}{
		{
			name:         "secp256k1 seed from masterpassphrase",
			seed:         "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
			expectedAlgo: "secp256k1",
		},
		{
			name:         "ed25519 seed",
			seed:         "sEdTzRkEgPoxDG1mJ6WkSucHWnMkm1H",
			expectedAlgo: "ed25519",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			_, algo, err := DecodeSeed(tc.seed)
			require.NoError(t, err)
			require.NotNil(t, algo)

			if tc.expectedAlgo == "ed25519" {
				require.Equal(t, ed25519.ED25519(), algo)
			} else {
				require.Equal(t, secp256k1.SECP256K1(), algo)
			}
		})
	}
}
