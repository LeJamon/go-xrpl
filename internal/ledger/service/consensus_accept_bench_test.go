package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type consensusAcceptanceBenchmarkCase struct {
	name        string
	coldDelay   time.Duration
	withRefresh bool
}

// BenchmarkService_ConsensusAcceptanceContention compares acceptance builds with
// injected state-fetch latency and concurrent generation refresh on rotating
// Pebble storage. It does not include speculative ingress or model physical HDDs.
func BenchmarkService_ConsensusAcceptanceContention(b *testing.B) {
	cases := []consensusAcceptanceBenchmarkCase{
		{name: "fast", coldDelay: 0},
		{name: "fast-refresh", coldDelay: 0, withRefresh: true},
		{name: "cold", coldDelay: time.Millisecond},
		{name: "cold-refresh", coldDelay: time.Millisecond, withRefresh: true},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			benchmarkConsensusAcceptanceContention(b, tc)
		})
	}
}

func benchmarkConsensusAcceptanceContention(b *testing.B, tc consensusAcceptanceBenchmarkCase) {
	b.Helper()
	b.StopTimer()
	fixture := newBenchmarkRefreshFixture(b, 16_384, 256<<10)
	var (
		gateWait         = make([]time.Duration, 0, b.N)
		gateHold         = make([]time.Duration, 0, b.N)
		build            = make([]time.Duration, 0, b.N)
		transactionApply = make([]time.Duration, 0, b.N)
		closeStateHash   = make([]time.Duration, 0, b.N)
		coldFetches      int64
	)

	b.ResetTimer()
	for round := range b.N {
		b.StopTimer()
		logger := &consensusAcceptanceBenchmarkLogger{}
		svc, parent, blob, delayedFamily := newConsensusAcceptanceBenchmarkService(
			b, fixture, logger, tc.coldDelay, round,
		)

		var refreshDone <-chan error
		if tc.withRefresh {
			fixture.db.promotionStart = make(chan struct{})
			fixture.db.promotionOnce = sync.Once{}
			fixture.db.promotionDelay = 100 * time.Microsecond
			done := make(chan error, 1)
			refreshDone = done
			go func() {
				err := svc.refreshGenerationStateWithBatch(
					b.Context(),
					fixture.root,
					fixture.seq,
					fixture.db,
					nil,
					1,
					storedSHAMapPromotionBatchNodes,
					storedSHAMapPromotionBatchBytes,
				)
				done <- err
			}()
			select {
			case <-fixture.db.promotionStart:
			case err := <-done:
				b.Fatalf("refresh ended before its first promotion: %v", err)
			case <-time.After(5 * time.Second):
				b.Fatal("refresh did not reach its first promotion")
			}
		}

		b.StartTimer()
		_, err := svc.AcceptConsensusResult(
			b.Context(),
			parent,
			[][]byte{blob},
			nil,
			parent.CloseTime().Add(10*time.Second),
			true,
		)
		b.StopTimer()
		require.NoError(b, err)
		if refreshDone != nil {
			require.NoError(b, <-refreshDone)
		}
		sample, ok := logger.latest()
		if !ok {
			b.Fatal("acceptance did not emit consensus acceptance timings")
		}
		gateWait = append(gateWait, sample.openLedgerWait)
		gateHold = append(gateHold, sample.openLedgerHold)
		build = append(build, sample.build)
		transactionApply = append(transactionApply, sample.transactionApply)
		closeStateHash = append(closeStateHash, sample.closeStateTxHash)
		coldFetches += delayedFamily.fetches.Load()
		fixture.db.promotionDelay = 0
		// Stop is outside the timed region and before the database is reused by
		// the next round.
		svc.Stop()
	}

	reportConsensusAcceptanceQuantiles(b, "gate-wait", gateWait)
	reportConsensusAcceptanceQuantiles(b, "gate-hold", gateHold)
	reportConsensusAcceptanceQuantiles(b, "build", build)
	reportConsensusAcceptanceQuantiles(b, "transaction-apply", transactionApply)
	reportConsensusAcceptanceQuantiles(b, "close-state-tx-hash", closeStateHash)
	b.ReportMetric(float64(coldFetches)/float64(len(gateWait)), "state-fetches/round")
	b.ReportMetric(float64(len(gateWait)), "acceptance-rounds")
}

type consensusAcceptanceBenchmarkLogger struct {
	mu           sync.Mutex
	latestSample consensusAcceptanceBenchmarkTiming
	haveSample   bool
}

type consensusAcceptanceBenchmarkTiming struct {
	openLedgerWait, openLedgerHold, build, transactionApply, closeStateTxHash time.Duration
}

func (l *consensusAcceptanceBenchmarkLogger) latest() (consensusAcceptanceBenchmarkTiming, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.latestSample, l.haveSample
}

func (l *consensusAcceptanceBenchmarkLogger) Info(msg string, args ...any) {
	if msg != "Consensus acceptance timings" {
		return
	}
	var sample consensusAcceptanceBenchmarkTiming
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
		case "open_ledger_wait":
			sample.openLedgerWait = duration
		case "open_ledger_hold":
			sample.openLedgerHold = duration
		case "build":
			sample.build = duration
		case "transaction_apply":
			sample.transactionApply = duration
		case "close_state_tx_hash":
			sample.closeStateTxHash = duration
		}
	}

	l.mu.Lock()
	l.latestSample = sample
	l.haveSample = true
	l.mu.Unlock()
}

func (l *consensusAcceptanceBenchmarkLogger) Trace(string, ...any)       {}
func (l *consensusAcceptanceBenchmarkLogger) Debug(string, ...any)       {}
func (l *consensusAcceptanceBenchmarkLogger) Warn(string, ...any)        {}
func (l *consensusAcceptanceBenchmarkLogger) Error(string, ...any)       {}
func (l *consensusAcceptanceBenchmarkLogger) Fatal(msg string, _ ...any) { panic(msg) }
func (l *consensusAcceptanceBenchmarkLogger) With(...any) log.Logger     { return l }
func (l *consensusAcceptanceBenchmarkLogger) Named(string) log.Logger    { return l }

func reportConsensusAcceptanceQuantiles(b *testing.B, name string, values []time.Duration) {
	if len(values) == 0 {
		return
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, percentile := range []int{50, 95, 99} {
		index := (len(sorted)*percentile+99)/100 - 1
		if index < 0 {
			index = 0
		}
		if index >= len(sorted) {
			index = len(sorted) - 1
		}
		b.ReportMetric(float64(sorted[index].Nanoseconds()), fmt.Sprintf("%s-p%d-ns", name, percentile))
	}
}

type delayedConsensusAcceptanceFamily struct {
	shamap.Family
	delay   time.Duration
	fetches atomic.Int64
}

func (f *delayedConsensusAcceptanceFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.fetches.Add(1)
	if f.delay > 0 {
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return f.Family.Fetch(ctx, hash)
}

func newConsensusAcceptanceBenchmarkService(
	b *testing.B,
	fixture *benchmarkRefreshFixture,
	logger log.Logger,
	delay time.Duration,
	round int,
) (*Service, *ledger.Ledger, []byte, *delayedConsensusAcceptanceFamily) {
	b.Helper()
	cfg := Config{
		Standalone: true,
		Startup: StartupConfig{
			Mode:   StartupLoad,
			Ledger: fmt.Sprintf("%X", fixture.template.Hash()),
		},
		Logger:       logger,
		NodeStore:    fixture.db,
		SHAMapFamily: fixture.svc.shamapFamily,
	}
	svc, err := New(cfg)
	require.NoError(b, err)
	require.NoError(b, svc.Start())
	parent := svc.GetClosedLedger()
	require.NotNil(b, parent)
	require.Equal(b, fixture.template.Hash(), parent.Hash())
	delayedFamily := &delayedConsensusAcceptanceFamily{Family: svc.shamapFamily, delay: delay}
	parent.SetStateMapFamily(delayedFamily)
	blob, _ := startupPaymentBlob(b, fmt.Sprintf("consensus-accept-bench-%d", round), 1)
	return svc, parent, blob, delayedFamily
}
