package sign

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestRoleSignatureRuleMatrix(t *testing.T) {
	for _, algorithm := range []string{"ed25519", "secp256k1"} {
		for _, role := range []binarycodec.SigningRole{binarycodec.CounterpartyRole, binarycodec.SponsorRole} {
			for _, multi := range []bool{false, true} {
				for _, cleanup := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/role=%d/multi=%t/cleanup=%t", algorithm, role, multi, cleanup), func(t *testing.T) {
						transaction := primarySignedTx(t)
						rules := roleVerificationRules(cleanup)
						priv, pub := roleVerificationKey(t, algorithm)
						prefix := "Counterparty: "
						if role == binarycodec.SponsorRole {
							prefix = "Sponsor: "
						}
						var signature string
						var err error
						if multi {
							signature, err = SignTransactionForMultiSignRole(transaction, transaction.GetCommon().Account, priv, role, rules)
						} else {
							signature, err = SignTransactionForRole(transaction, priv, role, rules)
						}
						require.NoError(t, err)
						var signers []txcore.SignerWrapper
						if multi {
							signers = []txcore.SignerWrapper{{Signer: txcore.Signer{
								Account: transaction.GetCommon().Account, SigningPubKey: pub, TxnSignature: signature,
							}}}
							pub, signature = "", ""
						}
						if role == binarycodec.CounterpartyRole {
							transaction.GetCommon().CounterpartySignature = &txcore.CounterpartySignature{SigningPubKey: pub, TxnSignature: signature, Signers: signers}
						} else {
							transaction.GetCommon().SponsorSignature = &txcore.SponsorSignature{SigningPubKey: pub, TxnSignature: signature, Signers: signers}
						}
						require.Empty(t, CheckSTTxSignature(transaction, rules, true))
						wantFailure := prefix + "Invalid signature."
						if multi {
							wantFailure = prefix + "Invalid signature on account " + transaction.GetCommon().Account + "."
						}
						require.Equal(t, wantFailure, CheckSTTxSignature(transaction, roleVerificationRules(!cleanup), true))
						raw, err := txcore.SerializeTransaction(transaction)
						require.NoError(t, err)
						require.NoError(t, txcore.BindRawBytes(transaction, raw))
						require.Empty(t, CheckSTTxSignature(transaction, rules, true))
						require.Equal(t, wantFailure, CheckSTTxSignature(transaction, roleVerificationRules(!cleanup), true))
						require.Empty(t, CheckSTTxSignature(transaction, roleVerificationRules(!cleanup), false))
					})
				}
			}
		}
	}
}

func roleVerificationRules(cleanup bool) *amendment.Rules {
	if cleanup {
		return amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
	}
	return amendment.NewRules(nil)
}

func roleVerificationKey(t *testing.T, algorithm string) (string, string) {
	t.Helper()
	entropy := make([]byte, 16)
	entropy[0] = 93
	var privateKey, publicKey string
	var err error
	if algorithm == "ed25519" {
		privateKey, publicKey, err = (ed25519.Algorithm{}).DeriveKeypair(entropy, false)
	} else {
		privateKey, publicKey, err = (secp256k1.Algorithm{}).DeriveKeypair(entropy, false)
	}
	require.NoError(t, err)
	return privateKey, publicKey
}

func TestRoleSignatureMalformedPrecedence(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, sponsor := range []bool{false, true} {
			for _, tc := range []struct {
				name        string
				publicKey   bool
				fields      []string
				signerCount int
				want        string
			}{
				{name: "single with present empty signers", publicKey: true, fields: []string{"Signers"}, want: "Cannot both single- and multi-sign."},
				{name: "missing signers", want: "Empty SigningPubKey."},
				{name: "empty signers", fields: []string{"Signers"}, want: "Invalid Signers array size."},
				{name: "empty signature precedes empty signer size", fields: []string{"Signers", "TxnSignature"}, want: "Cannot both single- and multi-sign."},
				{name: "too many signers", signerCount: MaxMultiSigners + 1, want: "Invalid Signers array size."},
				{name: "invalid signature", publicKey: true, want: "Invalid signature."},
			} {
				t.Run(fmt.Sprintf("cleanup=%t/sponsor=%t/%s", cleanup, sponsor, tc.name), func(t *testing.T) {
					transaction := primarySignedTx(t)
					pub := ""
					if tc.publicKey {
						_, pub = roleVerificationKey(t, "ed25519")
					}
					prefix := "Counterparty: "
					if sponsor {
						nested := &txcore.SponsorSignature{SigningPubKey: pub, Signers: make([]txcore.SignerWrapper, tc.signerCount)}
						for _, field := range tc.fields {
							nested.MarkFieldPresent(field)
						}
						transaction.GetCommon().SponsorSignature = nested
						prefix = "Sponsor: "
					} else {
						nested := &txcore.CounterpartySignature{SigningPubKey: pub, Signers: make([]txcore.SignerWrapper, tc.signerCount)}
						for _, field := range tc.fields {
							nested.MarkFieldPresent(field)
						}
						transaction.GetCommon().CounterpartySignature = nested
						transaction.GetCommon().SponsorSignature = &txcore.SponsorSignature{}
					}
					rules := roleVerificationRules(cleanup)
					require.Equal(t, prefix+tc.want, CheckSTTxSignature(transaction, rules, true))
					transaction.GetCommon().TxnSignature = "ABCD"
					require.Equal(t, "Invalid signature.", CheckSTTxSignature(transaction, rules, true))
				})
			}
		}
	}
}

func TestInnerBatchSignatureRejectionPrecedesDisabledChecks(t *testing.T) {
	transaction := primarySignedTx(t)
	transaction.GetCommon().SetFlags(txcore.TfInnerBatchTxn)
	for _, cleanup := range []bool{false, true} {
		require.Equal(t, "Batch inner transactions are never considered validly signed.", CheckSTTxSignature(transaction, roleVerificationRules(cleanup), false))
	}
}
