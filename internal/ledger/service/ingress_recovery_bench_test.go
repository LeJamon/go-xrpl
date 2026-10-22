package service

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	ledgerpkg "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

const (
	issue2109BatchCount     = 8
	issue2109OrdinaryCount  = 2
	issue2109RefreshEntries = 16_384
)

type issue2109IngressWorkload struct {
	agreed       [][]byte
	agreedHash   [][32]byte
	innerHash    [][32]byte
	innerParents [][32]byte
	ingress      []byte
	ingressHash  [32]byte
	ingressInner [][32]byte
	totalBytes   int
}

type issue2109ControlRoots struct {
	ledgerHash [32]byte
	stateRoot  [32]byte
	txRoot     [32]byte
	txCount    uint32
}

// BenchmarkService_Issue2109IngressRecovery measures a canonical ingress that
// applies privately, is invalidated by consensus acceptance, and then retries
// against the successor open ledger. The two refresh arms exercise the same
// path while a generation promotion walks the persisted parent SHAMap.
// Its stale-retry metric is local publication retry latency; peer catch-up is
// covered by the consensus adaptor replay tests.
func BenchmarkService_Issue2109IngressRecovery(b *testing.B) {
	for _, innerCount := range []int{2, 8} {
		for _, withRefresh := range []bool{false, true} {
			name := fmt.Sprintf("forced-stale-retry/inners=%d/refresh=%t", innerCount, withRefresh)
			b.Run(name, func(b *testing.B) {
				benchmarkIssue2109IngressRecovery(b, innerCount, withRefresh)
			})
		}
	}
}

func benchmarkIssue2109IngressRecovery(b *testing.B, innerCount int, withRefresh bool) {
	b.Helper()
	b.StopTimer()
	fixture, workload := newIssue2109IngressFixture(b, innerCount)
	b.Cleanup(func() { issue2090CloseFixture(b, fixture) })
	control := issue2109ControlAcceptance(b, fixture, workload)

	b.ReportAllocs()
	b.SetBytes(int64(workload.totalBytes))
	acceptance := make([]time.Duration, 0, b.N)
	staleRetry := make([]time.Duration, 0, b.N)
	refreshTime := make([]time.Duration, 0, b.N)
	parse := make([]time.Duration, 0, b.N)
	apply := make([]time.Duration, 0, b.N)
	b.ResetTimer()

	for round := range b.N {
		b.StopTimer()
		logger := &batchAcceptanceBenchmarkLogger{}
		svc, parent := newIssue2090AcceptanceService(b, fixture, logger)
		var (
			refreshDone    <-chan error
			refreshStarted time.Time
		)
		promotedBefore := fixture.db.promotedNodes.Load()
		if withRefresh {
			fixture.db.promotionStart = make(chan struct{})
			fixture.db.promotionOnce = sync.Once{}
			fixture.db.promotionDelay = 100 * time.Microsecond
			done := make(chan error, 1)
			refreshDone = done
			refreshStarted = time.Now()
			go func() {
				done <- svc.refreshGenerationStateWithBatch(
					b.Context(), fixture.root, fixture.seq, fixture.db, nil, 1,
					storedSHAMapPromotionBatchNodes, storedSHAMapPromotionBatchBytes,
				)
			}()
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

		roundCtx, cancel := context.WithTimeout(b.Context(), 30*time.Second)
		privateReady := make(chan struct{})
		acceptanceFinished := make(chan struct{})
		acceptDone := make(chan issue2109AcceptanceResult, 1)
		commits := new(atomic.Int64)
		b.StartTimer()
		ingressDone := issue2109SubmitIngress(
			roundCtx, svc, workload.ingress, privateReady, acceptanceFinished, commits,
		)

		select {
		case <-privateReady:
		case result := <-ingressDone:
			b.StopTimer()
			cancel()
			if refreshDone != nil {
				<-refreshDone
			}
			svc.Stop()
			require.NoError(b, result.err)
			b.Fatal("ingress returned before its private attempt")
		case <-roundCtx.Done():
			b.StopTimer()
			cancel()
			if refreshDone != nil {
				<-refreshDone
			}
			svc.Stop()
			b.Fatal("ingress did not reach its private attempt")
		}

		acceptStarted := time.Now()
		go func() {
			_, err := svc.AcceptConsensusResult(
				roundCtx, parent, workload.agreed, nil,
				parent.CloseTime().Add(10*time.Second), true,
			)
			acceptDone <- issue2109AcceptanceResult{err: err, finished: time.Now()}
			close(acceptanceFinished)
		}()

		accepted := <-acceptDone
		ingress := <-ingressDone
		b.StopTimer()
		cancel()

		if refreshDone != nil {
			require.NoError(b, <-refreshDone)
			refreshTime = append(refreshTime, time.Since(refreshStarted))
			require.Greater(b, fixture.db.promotedNodes.Load(), promotedBefore,
				"refresh did not promote parent state")
		}
		fixture.db.promotionDelay = 0
		require.NoError(b, accepted.err)
		require.NoError(b, ingress.err)
		require.Equal(b, openledger.ResultSuccess, ingress.outcome.Class)
		require.True(b, ingress.outcome.Applied)
		require.GreaterOrEqual(b, commits.Load(), int64(2),
			"consensus acceptance did not force a stale ingress retry")

		acceptance = append(acceptance, accepted.finished.Sub(acceptStarted))
		staleRetryDelay := ingress.finished.Sub(accepted.finished)
		require.GreaterOrEqual(b, staleRetryDelay, time.Duration(0))
		staleRetry = append(staleRetry, staleRetryDelay)
		sample, ok := logger.latest()
		if !ok {
			svc.Stop()
			b.Fatal("acceptance did not emit consensus acceptance timings")
		}
		parse = append(parse, sample.parse)
		apply = append(apply, sample.apply)

		closed := svc.GetClosedLedger()
		require.NotNil(b, closed)
		require.Equal(b, control.ledgerHash, closed.Hash())
		require.Equal(b, control.stateRoot, closed.Header().AccountHash)
		require.Equal(b, control.txRoot, closed.Header().TxHash)
		require.Equal(b, control.txCount, closed.TxCount())
		assertBatchAcceptanceEffects(b, closed, issue2109AcceptanceCase(workload))
		exists, err := svc.openLedgerView.Current().TxExists(workload.ingressHash)
		require.NoError(b, err)
		require.True(b, exists, "ingress transaction missing from successor open ledger")
		assertIssue2109IngressBatch(b, svc.openLedgerView.Current(), workload)
		svc.Stop()

		if round+1 < b.N {
			prepareIssue2082ParentForRotation(b, fixture, withRefresh)
			fixture.rotateAndReopen(b)
		}
	}

	reportConsensusAcceptanceQuantiles(b, "acceptance", acceptance)
	reportConsensusAcceptanceQuantiles(b, "stale-retry", staleRetry)
	reportConsensusAcceptanceQuantiles(b, "parse", parse)
	reportConsensusAcceptanceQuantiles(b, "transaction-apply", apply)
	if len(refreshTime) > 0 {
		reportConsensusAcceptanceQuantiles(b, "refresh", refreshTime)
	}
	b.ReportMetric(float64(issue2109BatchCount), "batches/op")
	b.ReportMetric(float64(len(workload.innerHash)), "inner-txs/op")
}

type issue2109AcceptanceResult struct {
	err      error
	finished time.Time
}

type issue2109IngressResult struct {
	outcome  openledger.SubmitOutcome
	err      error
	finished time.Time
}

func issue2109ControlAcceptance(b testing.TB, fixture *benchmarkRefreshFixture, workload issue2109IngressWorkload) issue2109ControlRoots {
	b.Helper()
	svc, parent := newIssue2090AcceptanceService(b, fixture, xrpllog.Discard())
	defer svc.Stop()
	_, err := svc.AcceptConsensusResult(
		b.Context(), parent, workload.agreed, nil,
		parent.CloseTime().Add(10*time.Second), true,
	)
	require.NoError(b, err)
	closed := svc.GetClosedLedger()
	require.NotNil(b, closed)
	require.Equal(b, uint32(len(workload.agreed)+len(workload.innerHash)), closed.TxCount())
	assertBatchAcceptanceEffects(b, closed, issue2109AcceptanceCase(workload))
	return issue2109ControlRoots{
		ledgerHash: closed.Hash(),
		stateRoot:  closed.Header().AccountHash,
		txRoot:     closed.Header().TxHash,
		txCount:    closed.TxCount(),
	}
}

func issue2109AcceptanceCase(workload issue2109IngressWorkload) serviceBatchAcceptanceCase {
	return serviceBatchAcceptanceCase{
		name:         "issue-2109-ingress-recovery",
		agreed:       workload.agreed,
		agreedHash:   workload.agreedHash,
		innerParents: workload.innerParents,
		innerHashes:  workload.innerHash,
	}
}

func newIssue2109IngressFixture(tb testing.TB, innerCount int) (*benchmarkRefreshFixture, issue2109IngressWorkload) {
	tb.Helper()
	env := jtx.NewTestEnvWithConfig(tb, batchBenchmarkGenesisConfig())
	env.SetVerifySignatures(true)
	env.EnableFeatureNow("BatchV1_1")
	batchOwner := jtx.NewAccount("issue2109-batch-owner")
	ingressOwner := jtx.NewAccount("issue2109-ingress-owner")
	ordinaryOwner := jtx.NewAccount("issue2109-ordinary-owner")
	destination := jtx.NewAccount("issue2109-destination")
	env.Fund(ordinaryOwner, destination)
	env.FundAmount(batchOwner, uint64(jtx.XRP(10_000)))
	env.FundAmount(ingressOwner, uint64(jtx.XRP(10_000)))
	for i := range issue2109RefreshEntries {
		var key [32]byte
		binary.BigEndian.PutUint32(key[28:], uint32(i+1))
		data := make([]byte, 12)
		binary.BigEndian.PutUint32(data[8:], uint32(i+1))
		require.NoError(tb, env.Ledger().Insert(keylet.Keylet{Key: key}, data))
	}
	env.Close()

	workload := issue2109BuildWorkload(tb, env, batchOwner, ingressOwner, ordinaryOwner, destination, innerCount)
	parent := env.LastClosedLedger()
	require.NotNil(tb, parent)
	return persistIssue2090Parent(tb, parent), workload
}

func issue2109BuildWorkload(
	tb testing.TB,
	env *jtx.TestEnv,
	batchOwner, ingressOwner, ordinaryOwner, destination *jtx.Account,
	innerCount int,
) issue2109IngressWorkload {
	tb.Helper()
	workload := issue2109IngressWorkload{
		agreed:       make([][]byte, 0, issue2109BatchCount+issue2109OrdinaryCount),
		agreedHash:   make([][32]byte, 0, issue2109BatchCount+issue2109OrdinaryCount),
		innerHash:    make([][32]byte, 0, issue2109BatchCount*innerCount),
		innerParents: make([][32]byte, 0, issue2109BatchCount*innerCount),
	}
	outerStart := env.Seq(batchOwner)
	for batchIndex := range issue2109BatchCount {
		outerSequence := outerStart + uint32(batchIndex*(innerCount+1))
		blob, hash, inners := buildIssue2109BatchBlob(
			tb, env, batchOwner, destination, innerCount,
			outerSequence, outerSequence+1, true,
		)
		workload.agreed = append(workload.agreed, blob)
		workload.agreedHash = append(workload.agreedHash, hash)
		workload.innerHash = append(workload.innerHash, inners...)
		for range inners {
			workload.innerParents = append(workload.innerParents, hash)
		}
	}
	ordinarySequence := env.Seq(ordinaryOwner)
	for i := range issue2109OrdinaryCount {
		blob, hash := buildServicePaymentBlob(
			tb, env, ordinaryOwner, destination, 1_000_000+uint64(i), ordinarySequence+uint32(i),
		)
		workload.agreed = append(workload.agreed, blob)
		workload.agreedHash = append(workload.agreedHash, hash)
	}
	workload.ingress, workload.ingressHash, workload.ingressInner = buildIssue2109BatchBlob(
		tb, env, ingressOwner, destination, innerCount,
		env.Seq(ingressOwner), env.Seq(ingressOwner)+1, true,
	)
	for _, blob := range workload.agreed {
		workload.totalBytes += len(blob)
	}
	workload.totalBytes += len(workload.ingress)
	return workload
}

func buildIssue2109BatchBlob(
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
			inner.GetCommon().AddMemo("74657874", "42415443482D32313039", "746578742F706C61696E")
		} else {
			inner = batchtest.MakeInnerPayment(
				outer, destination, int64(250_000_000+i), innerSequence+uint32(i),
			)
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

func assertIssue2109IngressBatch(
	b testing.TB,
	open *ledgerpkg.Ledger,
	workload issue2109IngressWorkload,
) {
	b.Helper()
	stored, found, err := open.GetTransaction(workload.ingressHash)
	require.NoError(b, err)
	require.True(b, found, "ingress batch missing from successor open ledger")
	raw, metadata, err := tx.SplitTxWithMetaBlobStrict(stored)
	require.NoError(b, err)
	require.Equal(b, workload.ingress, raw, "ingress batch changed on successor")
	decoded, err := binarycodec.DecodeBytes(metadata)
	require.NoError(b, err)
	require.Equal(b, "tesSUCCESS", decoded["TransactionResult"])
	parsed, err := tx.ParseFromBinary(raw)
	require.NoError(b, err)
	batch, ok := parsed.(*batchtx.Batch)
	require.True(b, ok, "successor ingress is not a Batch")
	require.Len(b, batch.RawTransactions, len(workload.ingressInner))
	for i, inner := range batch.RawTransactions {
		innerHash, err := tx.ComputeTransactionHash(inner.RawTransaction.InnerTx)
		require.NoError(b, err)
		require.Equal(b, workload.ingressInner[i], innerHash, "successor inner %d changed", i)
	}
}

func issue2109SubmitIngress(
	ctx context.Context,
	svc *Service,
	blob []byte,
	privateReady chan struct{},
	acceptanceFinished <-chan struct{},
	commits *atomic.Int64,
) <-chan issue2109IngressResult {
	done := make(chan issue2109IngressResult, 1)
	go func() {
		result := issue2109IngressResult{}
		defer func() {
			result.finished = time.Now()
			done <- result
		}()
		releaseAdmission, err := svc.detachedIngress.acquire()
		if err != nil {
			result.err = err
			return
		}
		defer releaseAdmission()
		ptx, err := openledger.ParsePendingTx(blob)
		if err != nil {
			result.err = err
			return
		}
		svc.mu.RLock()
		cfg, cfgErr := svc.applyConfigLocked()
		svc.mu.RUnlock()
		if cfgErr != nil {
			result.err = cfgErr
			return
		}
		initialRules := cfg.Rules
		initialSignatureCheck := !cfg.SkipSignatureVerification
		var signatureErr error
		if initialSignatureCheck {
			signatureErr = signatureVerificationError(
				txengine.PrewarmSignatureWithRules(ptx.Parsed, initialRules), initialRules,
			)
		}
		localReason := tx.TransactionLocalChecksFailureReason(ptx.Parsed)
		var readyOnce sync.Once
		result.outcome, _, _, _, result.err = svc.submitDetachedIngressOwned(detachedIngressRequest{
			pendingTx:             ptx,
			ownedParsed:           true,
			origin:                "issue-2109-ingress",
			initialRules:          initialRules,
			signatureEligible:     true,
			initialSignatureCheck: initialSignatureCheck,
			initialSignatureErr:   signatureErr,
			prepare: func(workCtx context.Context) {
				if signatureErr == nil && localReason == "" {
					svc.prefetchIngressState(workCtx, ptx)
				}
			},
			localReason: localReason,
			verifySignature: func(parsed tx.Transaction, rules *amendment.Rules) error {
				return signatureVerificationError(
					txengine.PrewarmSignatureWithRules(parsed, rules), rules,
				)
			},
			prepareCommit: func(current *ledgerpkg.Ledger, _ *ledgerpkg.Ledger, _ *txq.TxQ, _ openledger.ApplyConfig, outcome openledger.SubmitOutcome, attempt openledger.PendingTx) detachedIngressCommitData {
				commits.Add(1)
				readyOnce.Do(func() { close(privateReady) })
				select {
				case <-acceptanceFinished:
				case <-ctx.Done():
				}
				return detachedIngressCommitData{
					proposed: svc.prepareProposedTransaction(attempt, attempt.Blob, outcome, current),
				}
			},
			onCommit: func(_ *ledgerpkg.Ledger, _ *txq.TxQ, _ *localtxs.LocalTxs, _ openledger.ApplyConfig, _ openledger.SubmitOutcome, data detachedIngressCommitData, _ openledger.PendingTx) {
				svc.enqueueProposedTransaction(data.proposed)
			},
		})
	}()
	return done
}
