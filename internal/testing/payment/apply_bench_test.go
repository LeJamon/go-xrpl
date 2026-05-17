package payment

import (
	"testing"

	xrplgoTesting "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx/ledgerfields"
)

// BenchmarkApply_PaymentXRP measures allocs/op for an XRP-to-XRP payment
// flowing through the engine and its metadata builder. Two AccountRoots are
// modified per call; each one used to be decoded twice via
// `binarycodec.Decode -> map[string]any` to compute PreviousFields /
// FinalFields. The typed AccountRoot fast path skips both intermediate maps
// and emits only the fields that actually appear in metadata.
//
// Run with `-benchtime=2s -count=3` to smooth out the noisy submit/close
// machinery; allocs/op is the load-bearing number for the issue acceptance.
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
