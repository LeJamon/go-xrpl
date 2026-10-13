package service

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	ledgerpkg "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type serviceBatchAcceptanceCase struct {
	name         string
	agreed       [][]byte
	agreedHash   [][32]byte
	innerParents [][32]byte
	innerHashes  [][32]byte
	local        []byte
	localHash    [32]byte
	withRetry    bool
}

// BenchmarkService_BatchConsensusAcceptance measures the full consensus
// acceptance path with real signed Batch blobs. The fixture, signatures, and
// expected hashes are prepared before the timed region; each round starts a
// fresh service over the same persisted parent so no parsed transaction is
// reused between rounds.
func BenchmarkService_BatchConsensusAcceptance(b *testing.B) {
	b.StopTimer()
	profileAcceptance := os.Getenv("BENCH_PROFILE_ACCEPTANCE") == "1"
	profileContext := pprof.WithLabels(b.Context(), pprof.Labels("phase", "acceptance"))
	flatEnabled := benchmarkFlatStateEnabled()
	nodeCacheBytes, flatCacheBytes := benchmarkServiceCacheBytes(b, flatEnabled)
	fixture := newBatchBenchmarkRefreshFixture(b, 4_096, nodeCacheBytes)
	nodeMemory, err := fixture.options.MemoryCommitment(2)
	require.NoError(b, err)
	env := jtx.NewTestEnv(b)
	env.SetVerifySignatures(true)
	cases := newServiceBatchAcceptanceCases(b, env)
	b.StartTimer()

	// Ledger hashes bind the state, canonical transactions, and their metadata.
	wantLedgerHashes := map[string]string{
		"batch-2":                  "7F7B5E69BE9C4372637CF6DDFAEB595714F5BBC1F5A6A6BB639AE6E2B9D415F3",
		"batch-4":                  "29BFE33B05F24E829EB44493F62E8AFDEF4ECCF975656F8A7FE8001221303EC2",
		"batch-8":                  "9B2ADD1F8EFA124CF842ECF8B4E7BAC002A515041ADF60BF1A99965A940A915C",
		"ordinary-plus-batch":      "F211DEF4F0E628527BEED9A7DFD514534367C66B240FD9F836CD47C139F80C28",
		"batch-4-successor-retry":  "EEA0892FD3AB9F0FD8B13B6BFFEA4B0068B3F00A19107DC61B5333141DD21FEA",
		"load-32-batches-8-inners": "C35A7221D18221C233EB89141AF9B5AC7F87F0C8EC4C75580B8F909B567E8911",
		"load-32-batches-mixed":    "D871E66C19AF450F47A23486AEE43DBB48C40DB8BC10CD7A014C9E7B81F44029",
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var bytesPerRound int
			for _, blob := range tc.agreed {
				bytesPerRound += len(blob)
			}
			b.SetBytes(int64(bytesPerRound))
			var (
				total              = make([]time.Duration, 0, b.N)
				parse              = make([]time.Duration, 0, b.N)
				apply              = make([]time.Duration, 0, b.N)
				nextOpen           = make([]time.Duration, 0, b.N)
				build              = make([]time.Duration, 0, b.N)
				closeHash          = make([]time.Duration, 0, b.N)
				gcCycles           uint64
				gcPause            uint64
				flatHits           uint64
				flatFallbacks      uint64
				flatMemTableTarget int64
				wantHash           [32]byte
				wantStateRoot      [32]byte
				wantTxRoot         [32]byte
				haveHash           bool
			)

			b.ResetTimer()
			for round := range b.N {
				b.StopTimer()
				logger := &batchAcceptanceBenchmarkLogger{}
				svc, parent, cleanup := newBatchConsensusAcceptanceService(b, fixture, logger, flatCacheBytes)
				if svc.config.FlatState != nil {
					flatMemTableTarget = svc.config.FlatState.Stats().MemTableTargetBytes
				}
				if tc.withRetry {
					outcome, err := svc.SubmitOpenLedgerTxDetailed(tc.local, true)
					require.NoError(b, err)
					require.False(b, outcome.Applied, "successor transaction applied before its Batch")
					require.NotEqual(b, openledger.ResultFailure, outcome.Class)
				}

				beforeStats := svc.config.Execution.Stats()
				var memBefore, memAfter runtime.MemStats
				runtime.ReadMemStats(&memBefore)
				if profileAcceptance {
					pprof.SetGoroutineLabels(profileContext)
				}
				b.StartTimer()
				_, err := svc.AcceptConsensusResult(
					b.Context(),
					parent,
					tc.agreed,
					nil,
					parent.CloseTime().Add(10*time.Second),
					true,
				)
				b.StopTimer()
				if profileAcceptance {
					pprof.SetGoroutineLabels(b.Context())
				}
				runtime.ReadMemStats(&memAfter)
				gcCycles += uint64(memAfter.NumGC - memBefore.NumGC)
				gcPause += memAfter.PauseTotalNs - memBefore.PauseTotalNs
				require.NoError(b, err)
				afterStats := svc.config.Execution.Stats()
				if flatEnabled {
					hits := afterStats.FlatHits - beforeStats.FlatHits
					fallbacks := afterStats.FlatFallbacks - beforeStats.FlatFallbacks
					require.Positive(b, hits, "flat benchmark acceptance did not read the seeded flat state")
					flatHits += hits
					flatFallbacks += fallbacks
				}
				sample, ok := logger.latest()
				if !ok {
					b.Fatal("acceptance did not emit consensus acceptance timings")
				}
				total = append(total, sample.total)
				parse = append(parse, sample.parse)
				apply = append(apply, sample.apply)
				nextOpen = append(nextOpen, sample.nextOpen)
				build = append(build, sample.build)
				closeHash = append(closeHash, sample.closeHash)

				closed := svc.GetClosedLedger()
				if !haveHash {
					require.Equal(b, wantLedgerHashes[tc.name], fmt.Sprintf("%X", closed.Hash()), "closed ledger differs from the baseline")
					wantHash = closed.Hash()
					wantStateRoot = closed.Header().AccountHash
					wantTxRoot = closed.Header().TxHash
					haveHash = true
				} else {
					require.Equal(b, wantHash, closed.Hash(), "round %d changed closed ledger hash", round)
					require.Equal(b, wantStateRoot, closed.Header().AccountHash, "round %d changed state root", round)
					require.Equal(b, wantTxRoot, closed.Header().TxHash, "round %d changed transaction root", round)
				}
				require.Equal(b, uint32(len(tc.agreed)+len(tc.innerHashes)), closed.TxCount())
				assertBatchAcceptanceEffects(b, closed, tc)
				if tc.withRetry {
					exists, err := svc.openLedgerView.Current().TxExists(tc.localHash)
					require.NoError(b, err)
					require.True(b, exists, "successor transaction was not prepared in next open ledger")
				}
				// Stop and database cleanup remain outside the timed region.
				cleanup()
			}

			reportConsensusAcceptanceQuantiles(b, "accept-total", total)
			reportConsensusAcceptanceQuantiles(b, "parse", parse)
			reportConsensusAcceptanceQuantiles(b, "transaction-apply", apply)
			reportConsensusAcceptanceQuantiles(b, "next-open", nextOpen)
			reportConsensusAcceptanceQuantiles(b, "build", build)
			reportConsensusAcceptanceQuantiles(b, "close-hash", closeHash)
			b.ReportMetric(float64(nodeMemory), "nodestore-steady-memory-commitment-bytes")
			b.ReportMetric(float64(nodeCacheBytes), "nodestore-block-cache-bytes")
			b.ReportMetric(float64(flatCacheBytes), "flat-block-cache-bytes")
			b.ReportMetric(float64(gcCycles)/float64(b.N), "GCs/op")
			b.ReportMetric(float64(gcPause)/float64(b.N), "gc-pause-ns/op")
			if flatEnabled {
				b.ReportMetric(float64(flatHits)/float64(b.N), "flat-hits/op")
				b.ReportMetric(float64(flatFallbacks)/float64(b.N), "root-fallbacks/op")
				b.ReportMetric(float64(8<<20), "flat-memtable-limit-bytes")
				b.ReportMetric(float64(flatMemTableTarget), "flat-memtable-target-bytes")
			}
		})
	}
}

func newServiceBatchAcceptanceCases(b testing.TB, env *jtx.TestEnv) []serviceBatchAcceptanceCase {
	b.Helper()
	master := jtx.MasterAccount()
	destination := jtx.NewAccount("service-batch-benchmark-destination")
	cases := make([]serviceBatchAcceptanceCase, 0, 7)
	for _, count := range []int{2, 4, 8} {
		batchBlob, batchHash, innerHashes := buildServiceBatchBlob(b, env, master, destination, count, 1, 2, false)
		cases = append(cases, serviceBatchAcceptanceCase{
			name:         fmt.Sprintf("batch-%d", count),
			agreed:       [][]byte{batchBlob},
			agreedHash:   [][32]byte{batchHash},
			innerParents: repeatBatchParent(batchHash, len(innerHashes)),
			innerHashes:  innerHashes,
		})
	}

	ordinary, ordinaryHash := buildServicePaymentBlob(b, env, master, jtx.NewAccount("service-batch-benchmark-ordinary-destination"), 250_000_000, 1)
	mixedBatch, mixedBatchHash, mixedInnerHashes := buildServiceBatchBlob(b, env, master, destination, 4, 2, 3, false)
	cases = append(cases, serviceBatchAcceptanceCase{
		name:         "ordinary-plus-batch",
		agreed:       [][]byte{ordinary, mixedBatch},
		agreedHash:   [][32]byte{ordinaryHash, mixedBatchHash},
		innerParents: repeatBatchParent(mixedBatchHash, len(mixedInnerHashes)),
		innerHashes:  mixedInnerHashes,
	})

	retryBatch, retryBatchHash, retryInnerHashes := buildServiceBatchBlob(b, env, master, destination, 4, 1, 2, true)
	local, localHash := buildServicePaymentBlob(b, env, master, jtx.NewAccount("service-batch-benchmark-successor"), 250_000_000, 6)
	cases = append(cases, serviceBatchAcceptanceCase{
		name:         "batch-4-successor-retry",
		agreed:       [][]byte{retryBatch},
		agreedHash:   [][32]byte{retryBatchHash},
		innerParents: repeatBatchParent(retryBatchHash, len(retryInnerHashes)),
		innerHashes:  retryInnerHashes,
		local:        local,
		localHash:    localHash,
		withRetry:    true,
	})
	for _, mixed := range []bool{false, true} {
		name := "load-32-batches-8-inners"
		if mixed {
			name = "load-32-batches-mixed"
		}
		load := serviceBatchAcceptanceCase{name: name}
		sequence := uint32(1)
		for range 32 {
			if mixed {
				blob, hash := buildServicePaymentBlob(b, env, master, destination, 250_000_000, sequence)
				load.agreed = append(load.agreed, blob)
				load.agreedHash = append(load.agreedHash, hash)
				sequence++
			}
			blob, hash, inners := buildServiceBatchBlob(b, env, master, destination, 8, sequence, sequence+1, mixed)
			load.agreed = append(load.agreed, blob)
			load.agreedHash = append(load.agreedHash, hash)
			load.innerHashes = append(load.innerHashes, inners...)
			load.innerParents = append(load.innerParents, repeatBatchParent(hash, len(inners))...)
			sequence += 9
		}
		cases = append(cases, load)
	}
	return cases
}

func repeatBatchParent(hash [32]byte, count int) [][32]byte {
	parents := make([][32]byte, count)
	for i := range parents {
		parents[i] = hash
	}
	return parents
}

func buildServiceBatchBlob(
	b testing.TB,
	env *jtx.TestEnv,
	outer, destination *jtx.Account,
	count int,
	outerSequence, innerSequence uint32,
	fieldHeavy bool,
) ([]byte, [32]byte, [][32]byte) {
	b.Helper()
	builder := batchtest.NewBatchBuilder(
		outer,
		outerSequence,
		batchtest.CalcBatchFee(env.BaseFee(), 0, uint32(count)),
		batchtx.BatchFlagAllOrNothing,
	)
	for i := range count {
		var inner tx.Transaction
		if fieldHeavy && i%2 == 1 {
			inner = batchtest.MakeInnerAccountSet(outer, innerSequence+uint32(i))
			inner.GetCommon().AddMemo("74657874", "42415443482D42454E43484D41524B", "746578742F706C61696E")
		} else {
			inner = batchtest.MakeInnerPaymentXRP(outer, destination, int64(250_000_000+i), innerSequence+uint32(i))
		}
		builder.AddInnerTx(inner)
	}
	batch := builder.MustBuild()
	env.SignWith(batch, outer)
	blob, err := tx.SerializeTransaction(batch)
	require.NoError(b, err)
	hash, err := tx.ComputeTransactionHash(batch)
	require.NoError(b, err)
	innerHashes := make([][32]byte, len(batch.RawTransactions))
	for i, raw := range batch.RawTransactions {
		innerHashes[i], err = tx.ComputeTransactionHash(raw.RawTransaction.InnerTx)
		require.NoError(b, err)
	}
	return blob, hash, innerHashes
}

func buildServicePaymentBlob(
	b testing.TB,
	env *jtx.TestEnv,
	sender, receiver *jtx.Account,
	amount uint64,
	sequence uint32,
) ([]byte, [32]byte) {
	b.Helper()
	transaction := payment.Pay(sender, receiver, amount).Sequence(sequence).Build()
	env.SignWith(transaction, sender)
	blob, err := tx.SerializeTransaction(transaction)
	require.NoError(b, err)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(b, err)
	return blob, hash
}

func assertBatchAcceptanceEffects(b testing.TB, closed *ledgerpkg.Ledger, tc serviceBatchAcceptanceCase) {
	b.Helper()
	for i, hash := range tc.agreedHash {
		stored, found, err := closed.GetTransaction(hash)
		require.NoError(b, err)
		require.True(b, found, "agreed transaction %d missing", i)
		raw, metadata, err := tx.SplitTxWithMetaBlobStrict(stored)
		require.NoError(b, err)
		require.Equal(b, tc.agreed[i], raw, "agreed transaction %d changed on disk", i)
		decoded, err := binarycodec.DecodeBytes(metadata)
		require.NoError(b, err)
		require.Equal(b, "tesSUCCESS", decoded["TransactionResult"], "agreed transaction %d did not succeed", i)
	}
	for i, innerHash := range tc.innerHashes {
		stored, found, err := closed.GetTransaction(innerHash)
		require.NoError(b, err)
		require.True(b, found, "batch inner transaction %d missing", i)
		_, metadata, err := tx.SplitTxWithMetaBlobStrict(stored)
		require.NoError(b, err)
		decoded, err := binarycodec.DecodeBytes(metadata)
		require.NoError(b, err)
		require.Equal(b, "tesSUCCESS", decoded["TransactionResult"], "batch inner transaction %d did not succeed", i)
		require.Equal(b, fmt.Sprintf("%X", tc.innerParents[i]), decoded["ParentBatchID"], "batch inner transaction %d has wrong parent", i)
		exists, err := closed.TxExists(innerHash)
		require.NoError(b, err)
		require.True(b, exists, "batch inner transaction %d missing", i)
	}
}

type batchAcceptanceBenchmarkTiming struct {
	total, parse, apply, nextOpen, build, closeHash time.Duration
}

type batchAcceptanceBenchmarkLogger struct {
	mu           sync.Mutex
	latestSample batchAcceptanceBenchmarkTiming
	haveSample   bool
}

func (l *batchAcceptanceBenchmarkLogger) latest() (batchAcceptanceBenchmarkTiming, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.latestSample, l.haveSample
}

func (l *batchAcceptanceBenchmarkLogger) Info(msg string, args ...any) {
	if msg != "Consensus acceptance timings" {
		return
	}
	var sample batchAcceptanceBenchmarkTiming
	for i := 0; i+1 < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			continue
		}
		duration, ok := args[i+1].(time.Duration)
		if !ok {
			continue
		}
		switch key {
		case "total":
			sample.total = duration
		case "parse":
			sample.parse = duration
		case "transaction_apply":
			sample.apply = duration
		case "next_open":
			sample.nextOpen = duration
		case "build":
			sample.build = duration
		case "close_state_tx_hash":
			sample.closeHash = duration
		}
	}
	l.mu.Lock()
	l.latestSample = sample
	l.haveSample = true
	l.mu.Unlock()
}

func (l *batchAcceptanceBenchmarkLogger) Trace(string, ...any)       {}
func (l *batchAcceptanceBenchmarkLogger) Debug(string, ...any)       {}
func (l *batchAcceptanceBenchmarkLogger) Warn(string, ...any)        {}
func (l *batchAcceptanceBenchmarkLogger) Error(string, ...any)       {}
func (l *batchAcceptanceBenchmarkLogger) Fatal(msg string, _ ...any) { panic(msg) }
func (l *batchAcceptanceBenchmarkLogger) With(...any) log.Logger     { return l }
func (l *batchAcceptanceBenchmarkLogger) Named(string) log.Logger    { return l }

func newBatchConsensusAcceptanceService(
	b *testing.B,
	fixture *benchmarkRefreshFixture,
	logger log.Logger,
	flatCacheBytes int64,
) (*Service, *ledgerpkg.Ledger, func()) {
	b.Helper()
	var (
		execution         *ledgerpkg.ExecutionStore
		flat              *flatstate.Store
		persistenceMemory shamap.DirtyMemoryBudget
	)
	if flatCacheBytes > 0 {
		execution, flat, persistenceMemory = newBatchBenchmarkFlatExecution(b, fixture, flatCacheBytes)
	}
	cfg := Config{
		Standalone: true,
		Startup: StartupConfig{
			Mode:   StartupLoad,
			Ledger: fmt.Sprintf("%X", fixture.template.Hash()),
		},
		GenesisConfig:     batchBenchmarkGenesisConfig(),
		Logger:            logger,
		NodeStore:         fixture.db,
		SHAMapFamily:      fixture.svc.shamapFamily,
		Execution:         execution,
		FlatState:         flat,
		PersistenceMemory: persistenceMemory,
	}
	var svc *Service
	var closeOnce sync.Once
	cleanup := func() {
		closeOnce.Do(func() {
			currentSvc, currentExecution, currentFlat := svc, execution, flat
			if currentSvc != nil {
				currentSvc.Stop()
			}
			if currentExecution != nil {
				if err := currentExecution.Close(); err != nil {
					b.Errorf("close benchmark execution store: %v", err)
				}
			}
			if currentFlat != nil {
				if err := currentFlat.Close(); err != nil {
					b.Errorf("close benchmark flat state: %v", err)
				}
			}
			svc, execution, flat = nil, nil, nil
		})
	}
	b.Cleanup(cleanup)
	var err error
	svc, err = New(cfg)
	require.NoError(b, err)
	require.NoError(b, svc.Start())
	parent := svc.GetClosedLedger()
	require.NotNil(b, parent)
	require.Equal(b, fixture.template.Hash(), parent.Hash())
	return svc, parent, cleanup
}

func benchmarkFlatStateEnabled() bool {
	return os.Getenv("STATE_BENCH_FLAT") == "1"
}

const batchBenchmarkDefaultCacheBytes int64 = 256 << 10

func benchmarkServiceCacheBytes(b testing.TB, flatEnabled bool) (nodeCacheBytes, flatCacheBytes int64) {
	b.Helper()
	total := batchBenchmarkDefaultCacheBytes
	if raw := os.Getenv("STATE_BENCH_CACHE_BYTES"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 || parsed%2 != 0 {
			b.Fatalf("STATE_BENCH_CACHE_BYTES must be a positive even byte count, got %q", raw)
		}
		total = parsed
	}
	if !flatEnabled {
		return total, 0
	}
	return total / 2, total / 2
}

func newBatchBenchmarkFlatExecution(
	b *testing.B,
	fixture *benchmarkRefreshFixture,
	flatCacheBytes int64,
) (*ledgerpkg.ExecutionStore, *flatstate.Store, shamap.DirtyMemoryBudget) {
	b.Helper()
	flatPath := filepath.Join(b.TempDir(), "flat")
	pool := membudget.New(1 << 30)
	workspace, err := pool.NewAccount("batch_benchmark_workspace", 128<<20, 128<<20)
	require.NoError(b, err)
	memory, err := pool.NewAccount("batch_benchmark_overlay", 0, 256<<20)
	require.NoError(b, err)
	disk, err := pool.NewAccount("batch_benchmark_spill", 0, 512<<20)
	require.NoError(b, err)
	flat, err := flatstate.Open(flatPath, flatstate.Options{
		CacheBytes:   flatCacheBytes,
		MemTableSize: 8 << 20,
		MemoryBudget: workspace,
	})
	require.NoError(b, err)
	execution, err := ledgerpkg.NewExecutionStore(ledgerpkg.ExecutionConfig{
		Family:    fixture.svc.shamapFamily,
		Workspace: workspace,
		Flat:      flat,
		Overlay: overlay.Config{
			MemoryBudget:   memory,
			DiskBudget:     disk,
			MaxMemoryBytes: 16 << 20,
			MaxDiskBytes:   512 << 20,
			SpillDir:       filepath.Join(flatPath, "scratch"),
		},
	})
	require.NoError(b, err)

	changes := make([]flatstate.Change, 0, 4096)
	reader := shamap.NewRootReader(shamap.TypeState, fixture.root, fixture.svc.shamapFamily)
	err = reader.IterateLeaves(b.Context(), nil, func(item *shamap.Item, _ shamap.NodeType) bool {
		changes = append(changes, flatstate.Change{Key: item.Key(), Value: item.Data()})
		return true
	})
	require.NoError(b, err)
	sort.Slice(changes, func(i, j int) bool {
		return bytes.Compare(changes[i].Key[:], changes[j].Key[:]) < 0
	})
	target := flatDescriptor(fixture.template.Header(), 1)
	require.NoError(b, flat.Advance(b.Context(), flatstate.AdvanceRequest{
		Target: target,
		Authorization: flatstate.RootClosureAuthorization{
			TargetRoot: target.StateRoot,
			Token:      []byte("batch-benchmark-seed"),
		},
		Changes:         flatstate.NewSliceChangeIterator(changes),
		PublicationKind: ledgerPublication,
	}))
	return execution, flat, workspace
}

func batchBenchmarkGenesisConfig() genesis.Config {
	cfg := genesis.DefaultConfig()
	cfg.Amendments = append(cfg.Amendments, amendment.FeatureBatchV1_1)
	return cfg
}

func newBatchBenchmarkRefreshFixture(b *testing.B, entries int, cacheBytes int64) *benchmarkRefreshFixture {
	b.Helper()
	fixture := &benchmarkRefreshFixture{
		path: filepath.Join(b.TempDir(), "nodes"),
		options: kvpebble.Options{
			BlockCacheBytes: cacheBytes,
			MaxOpenFiles:    200,
		},
	}
	backend, err := kvpebble.NewRotating(fixture.path, fixture.options)
	require.NoError(b, err)
	base, err := nodestore.NewRotatingKVDatabase(backend, nodestore.DatabaseConfig{})
	require.NoError(b, err)
	db := &countingGenerationDatabase{Database: base, generation: base}
	family := shamapbackend.New(db)
	execution := newTestExecutionStore(b, family)
	initial, err := genesis.Create(batchBenchmarkGenesisConfig())
	require.NoError(b, err)
	parent, err := ledgerpkg.FromGenesis(initial.Header, initial.StateMap, initial.TxMap, drops.Fees{}, execution)
	require.NoError(b, err)
	writer := &Service{nodeStore: base, shamapFamily: family, config: Config{Execution: execution, SHAMapFamily: family}}
	require.NoError(b, writer.persistToNodeStore(b.Context(), parent, parent.Sequence()))
	closeTime := parent.CloseTime().Add(10 * time.Second)
	validated, err := ledgerpkg.NewOpen(parent, closeTime)
	require.NoError(b, err)
	for i := range entries {
		var key [32]byte
		binary.BigEndian.PutUint32(key[28:], uint32(i+1))
		data := make([]byte, 12)
		binary.BigEndian.PutUint32(data[8:], uint32(i+1))
		require.NoError(b, validated.Insert(keylet.Keylet{Key: key}, data))
	}
	require.NoError(b, validated.Close(closeTime, 0))
	seq := validated.Sequence()
	require.NoError(b, writer.persistToNodeStore(b.Context(), validated, seq))
	fixture.root, err = validated.StateMapHash()
	require.NoError(b, err)
	fixture.seq = seq
	fixture.rotation = seq
	fixture.template, err = validated.Snapshot()
	require.NoError(b, err)
	fixture.execution = execution
	validated.Discard()
	parent.Discard()
	committed, err := db.RotateGeneration(b.Context(), fixture.rotation, 1)
	require.True(b, committed)
	require.NoError(b, err)
	require.NoError(b, db.Close())
	fixture.open(b)
	b.Cleanup(func() { fixture.close(b) })
	return fixture
}

func TestServiceBatchAcceptanceFixtures(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	for _, fixture := range newServiceBatchAcceptanceCases(t, env) {
		for i, blob := range fixture.agreed {
			parsed, err := openledger.ParsePendingTx(blob)
			require.NoError(t, err)
			require.Equal(t, fixture.agreedHash[i], parsed.Hash)
			_, err = binarycodec.DecodeBytes(parsed.Blob)
			require.NoError(t, err)
		}
	}
}
