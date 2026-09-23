package sign

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/stretchr/testify/require"
)

func TestRoleSigningHelpers(t *testing.T) {
	for _, algorithm := range []string{"ed25519", "secp256k1"} {
		t.Run(algorithm, func(t *testing.T) {
			entropy := make([]byte, 16)
			entropy[0] = 42
			var privateKey, publicKey string
			var err error
			if algorithm == "ed25519" {
				privateKey, publicKey, err = (ed25519.Algorithm{}).DeriveKeypair(entropy, false)
			} else {
				privateKey, publicKey, err = (secp256k1.Algorithm{}).DeriveKeypair(entropy, false)
			}
			require.NoError(t, err)
			transaction := primarySignedTx(t)
			fields, err := flattenForSigning(transaction)
			require.NoError(t, err)
			single, err := binarycodec.EncodeForSigning(fields)
			require.NoError(t, err)
			multi, err := binarycodec.EncodeForMultisigningTarget(fields, transaction.GetCommon().Account)
			require.NoError(t, err)
			for _, tc := range []struct {
				role         binarycodec.SigningRole
				singlePrefix string
				multiPrefix  string
			}{
				{binarycodec.CounterpartyRole, "43505400", "43504D00"},
				{binarycodec.SponsorRole, "53504E00", "53504D00"},
			} {
				for _, enabled := range []bool{false, true} {
					t.Run(fmt.Sprintf("%d/cleanup=%t", tc.role, enabled), func(t *testing.T) {
						rules := amendment.NewRules(nil)
						if enabled {
							rules = amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
						}
						singlePayload, multiPayload := single, multi
						if enabled {
							singlePayload = tc.singlePrefix + single[8:]
							multiPayload = tc.multiPrefix + multi[8:]
						}
						signature, err := SignTransactionForRole(transaction, privateKey, tc.role, rules)
						require.NoError(t, err)
						verifyRoleHelperSignature(t, algorithm, singlePayload, publicKey, signature)
						if tc.role == binarycodec.CounterpartyRole {
							nested, err := SignCounterpartyWithRules(transaction, publicKey, privateKey, rules)
							require.NoError(t, err)
							require.Equal(t, publicKey, nested.SigningPubKey)
							require.Equal(t, signature, nested.TxnSignature)
						} else {
							nested, err := SignSponsorWithRules(transaction, publicKey, privateKey, rules)
							require.NoError(t, err)
							require.Equal(t, publicKey, nested.SigningPubKey)
							require.Equal(t, signature, nested.TxnSignature)
						}
						signature, err = SignTransactionForMultiSignRole(transaction, transaction.GetCommon().Account, privateKey, tc.role, rules)
						require.NoError(t, err)
						verifyRoleHelperSignature(t, algorithm, multiPayload, publicKey, signature)
						ordinary, err := SignTransactionForRole(transaction, privateKey, binarycodec.TransactionRole, rules)
						require.NoError(t, err)
						legacy, err := SignTransaction(transaction, privateKey)
						require.NoError(t, err)
						require.Equal(t, legacy, ordinary)
						after, err := getSigningPayload(transaction)
						require.NoError(t, err)
						require.Equal(t, single, after)
					})
				}
			}
		})
	}
}

func verifyRoleHelperSignature(t *testing.T, algorithm, payload, publicKey, signature string) {
	t.Helper()
	require.Equal(t, strings.ToUpper(signature), signature)
	message, err := hex.DecodeString(payload)
	require.NoError(t, err)
	if algorithm == "ed25519" {
		require.True(t, (ed25519.Algorithm{}).Validate(string(message), publicKey, signature))
	} else {
		require.True(t, (secp256k1.Algorithm{}).ValidateWithCanonicality(string(message), publicKey, signature, true))
	}
}
