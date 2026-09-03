package secp256k1_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/stretchr/testify/require"
)

func TestDeriveKeypairBytesMatchesHexAPI(t *testing.T) {
	seed := []byte{229, 81, 182, 134, 131, 220, 192, 126, 133, 114, 150, 132, 140, 237, 222, 196}
	for _, validator := range []bool{false, true} {
		t.Run(map[bool]string{false: "account", true: "validator"}[validator], func(t *testing.T) {
			privateKey, publicKey, err := (secp256k1.Algorithm{}).DeriveKeypairBytes(seed, validator)
			require.NoError(t, err)
			defer rootcrypto.SecureErase(privateKey)
			require.Len(t, privateKey, 32)
			require.Len(t, publicKey, 33)

			privateHex, publicHex, err := (secp256k1.Algorithm{}).DeriveKeypair(seed, validator)
			require.NoError(t, err)
			require.Equal(t, privateHex, "00"+strings.ToUpper(hex.EncodeToString(privateKey)))
			require.Equal(t, publicHex, strings.ToUpper(hex.EncodeToString(publicKey)))
		})
	}
}

func TestDeriveKeypairBytesValidatorVector(t *testing.T) {
	seed, err := hex.DecodeString("DEDCE9CE67B451D852FD4E846FCDE31C")
	require.NoError(t, err)
	privateKey, publicKey, err := (secp256k1.Algorithm{}).DeriveKeypairBytes(seed, true)
	require.NoError(t, err)
	defer rootcrypto.SecureErase(privateKey)

	require.Equal(t,
		"pnen77YEeUd4fFKG7iycBWcwKpTaeFRkW2WFostaATy1DSupwXe",
		addresscodec.Base58CheckEncode(privateKey, addresscodec.NodePrivateKeyPrefix),
	)
	require.Equal(t,
		"n94a1u4jAz288pZLtw6yFWVbi89YamiC6JBXPVUj5zmExe5fTVg9",
		addresscodec.Base58CheckEncode(publicKey, addresscodec.NodePublicKeyPrefix),
	)
}
