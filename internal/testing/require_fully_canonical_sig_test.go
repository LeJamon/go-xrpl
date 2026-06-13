package testing

import (
	"encoding/hex"
	"math/big"
	"testing"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

// secp256k1 curve order N; high-S = N - lowS.
var secpCurveOrderN, _ = new(big.Int).SetString(
	"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)

// flipSToHighS converts a low-S DER signature to its high-S (non-canonical but
// still valid) counterpart by replacing s with N-s.
func flipSToHighS(t testing.TB, sigHex string) string {
	t.Helper()
	sigBytes, err := hex.DecodeString(sigHex)
	require.NoError(t, err)
	r, s, err := rootcrypto.DERSigToRS(sigBytes)
	require.NoError(t, err)
	flipped := new(big.Int).Sub(secpCurveOrderN, new(big.Int).SetBytes(s))
	return hex.EncodeToString(
		rootcrypto.EncodeDERSignature(new(big.Int).SetBytes(r), flipped))
}

// signHighS produces a transaction signed with a high-S secp256k1 signature.
func (e *TestEnv) signHighS(txn tx.Transaction, signer *Account) {
	e.t.Helper()
	e.autoFillForSigning(txn)
	e.signReal(txn, signer)
	common := txn.GetCommon()
	common.TxnSignature = flipSToHighS(e.t, common.TxnSignature)
	require.Equal(e.t, rootcrypto.CanonicityCanonical,
		ecdsaCanonicalityOf(e.t, common.TxnSignature),
		"prepared signature must be high-S (canonical but not fully canonical)")
}

func ecdsaCanonicalityOf(t testing.TB, sigHex string) rootcrypto.Canonicality {
	t.Helper()
	b, err := hex.DecodeString(sigHex)
	require.NoError(t, err)
	return rootcrypto.ECDSACanonicality(b)
}

// TestRequireFullyCanonicalSig_Gate exercises both branches of the
// RequireFullyCanonicalSig amendment gate. When the amendment is enabled (or the
// tx opts in via tfFullyCanonicalSig) a high-S signature is rejected; when it is
// disabled and the flag is absent, the high-S signature is accepted.
// Reference: rippled apply.cpp:78-84 + STTx::checkSingleSign.
func TestRequireFullyCanonicalSig_Gate(t *testing.T) {
	t.Run("enabled rejects high-S", func(t *testing.T) {
		env := NewTestEnv(t)
		alice := NewAccount("alice")
		bob := NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		p := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(1_000_000))
		env.signHighS(p, alice)
		result := env.submitWithSigVerification(p)
		require.Equal(t, "temINVALID", result.Code,
			"high-S signature must be rejected while RequireFullyCanonicalSig is enabled")
	})

	t.Run("disabled accepts high-S", func(t *testing.T) {
		env := NewTestEnv(t)
		env.DisableFeature("RequireFullyCanonicalSig")
		alice := NewAccount("alice")
		bob := NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		p := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(1_000_000))
		env.signHighS(p, alice)
		result := env.submitWithSigVerification(p)
		require.Equal(t, "tesSUCCESS", result.Code,
			"high-S signature must be accepted while RequireFullyCanonicalSig is disabled and tfFullyCanonicalSig is absent")
	})

	t.Run("disabled but tfFullyCanonicalSig rejects high-S", func(t *testing.T) {
		env := NewTestEnv(t)
		env.DisableFeature("RequireFullyCanonicalSig")
		alice := NewAccount("alice")
		bob := NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		p := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(1_000_000))
		p.SetFlags(p.GetFlags() | tx.TfFullyCanonicalSig)
		env.signHighS(p, alice)
		result := env.submitWithSigVerification(p)
		require.Equal(t, "temINVALID", result.Code,
			"a tx opting in via tfFullyCanonicalSig must reject a high-S signature even with the amendment disabled")
	})
}
