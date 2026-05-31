package payment

import (
	"testing"

	xrplgoTesting "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
)

// BenchmarkApply_PaymentXRP measures allocs/op for an XRP-to-XRP payment
// flowing through the engine and its metadata builder. Two AccountRoots are
// modified per call; the typed fast path skips the per-side
// `binarycodec.Decode -> map[string]any` step the generic path uses to
// compute PreviousFields / FinalFields.
func BenchmarkApply_PaymentXRP(b *testing.B) {
	b.Run("typed", func(b *testing.B) {
		prev := ledgerfields.SetDisabledForBenchmarks(false)
		defer ledgerfields.SetDisabledForBenchmarks(prev)
		benchPaymentXRP(b)
	})
	b.Run("generic", func(b *testing.B) {
		prev := ledgerfields.SetDisabledForBenchmarks(true)
		defer ledgerfields.SetDisabledForBenchmarks(prev)
		benchPaymentXRP(b)
	})
}

func benchPaymentXRP(b *testing.B) {
	env := xrplgoTesting.NewTestEnv(b)
	alice := xrplgoTesting.NewAccount("alice")
	bob := xrplgoTesting.NewAccount("bob")
	env.FundAmount(alice, uint64(xrplgoTesting.XRP(1_000_000)))
	env.FundAmount(bob, uint64(xrplgoTesting.XRP(1_000_000)))
	env.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payment := Pay(alice, bob, uint64(xrplgoTesting.XRP(1))).Build()
		_ = env.Submit(payment)
	}
}
