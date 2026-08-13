package batch

import (
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
)

func TestBatchPaymentStructuralTefResults(t *testing.T) {
	t.Run("independent skips partial payment to new destination", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gateway := jtx.NewAccount("gateway")
		carol := jtx.NewAccount("carol")
		env.Fund(alice, bob, gateway)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		partial := MakeInnerPaymentXRP(alice, carol, 1, seq+1)
		partial.SendMax = amountPtr(tx.NewIssuedAmountFromFloat64(10, "USD", gateway.Address))
		partial.SetPartialPayment()
		valid := MakeInnerPaymentXRP(alice, bob, 5, seq+1)
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagIndependent).
			AddInnerTx(partial).
			AddInnerTx(valid).
			MustBuild()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		result := env.Submit(batch)

		jtx.RequireTxSuccess(t, result)
		require.Equal(t, batchFee, result.Fee)
		env.Close()
		closed := env.LastClosedLedger()
		require.Equal(t, uint32(2), closed.TxCount())
		partialID, err := tx.ComputeTransactionHash(partial)
		require.NoError(t, err)
		partialExists, err := closed.TxExists(partialID)
		require.NoError(t, err)
		require.False(t, partialExists)
		validID, err := tx.ComputeTransactionHash(valid)
		require.NoError(t, err)
		validExists, err := closed.TxExists(validID)
		require.NoError(t, err)
		require.True(t, validExists)
		require.False(t, env.Exists(carol))
		require.Equal(t, seq+2, env.Seq(alice))
		require.Equal(t, preAlice-batchFee-uint64(jtx.XRP(5)), env.Balance(alice))
		require.Equal(t, preBob+uint64(jtx.XRP(5)), env.Balance(bob))
	})

	t.Run("all or nothing rolls back oversized path failure", func(t *testing.T) {
		env := newBatchEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gateway := jtx.NewAccount("gateway")
		env.Fund(alice, bob, gateway)
		env.Close()

		seq := env.Seq(alice)
		batchFee := CalcBatchFeeFromEnv(env, 0, 2)
		valid := MakeInnerPaymentXRP(alice, bob, 5, seq+1)
		oversized := MakeInnerPaymentXRP(alice, bob, 1, seq+2)
		oversized.SendMax = amountPtr(tx.NewIssuedAmountFromFloat64(10, "USD", gateway.Address))
		oversized.Paths = make([][]payment.PathStep, payment.MaxPathSize+1)
		for i := range oversized.Paths {
			oversized.Paths[i] = []payment.PathStep{{Currency: "USD", Issuer: gateway.Address}}
		}
		batch := NewBatchBuilder(alice, seq, batchFee, batchtx.BatchFlagAllOrNothing).
			AddInnerTx(valid).
			AddInnerTx(oversized).
			MustBuild()

		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		result := env.Submit(batch)

		jtx.RequireTxSuccess(t, result)
		require.Equal(t, batchFee, result.Fee)
		require.Empty(t, result.AppliedInnerTransactions)
		require.Equal(t, seq+1, env.Seq(alice))
		require.Equal(t, preAlice-batchFee, env.Balance(alice))
		require.Equal(t, preBob, env.Balance(bob))
	})
}

func amountPtr(amount tx.Amount) *tx.Amount {
	return &amount
}
