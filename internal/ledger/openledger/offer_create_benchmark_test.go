package openledger_test

import (
	"context"
	"encoding/hex"
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

var offerCreateBenchmarkCounts = [...]int{32, 128, 512}

const (
	offerCreateIngressStateHash = "52c0fbdc181bc37ac9749f1c49f733ae93618ce3eefbf96e8a530b72ebb1119f"
	offerCreateIngressTxHash    = "6ccddd7f116fb8b5a36c3a97d8a4cf38fdcb39a621e5bc622bbb4da28d985e87"
	offerCreateRetryStateHash   = "e1b85f5385b0fcaf00d34e2ab59d3fa8b20b5ae7ee895e1572314c835ee5a494"
	offerCreateRetryTxHash      = "c2926a5049d50a4d4360d1f7544d70bf2e91019e61736f5ca5bc090689560918"
)

type offerCreateBenchmarkFixture struct {
	env           *jtx.TestEnv
	parent        *ledger.Ledger
	apply         openledger.ApplyConfig
	taker         *jtx.Account
	receiver      *jtx.Account
	takerSequence uint32
	takerPays     tx.Amount
	takerGets     tx.Amount
	pending       openledger.PendingTx
}

type offerCreateLedgerStore struct {
	parent            *ledger.Ledger
	path              string
	stateRoot, txRoot [32]byte
}

type offerCreateLedgerSnapshot struct {
	stateRoot       [32]byte
	txRoot          [32]byte
	transactionData map[[32]byte][]byte
}

func persistOfferCreateParent(tb testing.TB, parent *ledger.Ledger) offerCreateLedgerStore {
	tb.Helper()
	path := tb.TempDir()
	family, err := backend.OpenPebble(path, 1, 4096)
	require.NoError(tb, err)
	stateMap, err := shamap.NewBacked(shamap.TypeState, family)
	require.NoError(tb, err)
	txMap, err := shamap.NewBacked(shamap.TypeTransaction, family)
	require.NoError(tb, err)
	stateMap.SetLedgerSeq(parent.Sequence())
	txMap.SetLedgerSeq(parent.Sequence())
	copyOfferCreateMap(tb, stateMap, parent.ForEach, shamap.NodeTypeAccountState)
	copyOfferCreateMap(tb, txMap, parent.ForEachTransaction, shamap.NodeTypeTransactionWithMeta)
	store := func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(context.Background(), entries)
	}
	require.NoError(tb, stateMap.StoreDirty(store))
	require.NoError(tb, txMap.StoreDirty(store))
	require.NoError(tb, family.Sync(context.Background()))
	stateRoot, err := stateMap.Hash()
	require.NoError(tb, err)
	txRoot, err := txMap.Hash()
	require.NoError(tb, err)
	wantState, err := parent.StateMapHash()
	require.NoError(tb, err)
	wantTx, err := parent.TxMapHash()
	require.NoError(tb, err)
	require.Equal(tb, wantState, stateRoot)
	require.Equal(tb, wantTx, txRoot)
	require.NoError(tb, family.Close())
	return offerCreateLedgerStore{parent: parent, path: path, stateRoot: stateRoot, txRoot: txRoot}
}

func copyOfferCreateMap(tb testing.TB, dst *shamap.SHAMap, walk func(func([32]byte, []byte) bool) error, nodeType shamap.NodeType) {
	tb.Helper()
	var putErr error
	err := walk(func(key [32]byte, data []byte) bool {
		putErr = dst.PutWithNodeType(key, data, nodeType)
		return putErr == nil
	})
	require.NoError(tb, err)
	require.NoError(tb, putErr)
}

func (s offerCreateLedgerStore) open(tb testing.TB, warm bool) (*ledger.Ledger, *backend.NodeStore) {
	tb.Helper()
	family, err := backend.OpenPebble(s.path, 1, 4096)
	require.NoError(tb, err)
	stateMap, err := shamap.NewFromRootHash(shamap.TypeState, s.stateRoot, family)
	require.NoError(tb, err)
	txMap, err := shamap.NewFromRootHash(shamap.TypeTransaction, s.txRoot, family)
	require.NoError(tb, err)
	if warm {
		require.NoError(tb, stateMap.ForEach(func(*shamap.Item) bool { return true }))
		require.NoError(tb, txMap.ForEach(func(*shamap.Item) bool { return true }))
	}
	reconstructed, err := ledger.NewFromHeader(s.parent.Header(), stateMap, txMap, s.parent.Fees())
	require.NoError(tb, err)
	return reconstructed, family
}

func TestOpenLedger_OfferCreateIngressEquivalent(t *testing.T) {
	firstFixture := newOfferCreateBenchmarkFixture(t, 32)
	firstView, firstOutcome := submitOfferCreate(t, firstFixture, firstFixture.parent)
	firstSnapshot := snapshotOfferCreateLedger(t, firstView.Current())
	secondFixture := newOfferCreateBenchmarkFixture(t, 32)
	secondView, secondOutcome := submitOfferCreate(t, secondFixture, secondFixture.parent)
	secondSnapshot := snapshotOfferCreateLedger(t, secondView.Current())
	store := persistOfferCreateParent(t, firstFixture.parent)
	warmParent, warmFamily := store.open(t, true)
	warmView, warmOutcome := submitOfferCreate(t, firstFixture, warmParent)
	warmSnapshot := snapshotOfferCreateLedger(t, warmView.Current())
	require.NoError(t, warmFamily.Close())
	coldParent, coldFamily := store.open(t, false)
	coldView, coldOutcome := submitOfferCreate(t, firstFixture, coldParent)
	coldSnapshot := snapshotOfferCreateLedger(t, coldView.Current())
	require.NoError(t, coldFamily.Close())

	require.Equal(t, firstFixture.pending.Hash, secondFixture.pending.Hash)
	require.Equal(t, firstOutcome, secondOutcome)
	require.Equal(t, warmOutcome, coldOutcome)
	assertSameLedgerSnapshots(t, warmSnapshot, coldSnapshot)
	assertSameLedgerSnapshots(t, firstSnapshot, secondSnapshot)
	assertGoldenLedgerSnapshot(t, firstSnapshot, offerCreateIngressStateHash, offerCreateIngressTxHash)
	assertGoldenLedgerSnapshot(t, warmSnapshot, offerCreateIngressStateHash, offerCreateIngressTxHash)
	assertGoldenLedgerSnapshot(t, coldSnapshot, offerCreateIngressStateHash, offerCreateIngressTxHash)
}

func TestOpenLedger_OfferCreateRetryReplayEquivalent(t *testing.T) {
	seededFixture := newOfferCreateBenchmarkFixture(t, 32)
	prior, seeded := newOfferCreateRetryTransactions(t, seededFixture)
	store := persistOfferCreateParent(t, seededFixture.parent)
	warmParent, warmFamily := store.open(t, true)
	seededFirst := acceptOfferCreateSeededFirst(t, seededFixture, warmParent, prior, seeded)
	seededFirstSnapshot := snapshotOfferCreateLedger(t, seededFirst.Current())
	require.NoError(t, warmFamily.Close())
	coldParent, coldFamily := store.open(t, false)
	coldSeeded := acceptOfferCreateSeededFirst(t, seededFixture, coldParent, prior, seeded)
	coldSeededSnapshot := snapshotOfferCreateLedger(t, coldSeeded.Current())
	require.NoError(t, coldFamily.Close())

	currentFixture := newOfferCreateBenchmarkFixture(t, 32)
	currentPrior, currentOffer := newOfferCreateRetryTransactions(t, currentFixture)
	nextOpen, err := openledger.New(currentFixture.parent, openledger.Config{Rules: currentFixture.apply.Rules})
	require.NoError(t, err)
	priorOutcome := nextOpen.SubmitDetailed(currentPrior, currentFixture.apply, nil)
	require.Equal(t, openledger.ResultSuccess, priorOutcome.Class)
	require.True(t, priorOutcome.Applied)
	nextOpenRetries := []openledger.PendingTx{currentOffer}
	require.NoError(t, nextOpen.Accept(currentFixture.parent, nil, false, &nextOpenRetries, currentFixture.apply, nil, nil, nil))
	require.Empty(t, nextOpenRetries)
	nextOpenSnapshot := snapshotOfferCreateLedger(t, nextOpen.Current())

	require.Equal(t, prior.Hash, currentPrior.Hash)
	require.Equal(t, seeded.Hash, currentOffer.Hash)
	assertSameLedgerSnapshots(t, seededFirstSnapshot, coldSeededSnapshot)
	assertSameLedgerSnapshots(t, seededFirstSnapshot, nextOpenSnapshot)
	assertGoldenLedgerSnapshot(t, seededFirstSnapshot, offerCreateRetryStateHash, offerCreateRetryTxHash)
	assertGoldenLedgerSnapshot(t, coldSeededSnapshot, offerCreateRetryStateHash, offerCreateRetryTxHash)
	assertGoldenLedgerSnapshot(t, nextOpenSnapshot, offerCreateRetryStateHash, offerCreateRetryTxHash)
}

func submitOfferCreate(tb testing.TB, fixture offerCreateBenchmarkFixture, parent *ledger.Ledger) (*openledger.OpenLedger, openledger.SubmitOutcome) {
	tb.Helper()
	view, err := openledger.New(parent, openledger.Config{Rules: fixture.apply.Rules})
	require.NoError(tb, err)
	outcome := view.SubmitDetailed(fixture.pending, fixture.apply, nil)
	require.Equal(tb, openledger.ResultSuccess, outcome.Class)
	require.True(tb, outcome.Applied)
	return view, outcome
}

func acceptOfferCreateSeededFirst(tb testing.TB, fixture offerCreateBenchmarkFixture, parent *ledger.Ledger, prior, seeded openledger.PendingTx) *openledger.OpenLedger {
	tb.Helper()
	view, err := openledger.New(parent, openledger.Config{Rules: fixture.apply.Rules})
	require.NoError(tb, err)
	retries := []openledger.PendingTx{seeded}
	require.NoError(tb, view.AcceptWithPrecommitContextFrom(context.Background(), parent, []openledger.PendingTx{prior}, nil, true, &retries, fixture.apply, nil, nil, nil, nil, nil))
	require.Empty(tb, retries)
	return view
}

// warm-state preloads reconstructed SHAMaps; cold-state opens fresh SHAMap and NodeStore caches per operation.
func BenchmarkOpenLedger_OfferCreateIngress(b *testing.B) {
	for _, count := range offerCreateBenchmarkCounts {
		fixture := newOfferCreateBenchmarkFixture(b, count)
		store := persistOfferCreateParent(b, fixture.parent)
		name := "self-cross-prefix/offers=" + strconv.Itoa(count)
		b.Run(name+"/warm-state", func(b *testing.B) {
			benchmarkOfferCreateSubmit(b, fixture, fixture.pending, store, false)
		})
		b.Run(name+"/cold-state", func(b *testing.B) {
			benchmarkOfferCreateSubmit(b, fixture, fixture.pending, store, true)
		})
	}
}

func BenchmarkOpenLedger_OfferCreateAccept(b *testing.B) {
	for _, count := range offerCreateBenchmarkCounts {
		fixture := newOfferCreateBenchmarkFixture(b, count)
		prior, seeded := newOfferCreateRetryTransactions(b, fixture)
		store := persistOfferCreateParent(b, fixture.parent)
		name := "self-cross-prefix/offers=" + strconv.Itoa(count)
		for _, mode := range []struct {
			name string
			cold bool
		}{{"warm-state", false}, {"cold-state", true}} {
			b.Run(name+"/seeded-first/"+mode.name, func(b *testing.B) {
				benchmarkOfferCreateAccept(b, fixture, prior, seeded, false, store, mode.cold)
			})
			b.Run(name+"/next-open-current/"+mode.name, func(b *testing.B) {
				benchmarkOfferCreateAccept(b, fixture, prior, seeded, true, store, mode.cold)
			})
		}
	}
}

func newOfferCreateBenchmarkFixture(tb testing.TB, count int) offerCreateBenchmarkFixture {
	tb.Helper()
	makerCount := count / 2

	env := jtx.NewTestEnvBacked(tb)
	env.SetVerifySignatures(true)
	gateway := jtx.NewAccount("offer-benchmark-gateway")
	taker := jtx.NewAccount("offer-benchmark-taker")
	maker := jtx.NewAccount("offer-benchmark-maker")
	receiver := jtx.NewAccount("offer-benchmark-receiver")

	env.FundAmount(gateway, uint64(jtx.XRP(1_000_000)))
	reserveFunding := uint64(jtx.XRP(int64(100_000 + count*100)))
	env.FundAmount(taker, reserveFunding)
	env.FundAmount(maker, reserveFunding)
	env.FundAmount(receiver, uint64(jtx.XRP(10_000)))
	env.Close()

	usd := func(amount float64) tx.Amount { return jtx.USD(gateway, amount) }
	usdBudget := float64(2*count + makerCount + 100)
	env.Trust(taker, usd(usdBudget))
	env.Trust(maker, usd(usdBudget))
	env.Close()
	env.PayIOU(gateway, taker, gateway, "USD", usdBudget)
	env.PayIOU(gateway, maker, gateway, "USD", usdBudget)
	env.Close()

	for range count {
		jtx.RequireTxSuccess(tb, env.Submit(offerbuild.OfferCreate(taker, jtx.XRPTxAmountFromXRP(1), usd(2)).Build()))
	}
	for range makerCount {
		jtx.RequireTxSuccess(tb, env.Submit(offerbuild.OfferCreate(maker, jtx.XRPTxAmountFromXRP(2), usd(1)).Build()))
	}
	env.Close()

	parent := env.LastClosedLedger()
	require.NotNil(tb, parent)
	takerSequence := env.Seq(taker)
	takerPays := usd(float64(makerCount))
	takerGets := jtx.XRPTxAmountFromXRP(float64(count + 2*makerCount))
	transaction := offerbuild.OfferCreate(taker, takerPays, takerGets).
		Sequence(takerSequence).
		Build()
	pending := parseOfferCreatePending(tb, env, transaction, taker)

	return offerCreateBenchmarkFixture{
		env:           env,
		parent:        parent,
		apply:         openledger.ApplyConfig{BaseFee: env.BaseFee(), ReserveBase: env.ReserveBase(), ReserveIncrement: env.ReserveIncrement(), LedgerSequence: parent.Sequence() + 1, NetworkID: 0, Rules: amendment.AllSupportedRules()},
		taker:         taker,
		receiver:      receiver,
		takerSequence: takerSequence,
		takerPays:     takerPays,
		takerGets:     takerGets,
		pending:       pending,
	}
}

func parseOfferCreatePending(tb testing.TB, env *jtx.TestEnv, transaction tx.Transaction, signer *jtx.Account) openledger.PendingTx {
	tb.Helper()
	env.SignWith(transaction, signer)
	txMap, err := transaction.Flatten()
	require.NoError(tb, err)
	hexBlob, err := binarycodec.Encode(txMap)
	require.NoError(tb, err)
	blob, err := hex.DecodeString(hexBlob)
	require.NoError(tb, err)
	pending, err := openledger.ParsePendingTx(blob)
	require.NoError(tb, err)
	return pending
}

func newOfferCreateRetryTransactions(tb testing.TB, fixture offerCreateBenchmarkFixture) (openledger.PendingTx, openledger.PendingTx) {
	tb.Helper()
	prior := parseOfferCreatePending(tb, fixture.env, payment.Pay(fixture.taker, fixture.receiver, uint64(jtx.XRP(1))).
		Sequence(fixture.takerSequence).Build(), fixture.taker)
	seeded := parseOfferCreatePending(tb, fixture.env, offerbuild.OfferCreate(
		fixture.taker,
		fixture.takerPays,
		fixture.takerGets,
	).Sequence(fixture.takerSequence+1).Build(), fixture.taker)
	return prior, seeded
}

func benchmarkOfferCreateSubmit(b *testing.B, fixture offerCreateBenchmarkFixture, pending openledger.PendingTx, store offerCreateLedgerStore, cold bool) {
	b.Helper()
	b.ReportAllocs()
	b.StopTimer()
	var warmParent *ledger.Ledger
	var warmFamily *backend.NodeStore
	if !cold {
		warmParent, warmFamily = store.open(b, true)
	}
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		parent, family := warmParent, warmFamily
		if cold {
			parent, family = store.open(b, false)
		}
		view, err := openledger.New(parent, openledger.Config{Rules: fixture.apply.Rules})
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		outcome := view.SubmitDetailed(pending, fixture.apply, nil)
		b.StopTimer()
		if outcome.Class != openledger.ResultSuccess || !outcome.Applied {
			b.Fatalf("SubmitDetailed: class=%v result=%v applied=%v message=%s", outcome.Class, outcome.Result, outcome.Applied, outcome.Message)
		}
		if cold {
			if err := family.Close(); err != nil {
				b.Fatal(err)
			}
		}
	}
	if warmFamily != nil {
		b.StopTimer()
		if err := warmFamily.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkOfferCreateAccept(
	b *testing.B,
	fixture offerCreateBenchmarkFixture,
	prior, seeded openledger.PendingTx,
	currentReplay bool,
	store offerCreateLedgerStore,
	cold bool,
) {
	b.Helper()
	b.ReportAllocs()
	b.StopTimer()
	var warmParent *ledger.Ledger
	var warmFamily *backend.NodeStore
	if !cold {
		warmParent, warmFamily = store.open(b, true)
	}
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		parent, family := warmParent, warmFamily
		if cold {
			parent, family = store.open(b, false)
		}
		view, err := openledger.New(parent, openledger.Config{Rules: fixture.apply.Rules})
		if err != nil {
			b.Fatal(err)
		}
		if currentReplay {
			outcome := view.SubmitDetailed(prior, fixture.apply, nil)
			if outcome.Class != openledger.ResultSuccess || !outcome.Applied {
				b.Fatalf("prior SubmitDetailed: class=%v result=%v applied=%v message=%s", outcome.Class, outcome.Result, outcome.Applied, outcome.Message)
			}
		}
		retries := []openledger.PendingTx{seeded}
		b.StartTimer()
		var priorSnapshot []openledger.PendingTx
		if !currentReplay {
			priorSnapshot = []openledger.PendingTx{prior}
		}
		if currentReplay {
			err = view.Accept(parent, nil, false, &retries, fixture.apply, nil, nil, nil)
		} else {
			err = view.AcceptWithPrecommitContextFrom(context.Background(), parent, priorSnapshot, nil, true, &retries, fixture.apply, nil, nil, nil, nil, nil)
		}
		b.StopTimer()
		if err != nil {
			b.Fatalf("Accept: %v", err)
		}
		if len(retries) != 0 {
			b.Fatalf("Accept left %d retries", len(retries))
		}
		if cold {
			if err := family.Close(); err != nil {
				b.Fatal(err)
			}
		}
	}
	if warmFamily != nil {
		b.StopTimer()
		if err := warmFamily.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func snapshotOfferCreateLedger(tb testing.TB, view *ledger.Ledger) offerCreateLedgerSnapshot {
	tb.Helper()
	stateRoot, err := view.StateMapHash()
	require.NoError(tb, err)
	txRoot, err := view.TxMapHash()
	require.NoError(tb, err)
	return offerCreateLedgerSnapshot{
		stateRoot:       stateRoot,
		txRoot:          txRoot,
		transactionData: transactionLeaves(tb, view),
	}
}

func assertSameLedgerSnapshots(tb testing.TB, want, got offerCreateLedgerSnapshot) {
	tb.Helper()
	require.Equal(tb, want.stateRoot, got.stateRoot)
	require.Equal(tb, want.txRoot, got.txRoot)
	require.Equal(tb, want.transactionData, got.transactionData)
}

func assertGoldenLedgerSnapshot(tb testing.TB, snapshot offerCreateLedgerSnapshot, stateHash, txHash string) {
	tb.Helper()
	require.Equal(tb, stateHash, hex.EncodeToString(snapshot.stateRoot[:]))
	require.Equal(tb, txHash, hex.EncodeToString(snapshot.txRoot[:]))
}

func transactionLeaves(t testing.TB, view *ledger.Ledger) map[[32]byte][]byte {
	t.Helper()
	leaves := make(map[[32]byte][]byte)
	err := view.ForEachTransaction(func(hash [32]byte, data []byte) bool {
		leaves[hash] = append([]byte(nil), data...)
		return true
	})
	require.NoError(t, err)
	return leaves
}
