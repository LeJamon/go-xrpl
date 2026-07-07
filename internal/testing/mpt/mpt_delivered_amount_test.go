package mpt_test

import (
	"strconv"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	paybuilder "github.com/LeJamon/go-xrpl/internal/testing/payment"
)

// TestMPT_DeliveredAmount mirrors rippled's DeliveredAmount_test.cpp
// testMPTDeliveredAmountRPC. A direct MPT payment records sfDeliveredAmount in
// metadata only when the amount actually delivered differs from the requested
// Amount (partial payment or transfer fee), and only under fixMPTDeliveredAmount.
// Reference: rippled Payment.cpp:616-621.
func TestMPT_DeliveredAmount(t *testing.T) {
	// setup builds a 25%-transfer-fee MPT with two authorized holders and seeds
	// bob with 10000, returning the tester ready for holder-to-holder payments.
	setup := func(t *testing.T) (*jtx.TestEnv, *mpt.MPTTester, *jtx.Account, *jtx.Account) {
		t.Helper()
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice, bob, carol)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		transferFee := uint16(25_000) // 25%
		mptAlice.Create(mpt.CreateOpts{
			TransferFee: &transferFee,
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanTransfer,
		})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})
		mptAlice.Pay(alice, bob, 10_000)
		return env, mptAlice, bob, carol
	}

	requireMPTDelivered := func(t *testing.T, r jtx.TxResult, m *mpt.MPTTester, want int64) {
		t.Helper()
		jtx.RequireTxSuccess(t, r)
		if r.Metadata == nil || r.Metadata.DeliveredAmount == nil {
			t.Fatal("expected DeliveredAmount in metadata")
		}
		da := r.Metadata.DeliveredAmount
		if !da.IsMPT() {
			t.Fatalf("DeliveredAmount must be an MPT amount, got %#v", da)
		}
		if da.Value() != strconv.FormatInt(want, 10) {
			t.Fatalf("DeliveredAmount value = %s, want %d", da.Value(), want)
		}
		if !strings.EqualFold(da.MPTIssuanceID(), m.IssuanceID()) {
			t.Fatalf("DeliveredAmount mpt_issuance_id = %s, want %s", da.MPTIssuanceID(), m.IssuanceID())
		}
	}

	// Partial payment with no SendMax: required source 1000*1.25 = 1250 exceeds
	// the implicit maxSource (dstAmount 1000), so delivered = 1000/1.25 = 800.
	t.Run("PartialPayment_NoSendMax", func(t *testing.T) {
		env, mptAlice, bob, carol := setup(t)
		r := env.Submit(
			paybuilder.PayIssued(bob, carol, mptAlice.MPTAmount(1000)).
				MPTIssuanceID(mptAlice.IssuanceID()).
				PartialPayment().
				Build(),
		)
		requireMPTDelivered(t, r, mptAlice, 800)
		mptAlice.RequireMPTokenAmount(carol, 800)
	})

	// Partial payment with SendMax 1200 < required 1250: delivered = 1200/1.25 = 960.
	t.Run("PartialPayment_SendMax", func(t *testing.T) {
		env, mptAlice, bob, carol := setup(t)
		r := env.Submit(
			paybuilder.PayIssued(bob, carol, mptAlice.MPTAmount(1000)).
				MPTIssuanceID(mptAlice.IssuanceID()).
				SendMax(mptAlice.MPTAmount(1200)).
				PartialPayment().
				Build(),
		)
		requireMPTDelivered(t, r, mptAlice, 960)
		mptAlice.RequireMPTokenAmount(carol, 960)
	})

	// Exact delivery (SendMax covers the fee, no partial flag): delivered equals
	// the requested Amount, so no DeliveredAmount field is emitted.
	t.Run("ExactDelivery_NoField", func(t *testing.T) {
		env, mptAlice, bob, carol := setup(t)
		r := env.Submit(
			paybuilder.PayIssued(bob, carol, mptAlice.MPTAmount(100)).
				MPTIssuanceID(mptAlice.IssuanceID()).
				SendMax(mptAlice.MPTAmount(125)).
				Build(),
		)
		jtx.RequireTxSuccess(t, r)
		if r.Metadata != nil && r.Metadata.DeliveredAmount != nil {
			t.Fatalf("exact delivery must not set DeliveredAmount, got %v", r.Metadata.DeliveredAmount)
		}
		mptAlice.RequireMPTokenAmount(carol, 100)
	})

	// Issuer-to-holder full payment (no fee) also omits the field.
	t.Run("IssuerToHolderFull_NoField", func(t *testing.T) {
		env, mptAlice, bob, _ := setup(t)
		r := env.Submit(
			paybuilder.PayIssued(mptAlice.Issuer(), bob, mptAlice.MPTAmount(500)).
				MPTIssuanceID(mptAlice.IssuanceID()).
				Build(),
		)
		jtx.RequireTxSuccess(t, r)
		if r.Metadata != nil && r.Metadata.DeliveredAmount != nil {
			t.Fatalf("full issuer payment must not set DeliveredAmount, got %v", r.Metadata.DeliveredAmount)
		}
	})

	// With fixMPTDeliveredAmount disabled, the same partial payment still delivers
	// 800 to the holder but records no DeliveredAmount metadata.
	t.Run("AmendmentDisabled_NoField", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixMPTDeliveredAmount")
		env.Close()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice, bob, carol)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		transferFee := uint16(25_000)
		mptAlice.Create(mpt.CreateOpts{
			TransferFee: &transferFee,
			Flags:       mpt.TfMPTCanTransfer,
		})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})
		mptAlice.Pay(alice, bob, 10_000)

		r := env.Submit(
			paybuilder.PayIssued(bob, carol, mptAlice.MPTAmount(1000)).
				MPTIssuanceID(mptAlice.IssuanceID()).
				PartialPayment().
				Build(),
		)
		jtx.RequireTxSuccess(t, r)
		if r.Metadata != nil && r.Metadata.DeliveredAmount != nil {
			t.Fatalf("amendment off must not set DeliveredAmount, got %v", r.Metadata.DeliveredAmount)
		}
		mptAlice.RequireMPTokenAmount(carol, 800)
	})
}
