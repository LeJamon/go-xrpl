package service

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func BenchmarkService_RefreshWithPersistence(b *testing.B) {
	for _, workers := range []int{1, 2, 4} {
		for _, batchNodes := range []int{0, 64, storedSHAMapPromotionBatchNodes} {
			b.Run(fmt.Sprintf("workers=%d/batch=%d", workers, batchNodes), func(b *testing.B) {
				benchmarkRefreshWithPersistence(b, workers, batchNodes)
			})
		}
	}
}

func benchmarkRefreshWithPersistence(b *testing.B, workers, batchNodes int) {
	fixture := newBenchmarkRefreshFixture(b, 16_384, 256<<10)
	var persistence, overlapping []int64
	var refreshDuration time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		ledgers := benchmarkPersistenceLedgers(b, 32)
		writer := fixture.svc
		fixture.db.promotionStart = make(chan struct{})
		done := make(chan error, 1)
		var completed atomic.Bool
		var elapsed time.Duration
		runtime.GC()
		b.StartTimer()
		go func() {
			started := time.Now()
			err := fixture.svc.refreshGenerationStateWithBatch(
				b.Context(), fixture.root, fixture.seq, fixture.db, nil,
				workers, batchNodes, storedSHAMapPromotionBatchBytes,
			)
			elapsed = time.Since(started)
			completed.Store(true)
			done <- err
		}()
		select {
		case <-fixture.db.promotionStart:
		case err := <-done:
			b.Fatalf("refresh ended before its first promotion: %v", err)
		}
		var persistErr error
		for _, closed := range ledgers {
			concurrent := !completed.Load()
			started := time.Now()
			persistErr = writer.persistToNodeStore(b.Context(), closed, closed.Sequence())
			duration := time.Since(started).Nanoseconds()
			persistence = append(persistence, duration)
			if concurrent {
				overlapping = append(overlapping, duration)
			}
			if persistErr != nil {
				break
			}
		}
		refreshErr := <-done
		b.StopTimer()
		require.NoError(b, persistErr)
		require.NoError(b, refreshErr)
		refreshDuration += elapsed
		fixture.rotateAndReopen(b)
	}
	if len(overlapping) == 0 {
		b.Fatal("no ledger persistence operation overlapped refresh")
	}
	b.ReportMetric(float64(refreshDuration.Nanoseconds())/float64(b.N), "refresh-ns/op")
	b.ReportMetric(float64(len(persistence))/float64(b.N), "persists/op")
	b.ReportMetric(float64(len(overlapping))/float64(b.N), "overlapping-persists/op")
	reportPersistenceLatencies(b, persistence, "persist")
	reportPersistenceLatencies(b, overlapping, "overlap")
}

func reportPersistenceLatencies(b *testing.B, values []int64, prefix string) {
	slices.Sort(values)
	b.ReportMetric(float64(values[(len(values)-1)/2]), prefix+"-p50-ns")
	b.ReportMetric(float64(values[(len(values)*95+99)/100-1]), prefix+"-p95-ns")
	b.ReportMetric(float64(values[(len(values)*99+99)/100-1]), prefix+"-p99-ns")
	b.ReportMetric(float64(values[len(values)-1]), prefix+"-max-ns")
}

func benchmarkPersistenceLedgers(b *testing.B, count int) []*ledger.Ledger {
	b.Helper()
	initial, err := genesis.Create(genesis.DefaultConfig())
	require.NoError(b, err)
	parent, err := ledger.FromGenesis(initial.Header, initial.StateMap, initial.TxMap, drops.Fees{})
	require.NoError(b, err)
	closed := make([]*ledger.Ledger, 0, count)
	for i := range count {
		closeTime := parent.CloseTime().Add(10 * time.Second)
		next, err := ledger.NewOpen(parent, closeTime)
		require.NoError(b, err)
		for change := range 32 {
			var key [32]byte
			key[0] = 0xfe
			binary.BigEndian.PutUint32(key[28:], uint32(i*32+change))
			data := make([]byte, 128)
			binary.BigEndian.PutUint32(data[124:], uint32(i*32+change))
			require.NoError(b, next.Insert(keylet.Keylet{Key: key}, data))
		}
		require.NoError(b, next.Close(closeTime, 0))
		closed = append(closed, next)
		parent = next
	}
	return closed
}
