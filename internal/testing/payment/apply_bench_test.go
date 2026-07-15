package payment

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
)

// BenchmarkApply_PaymentXRP measures allocs/op for an XRP-to-XRP payment
// flowing through the engine and its metadata builder. Two AccountRoots are
// modified per call, exercising typed PreviousFields / FinalFields emission.
func BenchmarkApply_PaymentXRP(b *testing.B) {
	benchPaymentXRP(b)
}

func benchPaymentXRP(b *testing.B) {
	env := jtx.NewTestEnv(b)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(1_000_000)))
	env.FundAmount(bob, uint64(jtx.XRP(1_000_000)))
	env.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payment := Pay(alice, bob, uint64(jtx.XRP(1))).Build()
		_ = env.Submit(payment)
	}
}
