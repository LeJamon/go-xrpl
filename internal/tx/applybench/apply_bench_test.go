// Package applybench_test measures the per-transaction cost of the ingress
// apply path: binary parse, signature verification, preflight, preclaim,
// apply, invariants and metadata, against a prepared parent ledger.
package applybench_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/protocol"
)

type applyFixture struct {
	parent *ledger.Ledger
	config openledger.ApplyConfig
	blob   []byte
}

type applyKind struct {
	name  string
	build func(tb testing.TB) applyFixture
}

var applyKinds = []applyKind{
	{"payment-xrp", buildPaymentXRP},
	{"payment-iou", buildPaymentIOU},
	{"offer-create-cross", buildOfferCreateCross},
	{"nftoken-mint", buildNFTokenMint},
}

// BenchmarkApplyTransaction applies one signed transaction blob to a fresh
// open view of a prepared parent ledger. Each iteration parses the blob anew
// and starts from a cold signature cache, so every per-transaction cost of
// first-seen ingress is measured. The view-new sub-benchmark is the fixed
// per-iteration overhead (fresh view, cache reset) to subtract; the timer is
// not toggled per iteration because doing so stops the world and dominates
// the measurement.
func BenchmarkApplyTransaction(b *testing.B) {
	for _, kind := range applyKinds {
		fixture := kind.build(b)
		b.Run(kind.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sigcache.Reset()
				_, outcome := fixture.apply(b, fixture.newView(b))
				if !outcome.Applied {
					b.Fatalf("%s not applied: class=%v %s", kind.name, outcome.Class, outcome.Message)
				}
			}
		})
		if kind.name == applyKinds[0].name {
			b.Run("view-new", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					sigcache.Reset()
					fixture.newView(b)
				}
			})
		}
	}
}

// TestApplyFixtures keeps the benchmark fixtures honest under plain go test.
func TestApplyFixtures(t *testing.T) {
	for _, kind := range applyKinds {
		t.Run(kind.name, func(t *testing.T) {
			fixture := kind.build(t)
			view := fixture.newView(t)
			hash, outcome := fixture.apply(t, view)
			if !outcome.Applied {
				t.Fatalf("not applied: class=%v %s", outcome.Class, outcome.Message)
			}
			if got := appliedResult(t, view.Current(), hash); got != "tesSUCCESS" {
				t.Fatalf("result %s, want tesSUCCESS", got)
			}
		})
	}
}

func (f applyFixture) newView(tb testing.TB) *openledger.OpenLedger {
	tb.Helper()
	view, err := openledger.New(f.parent, openledger.Config{Rules: f.config.Rules})
	if err != nil {
		tb.Fatal(err)
	}
	return view
}

func (f applyFixture) apply(tb testing.TB, view *openledger.OpenLedger) ([32]byte, openledger.SubmitOutcome) {
	tb.Helper()
	pending, err := openledger.ParsePendingTx(f.blob)
	if err != nil {
		tb.Fatal(err)
	}
	return pending.Hash, view.SubmitDetailed(pending, f.config, nil)
}

func appliedResult(tb testing.TB, view *ledger.Ledger, hash [32]byte) string {
	tb.Helper()
	data, found, err := view.GetTransaction(hash)
	if err != nil || !found {
		tb.Fatalf("transaction %x not in view: found=%v err=%v", hash, found, err)
	}
	_, meta, err := tx.SplitTxWithMetaBlobStrict(data)
	if err != nil {
		tb.Fatal(err)
	}
	fields, err := binarycodec.DecodeBytes(meta)
	if err != nil {
		tb.Fatal(err)
	}
	result, _ := fields["TransactionResult"].(string)
	return result
}

// finish closes the env and signs transaction for the resulting parent ledger.
func finish(tb testing.TB, env *jtx.TestEnv, signer *jtx.Account, transaction tx.Transaction) applyFixture {
	tb.Helper()
	env.Close()
	parent := env.LastClosedLedger()
	common := transaction.GetCommon()
	if common.Sequence == nil {
		seq := env.Seq(signer)
		common.Sequence = &seq
	}
	if common.Fee == "" {
		common.Fee = "10"
	}
	env.SetVerifySignatures(true)
	env.SignWith(transaction, signer)
	blob, err := tx.SerializeTransaction(transaction)
	if err != nil {
		tb.Fatal(err)
	}
	rules := env.Ledger().Rules()
	if rules == nil {
		rules = amendment.AllSupportedRules()
	}
	return applyFixture{
		parent: parent,
		blob:   blob,
		config: openledger.ApplyConfig{
			BaseFee:          env.BaseFee(),
			ReserveBase:      env.ReserveBase(),
			ReserveIncrement: env.ReserveIncrement(),
			LedgerSequence:   parent.Sequence() + 1,
			ParentCloseTime:  protocol.ToRippleTime(parent.CloseTime()),
			Rules:            rules,
		},
	}
}

func buildPaymentXRP(tb testing.TB) applyFixture {
	env := jtx.NewTestEnv(tb)
	alice, bob := jtx.NewAccount("alice"), jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10_000)))
	env.FundAmount(bob, uint64(jtx.XRP(10_000)))
	return finish(tb, env, alice, payment.Pay(alice, bob, uint64(jtx.XRP(1))).Build())
}

func buildPaymentIOU(tb testing.TB) applyFixture {
	env := jtx.NewTestEnv(tb)
	gw, bob := jtx.NewAccount("gateway"), jtx.NewAccount("bob")
	env.FundAmount(gw, uint64(jtx.XRP(10_000)))
	env.FundAmount(bob, uint64(jtx.XRP(10_000)))
	env.Close()
	env.Trust(bob, jtx.USD(gw, 1_000))
	return finish(tb, env, gw, payment.PayIssued(gw, bob, jtx.USD(gw, 10)).Build())
}

func buildOfferCreateCross(tb testing.TB) applyFixture {
	env := jtx.NewTestEnv(tb)
	gw, maker, taker := jtx.NewAccount("gateway"), jtx.NewAccount("maker"), jtx.NewAccount("taker")
	env.FundAmount(gw, uint64(jtx.XRP(10_000)))
	env.FundAmount(maker, uint64(jtx.XRP(10_000)))
	env.FundAmount(taker, uint64(jtx.XRP(10_000)))
	env.Close()
	env.Trust(maker, jtx.USD(gw, 1_000))
	env.Trust(taker, jtx.USD(gw, 1_000))
	env.Close()
	env.PayIOU(gw, maker, gw, "USD", 500)
	env.Close()
	jtx.RequireTxSuccess(tb, env.Submit(offerbuild.OfferCreate(maker, jtx.XRPTxAmountFromXRP(100), jtx.USD(gw, 100)).Build()))
	return finish(tb, env, taker, offerbuild.OfferCreate(taker, jtx.USD(gw, 100), jtx.XRPTxAmountFromXRP(100)).Build())
}

func buildNFTokenMint(tb testing.TB) applyFixture {
	env := jtx.NewTestEnv(tb)
	minter := jtx.NewAccount("minter")
	env.FundAmount(minter, uint64(jtx.XRP(10_000)))
	return finish(tb, env, minter, nft.NFTokenMint(minter, 7).URI("ipfs://bench").Build())
}
