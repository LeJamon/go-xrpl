package adaptor

import (
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	ledgerservice "github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/stretchr/testify/require"
)

func TestTransactionIngressMemoCoalescesConcurrentWaiters(t *testing.T) {
	memo := newTransactionIngressMemo(time.Minute, 16, 1024)
	blob := []byte("same transaction input")
	owner := memo.acquire(blob)
	require.True(t, owner.owner)

	const waiterCount = 32
	var wg sync.WaitGroup
	results := make(chan transactionIngressAdmission, waiterCount)
	for range waiterCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticket := memo.acquire(blob)
			require.False(t, ticket.owner)
			require.False(t, ticket.bypass)
			admission, parseBlob, err := memo.wait(ticket.entry)
			require.NoError(t, err)
			require.Equal(t, blob, parseBlob)
			results <- admission
		}()
	}

	want := transactionIngressAdmission{hash: [32]byte{1}, account: [20]byte{2}, roleBearing: true}
	memo.complete(owner.entry, want, nil)
	wg.Wait()
	close(results)
	for got := range results {
		require.Equal(t, want, got)
	}
	entries, bytesHeld := memo.snapshot()
	require.Equal(t, 1, entries)
	require.Equal(t, len(blob), bytesHeld)
}

func TestTransactionIngressMemoBoundsAndExactInput(t *testing.T) {
	memo := newTransactionIngressMemo(time.Minute, 2, 6)
	complete := func(blob []byte) {
		ticket := memo.acquire(blob)
		require.True(t, ticket.owner)
		memo.complete(ticket.entry, transactionIngressAdmission{}, nil)
	}

	complete([]byte("one"))
	complete([]byte("two"))
	entries, bytesHeld := memo.snapshot()
	require.Equal(t, 2, entries)
	require.Equal(t, 6, bytesHeld)

	// The two-entry bound evicts the oldest entry before admitting this third
	// distinct input.
	complete([]byte("new"))
	entries, bytesHeld = memo.snapshot()
	require.Equal(t, 2, entries)
	require.Equal(t, 6, bytesHeld)
	require.Equal(t, uint64(1), memo.evictions.Load())

	// A one-byte mutation must never reuse the prior metadata entry.
	exact := newTransactionIngressMemo(time.Minute, 4, 32)
	raw := []byte("same")
	first := exact.acquire(raw)
	require.True(t, first.owner)
	exact.complete(first.entry, transactionIngressAdmission{hash: [32]byte{8}}, nil)
	original := exact.acquire([]byte("same"))
	require.False(t, original.owner)
	admission, parseBlob, err := exact.wait(original.entry)
	require.NoError(t, err)
	require.Equal(t, [32]byte{8}, admission.hash)
	require.Equal(t, []byte("same"), parseBlob)

	raw[0] = 'x'
	mutated := exact.acquire(raw)
	require.True(t, mutated.owner)
	exact.complete(mutated.entry, transactionIngressAdmission{hash: [32]byte{9}}, nil)
	hit := exact.acquire([]byte("xame"))
	require.False(t, hit.owner)
	require.False(t, hit.bypass)
	admission, _, err = exact.wait(hit.entry)
	require.NoError(t, err)
	require.Equal(t, [32]byte{9}, admission.hash)
}

func TestTransactionIngressMemoSkipsExpiredInflightEntry(t *testing.T) {
	now := time.Unix(1_000, 0)
	memo := newTransactionIngressMemo(10*time.Second, 3, 64)
	memo.now = func() time.Time { return now }

	inflight := memo.acquire([]byte("old"))
	require.True(t, inflight.owner)
	now = now.Add(11 * time.Second)
	fresh := memo.acquire([]byte("fresh"))
	require.True(t, fresh.owner)
	memo.complete(fresh.entry, transactionIngressAdmission{}, nil)

	newEntry := memo.acquire([]byte("new"))
	require.True(t, newEntry.owner)
	require.False(t, memo.acquire([]byte("fresh")).owner)
	entries, bytesHeld := memo.snapshot()
	require.Equal(t, 3, entries)
	require.Equal(t, len("oldfreshnew"), bytesHeld)

	memo.complete(inflight.entry, transactionIngressAdmission{}, nil)
	memo.complete(newEntry.entry, transactionIngressAdmission{}, nil)
}

func TestTransactionIngressMemoBypassesInflightCapacity(t *testing.T) {
	for _, limits := range []struct {
		name           string
		entries, bytes int
	}{{"entries", 2, 64}, {"bytes", 4, 6}} {
		t.Run(limits.name, func(t *testing.T) {
			memo := newTransactionIngressMemo(time.Minute, limits.entries, limits.bytes)
			first := memo.acquire([]byte("one"))
			second := memo.acquire([]byte("two"))
			require.True(t, first.owner)
			require.True(t, second.owner)
			require.True(t, memo.acquire([]byte("new")).bypass)
			entries, bytesHeld := memo.snapshot()
			require.Equal(t, 2, entries)
			require.Equal(t, 6, bytesHeld)
			require.Zero(t, memo.evictions.Load())

			memo.complete(first.entry, transactionIngressAdmission{}, nil)
			next := memo.acquire([]byte("new"))
			require.True(t, next.owner)
			require.Equal(t, uint64(1), memo.evictions.Load())
			memo.complete(second.entry, transactionIngressAdmission{}, nil)
			memo.complete(next.entry, transactionIngressAdmission{}, nil)
			memo.clear()
			entries, bytesHeld = memo.snapshot()
			require.Zero(t, entries)
			require.Zero(t, bytesHeld)
		})
	}
}

func TestTransactionIngressMemoEvictsExpiredAdmission(t *testing.T) {
	now := time.Unix(1_000, 0)
	memo := newTransactionIngressMemo(10*time.Second, 2, 64)
	memo.now = func() time.Time { return now }
	old := memo.acquire([]byte("old"))
	require.True(t, old.owner)
	memo.complete(old.entry, transactionIngressAdmission{}, nil)

	now = now.Add(11 * time.Second)
	fresh := memo.acquire([]byte("fresh"))
	require.True(t, fresh.owner)
	memo.complete(fresh.entry, transactionIngressAdmission{}, nil)
	entries, _ := memo.snapshot()
	require.Equal(t, 1, entries)
	require.Equal(t, uint64(1), memo.evictions.Load())
}

func TestTransactionIngressMemoBypassesOversizedInput(t *testing.T) {
	memo := newTransactionIngressMemo(time.Minute, 4, 8<<20)
	ticket := memo.acquire(make([]byte, transactionIngressMemoMaxEntryBytes+1))
	require.True(t, ticket.bypass)
	entries, bytesHeld := memo.snapshot()
	require.Zero(t, entries)
	require.Zero(t, bytesHeld)
}

func TestTransactionIngressMemoVerifiesDigestCollision(t *testing.T) {
	memo := newTransactionIngressMemo(time.Minute, 4, 64)
	target := []byte("target")
	digest := sha512half.Sum(target)

	collision := &transactionIngressMemoEntry{
		digest:    digest,
		raw:       []byte("other"),
		ready:     make(chan struct{}),
		touchedAt: time.Now(),
	}
	close(collision.ready)
	memo.mu.Lock()
	collision.order = memo.order.PushBack(collision)
	memo.entries[digest] = append(memo.entries[digest], collision)
	memo.entryCount++
	memo.byteCount += len(collision.raw)
	memo.mu.Unlock()

	owner := memo.acquire(target)
	require.True(t, owner.owner)
	memo.complete(owner.entry, transactionIngressAdmission{hash: [32]byte{3}}, nil)
	hit := memo.acquire(target)
	require.False(t, hit.owner)
	admission, _, err := memo.wait(hit.entry)
	require.NoError(t, err)
	require.Equal(t, [32]byte{3}, admission.hash)
}

func TestRouterTransactionIngressMemoSkipsDuplicateParse(t *testing.T) {
	a := newTestAdaptor(t)
	router := newTestRouter(&mockEngine{}, a, nil)
	env := jtx.NewTestEnv(t)
	blob := routerSignedPaymentWithFee(t, env, jtx.MasterAccount(), jtx.NewAccount("ingress-destination"), 10, 1)

	first := router.handleTransaction(routerIngressMessage(blob, 1))
	require.NoError(t, first.submitError)
	second := router.handleTransaction(routerIngressMessage(blob, 2))
	require.NoError(t, second.submitError)

	metrics := router.TransactionIngressMetrics()
	require.Equal(t, uint64(2), metrics.Frames)
	require.Equal(t, uint64(1), metrics.ParseWork)
	require.Equal(t, uint64(1), metrics.InputMisses)
	require.Equal(t, uint64(1), metrics.InputHits)
	require.Equal(t, uint64(1), metrics.Deduped)
}

func TestRouterTransactionIngressMemoCanonicalAliasStillDedupes(t *testing.T) {
	router := newTestRouter(&mockEngine{}, newTestAdaptor(t), nil)
	canonical := benchmarkIngressBatch(t, 1, 2)
	require.GreaterOrEqual(t, len(canonical), 8)
	alias := append([]byte(nil), canonical[3:8]...)
	alias = append(alias, canonical[:3]...)
	alias = append(alias, canonical[8:]...)
	_, err := tx.ParseFromBinary(alias)
	require.NoError(t, err)

	first := router.handleTransaction(routerIngressMessage(alias, 1))
	require.NoError(t, first.submitError)
	second := router.handleTransaction(routerIngressMessage(canonical, 2))
	require.NoError(t, second.submitError)
	require.False(t, second.relayed)
	metrics := router.TransactionIngressMetrics()
	require.Equal(t, uint64(2), metrics.ParseWork)
	require.Equal(t, uint64(1), metrics.Deduped)
}

func TestRouterTransactionIngressMemoCoalescesConcurrentFanout(t *testing.T) {
	a := newTestAdaptor(t)
	router := newTestRouter(&mockEngine{}, a, nil)
	env := jtx.NewTestEnv(t)
	blob := routerSignedPaymentWithFee(t, env, jtx.MasterAccount(), jtx.NewAccount("concurrent-ingress-destination"), 10, 1)

	const fanout = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	for peer := peermanagement.PeerID(1); peer <= fanout; peer++ {
		wg.Add(1)
		go func(peer peermanagement.PeerID) {
			defer wg.Done()
			<-start
			router.handleTransaction(routerIngressMessage(blob, peer))
		}(peer)
	}
	close(start)
	wg.Wait()

	metrics := router.TransactionIngressMetrics()
	require.Equal(t, uint64(fanout), metrics.Frames)
	// A waiter can win canonical admission before the parse owner and needs
	// its own parsed value. Every other frame must reuse the admission data.
	require.GreaterOrEqual(t, metrics.ParseWork, uint64(1))
	require.LessOrEqual(t, metrics.ParseWork, uint64(2))
	require.Equal(t, uint64(fanout-1), metrics.Deduped)
	require.Equal(t, uint64(fanout-1), metrics.InputHits)
}

func TestRouterTransactionIngressMemoPreservesMalformedCharge(t *testing.T) {
	router := newTestRouter(&mockEngine{}, newTestAdaptor(t), nil)
	blob := []byte{0xff, 0x00, 0x01}
	first := router.handleTransaction(routerIngressMessage(blob, 1))
	second := router.handleTransaction(routerIngressMessage(blob, 2))
	require.Equal(t, first.charge, second.charge)
	require.Equal(t, "transaction-invalid-data", first.chargeContext)
	require.Equal(t, "transaction-invalid-data", second.chargeContext)

	metrics := router.TransactionIngressMetrics()
	require.Equal(t, uint64(2), metrics.Frames)
	// Failed parses are removed after coalesced waiters wake, so malformed
	// retransmissions are charged and parsed independently.
	require.Equal(t, uint64(2), metrics.ParseWork)
	require.Equal(t, uint64(2), metrics.ParseErrors)
	require.Equal(t, uint64(2), metrics.InputMisses)
	require.Zero(t, metrics.InputHits)
	require.Zero(t, metrics.Deduped)
}

func BenchmarkRouterTransactionIngressMemo(b *testing.B) {
	fixtures := []struct {
		name  string
		blobs [][]byte
	}{
		{name: "ordinary", blobs: [][]byte{benchmarkIngressPayment(b, 1)}},
		{name: "batch2", blobs: [][]byte{benchmarkIngressBatch(b, 1, 2)}},
		{name: "batch8", blobs: [][]byte{benchmarkIngressBatch(b, 1, 8)}},
		{name: "mixed", blobs: benchmarkIngressMixed(b)},
	}
	for _, fixture := range fixtures {
		for _, memoEnabled := range []bool{false, true} {
			name := fmt.Sprintf("%s/memo-%t", fixture.name, memoEnabled)
			b.Run(name, func(b *testing.B) {
				router := newBenchmarkIngressRouter(b, memoEnabled)
				for i, blob := range fixture.blobs {
					result := router.handleTransaction(routerIngressMessage(blob, peermanagement.PeerID(i+1)))
					if result.submitError != nil || result.submitResult != openledger.ResultSuccess {
						b.Fatalf("benchmark fixture %s was not admitted: result=%v error=%v", fixture.name, result.submitResult, result.submitError)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for peer := 0; peer < 16; peer++ {
						blob := fixture.blobs[peer%len(fixture.blobs)]
						router.handleTransaction(routerIngressMessage(blob, peermanagement.PeerID(peer+1)))
					}
				}
				b.StopTimer()
				metrics := router.TransactionIngressMetrics()
				if metrics.MemoPayloadBytes > transactionIngressMemoMaxBytes {
					b.Fatalf("memo payload bytes %d exceed bound %d", metrics.MemoPayloadBytes, transactionIngressMemoMaxBytes)
				}
				b.ReportMetric(float64(metrics.MemoPayloadBytes), "memo_payload_bytes")
			})
		}
	}
}

func newBenchmarkIngressRouter(b *testing.B, memoEnabled bool) *Router {
	b.Helper()
	genesisConfig := genesis.DefaultConfig()
	genesisConfig.Amendments = append(genesisConfig.Amendments, amendment.FeatureBatchV1_1)
	destination := benchmarkIngressDestination()
	genesisConfig.InitialAccounts = []genesis.InitialAccount{{
		Address: destination.Address,
		Balance: 1_000_000_000,
	}}
	svc, err := ledgerservice.New(ledgerservice.Config{
		Standalone:    true,
		GenesisConfig: genesisConfig,
		Startup:       ledgerservice.StartupConfig{Mode: ledgerservice.StartupFresh},
	})
	if err != nil {
		b.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(svc.Stop)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	if err != nil {
		b.Fatal(err)
	}
	router := newTestRouter(&mockEngine{}, New(Config{
		LedgerService: svc,
		Identity:      identity,
	}), nil)
	if !memoEnabled {
		router.txIngressMemo = nil
	}
	return router
}

func benchmarkIngressMixed(b testing.TB) [][]byte {
	return [][]byte{
		benchmarkIngressPayment(b, 1),
		benchmarkIngressBatch(b, 2, 2),
		benchmarkIngressPayment(b, 3),
		benchmarkIngressBatch(b, 4, 8),
	}
}

func benchmarkIngressPayment(b testing.TB, sequence uint32) []byte {
	b.Helper()
	env := jtx.NewTestEnv(b)
	env.SetVerifySignatures(true)
	sender := jtx.MasterAccount()
	txn := payment.Pay(sender, benchmarkIngressDestination(), 100_000_000).
		Fee(10).
		Sequence(sequence).
		Build()
	env.SignWith(txn, sender)
	txMap, err := txn.Flatten()
	if err != nil {
		b.Fatal(err)
	}
	hexStr, err := binarycodec.Encode(txMap)
	if err != nil {
		b.Fatal(err)
	}
	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		b.Fatal(err)
	}
	return blob
}

func benchmarkIngressDestination() *jtx.Account {
	return jtx.NewAccount("router-ingress-benchmark-destination")
}

func benchmarkIngressBatch(b testing.TB, sequence uint32, innerCount int) []byte {
	b.Helper()
	env := jtx.NewTestEnv(b)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	builder := batchtest.NewBatchBuilder(master, sequence, batchtest.CalcBatchFeeFromEnv(env, 0, uint32(innerCount)), batchtx.BatchFlagAllOrNothing)
	for inner := 0; inner < innerCount; inner++ {
		builder.AddInnerTx(batchtest.MakeInnerAccountSet(master, sequence+uint32(inner)+1))
	}
	batch := builder.MustBuild()
	env.SignWith(batch, master)
	txMap, err := batch.Flatten()
	if err != nil {
		b.Fatal(err)
	}
	hexStr, err := binarycodec.Encode(txMap)
	if err != nil {
		b.Fatal(err)
	}
	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		b.Fatal(err)
	}
	return blob
}

func routerIngressMessage(blob []byte, peer peermanagement.PeerID) *peermanagement.InboundMessage {
	return &peermanagement.InboundMessage{
		PeerID: peer,
		Type:   message.TypeTransaction,
		Tx: &message.Transaction{
			RawTransaction: blob,
			Status:         message.TxStatusNew,
		},
	}
}
