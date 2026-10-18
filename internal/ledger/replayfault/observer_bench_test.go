package replayfault

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

const (
	benchmarkJournalDelay = 250 * time.Microsecond
	benchmarkSampleLimit  = 256
)

func installDelayedFileSync(delay time.Duration) func() {
	original := syncFileFn
	syncFileFn = func(file *os.File) error {
		time.Sleep(delay)
		return original(file)
	}
	return func() { syncFileFn = original }
}

func reportLatencyPercentiles(b *testing.B, name string, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	percentile := func(percent int) float64 {
		index := (len(sorted)*percent + 99) / 100
		if index < 1 {
			index = 1
		}
		return float64(sorted[index-1].Nanoseconds())
	}
	b.ReportMetric(percentile(50), name+"-p50-ns")
	b.ReportMetric(percentile(95), name+"-p95-ns")
	b.ReportMetric(percentile(99), name+"-p99-ns")
	b.ReportMetric(float64(len(sorted)), name+"-samples")
}

func benchmarkPath(dir string, index int) string {
	return filepath.Join(dir, fmt.Sprintf("fault-%d.json", index))
}

func BenchmarkReplayFaultObserverAndValidatorLatency(b *testing.B) {
	b.Run("observer-latency", func(b *testing.B) {
		dir := b.TempDir()
		original := syncFileFn
		var samples []time.Duration
		if b.N < benchmarkSampleLimit {
			samples = make([]time.Duration, 0, b.N)
		} else {
			samples = make([]time.Duration, 0, benchmarkSampleLimit)
		}
		defer func() {
			syncFileFn = original
			reportLatencyPercentiles(b, "observer", samples)
		}()

		for i := 0; i < b.N; i++ {
			path := benchmarkPath(dir, i)
			store, err := Open(path)
			if err != nil {
				b.Fatal(err)
			}
			entered := make(chan struct{})
			syncFileFn = func(file *os.File) error {
				select {
				case <-entered:
				default:
					close(entered)
				}
				time.Sleep(benchmarkJournalDelay)
				return original(file)
			}
			mutationDone := make(chan error, 1)
			go func() {
				_, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
				mutationDone <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				b.Fatal("timed out waiting for benchmark journal sync")
			}
			observerStarted := make(chan struct{})
			observerLatency := make(chan time.Duration, 1)
			go func() {
				close(observerStarted)
				started := time.Now()
				_ = store.Blocked()
				_ = store.Status()
				_ = store.Snapshot()
				observerLatency <- time.Since(started)
			}()
			<-observerStarted
			latency := <-observerLatency
			if len(samples) < benchmarkSampleLimit {
				samples = append(samples, latency)
			}
			if err := <-mutationDone; err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("validator-fence-wait", func(b *testing.B) {
		dir := b.TempDir()
		original := syncFileFn
		var samples []time.Duration
		if b.N < benchmarkSampleLimit {
			samples = make([]time.Duration, 0, b.N)
		} else {
			samples = make([]time.Duration, 0, benchmarkSampleLimit)
		}
		defer func() {
			syncFileFn = original
			reportLatencyPercentiles(b, "validator-fence", samples)
		}()

		for i := 0; i < b.N; i++ {
			path := benchmarkPath(dir, i)
			store, err := Open(path)
			if err != nil {
				b.Fatal(err)
			}
			entered := make(chan struct{})
			syncFileFn = func(file *os.File) error {
				select {
				case <-entered:
				default:
					close(entered)
				}
				time.Sleep(benchmarkJournalDelay)
				return original(file)
			}
			mutationDone := make(chan error, 1)
			go func() {
				_, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
				mutationDone <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				b.Fatal("timed out waiting for benchmark journal sync")
			}
			validatorStarted := make(chan struct{})
			validatorResult := make(chan struct {
				err     error
				elapsed time.Duration
			}, 1)
			go func() {
				close(validatorStarted)
				started := time.Now()
				err := store.WithValidator(func() error { return nil })
				validatorResult <- struct {
					err     error
					elapsed time.Duration
				}{err: err, elapsed: time.Since(started)}
			}()
			<-validatorStarted
			result := <-validatorResult
			if err := <-mutationDone; err != nil {
				b.Fatal(err)
			}
			if result.err != nil {
				b.Fatal(result.err)
			}
			if len(samples) < benchmarkSampleLimit {
				samples = append(samples, result.elapsed)
			}
		}
	})
}

func BenchmarkReplayFaultReplayThroughput(b *testing.B) {
	dir := b.TempDir()
	restore := installDelayedFileSync(benchmarkJournalDelay)
	defer restore()
	b.ResetTimer()
	started := time.Now()
	for i := 0; i < b.N; i++ {
		store, err := Open(benchmarkPath(dir, i))
		if err != nil {
			b.Fatal(err)
		}
		id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
		if err != nil {
			b.Fatal(err)
		}
		if err := store.CompleteReplay(id); err != nil {
			b.Fatal(err)
		}
	}
	elapsed := time.Since(started)
	b.StopTimer()
	if elapsed > 0 {
		b.ReportMetric(float64(b.N)/elapsed.Seconds(), "journal-cycles-per-second")
	}
}

func BenchmarkReplayFaultRecoveryLatency(b *testing.B) {
	dir := b.TempDir()
	stores := make([]*Store, b.N)
	ids := make([]string, b.N)
	for i := range stores {
		store, err := Open(benchmarkPath(dir, i))
		if err != nil {
			b.Fatal(err)
		}
		id := seedFaultForBenchmark(store)
		stores[i] = store
		ids[i] = id
	}
	restore := installDelayedFileSync(benchmarkJournalDelay)
	defer restore()
	samples := make([]time.Duration, 0, min(b.N, benchmarkSampleLimit))
	b.ResetTimer()
	started := time.Now()
	for i, store := range stores {
		recoveryStarted := time.Now()
		if err := store.Revalidate(context.Background(), ids[i], func(context.Context, Fault) error { return nil }); err != nil {
			b.Fatal(err)
		}
		if len(samples) < benchmarkSampleLimit {
			samples = append(samples, time.Since(recoveryStarted))
		}
	}
	elapsed := time.Since(started)
	b.StopTimer()
	reportLatencyPercentiles(b, "recovery", samples)
	if elapsed > 0 {
		b.ReportMetric(float64(b.N)/elapsed.Seconds(), "recovery-per-second")
	}
}

func seedFaultForBenchmark(store *Store) string {
	id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
	if err != nil {
		panic(err)
	}
	if err := store.FailReplay(id, Fault{Class: ExecutionDisagreement}); err != nil {
		panic(err)
	}
	return id
}
