package service

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

func benchTxHash(seq uint32, i int) [32]byte {
	var h [32]byte
	binary.BigEndian.PutUint32(h[0:4], seq)
	binary.BigEndian.PutUint32(h[4:8], uint32(i))
	h[8] = 0xB5
	return h
}

func benchStagedResults(seq uint32, txPerLedger int) *stagedTransactionResults {
	staged := &stagedTransactionResults{
		results:   make([]TransactionResultEvent, txPerLedger),
		positions: make(map[[32]byte]uint32, txPerLedger),
		ledgerSeq: seq,
	}
	for i := range txPerLedger {
		h := benchTxHash(seq, i)
		staged.results[i].TxHash = h
		staged.positions[h] = uint32(i)
	}
	return staged
}

// BenchmarkCommitAndEvictHistory measures the per-close work done under
// Service.mu and historyComponent.mu once the history window is full: index
// one ledger's transactions and evict the ledger that slid out of the window.
func BenchmarkCommitAndEvictHistory(b *testing.B) {
	const window = 256
	for _, txPerLedger := range []int{50, 200, 1000} {
		b.Run(fmt.Sprintf("window=%d/tx=%d", window, txPerLedger), func(b *testing.B) {
			cfg := DefaultConfig()
			cfg.LedgerCacheSize = window
			svc, err := New(cfg)
			if err != nil {
				b.Fatal(err)
			}
			stub, err := ledger.NewFromHeader(header.LedgerHeader{}, shamap.New(shamap.TypeState), shamap.New(shamap.TypeTransaction), drops.Fees{})
			if err != nil {
				b.Fatal(err)
			}

			svc.mu.Lock()
			svc.historyComponent.mu.Lock()
			defer func() {
				svc.historyComponent.mu.Unlock()
				svc.mu.Unlock()
			}()
			var seq uint32
			for seq = 1; seq <= window; seq++ {
				svc.ledgerHistory[seq] = stub
				svc.commitTransactionResultsLocked(benchStagedResults(seq, txPerLedger))
				svc.evictOldHistoryLocked(seq)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				staged := benchStagedResults(seq, txPerLedger)
				b.StartTimer()
				svc.ledgerHistory[seq] = stub
				svc.commitTransactionResultsLocked(staged)
				svc.evictOldHistoryLocked(seq)
				seq++
			}
			b.StopTimer()
			if got, want := len(svc.txIndex), window*txPerLedger; got != want {
				b.Fatalf("txIndex size = %d, want %d", got, want)
			}
		})
	}
}
