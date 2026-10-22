package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"sync"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	ledgerpkg "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	offertest "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	cockroachpebble "github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
)

const (
	issue2090BatchCount        = 250
	issue2090InnerCount        = 8
	issue2090OrdinaryCount     = 2
	issue2090ExpectedLeafCount = issue2090BatchCount*(issue2090InnerCount+1) + issue2090OrdinaryCount
	issue2090RefreshEntries    = 16_384
	issue2090CacheBytes        = int64(256 << 10)
)

type issue2090BatchWorkload struct {
	agreed       [][]byte
	agreedHash   [][32]byte
	innerHash    [][32]byte
	innerParents [][32]byte
	innerRaw     [][]byte
	totalBytes   int
}

func BenchmarkService_Issue2090Parse(b *testing.B) {
	b.StopTimer()
	fixture, workload := newIssue2090BatchFixture(b)
	defer issue2090CloseFixture(b, fixture)
	b.ReportAllocs()
	b.SetBytes(int64(workload.totalBytes))
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		pprof.Do(b.Context(), pprofLabelsIssue2090("parse", false), func(context.Context) {
			for i, blob := range workload.agreed {
				parsed, err := openledger.ParsePendingTx(blob)
				if err != nil {
					b.Fatalf("ParsePendingTx(%d): %v", i, err)
				}
				if parsed.Hash != workload.agreedHash[i] {
					b.Fatalf("ParsePendingTx(%d) hash changed: got %X want %X", i, parsed.Hash, workload.agreedHash[i])
				}
			}
		})
	}
	b.StopTimer()
}

func BenchmarkService_Issue2090Application(b *testing.B) {
	b.StopTimer()
	fixture, workload := newIssue2090BatchFixture(b)
	defer issue2090CloseFixture(b, fixture)
	prepared := issue2090PendingTransactions(b, workload)
	salt, err := openledger.ComputeSalt(prepared)
	require.NoError(b, err)
	b.ReportAllocs()
	b.SetBytes(int64(workload.totalBytes))
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		b.StopTimer()
		view, err := ledgerpkg.NewOpen(fixture.template, fixture.template.CloseTime().Add(10*time.Second))
		require.NoError(b, err)
		pending := issue2090ClonePendingTransactions(b, prepared)
		retries := make([]openledger.PendingTx, 0)
		cfg := issue2090ApplyConfig(view, &salt)
		b.StartTimer()
		pprof.Do(b.Context(), pprofLabelsIssue2090("application", false), func(ctx context.Context) {
			err = openledger.ApplyOwnedTxsContext(ctx, view, pending, &retries, cfg)
		})
		b.StopTimer()
		require.NoError(b, err)
		require.Empty(b, retries, "valid issue #2090 workload unexpectedly produced retries")
		require.Equal(b, uint32(issue2090ExpectedLeafCount), view.TxCount())
	}
	b.StopTimer()
}

func BenchmarkService_Issue2090ClosedFeeLevels(b *testing.B) {
	b.StopTimer()
	fixture, workload := newIssue2090BatchFixture(b)
	defer issue2090CloseFixture(b, fixture)
	closed := issue2090AcceptedLedger(b, fixture, workload)
	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(closed)
	ctx := &closedLedgerCtx{
		ledger:           closed,
		baseFee:          baseFee,
		reserveBase:      reserveBase,
		reserveIncrement: reserveIncrement,
	}
	wantLevels := issue2090FeeLevels(b, ctx)
	pending := issue2090PendingTransactions(b, workload)
	require.Equal(b, wantLevels, consensusFeeLevels(closed, pending))
	require.Len(b, wantLevels, issue2090ExpectedLeafCount)
	b.ReportAllocs()
	b.SetBytes(int64(workload.totalBytes))
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		var levels []txq.FeeLevel
		pprof.Do(b.Context(), pprofLabelsIssue2090("closed-fee-levels", false), func(context.Context) {
			levels = consensusFeeLevels(closed, pending)
		})
		if len(levels) != issue2090ExpectedLeafCount {
			b.Fatalf("fee level count = %d, want %d", len(levels), issue2090ExpectedLeafCount)
		}
	}
	b.StopTimer()
	issue2090AssertFeeLevels(b, wantLevels)
}

func BenchmarkService_Issue2090Successor(b *testing.B) {
	benchmarkIssue2090Successor(b)
}

func BenchmarkService_Issue2090FullAcceptance(b *testing.B) {
	for _, refresh := range []bool{false, true} {
		name := "refresh=disabled"
		if refresh {
			name = "refresh=enabled"
		}
		b.Run(name, func(b *testing.B) {
			benchmarkIssue2090Acceptance(b, refresh)
		})
	}
}

func benchmarkIssue2090Acceptance(b *testing.B, withRefresh bool) {
	b.Helper()
	b.StopTimer()
	fixture, workload := newIssue2090BatchFixture(b)
	b.ReportAllocs()
	b.SetBytes(int64(workload.totalBytes))
	total := make([]time.Duration, 0, b.N)
	parse := make([]time.Duration, 0, b.N)
	apply := make([]time.Duration, 0, b.N)
	nextOpen := make([]time.Duration, 0, b.N)
	refreshTime := make([]time.Duration, 0, b.N)
	b.Cleanup(func() { issue2090CloseFixture(b, fixture) })
	b.ResetTimer()
	for round := range b.N {
		b.StopTimer()
		logger := &batchAcceptanceBenchmarkLogger{}
		svc, parent := newIssue2090AcceptanceService(b, fixture, logger)
		var refreshDone <-chan error
		refreshStarted := time.Time{}
		promotedBefore := fixture.db.promotedNodes.Load()
		if withRefresh {
			fixture.db.promotionStart = make(chan struct{})
			fixture.db.promotionOnce = sync.Once{}
			fixture.db.promotionDelay = 100 * time.Microsecond
			done := make(chan error, 1)
			refreshDone = done
			refreshStarted = time.Now()
			go func(db *countingGenerationDatabase) {
				var refreshErr error
				pprof.Do(b.Context(), pprofLabelsIssue2090("refresh", true), func(ctx context.Context) {
					refreshErr = svc.refreshGenerationStateWithBatch(
						ctx, fixture.root, fixture.seq, db, nil, 1,
						storedSHAMapPromotionBatchNodes, storedSHAMapPromotionBatchBytes,
					)
				})
				done <- refreshErr
			}(fixture.db)
			select {
			case <-fixture.db.promotionStart:
			case err := <-done:
				svc.Stop()
				b.Fatalf("refresh ended before first promotion: %v", err)
			case <-time.After(5 * time.Second):
				svc.Stop()
				b.Fatal("refresh did not reach first promotion")
			}
		}

		var acceptedAt time.Time
		var err error
		b.StartTimer()
		pprof.Do(b.Context(), pprofLabelsIssue2090("acceptance", withRefresh), func(ctx context.Context) {
			acceptedAt = time.Now()
			_, err = svc.AcceptConsensusResult(
				ctx,
				parent,
				workload.agreed,
				nil,
				parent.CloseTime().Add(10*time.Second),
				true,
			)
		})
		acceptElapsed := time.Since(acceptedAt)
		b.StopTimer()
		require.NoError(b, err)
		if refreshDone != nil {
			require.NoError(b, <-refreshDone)
			refreshTime = append(refreshTime, time.Since(refreshStarted))
			require.Greater(b, fixture.db.promotedNodes.Load(), promotedBefore, "refresh did not promote parent state")
		}
		fixture.db.promotionDelay = 0
		sample, ok := logger.latest()
		if !ok {
			svc.Stop()
			b.Fatal("acceptance did not emit consensus acceptance timings")
		}
		total = append(total, acceptElapsed)
		parse = append(parse, sample.parse)
		apply = append(apply, sample.apply)
		nextOpen = append(nextOpen, sample.nextOpen)
		closed := svc.GetClosedLedger()
		require.NotNil(b, closed)
		require.Equal(b, uint32(issue2090ExpectedLeafCount), closed.TxCount())
		require.Equal(b, "D46F6DBD78A61D8C614A91DC17407DB2DA74E401A67365796785242A3526007D", fmt.Sprintf("%X", closed.Hash()))
		svc.Stop()

		if round+1 < b.N {
			prepareIssue2082ParentForRotation(b, fixture, withRefresh)
			fixture.rotateAndReopen(b)
		}
	}
	b.StopTimer()
	reportConsensusAcceptanceQuantiles(b, "acceptance", total)
	reportConsensusAcceptanceQuantiles(b, "parse", parse)
	reportConsensusAcceptanceQuantiles(b, "transaction-apply", apply)
	reportConsensusAcceptanceQuantiles(b, "next-open", nextOpen)
	if len(refreshTime) > 0 {
		reportConsensusAcceptanceQuantiles(b, "refresh", refreshTime)
	}
	b.ReportMetric(float64(issue2090BatchCount), "batches/op")
	b.ReportMetric(float64(issue2090ExpectedLeafCount), "leaves/op")
}

func benchmarkIssue2090Successor(b *testing.B) {
	b.Helper()
	b.StopTimer()
	fixture, workload := newIssue2090BatchFixture(b)
	b.ReportAllocs()
	b.SetBytes(int64(workload.totalBytes))
	successor := make([]time.Duration, 0, b.N)
	b.Cleanup(func() { issue2090CloseFixture(b, fixture) })
	b.ResetTimer()
	for round := range b.N {
		b.StopTimer()
		svc, parent := newIssue2090AcceptanceService(b, fixture, xrpllog.Discard())
		pending := issue2090PendingTransactions(b, workload)
		salt, err := openledger.ComputeSalt(pending)
		if err != nil {
			svc.Stop()
			b.Fatalf("compute issue #2090 successor salt: %v", err)
		}
		closeTime := parent.CloseTime().Add(10 * time.Second)
		closed, retries, err := svc.buildClosedLedgerWithOwnership(
			b.Context(), parent, pending, salt, closeTime, false, nil, true,
		)
		if err == nil {
			err = closed.Close(closeTime, 0)
		}
		if err != nil {
			svc.Stop()
			b.Fatalf("build issue #2090 successor candidate: %v", err)
		}
		if closed.TxCount() != issue2090ExpectedLeafCount {
			svc.Stop()
			b.Fatalf("successor candidate transaction count = %d, want %d", closed.TxCount(), issue2090ExpectedLeafCount)
		}

		b.StartTimer()
		started := time.Now()
		var prepared *PreparedPreferredLedger
		pprof.Do(b.Context(), pprofLabelsIssue2090("successor", false), func(ctx context.Context) {
			prepared, err = svc.prepareSuccessor(ctx, closed, successorOptions{
				admitted:         true,
				deferSpeculation: true,
				retries:          retries,
				retrySalt:        &salt,
				feeLevels:        consensusFeeLevels(closed, pending),
			})
			if err == nil {
				err = prepared.Wait(ctx)
			}
		})
		elapsed := time.Since(started)
		b.StopTimer()
		if prepared != nil {
			prepared.Release()
		}
		if err != nil {
			svc.Stop()
			b.Fatalf("prepare issue #2090 successor: %v", err)
		}
		successor = append(successor, elapsed)
		svc.Stop()

		if round+1 < b.N {
			prepareIssue2082ParentForRotation(b, fixture, false)
			fixture.rotateAndReopen(b)
		}
	}
	b.StopTimer()
	reportConsensusAcceptanceQuantiles(b, "successor", successor)
	b.ReportMetric(float64(issue2090BatchCount), "batches/op")
	b.ReportMetric(float64(issue2090ExpectedLeafCount), "leaves/op")
}

func pprofLabelsIssue2090(phase string, refresh bool) pprof.LabelSet {
	return pprof.Labels(
		"benchmark", "issue-2090",
		"phase", phase,
		"refresh", strconv.FormatBool(refresh),
	)
}

func TestIssue2090BatchAcceptanceRegression(t *testing.T) {
	fixture, workload := newIssue2090BatchFixture(t)
	t.Cleanup(func() { issue2090CloseFixture(t, fixture) })
	closed := issue2090AcceptedLedger(t, fixture, workload)
	metadataDigest, levels := issue2090AssertAcceptance(t, closed, workload)
	require.Equal(t, issue2090ExpectedLeafCount, int(closed.TxCount()))
	require.Len(t, levels, issue2090ExpectedLeafCount)
	issue2090AssertFeeLevels(t, levels)
	require.Equal(t, levels, consensusFeeLevels(closed, issue2090PendingTransactions(t, workload)))

	// These values pin the generated parent, canonical workload, and accepted
	// metadata. They catch accidental transaction reordering and ownership or
	// serialization changes in the acceptance path.
	const (
		wantLedgerHash = "D46F6DBD78A61D8C614A91DC17407DB2DA74E401A67365796785242A3526007D"
		wantStateRoot  = "28CBC9B838CD1E74BA642DA9B96063660F8B55567BD9E34F7887D5080B8DE174"
		wantTxRoot     = "9C88A74719032C6F607F74775E4CF9E6FDC045F7BC98321E79D97BC8DBADBF69"
		wantMetadata   = "82C90F80BFF77B077D92ABAD4C9EA9C7BFEC2F46ABECFBE4BEAA94D0C5944706"
	)
	require.Equal(t, wantLedgerHash, fmt.Sprintf("%X", closed.Hash()))
	require.Equal(t, wantStateRoot, fmt.Sprintf("%X", closed.Header().AccountHash))
	require.Equal(t, wantTxRoot, fmt.Sprintf("%X", closed.Header().TxHash))
	require.Equal(t, wantMetadata, fmt.Sprintf("%X", metadataDigest))
}

func issue2090AcceptedLedger(tb testing.TB, fixture *benchmarkRefreshFixture, workload issue2090BatchWorkload) *ledgerpkg.Ledger {
	tb.Helper()
	svc, parent := newIssue2090AcceptanceService(tb, fixture, xrpllog.Discard())
	_, err := svc.AcceptConsensusResult(
		tb.Context(),
		parent,
		workload.agreed,
		nil,
		parent.CloseTime().Add(10*time.Second),
		true,
	)
	require.NoError(tb, err)
	closed := svc.GetClosedLedger()
	require.NotNil(tb, closed)
	svc.Stop()
	return closed
}

func issue2090AssertAcceptance(tb testing.TB, closed *ledgerpkg.Ledger, workload issue2090BatchWorkload) ([32]byte, []txq.FeeLevel) {
	tb.Helper()
	require.Equal(tb, uint32(issue2090ExpectedLeafCount), closed.TxCount())
	digest := sha256.New()
	for i, hash := range workload.agreedHash {
		stored, found, err := closed.GetTransaction(hash)
		require.NoError(tb, err)
		require.True(tb, found, "agreed transaction %d missing", i)
		raw, metadata, err := tx.SplitTxWithMetaBlobStrict(stored)
		require.NoError(tb, err)
		require.Equal(tb, workload.agreed[i], raw, "agreed transaction %d changed on disk", i)
		decoded, err := binarycodec.DecodeBytes(metadata)
		require.NoError(tb, err)
		require.Equal(tb, "tesSUCCESS", decoded["TransactionResult"], "agreed transaction %d failed", i)
		_, _ = digest.Write(metadata)
	}
	for i, hash := range workload.innerHash {
		stored, found, err := closed.GetTransaction(hash)
		require.NoError(tb, err)
		require.True(tb, found, "inner transaction %d missing", i)
		raw, metadata, err := tx.SplitTxWithMetaBlobStrict(stored)
		require.NoError(tb, err)
		require.Equal(tb, workload.innerRaw[i], raw, "inner transaction %d changed on disk", i)
		decoded, err := binarycodec.DecodeBytes(metadata)
		require.NoError(tb, err)
		require.Equal(tb, "tesSUCCESS", decoded["TransactionResult"], "inner transaction %d failed", i)
		require.Equal(tb, fmt.Sprintf("%X", workload.innerParents[i]), decoded["ParentBatchID"], "inner transaction %d parent changed", i)
		_, _ = digest.Write(metadata)
	}
	var metadataDigest [32]byte
	copy(metadataDigest[:], digest.Sum(nil))
	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(closed)
	levels := issue2090FeeLevels(tb, &closedLedgerCtx{
		ledger:           closed,
		baseFee:          baseFee,
		reserveBase:      reserveBase,
		reserveIncrement: reserveIncrement,
	})
	return metadataDigest, levels
}

func issue2090FeeLevels(tb testing.TB, ctx *closedLedgerCtx) []txq.FeeLevel {
	tb.Helper()
	levels := ctx.GetTransactionFeeLevels()
	require.Len(tb, levels, issue2090ExpectedLeafCount)
	return levels
}

func issue2090AssertFeeLevels(tb testing.TB, levels []txq.FeeLevel) {
	tb.Helper()
	counts := make(map[txq.FeeLevel]int)
	for _, level := range levels {
		counts[level]++
	}
	require.Equal(tb, issue2090BatchCount+issue2090OrdinaryCount, counts[txq.FeeLevel(txq.BaseLevel)])
	require.Equal(tb, issue2090BatchCount*issue2090InnerCount, counts[0])
}

func issue2090PendingTransactions(tb testing.TB, workload issue2090BatchWorkload) []openledger.PendingTx {
	tb.Helper()
	pending := make([]openledger.PendingTx, len(workload.agreed))
	for i, blob := range workload.agreed {
		parsed, err := openledger.ParsePendingTx(blob)
		require.NoError(tb, err)
		pending[i] = parsed
	}
	return pending
}

func issue2090ClonePendingTransactions(tb testing.TB, pending []openledger.PendingTx) []openledger.PendingTx {
	tb.Helper()
	clone := make([]openledger.PendingTx, len(pending))
	for i, ptx := range pending {
		parsed, err := openledger.ParsePendingTx(ptx.Blob)
		require.NoError(tb, err)
		clone[i] = parsed
	}
	return clone
}

func issue2090ApplyConfig(view *ledgerpkg.Ledger, salt *[32]byte) openledger.ApplyConfig {
	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(view)
	return openledger.ApplyConfig{
		BaseFee:          baseFee,
		ReserveBase:      reserveBase,
		ReserveIncrement: reserveIncrement,
		LedgerSequence:   view.Sequence(),
		ParentCloseTime:  parentCloseTimeRippleEpoch(view),
		Rules:            view.Rules(),
		Mode:             openledger.BuildLedgerMode,
		RetrySalt:        salt,
	}
}

func newIssue2090AcceptanceService(tb testing.TB, fixture *benchmarkRefreshFixture, logger xrpllog.Logger) (*Service, *ledgerpkg.Ledger) {
	tb.Helper()
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
	require.NoError(tb, err)
	require.NoError(tb, svc.Start())
	parent := svc.GetClosedLedger()
	require.NotNil(tb, parent)
	require.Equal(tb, fixture.template.Hash(), parent.Hash())
	return svc, parent
}

func newIssue2090BatchFixture(tb testing.TB) (*benchmarkRefreshFixture, issue2090BatchWorkload) {
	tb.Helper()
	env := jtx.NewTestEnvWithConfig(tb, batchBenchmarkGenesisConfig())
	env.SetVerifySignatures(true)
	env.EnableFeatureNow("BatchV1_1")
	batchOwner := jtx.NewAccount("issue2090-batch-owner")
	issuer := jtx.NewAccount("issue2090-issuer")
	paymentSender := jtx.NewAccount("issue2090-payment-sender")
	paymentDestination := jtx.NewAccount("issue2090-payment-destination")
	env.Fund(batchOwner, issuer, paymentSender, paymentDestination)
	for i := range issue2090RefreshEntries {
		var key [32]byte
		binary.BigEndian.PutUint32(key[28:], uint32(i+1))
		data := make([]byte, 12)
		binary.BigEndian.PutUint32(data[8:], uint32(i+1))
		require.NoError(tb, env.Ledger().Insert(keylet.Keylet{Key: key}, data))
	}
	env.Close()

	workload := buildIssue2090BatchWorkload(tb, env, batchOwner, issuer, paymentSender, paymentDestination)
	parent := env.LastClosedLedger()
	require.NotNil(tb, parent)
	fixture := persistIssue2090Parent(tb, parent)
	return fixture, workload
}

func buildIssue2090BatchWorkload(
	tb testing.TB,
	env *jtx.TestEnv,
	batchOwner, issuer, paymentSender, paymentDestination *jtx.Account,
) issue2090BatchWorkload {
	tb.Helper()
	workload := issue2090BatchWorkload{
		agreed:       make([][]byte, 0, issue2090BatchCount+issue2090OrdinaryCount),
		agreedHash:   make([][32]byte, 0, issue2090BatchCount+issue2090OrdinaryCount),
		innerHash:    make([][32]byte, 0, issue2090BatchCount*issue2090InnerCount),
		innerParents: make([][32]byte, 0, issue2090BatchCount*issue2090InnerCount),
		innerRaw:     make([][]byte, 0, issue2090BatchCount*issue2090InnerCount),
	}
	outerStart := env.Seq(batchOwner)
	for batchIndex := range issue2090BatchCount {
		outerSequence := outerStart + uint32(batchIndex*(issue2090InnerCount+1))
		innerSequence := outerSequence + 1
		builder := batchtest.NewBatchBuilder(
			batchOwner,
			outerSequence,
			batchtest.CalcBatchFee(env.BaseFee(), 0, issue2090InnerCount),
			batchtx.BatchFlagAllOrNothing,
		)
		for innerIndex := range issue2090InnerCount {
			builder.AddInnerTx(batchtest.MakeInnerAccountSet(
				batchOwner,
				innerSequence+uint32(innerIndex),
			))
		}
		batch := builder.MustBuild()
		env.SignWith(batch, batchOwner)
		blob, err := tx.SerializeTransaction(batch)
		require.NoError(tb, err)
		hash, err := tx.ComputeTransactionHash(batch)
		require.NoError(tb, err)
		workload.agreed = append(workload.agreed, blob)
		workload.agreedHash = append(workload.agreedHash, hash)
		for _, raw := range batch.RawTransactions {
			inner := raw.RawTransaction.InnerTx
			innerBlob, err := tx.SerializeTransaction(inner)
			require.NoError(tb, err)
			innerID, err := tx.ComputeTransactionHash(inner)
			require.NoError(tb, err)
			workload.innerRaw = append(workload.innerRaw, innerBlob)
			workload.innerHash = append(workload.innerHash, innerID)
			workload.innerParents = append(workload.innerParents, hash)
		}
	}

	offer := offertest.OfferCreate(
		issuer,
		issuer.IOU("USD", 1),
		jtx.XRPTxAmount(1_000_000),
	).Sequence(env.Seq(issuer)).Build()
	env.SignWith(offer, issuer)
	appendIssue2090Agreed(tb, &workload, offer)

	paymentTx := payment.Pay(paymentSender, paymentDestination, 1_000_000).
		Sequence(env.Seq(paymentSender)).Build()
	env.SignWith(paymentTx, paymentSender)
	appendIssue2090Agreed(tb, &workload, paymentTx)

	for _, blob := range workload.agreed {
		workload.totalBytes += len(blob)
	}
	return workload
}

func appendIssue2090Agreed(tb testing.TB, workload *issue2090BatchWorkload, transaction tx.Transaction) {
	tb.Helper()
	blob, err := tx.SerializeTransaction(transaction)
	require.NoError(tb, err)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(tb, err)
	workload.agreed = append(workload.agreed, blob)
	workload.agreedHash = append(workload.agreedHash, hash)
}

func persistIssue2090Parent(tb testing.TB, parent *ledgerpkg.Ledger) *benchmarkRefreshFixture {
	tb.Helper()
	fixture := &benchmarkRefreshFixture{
		path: filepath.Join(tb.TempDir(), "nodes"),
		options: kvpebble.Options{
			BlockCacheBytes: issue2090CacheBytes,
			MaxOpenFiles:    200,
		},
	}
	backend, err := kvpebble.NewRotating(fixture.path, fixture.options)
	require.NoError(tb, err)
	base, err := nodestore.NewRotatingKVDatabase(backend, nodestore.DatabaseConfig{})
	require.NoError(tb, err)
	db := &countingGenerationDatabase{Database: base, generation: base}
	writer := &Service{nodeStore: base}
	require.NoError(tb, writer.persistToNodeStore(tb.Context(), parent, parent.Sequence()))
	fixture.root, err = parent.StateMapHash()
	require.NoError(tb, err)
	fixture.seq = parent.Sequence()
	fixture.rotation = fixture.seq
	fixture.template, err = parent.Snapshot()
	require.NoError(tb, err)
	committed, err := db.RotateGeneration(tb.Context(), fixture.rotation, 1)
	require.True(tb, committed)
	require.NoError(tb, err)
	require.NoError(tb, db.Close())
	issue2090OpenFixture(tb, fixture)
	return fixture
}

func issue2090OpenFixture(tb testing.TB, fixture *benchmarkRefreshFixture) {
	tb.Helper()
	data, err := os.ReadFile(fixture.path + ".generations.json")
	require.NoError(tb, err)
	var manifest struct {
		Writable string `json:"writable"`
		Archive  string `json:"archive"`
	}
	require.NoError(tb, json.Unmarshal(data, &manifest))
	for _, name := range []string{manifest.Writable, manifest.Archive} {
		require.True(tb, filepath.IsLocal(name))
		require.Equal(tb, filepath.Base(name), name)
		database, openErr := cockroachpebble.Open(filepath.Join(filepath.Dir(fixture.path), name), &cockroachpebble.Options{})
		require.NoError(tb, openErr)
		flushErr := database.Flush()
		closeErr := database.Close()
		require.NoError(tb, flushErr)
		require.NoError(tb, closeErr)
	}
	backend, err := kvpebble.NewRotating(fixture.path, fixture.options)
	require.NoError(tb, err)
	base, err := nodestore.NewRotatingKVDatabase(backend, nodestore.DatabaseConfig{})
	require.NoError(tb, err)
	db := &countingGenerationDatabase{Database: base, generation: base}
	family := shamapbackend.New(db)
	svc := &Service{
		nodeStore:    db,
		shamapFamily: family,
		logger:       xrpllog.Discard(),
	}
	fixture.template.SetSHAMapFamily(family)
	svc.validatedLedger = fixture.template
	svc.validatedSignTime = fixture.template.CloseTime()
	gotRoot, err := fixture.template.StateMapHash()
	require.NoError(tb, err)
	require.Equal(tb, fixture.root, gotRoot)
	fixture.base = base
	fixture.db = db
	fixture.svc = svc
	if metrics := base.PromotionIOMetrics(); metrics.SSTableBytes <= uint64(fixture.options.BlockCacheBytes) {
		tb.Fatalf("refresh fixture SSTables (%d bytes) do not exceed block cache (%d bytes)", metrics.SSTableBytes, fixture.options.BlockCacheBytes)
	}
}

func issue2090CloseFixture(tb testing.TB, fixture *benchmarkRefreshFixture) {
	tb.Helper()
	fixture.svc = nil
	if fixture.db != nil {
		require.NoError(tb, fixture.db.Close())
		fixture.db = nil
		fixture.base = nil
	}
}
