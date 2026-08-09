package batch

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TestSerializeBatchDigest pins the digest layout to rippled serializeBatch.
func TestSerializeBatchDigest(t *testing.T) {
	outer := [20]byte{0xAA, 0xBB}
	txids := [][32]byte{
		{0x01},
		{0x02, 0x03},
	}
	flags := BatchFlagAllOrNothing
	sequence := uint32(0x10203040)

	got := serializeBatch(outer, sequence, flags, txids)

	require.Len(t, got, 4+20+4+4+4+len(txids)*32)
	require.Equal(t, protocol.HashPrefixBatch().Bytes(), got[0:4])
	require.Equal(t, outer[:], got[4:24])
	require.Equal(t, sequence, binary.BigEndian.Uint32(got[24:28]))
	require.Equal(t, flags, binary.BigEndian.Uint32(got[28:32]))
	require.Equal(t, uint32(len(txids)), binary.BigEndian.Uint32(got[32:36]))
	require.Equal(t, txids[0][:], got[36:68])
	require.Equal(t, txids[1][:], got[68:100])
	require.Equal(t,
		"42434800AABB000000000000000000000000000000000000102030400001000000000002"+
			"0100000000000000000000000000000000000000000000000000000000000000"+
			"0203000000000000000000000000000000000000000000000000000000000000",
		stringsUpperHex(got),
	)
}

// TestBatchSigningMessageMatchesTxids confirms the batch digest is built from the
// inner transaction IDs in order and is sensitive to the outer flags.
func TestBatchSigningMessageMatchesTxids(t *testing.T) {
	b := NewBatch(testOuter)
	b.AddInnerTransaction(makeTestPayment())
	b.AddInnerTransaction(makeTestPayment())
	b.SetFlags(BatchFlagAllOrNothing)
	b.SetSequence(7)

	ids := make([][32]byte, len(b.RawTransactions))
	for i, rt := range b.RawTransactions {
		id, err := tx.ComputeTransactionHash(rt.RawTransaction.InnerTx)
		require.NoError(t, err)
		ids[i] = id
	}

	msg, err := b.BatchSigningMessage()
	require.NoError(t, err)
	outerID, err := state.DecodeAccountID(testOuter)
	require.NoError(t, err)
	require.Equal(t, serializeBatch(outerID, 7, BatchFlagAllOrNothing, ids), msg)

	// Flipping the outer flag changes the digest.
	b.SetFlags(BatchFlagOnlyOne)
	msg2, err := b.BatchSigningMessage()
	require.NoError(t, err)
	require.NotEqual(t, msg, msg2)
}

func TestBatchSignerSigningDataBindings(t *testing.T) {
	privateKey, publicKey, err := ed25519.Algorithm{}.DeriveKeypair([]byte("batch-v1.1-signer"), false)
	require.NoError(t, err)
	signerAccount, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	require.NoError(t, err)

	b := NewBatch(testOuter)
	b.Fee = "50"
	b.SetSequence(7)
	b.SetFlags(BatchFlagAllOrNothing)
	b.AddInnerTransaction(makeTestPaymentFrom(signerAccount))
	b.AddInnerTransaction(makeTestPayment())

	message, err := b.BatchSigningMessage()
	require.NoError(t, err)
	signerID, err := state.DecodeAccountID(signerAccount)
	require.NoError(t, err)
	signingData := append(append([]byte{}, message...), signerID[:]...)
	signature, err := ed25519.Algorithm{}.Sign(string(signingData), privateKey)
	require.NoError(t, err)
	b.BatchSigners = []BatchSigner{{BatchSigner: BatchSignerData{
		Account:           signerAccount,
		SigningPubKey:     publicKey,
		BatchTxnSignature: signature,
	}}}

	require.NoError(t, b.Validate())
	require.NoError(t, b.VerifyBatchSignatures())
	t.Run("explicit empty Signers", func(t *testing.T) {
		fields, err := b.Flatten()
		require.NoError(t, err)
		batchSigners := fields["BatchSigners"].([]map[string]any)
		batchSigner := batchSigners[0]["BatchSigner"].(map[string]any)
		batchSigner["Signers"] = []map[string]any{}

		encoded, err := binarycodec.Encode(fields)
		require.NoError(t, err)
		blob, err := hex.DecodeString(encoded)
		require.NoError(t, err)
		parsed, err := tx.ParseFromBinary(blob)
		require.NoError(t, err)
		parsedBatch := parsed.(*Batch)

		require.True(t, parsedBatch.BatchSigners[0].BatchSigner.hasSigners())
		require.Error(t, parsedBatch.VerifyBatchSignatures())
	})

	t.Run("outer account", func(t *testing.T) {
		original := b.Account
		b.Account = testSigner2
		require.Error(t, b.VerifyBatchSignatures())
		b.Account = original
	})
	t.Run("sequence", func(t *testing.T) {
		original := *b.Sequence
		b.SetSequence(original + 1)
		require.Error(t, b.VerifyBatchSignatures())
		b.SetSequence(original)
	})
	t.Run("ticket proxy", func(t *testing.T) {
		original := b.Sequence
		zero := uint32(0)
		ticket := uint32(9)
		b.Sequence = &zero
		b.TicketSequence = &ticket
		require.Error(t, b.VerifyBatchSignatures())
		b.Sequence = original
		b.TicketSequence = nil
	})
	t.Run("flags", func(t *testing.T) {
		b.SetFlags(BatchFlagOnlyOne)
		require.Error(t, b.VerifyBatchSignatures())
		b.SetFlags(BatchFlagAllOrNothing)
	})
	t.Run("inner order", func(t *testing.T) {
		b.RawTransactions[0], b.RawTransactions[1] = b.RawTransactions[1], b.RawTransactions[0]
		require.Error(t, b.VerifyBatchSignatures())
		b.RawTransactions[0], b.RawTransactions[1] = b.RawTransactions[1], b.RawTransactions[0]
	})
	t.Run("inner count", func(t *testing.T) {
		originalCount := len(b.RawTransactions)
		b.AddInnerTransaction(makeTestPayment())
		require.Error(t, b.VerifyBatchSignatures())
		b.RawTransactions = b.RawTransactions[:originalCount]
	})
	t.Run("inner transaction ID", func(t *testing.T) {
		common := b.RawTransactions[0].RawTransaction.InnerTx.GetCommon()
		original := *common.Sequence
		common.SetSequence(original + 1)
		require.Error(t, b.VerifyBatchSignatures())
		common.SetSequence(original)
	})
	t.Run("batch signer account", func(t *testing.T) {
		b.BatchSigners[0].BatchSigner.Account = testSigner1
		require.Error(t, b.VerifyBatchSignatures())
		b.BatchSigners[0].BatchSigner.Account = signerAccount
	})
}

func TestTicketedBatchSignerSigningData(t *testing.T) {
	privateKey, publicKey, err := ed25519.Algorithm{}.DeriveKeypair([]byte("batch-v1.1-ticket-signer"), false)
	require.NoError(t, err)
	signerAccount, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	require.NoError(t, err)

	b := NewBatch(testOuter)
	zero := uint32(0)
	ticket := uint32(19)
	b.Sequence = &zero
	b.TicketSequence = &ticket
	b.SetFlags(BatchFlagAllOrNothing)
	b.AddInnerTransaction(makeTestPaymentFrom(signerAccount))
	b.AddInnerTransaction(makeTestPayment())

	message, err := b.BatchSigningMessage()
	require.NoError(t, err)
	outerID, err := state.DecodeAccountID(testOuter)
	require.NoError(t, err)
	txids, err := b.batchTransactionIDs()
	require.NoError(t, err)
	require.Equal(t, serializeBatch(outerID, ticket, BatchFlagAllOrNothing, txids), message)

	signerID, err := state.DecodeAccountID(signerAccount)
	require.NoError(t, err)
	signingData := append(append([]byte{}, message...), signerID[:]...)
	signature, err := ed25519.Algorithm{}.Sign(string(signingData), privateKey)
	require.NoError(t, err)
	b.BatchSigners = []BatchSigner{{BatchSigner: BatchSignerData{
		Account:           signerAccount,
		SigningPubKey:     publicKey,
		BatchTxnSignature: signature,
	}}}

	require.NoError(t, b.Validate())
	require.NoError(t, b.VerifyBatchSignatures())
}

func TestMultiSignedBatchSignerBindsMasterAccount(t *testing.T) {
	privateKey, publicKey, err := ed25519.Algorithm{}.DeriveKeypair([]byte("batch-v1.1-nested-signer"), false)
	require.NoError(t, err)
	nestedAccount, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	require.NoError(t, err)

	b := NewBatch(testOuter)
	b.SetSequence(7)
	b.SetFlags(BatchFlagAllOrNothing)
	b.AddInnerTransaction(makeTestPaymentFrom(testSigner1))
	b.AddInnerTransaction(makeTestPayment())
	message, err := b.BatchSigningMessage()
	require.NoError(t, err)
	masterID, err := state.DecodeAccountID(testSigner1)
	require.NoError(t, err)
	nestedID, err := state.DecodeAccountID(nestedAccount)
	require.NoError(t, err)
	signingData := append(append(append([]byte{}, message...), masterID[:]...), nestedID[:]...)
	signature, err := ed25519.Algorithm{}.Sign(string(signingData), privateKey)
	require.NoError(t, err)
	b.BatchSigners = []BatchSigner{{BatchSigner: BatchSignerData{
		Account: testSigner1,
		Signers: []tx.SignerWrapper{{Signer: tx.Signer{
			Account:       nestedAccount,
			SigningPubKey: publicKey,
			TxnSignature:  signature,
		}}},
	}}}

	require.NoError(t, b.Validate())
	require.NoError(t, b.VerifyBatchSignatures())
	b.BatchSigners[0].BatchSigner.Account = testSigner2
	require.Error(t, b.VerifyBatchSignatures())
}

func stringsUpperHex(value []byte) string {
	return strings.ToUpper(hex.EncodeToString(value))
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
