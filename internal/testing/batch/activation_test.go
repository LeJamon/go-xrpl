package batch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
)

func TestBatchWrapperActivationBoundary(t *testing.T) {
	env := newBatchEnv(t)
	env.DisableFeature("fixBatchV1_2")

	alice, bob := jtx.NewAccount("alice"), jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	require.True(t, env.Rules().Enabled(amendment.FeatureBatchV1_1))
	require.False(t, env.Rules().Enabled(amendment.FeatureFixBatchV1_2))

	env.EnableFeature("fixBatchV1_2")
	require.True(t, env.FeatureEnabled("fixBatchV1_2"))
	require.False(t, env.Rules().Enabled(amendment.FeatureFixBatchV1_2))

	current := poisonedBatch(alice, bob, env)
	jtx.RequireTxSuccess(t, env.Submit(current))

	env.Close()
	require.True(t, env.Rules().Enabled(amendment.FeatureFixBatchV1_2))

	next := poisonedBatch(alice, bob, env)
	jtx.RequireTxFail(t, env.Submit(next), "temMALFORMED")
}

func poisonedBatch(alice, bob *jtx.Account, env *jtx.TestEnv) *batchtx.Batch {
	seq := env.Seq(alice)
	batch := NewBatchBuilder(alice, seq, CalcBatchFeeFromEnv(env, 0, 2), batchtx.BatchFlagAllOrNothing).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).
		MustBuild()
	for i := range batch.RawTransactions {
		batch.RawTransactions[i].Wrapper = "Memo"
	}
	return batch
}
