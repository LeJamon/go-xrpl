package secp256k1

import (
	"encoding/hex"
	"strings"
	"testing"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/stretchr/testify/require"
)

func TestDeriveKeypairBytesMatchesHexAPI(t *testing.T) {
	seed := []byte{229, 81, 182, 134, 131, 220, 192, 126, 133, 114, 150, 132, 140, 237, 222, 196}
	for _, validator := range []bool{false, true} {
		t.Run(map[bool]string{false: "account", true: "validator"}[validator], func(t *testing.T) {
			privateKey, publicKey, err := (Algorithm{}).DeriveKeypairBytes(seed, validator)
			require.NoError(t, err)
			defer rootcrypto.SecureErase(privateKey)
			require.Len(t, privateKey, 32)
			require.Len(t, publicKey, 33)

			privateHex, publicHex, err := (Algorithm{}).DeriveKeypair(seed, validator)
			require.NoError(t, err)
			require.Equal(t, privateHex, "00"+strings.ToUpper(hex.EncodeToString(privateKey)))
			require.Equal(t, publicHex, strings.ToUpper(hex.EncodeToString(publicKey)))
		})
	}
}
