package service

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/observability"
	"github.com/LeJamon/go-xrpl/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type detachedIngressDiagnosticBudget struct {
	mu sync.Mutex

	limit   uint64
	used    uint64
	changed chan struct{}

	changedReady chan struct{}
	changedOnce  sync.Once
	denied       chan struct{}
	deniedOnce   sync.Once
	wakeOnce     sync.Once
}

func newDetachedIngressDiagnosticBudget(limit uint64) *detachedIngressDiagnosticBudget {
	return &detachedIngressDiagnosticBudget{
		limit:        limit,
		used:         limit,
		changed:      make(chan struct{}),
		changedReady: make(chan struct{}),
		denied:       make(chan struct{}),
	}
}

func (b *detachedIngressDiagnosticBudget) Limit() uint64 { return b.limit }

func (b *detachedIngressDiagnosticBudget) Changed() <-chan struct{} {
	b.changedOnce.Do(func() { close(b.changedReady) })
	return b.changed
}

func (b *detachedIngressDiagnosticBudget) TryReserve(bytes uint64) bool {
	b.mu.Lock()
	if bytes > b.limit-b.used {
		b.mu.Unlock()
		b.deniedOnce.Do(func() { close(b.denied) })
		return false
	}
	b.used += bytes
	b.mu.Unlock()
	return true
}

func (b *detachedIngressDiagnosticBudget) Release(bytes uint64) {
	b.mu.Lock()
	if bytes > b.used {
		b.mu.Unlock()
		panic("detached ingress diagnostic budget underflow")
	}
	b.used -= bytes
	b.mu.Unlock()
	b.wake()
}

func (b *detachedIngressDiagnosticBudget) wake() {
	b.wakeOnce.Do(func() { close(b.changed) })
}

type detachedIngressDiagnosticLogRecord struct {
	outcome string
	retries int
}

type detachedIngressDiagnosticLogger struct {
	mu      sync.Mutex
	records []detachedIngressDiagnosticLogRecord
}

func (l *detachedIngressDiagnosticLogger) Debug(msg string, args ...any) {
	if msg != "Speculative memory admission" {
		return
	}
	record := detachedIngressDiagnosticLogRecord{}
	for i := 0; i+1 < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			continue
		}
		switch key {
		case "outcome":
			record.outcome, _ = args[i+1].(string)
		case "retries":
			record.retries, _ = args[i+1].(int)
		}
	}
	l.mu.Lock()
	l.records = append(l.records, record)
	l.mu.Unlock()
}

func (l *detachedIngressDiagnosticLogger) Trace(string, ...any) {}
func (l *detachedIngressDiagnosticLogger) Info(string, ...any)  {}
func (l *detachedIngressDiagnosticLogger) Warn(string, ...any)  {}
func (l *detachedIngressDiagnosticLogger) Error(string, ...any) {}
func (l *detachedIngressDiagnosticLogger) Fatal(string, ...any) {}
func (l *detachedIngressDiagnosticLogger) With(...any) log.Logger {
	return l
}
func (l *detachedIngressDiagnosticLogger) Named(string) log.Logger {
	return l
}

func detachedIngressDiagnosticCounter(t testing.TB, registry *prometheus.Registry, origin, outcome string) float64 {
	t.Helper()
	metrics, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range metrics {
		if family.GetName() != "goxrpl_memory_admission_total" {
			continue
		}
		for _, metric := range family.Metric {
			labels := make(map[string]string, len(metric.Label))
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["origin"] == origin && labels["outcome"] == outcome {
				return metric.Counter.GetValue()
			}
		}
	}
	return 0
}

func TestDetachedIngressWakeThenReplayBlockFinalizesAdmissionDiagnostic(t *testing.T) {
	const origin = "pr2077"

	budget := newDetachedIngressDiagnosticBudget(8 << 20)
	logger := &detachedIngressDiagnosticLogger{}
	cfg := DefaultConfig()
	cfg.Logger = logger
	cfg.PersistenceMemory = budget
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	blob, _ := startupPaymentBlob(t, "detached-ingress-diagnostic", 1)
	pending, err := openledger.ParsePendingTx(blob)
	require.NoError(t, err)
	releaseAdmission, err := svc.detachedIngress.acquire()
	require.NoError(t, err)
	defer releaseAdmission()

	poolCleanup := observability.SetMemoryPool(membudget.New(1))
	defer poolCleanup()
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(observability.NewMemoryCollector()))
	beforeCounter := detachedIngressDiagnosticCounter(t, registry, origin, "aborted")

	done := make(chan error, 1)
	go func() {
		_, _, _, _, submitErr := svc.submitDetachedIngressOwned(detachedIngressRequest{
			pendingTx: pending,
			origin:    origin,
		})
		done <- submitErr
	}()
	select {
	case <-budget.denied:
	case <-time.After(time.Second):
		t.Fatal("detached ingress did not attempt a denied memory reservation")
	}
	select {
	case <-budget.changedReady:
	case <-time.After(time.Second):
		t.Fatal("detached ingress did not subscribe to memory wakeups")
	}

	faultID, err := svc.replayFaults.BeginReplay(replayfault.Fault{Class: replayfault.ExecutionDisagreement})
	require.NoError(t, err)
	require.NoError(t, svc.replayFaults.FailReplay(faultID, replayfault.Fault{Class: replayfault.ExecutionDisagreement}))
	require.True(t, svc.ReplayBlocked())
	budget.wake()

	select {
	case submitErr := <-done:
		require.ErrorIs(t, submitErr, replayfault.ErrBlocked)
	case <-time.After(time.Second):
		t.Fatal("detached ingress did not stop after replay fault wake")
	}

	logger.mu.Lock()
	records := append([]detachedIngressDiagnosticLogRecord(nil), logger.records...)
	logger.mu.Unlock()
	require.Len(t, records, 1)
	require.Equal(t, "aborted", records[0].outcome)
	require.Equal(t, 1, records[0].retries)
	require.Equal(t, beforeCounter+1, detachedIngressDiagnosticCounter(t, registry, origin, "aborted"))
}
