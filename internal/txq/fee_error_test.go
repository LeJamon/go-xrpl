package txq

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestApplyBaseFeeFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		sequence   uint32
		txInLedger uint32
		preflight  ter.Result
		want       ter.Result
	}{
		{name: "direct apply", sequence: 1, want: ter.TefEXCEPTION},
		{name: "queue under load", sequence: 1, txInLedger: 100, want: ter.TefEXCEPTION},
		{name: "future sequence", sequence: 2, want: ter.TefEXCEPTION},
		{name: "preflight precedence", sequence: 1, preflight: ter.TemBAD_FEE, want: ter.TemBAD_FEE},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := mustNew(makeAdmissionConfig())
			ctx := &stubApplyCtx{
				seq: 1, balance: 1_000_000, exists: true, baseFee: 10,
				baseFeeErr: ter.Errorf(ter.TefEXCEPTION, "fee calculation failed"),
				txInLedger: test.txInLedger, preflight: test.preflight,
				applyFn: func(tx.Transaction) (ter.Result, bool) {
					t.Fatal("failed fee calculation reached application")
					return ter.TesSUCCESS, true
				},
			}
			result := q.Apply(ctx, &seqTx{seq: test.sequence, fee: "10"}, [32]byte{1}, [20]byte{1})
			require.Equal(t, ApplyResult{Result: test.want}, result)
			require.Zero(t, q.Size())
			require.Empty(t, q.byAccount)
			require.Equal(t, uint32(1), ctx.seq)
			require.Equal(t, uint64(1_000_000), ctx.balance)
		})
	}
}

type partialFeeLedger struct {
	count  uint32
	levels []FeeLevel
}

func (c partialFeeLedger) GetLedgerSequence() uint32           { return 1 }
func (c partialFeeLedger) GetTransactionCount() uint32         { return c.count }
func (c partialFeeLedger) GetTransactionFeeLevels() []FeeLevel { return c.levels }

func TestClosedLedgerCountsTransactionsWithoutFeeSamples(t *testing.T) {
	for _, test := range []struct {
		name   string
		levels []FeeLevel
		median uint64
	}{
		{name: "some failed", levels: []FeeLevel{300_000, 500_000}, median: 400_000},
		{name: "all failed", median: DefaultConfig().MinimumEscalationMultiplier},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, slow := range []bool{false, true} {
				q := mustNew(makeAdmissionConfig())
				control := mustNew(makeAdmissionConfig())
				// Establish the same capacity before exercising a slow close.
				prior := partialFeeLedger{count: 40, levels: []FeeLevel{256}}
				q.ProcessClosedLedger(prior, false)
				control.ProcessClosedLedger(prior, false)
				count := q.ProcessClosedLedger(partialFeeLedger{count: 20, levels: test.levels}, slow)
				control.ProcessClosedLedger(partialFeeLedger{count: 20, levels: make([]FeeLevel, 20)}, slow)
				require.Equal(t, uint64(20), count)
				got, want := q.Metrics(0), control.Metrics(0)
				require.Equal(t, want.TxPerLedger, got.TxPerLedger)
				require.Equal(t, want.TxQMaxSize, got.TxQMaxSize)
				require.Equal(t, test.median, got.MedFeeLevel)
			}
		})
	}
}
