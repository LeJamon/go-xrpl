package peermanagement

import (
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// rollingWindow is the number of per-interval samples averaged together,
// matching rippled metrics::SingleMetrics rollingAvgAggreg, a circular
// buffer of capacity 30 pre-filled with zeros (TxMetrics.h:56).
const rollingWindow = 30

// singleMetric is a rolling average of a value, computed either per elapsed
// second (the default) or per accumulated sample (sampleAvg). Mirrors
// rippled metrics::SingleMetrics (TxMetrics.h:41-62, TxMetrics.cpp:92-115).
//
// sampleAvg inverts rippled's perTimeUnit so the zero value (false →
// per-second) is the common case; only the peer-selection averages set it.
type singleMetric struct {
	sampleAvg     bool
	intervalStart time.Time
	accum         uint64
	n             uint64
	rollingAvg    uint64
	samples       [rollingWindow]uint64
	pos           int
}

// add folds val into the current interval and, once at least a second has
// elapsed, rolls the interval average into the 30-sample window. Matches
// SingleMetrics::addMetrics: the rolling average only advances on traffic.
func (s *singleMetric) add(val uint64) {
	now := time.Now()
	if s.intervalStart.IsZero() {
		s.intervalStart = now
	}
	s.accum += val
	s.n++

	elapsed := now.Sub(s.intervalStart)
	if elapsed < time.Second {
		return
	}

	divisor := s.n
	if !s.sampleAvg {
		divisor = uint64(elapsed / time.Second)
	}
	if divisor == 0 {
		divisor = 1
	}
	s.samples[s.pos] = s.accum / divisor
	s.pos = (s.pos + 1) % rollingWindow

	var total uint64
	for _, v := range s.samples {
		total += v
	}
	s.rollingAvg = total / rollingWindow

	s.intervalStart = now
	s.accum = 0
	s.n = 0
}

// multiMetric tracks count (m1) and byte-size (m2) rolling averages for a
// protocol message type. Mirrors rippled metrics::MultipleMetrics
// (TxMetrics.h:66-85): a single message adds 1 to the count and its size to
// the size metric.
type multiMetric struct {
	count singleMetric
	size  singleMetric
}

func (m *multiMetric) add(size uint64) {
	m.count.add(1)
	m.size.add(size)
}

// txMetrics holds the transaction reduce-relay rolling averages surfaced by
// the tx_reduce_relay RPC. Mirrors rippled metrics::TxMetrics (TxMetrics.h:88-135);
// snapshot() corresponds to TxMetrics::json() (TxMetrics.cpp:117-148).
//
// getLedger/ledgerData and the selected/suppressed/notEnabled peer-selection
// averages are part of rippled's shape and reported here, but goXRPL does not
// yet feed them: getLedger/ledgerData require rippled's per-message
// TrafficCount categorisation (only tx-set-acquisition ledger syncs count),
// and the peer-selection averages come from rippled's reduce-relay relay()
// squelch selection, which goXRPL's broadcast relay path does not perform.
// They therefore report 0 until those subsystems exist.
type txMetrics struct {
	mu           sync.Mutex
	tx           multiMetric
	haveTx       multiMetric
	getLedger    multiMetric
	ledgerData   multiMetric
	transactions multiMetric
	selected     singleMetric
	suppressed   singleMetric
	notEnabled   singleMetric
	missingTx    singleMetric
}

// addMessage records an inbound tx-relay-related protocol message of the
// given wire size, mirroring rippled metrics::TxMetrics::addMetrics(type, val)
// (TxMetrics.cpp:30-58). Message types outside the reduce-relay set are
// ignored, exactly as rippled's switch default returns.
func (m *txMetrics) addMessage(t message.MessageType, size uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch t {
	case message.TypeTransaction:
		m.tx.add(size)
	case message.TypeHaveTransactions:
		m.haveTx.add(size)
	case message.TypeGetLedger:
		m.getLedger.add(size)
	case message.TypeLedgerData:
		m.ledgerData.add(size)
	case message.TypeTransactions:
		m.transactions.add(size)
	}
}

// addMissingTx records the number of transactions carried by an inbound
// TMTransactions message, mirroring rippled's
// addTxMetrics(m->transactions_size()) at PeerImp.cpp:2680.
func (m *txMetrics) addMissingTx(n uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.missingTx.add(n)
}

// addRelayPeers records a reduce-relay peer-selection sample: how many peers
// were selected to relay to, how many were suppressed (already had the tx),
// and how many had the feature disabled. Mirrors rippled
// metrics::TxMetrics::addMetrics(selected, suppressed, notEnabled), fed from
// OverlayImpl::relay (OverlayImpl.cpp:1257,1267).
func (m *txMetrics) addRelayPeers(selected, suppressed, notEnabled uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.selected.add(selected)
	m.suppressed.add(suppressed)
	m.notEnabled.add(notEnabled)
}

// TxMetricsSnapshot is a point-in-time copy of the reduce-relay rolling
// averages. Field pairs mirror rippled metrics::TxMetrics::json()
// (TxMetrics.cpp:117-148); the RPC layer renders them into the txr_* wire keys.
type TxMetricsSnapshot struct {
	TxCnt           uint64
	TxSz            uint64
	HaveTxCnt       uint64
	HaveTxSz        uint64
	GetLedgerCnt    uint64
	GetLedgerSz     uint64
	LedgerDataCnt   uint64
	LedgerDataSz    uint64
	TransactionsCnt uint64
	TransactionsSz  uint64
	SelectedCnt     uint64
	SuppressedCnt   uint64
	NotEnabledCnt   uint64
	MissingTxFreq   uint64
}

func (m *txMetrics) snapshot() TxMetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return TxMetricsSnapshot{
		TxCnt:           m.tx.count.rollingAvg,
		TxSz:            m.tx.size.rollingAvg,
		HaveTxCnt:       m.haveTx.count.rollingAvg,
		HaveTxSz:        m.haveTx.size.rollingAvg,
		GetLedgerCnt:    m.getLedger.count.rollingAvg,
		GetLedgerSz:     m.getLedger.size.rollingAvg,
		LedgerDataCnt:   m.ledgerData.count.rollingAvg,
		LedgerDataSz:    m.ledgerData.size.rollingAvg,
		TransactionsCnt: m.transactions.count.rollingAvg,
		TransactionsSz:  m.transactions.size.rollingAvg,
		SelectedCnt:     m.selected.rollingAvg,
		SuppressedCnt:   m.suppressed.rollingAvg,
		NotEnabledCnt:   m.notEnabled.rollingAvg,
		MissingTxFreq:   m.missingTx.rollingAvg,
	}
}

// TxMetricsSnapshot returns the current transaction reduce-relay rolling
// averages. Mirrors rippled OverlayImpl::txMetrics(), the source for the
// tx_reduce_relay RPC.
func (o *Overlay) TxMetricsSnapshot() TxMetricsSnapshot {
	return o.txm.snapshot()
}

// recordInboundTxMetric records an inbound tx-relay-related message into the
// reduce-relay metrics, mirroring the gated addTxMetrics call in rippled
// PeerImp::onMessageBegin (PeerImp.cpp:1038-1053). TMTransaction,
// TMHaveTransactions and TMTransactions are always counted; for TMGetLedger /
// TMLedgerData only the transaction-set-candidate variants count, matching
// rippled's TrafficCount::categorize gl_tsc_*/ld_tsc_* categories
// (TrafficCount.cpp:64-106) — general ledger-history sync is excluded.
func (o *Overlay) recordInboundTxMetric(msgType message.MessageType, payload []byte, wireSize uint64) {
	switch msgType {
	case message.TypeTransaction, message.TypeHaveTransactions, message.TypeTransactions:
		o.txm.addMessage(msgType, wireSize)
	case message.TypeGetLedger:
		if decoded, err := message.Decode(msgType, payload); err == nil {
			if gl, ok := decoded.(*message.GetLedger); ok && gl.InfoType == message.LedgerInfoTsCandidate {
				o.txm.addMessage(msgType, wireSize)
			}
		}
	case message.TypeLedgerData:
		if decoded, err := message.Decode(msgType, payload); err == nil {
			if ld, ok := decoded.(*message.LedgerData); ok && ld.InfoType == message.LedgerInfoTsCandidate {
				o.txm.addMessage(msgType, wireSize)
			}
		}
	}
}
