package adaptor

import (
	"bytes"
	"container/list"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

const (
	transactionIngressMemoTTL        = 5 * time.Minute
	transactionIngressMemoMaxEntries = 1 << 12
	// This bound covers retained exact-input payload copies. Entry bookkeeping
	// is separately bounded by transactionIngressMemoMaxEntries.
	transactionIngressMemoMaxBytes = 8 << 20
	// ParseFromBinary rejects larger wire inputs before decoding them.
	transactionIngressMemoMaxEntryBytes = 1 << 20
)

// TransactionIngressMetrics is a bounded snapshot of peer transaction
// admission work. Duration fields are aggregate microseconds.
type TransactionIngressMetrics struct {
	Frames              uint64
	ParseWork           uint64
	ParseErrors         uint64
	InputHits           uint64
	InputMisses         uint64
	InputBypass         uint64
	Deduped             uint64
	ParseWorkUs         uint64
	QueueLatencyUs      uint64
	QueueLatencySamples uint64
	MemoEvictions       uint64
	MemoEntries         uint64
	MemoPayloadBytes    uint64 // retained exact-input payload bytes
}

type transactionIngressStats struct {
	frames              atomic.Uint64
	parseWork           atomic.Uint64
	parseErrors         atomic.Uint64
	inputHits           atomic.Uint64
	inputMisses         atomic.Uint64
	inputBypass         atomic.Uint64
	deduped             atomic.Uint64
	parseWorkUs         atomic.Uint64
	queueLatencyUs      atomic.Uint64
	queueLatencySamples atomic.Uint64
}

func (s *transactionIngressStats) snapshot(memo *transactionIngressMemo) TransactionIngressMetrics {
	metrics := TransactionIngressMetrics{
		Frames:              s.frames.Load(),
		ParseWork:           s.parseWork.Load(),
		ParseErrors:         s.parseErrors.Load(),
		InputHits:           s.inputHits.Load(),
		InputMisses:         s.inputMisses.Load(),
		InputBypass:         s.inputBypass.Load(),
		Deduped:             s.deduped.Load(),
		ParseWorkUs:         s.parseWorkUs.Load(),
		QueueLatencyUs:      s.queueLatencyUs.Load(),
		QueueLatencySamples: s.queueLatencySamples.Load(),
	}
	if memo != nil {
		entries, bytes := memo.snapshot()
		metrics.MemoEntries = uint64(entries)
		metrics.MemoPayloadBytes = uint64(bytes)
		metrics.MemoEvictions = memo.evictions.Load()
	}
	return metrics
}

// TransactionIngressMetrics returns a current bounded snapshot of transaction
// ingress admission and worker queue activity.
func (r *Router) TransactionIngressMetrics() TransactionIngressMetrics {
	if r == nil {
		return TransactionIngressMetrics{}
	}
	return r.txIngressStats.snapshot(r.txIngressMemo)
}

func (r *Router) noteTransactionFrame(msgReceivedAt time.Time) {
	r.txIngressStats.frames.Add(1)
	if msgReceivedAt.IsZero() {
		return
	}
	if latency := time.Since(msgReceivedAt); latency >= 0 {
		r.txIngressStats.queueLatencyUs.Add(uint64(latency / time.Microsecond))
		r.txIngressStats.queueLatencySamples.Add(1)
	}
}

func (r *Router) parseIngressPendingTx(blob []byte) (openledger.PendingTx, error) {
	started := time.Now()
	r.txIngressStats.parseWork.Add(1)
	pending, err := openledger.ParsePendingTx(blob)
	r.txIngressStats.parseWorkUs.Add(uint64(time.Since(started) / time.Microsecond))
	if err != nil {
		r.txIngressStats.parseErrors.Add(1)
	}
	return pending, err
}

type transactionIngressAdmission struct {
	hash                  [32]byte
	account               [20]byte
	innerBatch            bool
	roleBearing           bool
	hasLastLedgerSequence bool
	lastLedgerSequence    uint32
}

func transactionIngressAdmissionFromPending(pending openledger.PendingTx) transactionIngressAdmission {
	innerBatch := false
	roleBearing := false
	if pending.Parsed != nil && pending.Parsed.GetCommon() != nil {
		common := pending.Parsed.GetCommon()
		innerBatch = common.GetFlags()&tx.TfInnerBatchTxn != 0
		roleBearing = transactionHasRoleSignature(pending.Parsed)
	}
	return transactionIngressAdmission{
		hash:                  pending.Hash,
		account:               pending.Account,
		innerBatch:            innerBatch,
		roleBearing:           roleBearing,
		hasLastLedgerSequence: pending.HasLastLedgerSequence,
		lastLedgerSequence:    pending.LastLedgerSequence,
	}
}

type transactionIngressPreparation struct {
	pending   openledger.PendingTx
	admission transactionIngressAdmission
	parseBlob []byte
	parsed    bool
	err       error
}

var errTransactionIngressMemoAborted = errors.New("transaction ingress memo parse aborted")

// prepareTransaction performs a full parse only for a cache miss or a frame
// that the existing transaction suppression admits. The cached entry contains
// no parsed transaction: Parsed objects can be annotated by apply and are
// therefore never shared between workers.
func (r *Router) prepareTransaction(blob []byte) transactionIngressPreparation {
	memo := r.txIngressMemo
	if memo == nil {
		pending, err := r.parseIngressPendingTx(blob)
		if err != nil {
			return transactionIngressPreparation{err: err}
		}
		return transactionIngressPreparation{
			pending:   pending,
			admission: transactionIngressAdmissionFromPending(pending),
			parsed:    true,
		}
	}

	ticket := memo.acquire(blob)
	if ticket.bypass {
		r.txIngressStats.inputBypass.Add(1)
		pending, err := r.parseIngressPendingTx(blob)
		if err != nil {
			return transactionIngressPreparation{err: err}
		}
		return transactionIngressPreparation{
			pending:   pending,
			admission: transactionIngressAdmissionFromPending(pending),
			parsed:    true,
		}
	}
	if ticket.owner {
		r.txIngressStats.inputMisses.Add(1)
		completed := false
		defer func() {
			if !completed {
				memo.complete(ticket.entry, transactionIngressAdmission{}, errTransactionIngressMemoAborted)
			}
		}()
		pending, err := r.parseIngressPendingTx(ticket.entry.raw)
		if err != nil {
			memo.complete(ticket.entry, transactionIngressAdmission{}, err)
			completed = true
			return transactionIngressPreparation{err: err}
		}
		admission := transactionIngressAdmissionFromPending(pending)
		memo.complete(ticket.entry, admission, nil)
		completed = true
		return transactionIngressPreparation{
			pending:   pending,
			admission: admission,
			parsed:    true,
		}
	}

	r.txIngressStats.inputHits.Add(1)
	admission, parseBlob, err := memo.wait(ticket.entry)
	if err != nil {
		return transactionIngressPreparation{err: err}
	}
	return transactionIngressPreparation{
		admission: admission,
		parseBlob: parseBlob,
	}
}

func (r *Router) parseAdmittedTransaction(prepared transactionIngressPreparation) (openledger.PendingTx, error) {
	if prepared.parsed {
		return prepared.pending, nil
	}
	blob := prepared.parseBlob
	if len(blob) == 0 {
		return openledger.PendingTx{}, errTransactionIngressMemoAborted
	}
	pending, err := r.parseIngressPendingTx(blob)
	if err != nil {
		return openledger.PendingTx{}, err
	}
	if transactionIngressAdmissionFromPending(pending) != prepared.admission {
		return openledger.PendingTx{}, errors.New("transaction ingress hash changed after memo admission")
	}
	return pending, nil
}

type transactionIngressMemo struct {
	mu         sync.Mutex
	entries    map[[32]byte][]*transactionIngressMemoEntry
	order      list.List
	entryCount int
	byteCount  int
	maxEntries int
	maxBytes   int
	ttl        time.Duration
	now        func() time.Time
	evictions  atomic.Uint64
}

type transactionIngressMemoEntry struct {
	digest    [32]byte
	raw       []byte
	admission transactionIngressAdmission
	err       error
	ready     chan struct{}
	parsing   bool
	touchedAt time.Time
	order     *list.Element
}

type transactionIngressMemoTicket struct {
	entry  *transactionIngressMemoEntry
	owner  bool
	bypass bool
}

func newTransactionIngressMemo(ttl time.Duration, maxEntries, maxBytes int) *transactionIngressMemo {
	if maxEntries < 1 {
		maxEntries = 1
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	return &transactionIngressMemo{
		entries:    make(map[[32]byte][]*transactionIngressMemoEntry),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		ttl:        ttl,
		now:        time.Now,
	}
}

func (m *transactionIngressMemo) acquire(blob []byte) transactionIngressMemoTicket {
	if len(blob) > m.maxBytes || len(blob) > transactionIngressMemoMaxEntryBytes {
		return transactionIngressMemoTicket{bypass: true}
	}
	digest := sha512half.Sum(blob)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.evictExpiredLocked(now)
	for _, entry := range m.entries[digest] {
		if !bytes.Equal(entry.raw, blob) {
			continue
		}
		entry.touchedAt = now
		m.order.MoveToBack(entry.order)
		return transactionIngressMemoTicket{entry: entry}
	}
	if !m.makeSpaceLocked(len(blob)) {
		return transactionIngressMemoTicket{bypass: true}
	}
	entry := &transactionIngressMemoEntry{
		digest:    digest,
		raw:       append([]byte(nil), blob...),
		ready:     make(chan struct{}),
		parsing:   true,
		touchedAt: now,
	}
	entry.order = m.order.PushBack(entry)
	m.entries[digest] = append(m.entries[digest], entry)
	m.entryCount++
	m.byteCount += len(entry.raw)
	return transactionIngressMemoTicket{entry: entry, owner: true}
}

func (m *transactionIngressMemo) wait(entry *transactionIngressMemoEntry) (transactionIngressAdmission, []byte, error) {
	<-entry.ready
	m.mu.Lock()
	defer m.mu.Unlock()
	return entry.admission, entry.raw, entry.err
}

func (m *transactionIngressMemo) complete(entry *transactionIngressMemoEntry, admission transactionIngressAdmission, err error) {
	m.mu.Lock()
	if !entry.parsing {
		m.mu.Unlock()
		return
	}
	entry.admission = admission
	entry.err = err
	entry.parsing = false
	close(entry.ready)
	if err != nil {
		m.removeLocked(entry)
	}
	m.mu.Unlock()
}

func (m *transactionIngressMemo) makeSpaceLocked(incoming int) bool {
	for m.entryCount >= m.maxEntries || m.byteCount+incoming > m.maxBytes {
		var candidate *transactionIngressMemoEntry
		for element := m.order.Front(); element != nil; element = element.Next() {
			entry := element.Value.(*transactionIngressMemoEntry)
			if !entry.parsing {
				candidate = entry
				break
			}
		}
		if candidate == nil {
			return false
		}
		m.removeLocked(candidate)
	}
	return true
}

func (m *transactionIngressMemo) evictExpiredLocked(now time.Time) {
	if m.ttl <= 0 {
		return
	}
	for element := m.order.Front(); element != nil; {
		next := element.Next()
		entry := element.Value.(*transactionIngressMemoEntry)
		if entry.parsing {
			element = next
			continue
		}
		if now.Sub(entry.touchedAt) < m.ttl {
			break
		}
		m.removeLocked(entry)
		element = next
	}
}

func (m *transactionIngressMemo) removeLocked(entry *transactionIngressMemoEntry) {
	if entry.parsing {
		return
	}
	bucket := m.entries[entry.digest]
	for i, candidate := range bucket {
		if candidate != entry {
			continue
		}
		bucket = append(bucket[:i], bucket[i+1:]...)
		if len(bucket) == 0 {
			delete(m.entries, entry.digest)
		} else {
			m.entries[entry.digest] = bucket
		}
		break
	}
	m.order.Remove(entry.order)
	m.entryCount--
	m.byteCount -= len(entry.raw)
	m.evictions.Add(1)
}

func (m *transactionIngressMemo) snapshot() (entries, bytes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entryCount, m.byteCount
}

func (m *transactionIngressMemo) clear() {
	m.mu.Lock()
	for element := m.order.Front(); element != nil; {
		next := element.Next()
		entry := element.Value.(*transactionIngressMemoEntry)
		if !entry.parsing {
			m.removeLocked(entry)
		}
		element = next
	}
	m.mu.Unlock()
}
