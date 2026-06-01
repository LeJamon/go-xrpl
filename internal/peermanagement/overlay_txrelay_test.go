package peermanagement

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// forceRoll backdates the interval by 2s so the next add() rolls the
// accumulated value into the rolling-average window (rippled's >=1s gate),
// with a deterministic per-second divisor of 2.
func (s *singleMetric) forceRoll() {
	s.intervalStart = time.Now().Add(-2 * time.Second)
}

func TestSingleMetricRollingAverage(t *testing.T) {
	var s singleMetric // per-second (sampleAvg=false)

	s.add(100)
	if s.rollingAvg != 0 {
		t.Fatalf("rollingAvg before first roll = %d, want 0", s.rollingAvg)
	}

	s.add(100) // accum now 200, still in the open interval
	s.forceRoll()
	s.add(0) // rolls: 200 / 2s = 100, averaged over the 30-slot window

	if want := uint64(200 / 2 / rollingWindow); s.rollingAvg != want {
		t.Fatalf("rollingAvg after roll = %d, want %d", s.rollingAvg, want)
	}
}

func TestTxMetricsAddMessageByType(t *testing.T) {
	var m txMetrics

	// Feed each fed metric one large sample, roll a 2s interval, then add a
	// zero to trigger the roll: rollingAvg = val / 2 / rollingWindow.
	m.addMessage(message.TypeTransaction, 6000)
	m.addMessage(message.TypeHaveTransactions, 3000)
	m.addMessage(message.TypeTransactions, 9000)
	m.addMissingTx(1800)

	for _, s := range []*singleMetric{
		&m.tx.size, &m.haveTx.size, &m.transactions.size, &m.missingTx,
	} {
		s.forceRoll()
	}
	m.addMessage(message.TypeTransaction, 0)
	m.addMessage(message.TypeHaveTransactions, 0)
	m.addMessage(message.TypeTransactions, 0)
	m.addMissingTx(0)

	snap := m.snapshot()
	checks := []struct {
		name string
		got  uint64
		want uint64
	}{
		{"TxSz", snap.TxSz, 6000 / 2 / rollingWindow},
		{"HaveTxSz", snap.HaveTxSz, 3000 / 2 / rollingWindow},
		{"TransactionsSz", snap.TransactionsSz, 9000 / 2 / rollingWindow},
		{"MissingTxFreq", snap.MissingTxFreq, 1800 / 2 / rollingWindow},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	// Metrics go-xrpl does not feed stay at zero.
	if snap.GetLedgerCnt != 0 || snap.GetLedgerSz != 0 ||
		snap.LedgerDataCnt != 0 || snap.LedgerDataSz != 0 ||
		snap.SelectedCnt != 0 || snap.SuppressedCnt != 0 || snap.NotEnabledCnt != 0 {
		t.Errorf("unwired metrics should be zero, got %+v", snap)
	}
}

func TestTxMetricsSnapshotZeroValue(t *testing.T) {
	var m txMetrics
	if snap := m.snapshot(); snap != (TxMetricsSnapshot{}) {
		t.Errorf("zero-value snapshot = %+v, want all zero", snap)
	}
}
