package service

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	ledgerpkg "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/log"
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
	fixture := newBatchBenchmarkRefreshFixture(b, 4_096, 256<<10)
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
				total         = make([]time.Duration, 0, b.N)
				parse         = make([]time.Duration, 0, b.N)
				apply         = make([]time.Duration, 0, b.N)
				nextOpen      = make([]time.Duration, 0, b.N)
				wantHash      [32]byte
				wantStateRoot [32]byte
				wantTxRoot    [32]byte
				haveHash      bool
			)

			b.ResetTimer()
			for round := range b.N {
				b.StopTimer()
				logger := &batchAcceptanceBenchmarkLogger{}
				svc, parent := newBatchConsensusAcceptanceService(b, fixture, logger)
				if tc.withRetry {
					outcome, err := svc.SubmitOpenLedgerTxDetailed(tc.local, true)
					require.NoError(b, err)
					require.False(b, outcome.Applied, "successor transaction applied before its Batch")
					require.NotEqual(b, openledger.ResultFailure, outcome.Class)
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
				require.NoError(b, err)
				sample, ok := logger.latest()
				if !ok {
					b.Fatal("acceptance did not emit consensus acceptance timings")
				}
				total = append(total, sample.total)
				parse = append(parse, sample.parse)
				apply = append(apply, sample.apply)
				nextOpen = append(nextOpen, sample.nextOpen)

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
				svc.Stop()
			}

			reportConsensusAcceptanceQuantiles(b, "accept-total", total)
			reportConsensusAcceptanceQuantiles(b, "parse", parse)
			reportConsensusAcceptanceQuantiles(b, "transaction-apply", apply)
			reportConsensusAcceptanceQuantiles(b, "next-open", nextOpen)
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
	total, parse, apply, nextOpen time.Duration
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
) (*Service, *ledgerpkg.Ledger) {
	b.Helper()
	cfg := Config{
		Standalone: true,
		Startup: StartupConfig{
			Mode:   StartupLoad,
			Ledger: fmt.Sprintf("%X", fixture.template.Hash()),
		},
		GenesisConfig: batchBenchmarkGenesisConfig(),
		Logger:        logger,
		NodeStore:     fixture.db,
		SHAMapFamily:  fixture.svc.shamapFamily,
	}
	svc, err := New(cfg)
	require.NoError(b, err)
	require.NoError(b, svc.Start())
	parent := svc.GetClosedLedger()
	require.NotNil(b, parent)
	require.Equal(b, fixture.template.Hash(), parent.Hash())
	return svc, parent
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
