package batch

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TestSerializeBatchDigest pins the digest layout to rippled serializeBatch:
// HashPrefix::batch || flags || txid count || inner txids.
func TestSerializeBatchDigest(t *testing.T) {
	txids := [][32]byte{
		{0x01},
		{0x02, 0x03},
	}
	flags := uint32(0x00000001)

	got := serializeBatch(flags, txids)

	require.Len(t, got, 4+4+4+len(txids)*32)
	require.Equal(t, protocol.HashPrefixBatch.Bytes(), got[0:4])
	require.Equal(t, flags, binary.BigEndian.Uint32(got[4:8]))
	require.Equal(t, uint32(len(txids)), binary.BigEndian.Uint32(got[8:12]))
	require.Equal(t, txids[0][:], got[12:44])
	require.Equal(t, txids[1][:], got[44:76])
}

// TestBatchSigningMessageMatchesTxids confirms the batch digest is built from the
// inner transaction IDs in order and is sensitive to the outer flags.
func TestBatchSigningMessageMatchesTxids(t *testing.T) {
	b := NewBatch(testOuter)
	b.AddInnerTransaction(makeTestPayment())
	b.AddInnerTransaction(makeTestPayment())
	b.SetFlags(BatchFlagAllOrNothing)

	ids := make([][32]byte, len(b.RawTransactions))
	for i, rt := range b.RawTransactions {
		id, err := tx.ComputeTransactionHash(rt.RawTransaction.InnerTx)
		require.NoError(t, err)
		ids[i] = id
	}

	msg, err := b.BatchSigningMessage()
	require.NoError(t, err)
	require.Equal(t, serializeBatch(BatchFlagAllOrNothing, ids), msg)

	// Flipping the outer flag changes the digest.
	b.SetFlags(BatchFlagOnlyOne)
	msg2, err := b.BatchSigningMessage()
	require.NoError(t, err)
	require.NotEqual(t, msg, msg2)
}

// TestVerifyBatchSignaturesRejectsBadSignatures confirms the cryptographic check
// rejects signers whose SigningPubKey/BatchTxnSignature do not verify over the
// batch digest. This is the crypto half of checkBatchSign that the engine runs
// from its signature stage; structural coverage is validated separately.
func TestVerifyBatchSignaturesRejectsBadSignatures(t *testing.T) {
	b := NewBatch(testOuter)
	b.AddInnerTransaction(makeTestPaymentFrom(testSigner1))
	b.AddInnerTransaction(makeTestPaymentFrom(testSigner2))
	b.SetFlags(BatchFlagAllOrNothing)
	b.BatchSigners = []BatchSigner{
		{BatchSigner: BatchSignerData{Account: testSigner1, SigningPubKey: "ABC", BatchTxnSignature: "DEF"}},
		{BatchSigner: BatchSignerData{Account: testSigner2, SigningPubKey: "GHI", BatchTxnSignature: "JKL"}},
	}

	// Structural coverage passes — both signers correspond to required inners.
	require.NoError(t, b.Validate())

	// The crypto verification rejects the unverifiable signatures.
	err := b.VerifyBatchSignatures()
	require.Error(t, err)
	require.Contains(t, err.Error(), "temBAD_SIGNATURE")
}
