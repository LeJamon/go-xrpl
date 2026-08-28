package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestBatchSigCacheCoversEverySignature(t *testing.T) {
	batch.Register()
	payment.Register()
	outerPrivate, outerPublic, err := ed25519.Algorithm{}.DeriveKeypair([]byte("batch-outer-0001"), false)
	require.NoError(t, err)
	outerAccount, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(outerPublic)
	require.NoError(t, err)
	signerPrivate, signerPublic, err := ed25519.Algorithm{}.DeriveKeypair([]byte("batch-signr-0001"), false)
	require.NoError(t, err)
	signerAccount, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(signerPublic)
	require.NoError(t, err)

	makeInner := func(account, destination string, sequence uint32) txcore.Transaction {
		inner := payment.NewPayment(account, destination, txcore.NewXRPAmount(1))
		inner.Fee = "0"
		inner.SigningPubKey = ""
		inner.Sequence = &sequence
		inner.SetFlags(txcore.TfInnerBatchTxn)
		return inner
	}
	b := batch.NewBatch(outerAccount)
	b.Fee = "60"
	b.SetSequence(1)
	b.SetFlags(batch.BatchFlagAllOrNothing)
	b.AddInnerTransaction(makeInner(signerAccount, outerAccount, 1))
	b.AddInnerTransaction(makeInner(outerAccount, signerAccount, 2))
	message, err := b.BatchSigningMessage()
	require.NoError(t, err)
	signerID, err := state.DecodeAccountID(signerAccount)
	require.NoError(t, err)
	batchSignature, err := ed25519.Algorithm{}.Sign(string(append(message, signerID[:]...)), signerPrivate)
	require.NoError(t, err)
	b.BatchSigners = []batch.BatchSigner{{BatchSigner: batch.BatchSignerData{
		Account:           signerAccount,
		SigningPubKey:     signerPublic,
		BatchTxnSignature: batchSignature,
	}}}
	b.SigningPubKey = outerPublic
	b.TxnSignature, err = sign.SignTransaction(b, outerPrivate)
	require.NoError(t, err)
	blob, err := txcore.SerializeTransaction(b)
	require.NoError(t, err)
	parsed, err := txcore.ParseFromBinary(blob)
	require.NoError(t, err)
	parsedBatch := parsed.(*batch.Batch)
	sigcache.Reset()
	engine := verifyingEngine(amendment.AllSupportedRules())
	require.Equal(t, ter.TesSUCCESS, engine.verifySignatures(parsedBatch))
	id, err := txcore.ComputeTransactionHash(parsedBatch)
	require.NoError(t, err)
	require.True(t, sigcache.Verified(id))
	require.True(t, parsedBatch.GetCommon().SignatureVerified())
	require.Equal(t, ter.TesSUCCESS, engine.verifySignatures(parsedBatch), "unchanged transaction must reuse its whole-signature verdict")

	innerSequence := uint32(9)
	_, _, replacementInnerAccount := cacheMutationKeypair(t, "batch-cache-inner-account")
	_, _, innerDelegate := cacheMutationKeypair(t, "batch-cache-inner-delegate")
	innerMutations := []struct {
		name   string
		mutate func(txcore.Transaction)
	}{
		{name: "amount", mutate: func(inner txcore.Transaction) {
			inner.(*payment.Payment).Amount = txcore.NewXRPAmount(2)
		}},
		{name: "fee", mutate: func(inner txcore.Transaction) { inner.GetCommon().Fee = "1" }},
		{name: "sequence", mutate: func(inner txcore.Transaction) { inner.GetCommon().Sequence = &innerSequence }},
		{name: "account", mutate: func(inner txcore.Transaction) { inner.GetCommon().Account = replacementInnerAccount }},
		{name: "delegate", mutate: func(inner txcore.Transaction) { inner.GetCommon().Delegate = innerDelegate }},
	}
	for _, test := range innerMutations {
		t.Run("inner "+test.name, func(t *testing.T) {
			sigcache.Reset()
			fresh, err := txcore.ParseFromBinary(blob)
			require.NoError(t, err)
			freshBatch := fresh.(*batch.Batch)
			require.Equal(t, ter.TesSUCCESS, engine.verifySignatures(freshBatch))
			test.mutate(freshBatch.RawTransactions[0].RawTransaction.InnerTx)
			require.Equal(t, ter.TemINVALID, engine.verifySignatures(freshBatch))
		})
	}

	parsedBatch.BatchSigners[0].BatchSigner.BatchTxnSignature = "00"
	require.Equal(t, ter.TemINVALID, engine.verifySignatures(parsedBatch), "a cached verdict must not authorize a mutated BatchSigner")
	mutatedID, err := txcore.ComputeCurrentTransactionHash(parsedBatch)
	require.NoError(t, err)
	require.NotEqual(t, id, mutatedID)
	require.False(t, sigcache.Verified(mutatedID))
}
