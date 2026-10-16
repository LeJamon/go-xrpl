package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type issue2071BenchmarkStore struct {
	started chan struct{}
	release chan struct{}
	delay   time.Duration
	once    sync.Once
	batches atomic.Uint64
	bytes   atomic.Uint64
}

func (s *issue2071BenchmarkStore) write(entries []shamap.FlushEntry) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	if s.delay != 0 {
		time.Sleep(s.delay)
	}
	s.batches.Add(1)
	var bytes uint64
	for _, entry := range entries {
		bytes += uint64(len(entry.Data))
	}
	s.bytes.Add(bytes)
	return nil
}

type issue2071BenchmarkAdmission struct {
	duration time.Duration
	err      error
}

type issue2071BenchmarkFixture struct {
	service       *Service
	budget        issue2071Budget
	parent        *ledger.Ledger
	pending       openledger.PendingTx
	retries       []openledger.PendingTx
	storeLedger   *ledger.Ledger
	workloadBytes int
}

func newIssue2071BenchmarkFixture(b *testing.B, isolated bool) issue2071BenchmarkFixture {
	b.Helper()
	budget := newIssue2071Budget(b, 256<<20, 256<<20, 256<<20, 0)
	cfg := DefaultConfig()
	cfg.PersistenceMemory = budget.persistence
	if isolated {
		cfg.SpeculativeMemory = budget.speculative
	}
	svc, err := New(cfg)
	require.NoError(b, err)
	require.NoError(b, svc.Start())

	env := jtx.NewTestEnv(b)
	receiver := jtx.NewAccount("issue-2071-benchmark-receiver")
	funding, _ := issue2071SignedPaymentAmount(b, env, jtx.MasterAccount(), receiver, 100_000_000, 1)
	fundingOutcome, err := svc.SubmitOpenLedgerTxDetailed(funding, false)
	require.NoError(b, err)
	require.True(b, fundingOutcome.Applied, "account funding returned %s", fundingOutcome.Result)
	_, err = svc.AcceptLedger(b.Context())
	require.NoError(b, err)

	const retryCount = 128
	retries := make([]openledger.PendingTx, 0, retryCount)
	workloadBytes := 0
	seen := make(map[[32]byte]struct{}, retryCount)
	for sequence := uint32(2); sequence < 2+retryCount; sequence++ {
		target, _ := issue2071SignedPaymentAmount(b, env, jtx.MasterAccount(), receiver, 1, sequence)
		pending, parseErr := openledger.ParsePendingTx(target)
		require.NoError(b, parseErr)
		require.NotContains(b, seen, pending.Hash)
		seen[pending.Hash] = struct{}{}
		retries = append(retries, pending)
		workloadBytes += len(target)
	}
	return issue2071BenchmarkFixture{
		service:       svc,
		budget:        budget,
		parent:        issue2071ClosedChild(b, svc.GetClosedLedger()),
		pending:       retries[0],
		retries:       retries,
		storeLedger:   benchmarkPersistenceLedgers(b, 1)[0],
		workloadBytes: workloadBytes + len(retries[0].Blob),
	}
}

func issue2071BenchmarkSuccessorError(prepared *PreparedPreferredLedger, retries []openledger.PendingTx) error {
	if prepared == nil || prepared.openView == nil {
		return fmt.Errorf("successor did not retain a private open view")
	}
	candidate := prepared.openView.Current()
	if candidate == nil {
		return fmt.Errorf("successor private open view has no candidate ledger")
	}
	for i, expected := range retries {
		exists, err := candidate.TxExists(expected.Hash)
		if err != nil {
			return fmt.Errorf("successor replay %d (%x): check accepted transaction: %w", i, expected.Hash, err)
		}
		if !exists {
			return fmt.Errorf("successor replay %d (%x) was not accepted (candidate tx count %d)", i, expected.Hash, candidate.TxCount())
		}
	}
	return nil
}

// BenchmarkIssue2071PersistenceIngressContention uses a blocking callback as a
// synthetic storage backend; its throughput metrics describe contention and
// admission behavior rather than live node-store throughput.
func BenchmarkIssue2071PersistenceIngressContention(b *testing.B) {
	for _, isolated := range []bool{false, true} {
		arm := "shared"
		if isolated {
			arm = "isolated"
		}
		for _, delayed := range []bool{false, true} {
			storage := "fast"
			if delayed {
				storage = "delayed"
			}
			b.Run(fmt.Sprintf("%s/%s", arm, storage), func(b *testing.B) {
				benchmarkIssue2071PersistenceIngressContention(b, isolated, delayed)
			})
		}
	}
}

func benchmarkIssue2071PersistenceIngressContention(b *testing.B, isolated, delayed bool) {
	var admissions, successorReady []int64
	var budgetDenials, operationErrors, persisted, batches uint64
	var peak uint64
	var targetBytes int
	arm := "shared"
	if isolated {
		arm = "isolated"
	}
	b.ReportAllocs()

	for range b.N {
		b.StopTimer()
		fixture := newIssue2071BenchmarkFixture(b, isolated)
		targetBytes = fixture.workloadBytes
		ctx, cancel := context.WithTimeout(b.Context(), 5*time.Second)
		storage := &issue2071BenchmarkStore{
			started: make(chan struct{}),
			release: make(chan struct{}),
		}
		if delayed {
			storage.delay = 5 * time.Millisecond
		}
		storeDone := make(chan error, 1)
		go func() {
			storeDone <- fixture.storeLedger.StoreDirtyBatched(ctx, fixture.budget.persistence, storage.write, storage.write)
		}()
		select {
		case <-storage.started:
		case err := <-storeDone:
			cancel()
			fixture.service.Stop()
			b.Fatalf("persistence ended before storage callback: %v", err)
		case <-time.After(time.Second):
			cancel()
			fixture.service.Stop()
			b.Fatal("persistence did not reach the slow storage callback")
		}
		baselinePersistence := fixture.budget.persistence.Snapshot()
		baselineSpeculative := fixture.budget.speculative.Snapshot()

		start := make(chan struct{})
		ingressStarted := make(chan struct{})
		validationStarted := make(chan struct{})
		ingressDone := make(chan issue2071BenchmarkAdmission, 1)
		validationDone := make(chan issue2071BenchmarkAdmission, 1)
		b.StartTimer()
		go func() {
			<-start
			close(ingressStarted)
			started := time.Now()
			_, err := fixture.service.SubmitOpenLedgerTxDetailed(fixture.pending.Blob, false)
			ingressDone <- issue2071BenchmarkAdmission{duration: time.Since(started), err: err}
		}()
		go func() {
			<-start
			close(validationStarted)
			started := time.Now()
			prepared, err := fixture.service.prepareSuccessor(ctx, fixture.parent, successorOptions{
				preferred:   true,
				admitted:    true,
				anyDisputes: true,
				retries:     fixture.retries,
			})
			if err == nil {
				err = prepared.Wait(ctx)
				if err == nil {
					err = issue2071BenchmarkSuccessorError(prepared, fixture.retries)
				}
				prepared.Release()
			}
			validationDone <- issue2071BenchmarkAdmission{duration: time.Since(started), err: err}
		}()
		close(start)

		startupDeadline := time.NewTimer(time.Second)
		select {
		case <-ingressStarted:
		case <-startupDeadline.C:
			close(storage.release)
			cancel()
			fixture.service.Stop()
			b.Fatalf("%s ingress did not start", arm)
		}
		select {
		case <-validationStarted:
		case <-startupDeadline.C:
			close(storage.release)
			cancel()
			fixture.service.Stop()
			b.Fatalf("%s successor replay did not start", arm)
		}
		startupDeadline.Stop()
		// Hold persistence for the same bounded interval in every arm. This keeps
		// the shared and isolated runs on the same schedule even when final
		// right-sized persistence does not need to deny speculative work.
		releaseTimer := time.NewTimer(5 * time.Millisecond)
		<-releaseTimer.C
		close(storage.release)
		result := func() issue2071BenchmarkAdmission {
			select {
			case result := <-ingressDone:
				return result
			case <-time.After(5 * time.Second):
				cancel()
				fixture.service.Stop()
				b.Fatal("ingress did not complete after storage release")
				return issue2071BenchmarkAdmission{}
			}
		}()
		admissions = append(admissions, result.duration.Nanoseconds())
		if result.err != nil {
			operationErrors++
		}
		require.NoError(b, result.err)
		result = func() issue2071BenchmarkAdmission {
			select {
			case result := <-validationDone:
				return result
			case <-time.After(5 * time.Second):
				cancel()
				fixture.service.Stop()
				b.Fatal("successor replay did not complete after storage release")
				return issue2071BenchmarkAdmission{}
			}
		}()
		successorReady = append(successorReady, result.duration.Nanoseconds())
		if result.err != nil {
			operationErrors++
		}
		require.NoError(b, result.err)
		require.NoError(b, <-storeDone)
		b.StopTimer()
		cancel()
		fixture.service.Stop()

		p := fixture.budget.persistence.Snapshot()
		s := fixture.budget.speculative.Snapshot()
		budgetDenials += (p.Denied - baselinePersistence.Denied) + (s.Denied - baselineSpeculative.Denied)
		persisted += storage.bytes.Load()
		batches += storage.batches.Load()
		if current := fixture.budget.pool.Snapshot().Peak; current > peak {
			peak = current
		}
	}

	b.SetBytes(int64(targetBytes))
	reportPersistenceLatencies(b, admissions, "admission")
	reportPersistenceLatencies(b, successorReady, "successor-ready")
	b.ReportMetric(float64(budgetDenials)/float64(b.N), "budget-denials/op")
	b.ReportMetric(float64(operationErrors)/float64(b.N), "operation-errors/op")
	b.ReportMetric(float64(peak), "memory-peak-B")
	b.ReportMetric(float64(persisted)/float64(b.N), "persisted-B/op")
	b.ReportMetric(float64(batches)/float64(b.N), "storage-batches/op")
}
