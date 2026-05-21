package service

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComputeBaseFeeForTx_Multisign verifies that the fee dispatch matches
// rippled Transactor::calculateBaseFee (Transactor.cpp:229-245):
// baseFee + signerCount * baseFee. The dispatch must not be gated on
// SigningPubKey being empty — rippled counts sfSigners entries directly.
func TestComputeBaseFeeForTx_Multisign(t *testing.T) {
	cfg := tx.EngineConfig{BaseFee: 10}

	t.Run("no signers → baseFee", func(t *testing.T) {
		parsed, err := tx.ParseJSON([]byte(`{
			"TransactionType":"AccountSet",
			"Account":"rEFNJWaJN6JYW9zXxFq1KqtaqgsMcLs9wK"
		}`))
		require.NoError(t, err)
		assert.Equal(t, uint64(10), computeBaseFeeForTx(nil, parsed, cfg))
	})

	t.Run("one signer with empty SigningPubKey → 2 * baseFee", func(t *testing.T) {
		parsed, err := tx.ParseJSON([]byte(`{
			"TransactionType":"AccountSet",
			"Account":"rEFNJWaJN6JYW9zXxFq1KqtaqgsMcLs9wK",
			"SigningPubKey":"",
			"Signers":[
				{"Signer":{"Account":"rPmsLuwgD3yp6mvCXyz44itC9V2qZpDvm6","SigningPubKey":"","TxnSignature":""}}
			]
		}`))
		require.NoError(t, err)
		assert.Equal(t, uint64(20), computeBaseFeeForTx(nil, parsed, cfg))
	})

	t.Run("one signer with non-empty SigningPubKey still gets multisign fee", func(t *testing.T) {
		// 33-byte compressed secp256k1 pubkey shape (66 hex chars). The value
		// itself is synthetic; the dispatch under test only counts sfSigners
		// (rippled Transactor.cpp:229-245), but a well-shaped pubkey keeps
		// this case robust against future parser tightening.
		const signingPubKey = "03ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB"
		parsed, err := tx.ParseJSON([]byte(`{
			"TransactionType":"AccountSet",
			"Account":"rEFNJWaJN6JYW9zXxFq1KqtaqgsMcLs9wK",
			"SigningPubKey":"` + signingPubKey + `",
			"Signers":[
				{"Signer":{"Account":"rPmsLuwgD3yp6mvCXyz44itC9V2qZpDvm6","SigningPubKey":"","TxnSignature":""}}
			]
		}`))
		require.NoError(t, err)
		assert.Equal(t, uint64(20), computeBaseFeeForTx(nil, parsed, cfg),
			"rippled Transactor.cpp:229-245 counts sfSigners regardless of SigningPubKey")
	})

	t.Run("three signers → 4 * baseFee", func(t *testing.T) {
		parsed, err := tx.ParseJSON([]byte(`{
			"TransactionType":"AccountSet",
			"Account":"rEFNJWaJN6JYW9zXxFq1KqtaqgsMcLs9wK",
			"Signers":[
				{"Signer":{"Account":"rPmsLuwgD3yp6mvCXyz44itC9V2qZpDvm6","SigningPubKey":"","TxnSignature":""}},
				{"Signer":{"Account":"rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH","SigningPubKey":"","TxnSignature":""}},
				{"Signer":{"Account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","SigningPubKey":"","TxnSignature":""}}
			]
		}`))
		require.NoError(t, err)
		assert.Equal(t, uint64(40), computeBaseFeeForTx(nil, parsed, cfg))
	})

	t.Run("nil parsedTx falls back to baseFee", func(t *testing.T) {
		assert.Equal(t, uint64(10), computeBaseFeeForTx(nil, nil, cfg))
	})
}

func TestMulDivU64(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		got, ok := mulDivU64(10, 3, 2)
		assert.True(t, ok)
		assert.Equal(t, uint64(15), got)
	})
	t.Run("zero divisor", func(t *testing.T) {
		_, ok := mulDivU64(10, 3, 0)
		assert.False(t, ok)
	})
	t.Run("overflow", func(t *testing.T) {
		_, ok := mulDivU64(^uint64(0), 2, 1)
		assert.False(t, ok, "uint64 max * 2 overflows")
	})
}
