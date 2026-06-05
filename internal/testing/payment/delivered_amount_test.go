package payment

import (
	"testing"

	testing_ "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

// rippled sets sfDeliveredAmount in Payment metadata only when the delivered
// amount differs from the requested Amount (Payment.cpp:495 actualAmountOut !=
// dstAmount). A FULL delivery must omit it. goXRPL previously set it
// unconditionally, which forked the transaction tree from rippled on full
// (non-partial) IOU payments (identical account_hash, different
// transaction_hash) — the mixed-network seq-~13 divergence.
func TestPayment_DeliveredAmount_OnlyWhenPartial(t *testing.T) {
	env := testing_.NewTestEnv(t)
	gw := testing_.NewAccount("gw")
	alice := testing_.NewAccount("alice")
	bob := testing_.NewAccount("bob")
	env.Fund(gw, alice, bob)

	if r := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build()); !r.Success {
		t.Fatalf("alice trustline: %s", r.Code)
	}
	if r := env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build()); !r.Success {
		t.Fatalf("bob trustline: %s", r.Code)
	}
	if r := env.Submit(PayIssued(gw, alice, tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)).Build()); !r.Success {
		t.Fatalf("gw->alice issue: %s", r.Code)
	}

	// FULL payment: delivered == requested → NO DeliveredAmount.
	full := env.Submit(PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)).Build())
	if !full.Success {
		t.Fatalf("full payment: %s", full.Code)
	}
	if full.Metadata != nil && full.Metadata.DeliveredAmount != nil {
		t.Fatalf("FULL IOU payment must NOT set DeliveredAmount (rippled omits it); got %v",
			full.Metadata.DeliveredAmount)
	}

	// PARTIAL payment: SendMax 20 < Amount 50 with tfPartialPayment → delivers
	// 20 != 50 → DeliveredAmount IS set.
	part := env.Submit(PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)).
		SendMax(tx.NewIssuedAmountFromFloat64(20, "USD", gw.Address)).
		PartialPayment().
		Build())
	if !part.Success {
		t.Fatalf("partial payment: %s", part.Code)
	}
	if part.Metadata == nil || part.Metadata.DeliveredAmount == nil {
		t.Fatal("PARTIAL IOU payment MUST set DeliveredAmount (delivered != requested)")
	}
}
