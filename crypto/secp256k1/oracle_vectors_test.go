package secp256k1_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/stretchr/testify/require"
)

func TestRippledKeyAndSignatureVectors(t *testing.T) {
	t.Parallel()
	var fixture struct {
		Message string
		Digest  string
		Vectors []struct {
			Seed             string
			AccountPrivate   string `json:"account_private"`
			AccountPublic    string `json:"account_public"`
			ValidatorPrivate string `json:"validator_private"`
			ValidatorPublic  string `json:"validator_public"`
			Signature        string
		}
	}
	data, err := os.ReadFile("testdata/rippled-3.3.0.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Vectors)
	digest, err := hex.DecodeString(fixture.Digest)
	require.NoError(t, err)
	algorithm := secp256k1.Algorithm{}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Seed, func(t *testing.T) {
			t.Parallel()
			seed, err := hex.DecodeString(vector.Seed)
			require.NoError(t, err)
			account, public, err := algorithm.DeriveKeypairBytes(seed, false)
			require.NoError(t, err)
			defer rootcrypto.SecureErase(account)
			require.Equal(t, vector.AccountPrivate, hex.EncodeToString(account))
			require.Equal(t, vector.AccountPublic, hex.EncodeToString(public))
			validator, generator, err := algorithm.DeriveKeypairBytes(seed, true)
			require.NoError(t, err)
			defer rootcrypto.SecureErase(validator)
			require.Equal(t, vector.ValidatorPrivate, hex.EncodeToString(validator))
			require.Equal(t, vector.ValidatorPublic, hex.EncodeToString(generator))
			derivedPublic, err := algorithm.DerivePublicKeyFromPublicGenerator(generator)
			require.NoError(t, err)
			require.Equal(t, public, derivedPublic)
			signature, err := secp256k1.SignDigestBytes(digest, account)
			require.NoError(t, err)
			require.Equal(t, vector.Signature, hex.EncodeToString(signature))
			messageSignature, err := algorithm.SignBytes([]byte(fixture.Message), account)
			require.NoError(t, err)
			require.Equal(t, signature, messageSignature)
			require.True(t, secp256k1.VerifyDigestBytes(digest, public, signature))
		})
	}
}
