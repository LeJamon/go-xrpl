package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func batchCacheMutationFixture(t *testing.T) (*batch.Batch, []byte) {
	t.Helper()
	registerBatchCacheTypes()
	privateKey, publicKey, account := cacheMutationKeypair(t, "batch-cache-structural-mutation")

	inner := func(sequence uint32, amount int64) txcore.Transaction {
		transaction := payment.NewPayment(account, account, txcore.NewXRPAmount(amount))
		transaction.Fee = "0"
		transaction.SigningPubKey = ""
		transaction.Sequence = &sequence
		transaction.SetFlags(txcore.TfInnerBatchTxn)
		return transaction
	}
	transaction := batch.NewBatch(account)
	transaction.Fee = "60"
	transaction.SetSequence(1)
	transaction.SetFlags(batch.BatchFlagAllOrNothing)
	transaction.AddInnerTransaction(inner(2, 1))
	transaction.AddInnerTransaction(inner(3, 2))
	transaction.SigningPubKey = publicKey
	var err error
	transaction.TxnSignature, err = sign.SignTransaction(transaction, privateKey)
	require.NoError(t, err)

	blob, err := txcore.SerializeTransaction(transaction)
	require.NoError(t, err)
	parsed, err := txcore.ParseFromBinary(blob)
	require.NoError(t, err)
	parsedBatch, ok := parsed.(*batch.Batch)
	require.True(t, ok)
	return parsedBatch, blob
}

func TestBatchSignatureCacheRejectsStructuralMutations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*batch.Batch)
	}{
		{
			name: "inner replacement",
			mutate: func(transaction *batch.Batch) {
				account := transaction.Account
				sequence := uint32(9)
				replacement := payment.NewPayment(account, account, txcore.NewXRPAmount(3))
				replacement.Fee = "0"
				replacement.SigningPubKey = ""
				replacement.Sequence = &sequence
				replacement.SetFlags(txcore.TfInnerBatchTxn)
				transaction.RawTransactions[0].RawTransaction.InnerTx = replacement
			},
		},
		{
			name: "inner reordering",
			mutate: func(transaction *batch.Batch) {
				transaction.RawTransactions[0], transaction.RawTransactions[1] =
					transaction.RawTransactions[1], transaction.RawTransactions[0]
			},
		},
		{
			name: "nested empty sponsor signature presence",
			mutate: func(transaction *batch.Batch) {
				inner := transaction.RawTransactions[0].RawTransaction.InnerTx
				inner.GetCommon().SponsorSignature = &txcore.SponsorSignature{}
				inner.GetCommon().SponsorSignature.MarkFieldPresent("TxnSignature")
			},
		},
		{
			name:   "outer fee",
			mutate: func(transaction *batch.Batch) { transaction.Fee = "61" },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sigcache.Reset()
			transaction, blob := batchCacheMutationFixture(t)
			require.NoError(t, PrewarmSignature(transaction))
			engine := verifyingEngine(amendment.AllSupportedRules())
			require.Equal(t, ter.TesSUCCESS, engine.verifySignatures(transaction))
			originalID, err := txcore.ComputeCurrentTransactionHash(transaction)
			require.NoError(t, err)
			require.True(t, sigcache.Verified(originalID))
			require.True(t, transaction.GetCommon().SignatureVerified())

			fresh, err := txcore.ParseFromBinary(blob)
			require.NoError(t, err)
			freshBatch, ok := fresh.(*batch.Batch)
			require.True(t, ok)
			require.Equal(t, ter.TesSUCCESS, engine.verifySignatures(freshBatch))

			test.mutate(freshBatch)
			mutatedID, err := txcore.ComputeCurrentTransactionHash(freshBatch)
			require.NoError(t, err)
			require.NotEqual(t, originalID, mutatedID)
			require.Equal(t, ter.TemINVALID, engine.verifySignatures(freshBatch))
			require.False(t, sigcache.Verified(mutatedID))
		})
	}
}
