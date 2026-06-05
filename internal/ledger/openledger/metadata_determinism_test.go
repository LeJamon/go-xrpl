package openledger_test

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	testenv "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

// Reproduction for the mixed-soak non-deterministic metadata fork (iter-5
// seq-70 / #724 core): two goXRPL nodes running the same binary built the same
// agreed tx-set on the same parent and produced the SAME account_hash but
// DIFFERENT transaction_hash (byte-identical JSON metadata, different binary
// tx-tree). That is invisible to -race (map-iteration order, not a data race)
// and normalized away in JSON. This builds the same set on the same parent N
// times in ONE process (Go re-randomizes map iteration per range), asserting
// transaction_hash is identical every time. A failure pins the divergence to
// metadata generation; bisect the apply_state_table map iterations next.
func TestBuildDeterminism_TransactionHash(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)

	g := testenv.NewAccount("gateway")
	a := testenv.NewAccount("alice")
	b := testenv.NewAccount("bob")
	d := testenv.NewAccount("dave")
	c := testenv.NewAccount("carol")
	env.Fund(g, a, b, d, c)

	usd := func(v float64) tx.Amount { return tx.NewIssuedAmountFromFloat64(v, "USD", g.Address) }
	gSeq, aSeq, bSeq, dSeq, cSeq := env.Seq(g), env.Seq(a), env.Seq(b), env.Seq(d), env.Seq(c)

	// Offer-crossing set: A, B, D each rest a SAME-QUALITY sell-USD offer
	// (10 USD for 10 XRP); C crosses all three with one 30-XRP-for-30-USD
	// buy. Which resting offer C consumes first (and the per-offer fill
	// metadata) must be a deterministic book order — if goXRPL selects
	// candidates via a map, the crossing sequence (hence metadata) varies
	// run-to-run while the final state is identical.
	type spec struct {
		txn    tx.Transaction
		signer *testenv.Account
	}
	sellUSD := func(acct *testenv.Account, seq uint32) tx.Transaction {
		// taker pays 10 XRP, gets 10 USD → owner gives USD, gets XRP.
		return offer.OfferCreate(acct, tx.NewXRPAmount(10_000_000), usd(10)).Sequence(seq).Build()
	}
	specs := []spec{
		{trustset.TrustSet(a, usd(1000)).Sequence(aSeq).Build(), a},
		{trustset.TrustSet(b, usd(1000)).Sequence(bSeq).Build(), b},
		{trustset.TrustSet(d, usd(1000)).Sequence(dSeq).Build(), d},
		{trustset.TrustSet(c, usd(1000)).Sequence(cSeq).Build(), c},
		{payment.PayIssued(g, a, usd(100)).Sequence(gSeq).Build(), g},
		{payment.PayIssued(g, b, usd(100)).Sequence(gSeq + 1).Build(), g},
		{payment.PayIssued(g, d, usd(100)).Sequence(gSeq + 2).Build(), g},
		{sellUSD(a, aSeq+1), a},
		{sellUSD(b, bSeq+1), b},
		{sellUSD(d, dSeq+1), d},
		// C crosses all three: taker pays 30 USD, gets 30 XRP → C gives 30 XRP, gets 30 USD.
		{offer.OfferCreate(c, usd(30), tx.NewXRPAmount(30_000_000)).Sequence(cSeq + 1).Build(), c},
	}

	// Cross-account escrows (scenario focus): each threads the source AND
	// destination owner directories — the multi-owner threading path
	// (apply_state_table threadOwners) suspected of map-order leakage.
	const finishAfter = uint32(4_000_000_000) // far future, won't finish
	esc := func(from, to *testenv.Account, amt int64, seq uint32) tx.Transaction {
		return escrow.EscrowCreate(from, to, amt).FinishAfter(finishAfter).Sequence(seq).Build()
	}
	specs = append(specs,
		spec{esc(a, b, 1_000_000, aSeq+2), a},
		spec{esc(a, c, 1_000_000, aSeq+3), a},
		spec{esc(b, c, 1_000_000, bSeq+2), b},
		spec{esc(b, d, 1_000_000, bSeq+3), b},
		spec{esc(d, a, 1_000_000, dSeq+2), d},
		spec{esc(d, c, 1_000_000, dSeq+3), d},
		spec{esc(c, a, 1_000_000, cSeq+2), c},
		spec{esc(c, b, 1_000_000, cSeq+3), c},
	)

	var pending []openledger.PendingTx
	for _, s := range specs {
		blob := buildSignedBlob(t, env, s.txn, s.signer)
		pt, err := openledger.ParsePendingTx(blob)
		if err != nil {
			t.Fatalf("ParsePendingTx: %v", err)
		}
		pending = append(pending, pt)
	}
	openledger.CanonicalSort(pending, openledger.ComputeSalt(pending))

	env.Close()
	parent := env.LastClosedLedger()
	if parent == nil {
		t.Fatal("no parent ledger")
	}
	closeTime := time.Unix(1_000_000, 0)

	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   parent.Sequence() + 1,
		NetworkID:        0,
		Rules:            amendment.AllSupportedRules(),
		Mode:             openledger.BuildLedgerMode,
	}

	const N = 30
	var firstTx, firstState [32]byte
	var firstCommitted uint32
	for i := 0; i < N; i++ {
		view, err := ledger.NewOpen(parent, closeTime)
		if err != nil {
			t.Fatalf("NewOpen[%d]: %v", i, err)
		}
		var retries []openledger.PendingTx
		if err := openledger.ApplyTxs(view, pending, &retries, cfg); err != nil {
			t.Fatalf("ApplyTxs[%d]: %v", i, err)
		}
		if err := view.Close(closeTime, 0); err != nil {
			t.Fatalf("Close[%d]: %v", i, err)
		}
		th, _ := view.TxMapHash()
		sh, _ := view.StateMapHash()
		cc := view.TxCount()
		if i == 0 {
			firstTx, firstState, firstCommitted = th, sh, cc
			t.Logf("iter0: tx_root=%x state_root=%x committed=%d/%d", th[:8], sh[:8], cc, len(pending))
			continue
		}
		if th != firstTx {
			t.Fatalf("NON-DETERMINISTIC transaction_hash at iter %d: %x != %x (account_hash %x vs %x; committed %d vs %d) — metadata-generation map-iteration leak",
				i, th[:8], firstTx[:8], sh[:8], firstState[:8], cc, firstCommitted)
		}
		if sh != firstState {
			t.Fatalf("NON-DETERMINISTIC account_hash at iter %d: %x != %x", i, sh[:8], firstState[:8])
		}
	}
	t.Logf("all %d in-process builds identical: tx_root=%x state_root=%x committed=%d", N, firstTx[:8], firstState[:8], firstCommitted)
}
