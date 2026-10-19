package adaptor

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
)

type syntheticServingOffer struct {
	class  ledgerServeClass
	peerID peermanagement.PeerID
	bytes  int64
}

type syntheticServingResult struct {
	offered    [ledgerServeClassCount]int
	completed  [ledgerServeClassCount]int
	dropped    [ledgerServeClassCount]int
	queueWaits [ledgerServeClassCount][]time.Duration
	foreground time.Duration
}

var syntheticServingSink atomic.Uint64

func syntheticServingOffers(ordinary, txSet int, ordinaryFirst bool) []syntheticServingOffer {
	offers := make([]syntheticServingOffer, 0, ordinary+txSet)
	appendClass := func(class ledgerServeClass, count int, peerBase peermanagement.PeerID) {
		for i := 0; i < count; i++ {
			offers = append(offers, syntheticServingOffer{
				class:  class,
				peerID: peerBase + peermanagement.PeerID(i/ledgerServePerPeerQueue),
				bytes:  1,
			})
		}
	}
	if ordinaryFirst {
		appendClass(ledgerServeOrdinary, ordinary, 10_000)
		appendClass(ledgerServeTxSet, txSet, 20_000)
	} else {
		appendClass(ledgerServeTxSet, txSet, 20_000)
		appendClass(ledgerServeOrdinary, ordinary, 10_000)
	}
	return offers
}

func syntheticServingWork() {
	value := uint64(0x9e3779b9)
	for i := 0; i < 256; i++ {
		value = value*1664525 + uint64(i+1)
	}
	syntheticServingSink.Add(value)
}

func recordSyntheticServingStart(
	mu *sync.Mutex,
	result *syntheticServingResult,
	job syntheticServingOffer,
	admitted time.Time,
) {
	mu.Lock()
	result.queueWaits[job.class] = append(result.queueWaits[job.class], time.Since(admitted))
	result.completed[job.class]++
	mu.Unlock()
}

func waitSyntheticServingReady(ready <-chan struct{}, count int) {
	for range count {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			panic("synthetic serving workers did not become ready")
		}
	}
}

func runSyntheticServingFIFO(offers []syntheticServingOffer, workers int) syntheticServingResult {
	type queuedOffer struct {
		job      syntheticServingOffer
		admitted time.Time
	}
	result := syntheticServingResult{}
	for _, offer := range offers {
		result.offered[offer.class]++
	}
	queue := make(chan queuedOffer, serveQueueDepth)
	var resultMu sync.Mutex
	var jobs sync.WaitGroup
	for _, job := range offers {
		offer := queuedOffer{job: job, admitted: time.Now()}
		select {
		case queue <- offer:
			jobs.Add(1)
		default:
			result.dropped[offer.job.class]++
		}
	}
	close(queue)

	gate := make(chan struct{})
	ready := make(chan struct{}, len(offers))
	var workersDone sync.WaitGroup
	workersDone.Add(workers)
	for range workers {
		go func() {
			defer workersDone.Done()
			for offer := range queue {
				recordSyntheticServingStart(&resultMu, &result, offer.job, offer.admitted)
				ready <- struct{}{}
				<-gate
				syntheticServingWork()
				jobs.Done()
			}
		}()
	}
	result.foreground = runSyntheticForeground(gate, ready, workers, &jobs)
	workersDone.Wait()
	return result
}

func runSyntheticServingFair(offers []syntheticServingOffer, workers int) syntheticServingResult {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pools := newLedgerServePools(ctx, workers)
	result := syntheticServingResult{}
	for _, offer := range offers {
		result.offered[offer.class]++
	}
	var resultMu sync.Mutex
	var jobs sync.WaitGroup
	gate := make(chan struct{})
	ready := make(chan struct{}, len(offers))
	for _, job := range offers {
		admitted := time.Now()
		jobs.Add(1)
		accepted := pools[job.class].SubmitOwnedBytes(ctx, job.peerID, job.bytes, func(context.Context) {
			defer jobs.Done()
			recordSyntheticServingStart(&resultMu, &result, job, admitted)
			ready <- struct{}{}
			<-gate
			syntheticServingWork()
		}, nil)
		if !accepted {
			jobs.Done()
			result.dropped[job.class]++
		}
	}

	runDone := make(chan struct{}, ledgerServeClassCount)
	for _, pool := range pools {
		go func() {
			_ = pool.Run(ctx)
			runDone <- struct{}{}
		}()
	}
	result.foreground = runSyntheticForeground(gate, ready, workers, &jobs)
	cancel()
	for range pools {
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			panic("fair serving scheduler did not stop")
		}
	}
	return result
}

func runSyntheticForeground(gate chan struct{}, ready <-chan struct{}, workers int, jobs *sync.WaitGroup) time.Duration {
	waitSyntheticServingReady(ready, workers)
	started := make(chan struct{})
	done := make(chan time.Duration, 1)
	go func() {
		close(started)
		startedAt := time.Now()
		syntheticServingWork()
		done <- time.Since(startedAt)
	}()
	<-started
	close(gate)
	jobs.Wait()
	return <-done
}

func syntheticServingPercentile(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(float64(len(ordered)-1) * percentile)
	return ordered[index]
}

func reportSyntheticServingResult(b *testing.B, result syntheticServingResult, iterations int) {
	var offered, completed, dropped int
	for class, label := range []string{"ledger", "txset"} {
		offered += result.offered[class]
		completed += result.completed[class]
		dropped += result.dropped[class]
		b.ReportMetric(float64(result.offered[class])/float64(iterations), "offered_jobs/op_"+label)
		b.ReportMetric(float64(result.completed[class])/float64(iterations), "completed_jobs/op_"+label)
		b.ReportMetric(float64(result.dropped[class])/float64(iterations), "dropped_jobs/op_"+label)
	}
	for class, label := range []string{"ledger", "txset"} {
		waits := result.queueWaits[class]
		b.ReportMetric(float64(syntheticServingPercentile(waits, 0.95))/float64(time.Microsecond), "queue_p95_us_"+label)
		b.ReportMetric(float64(syntheticServingPercentile(waits, 0.99))/float64(time.Microsecond), "queue_p99_us_"+label)
	}
	b.ReportMetric(float64(offered)/float64(iterations), "offered_jobs/op")
	b.ReportMetric(float64(completed)/float64(iterations), "completed_jobs/op")
	b.ReportMetric(float64(dropped)/float64(iterations), "dropped_jobs/op")
	b.ReportMetric(float64(result.foreground)/float64(time.Microsecond), "foreground_wall_us")
}

// BenchmarkLedgerServingScheduler compares a matched synthetic offer stream
// against the old single FIFO queue and the class-separated scheduler. It is
// an admission/scheduling benchmark only; the work is synthetic and makes no
// claim about production ledger latency or consensus behavior.
func BenchmarkLedgerServingScheduler(b *testing.B) {
	const workers = 4
	for _, scenario := range []struct {
		name          string
		ordinaryFirst bool
	}{
		{name: "bulk_state_first", ordinaryFirst: true},
		{name: "bulk_state_reverse", ordinaryFirst: false},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			offers := syntheticServingOffers(serveQueueDepth+serveQueueDepth*3/4, serveQueueDepth/4, scenario.ordinaryFirst)
			for _, mode := range []struct {
				name string
				run  func([]syntheticServingOffer, int) syntheticServingResult
			}{
				{name: "fifo", run: runSyntheticServingFIFO},
				{name: "fair", run: runSyntheticServingFair},
			} {
				b.Run(mode.name, func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					var aggregate syntheticServingResult
					for range b.N {
						result := mode.run(offers, workers)
						for class := range aggregate.offered {
							aggregate.offered[class] += result.offered[class]
							aggregate.completed[class] += result.completed[class]
							aggregate.dropped[class] += result.dropped[class]
							aggregate.queueWaits[class] = append(aggregate.queueWaits[class], result.queueWaits[class]...)
						}
						aggregate.foreground += result.foreground
					}
					b.StopTimer()
					wantOffered := [ledgerServeClassCount]int{
						ledgerServeOrdinary: (serveQueueDepth + serveQueueDepth*3/4) * b.N,
						ledgerServeTxSet:    (serveQueueDepth / 4) * b.N,
					}
					wantCompleted := [ledgerServeClassCount]int{
						ledgerServeOrdinary: (serveQueueDepth * 3 / 4) * b.N,
						ledgerServeTxSet:    (serveQueueDepth / 4) * b.N,
					}
					if mode.name == "fifo" {
						if scenario.ordinaryFirst {
							wantCompleted = [ledgerServeClassCount]int{ledgerServeOrdinary: serveQueueDepth * b.N}
						} else {
							wantCompleted = [ledgerServeClassCount]int{
								ledgerServeOrdinary: (serveQueueDepth - serveQueueDepth/4) * b.N,
								ledgerServeTxSet:    (serveQueueDepth / 4) * b.N,
							}
						}
					}
					for class := range aggregate.offered {
						if aggregate.offered[class] != wantOffered[class] {
							b.Fatalf("%s offer mismatch class %d: got=%d want=%d", mode.name, class, aggregate.offered[class], wantOffered[class])
						}
						if aggregate.offered[class] != aggregate.completed[class]+aggregate.dropped[class] {
							b.Fatalf("%s accounting mismatch class %d: offered=%d completed=%d dropped=%d", mode.name, class, aggregate.offered[class], aggregate.completed[class], aggregate.dropped[class])
						}
						if aggregate.completed[class] != wantCompleted[class] {
							b.Fatalf("%s class completion skew class %d: got=%d want=%d", mode.name, class, aggregate.completed[class], wantCompleted[class])
						}
					}
					aggregate.foreground /= time.Duration(b.N)
					reportSyntheticServingResult(b, aggregate, b.N)
				})
			}
		})
	}
}
