package jtx

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
	require.Equal(e.t, rootcrypto.CanonicalityCanonical,
		ecdsaCanonicalityOf(e.t, common.TxnSignature),
		"prepared signature must be high-S (canonical but not fully canonical)")
}

func ecdsaCanonicalityOf(t testing.TB, sigHex string) rootcrypto.Canonicality {
	t.Helper()
	b, err := hex.DecodeString(sigHex)
	require.NoError(t, err)
	return rootcrypto.ECDSACanonicality(b)
}

// TestRequireFullyCanonicalSig_Gate verifies that a high-S signature is
// rejected. RequireFullyCanonicalSig is retired, so full canonicality is
// required unconditionally.
// Reference: rippled STTx::checkSingleSign (verify() defaults to fullyCanonical).
func TestRequireFullyCanonicalSig_Gate(t *testing.T) {
	env := NewTestEnv(t)
	alice := NewAccount("alice")
	bob := NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	p := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(1_000_000))
	env.signHighS(p, alice)
	result := env.submitWithSigVerification(p)
	require.Equal(t, "temINVALID", result.Code,
		"high-S signature must be rejected (fully-canonical required unconditionally)")
}
