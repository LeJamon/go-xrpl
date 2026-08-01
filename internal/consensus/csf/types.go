package csf

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
	"github.com/LeJamon/go-xrpl/protocol"
)

type PeerID uint32

type Tx struct {
	ID uint32
}

func (tx Tx) Bytes() []byte {
	var blob [4]byte
	binary.BigEndian.PutUint32(blob[:], tx.ID)
	return blob[:]
}

func (tx Tx) TxID() consensus.TxID {
	return txBlobID(tx.Bytes())
}

func txBlobID(blob []byte) consensus.TxID {
	return consensus.TxID(sha256.Sum256(blob))
}

func txFromBlob(blob []byte) (Tx, bool) {
	if len(blob) != 4 {
		return Tx{}, false
	}
	return Tx{ID: binary.BigEndian.Uint32(blob)}, true
}

type TxSet struct {
	txs map[consensus.TxID][]byte
}

func NewTxSet() *TxSet {
	return &TxSet{txs: make(map[consensus.TxID][]byte)}
}

func NewTxSetFrom(txs []Tx) *TxSet {
	set := NewTxSet()
	for _, tx := range txs {
		set.Insert(tx)
	}
	return set
}

func newTxSetFromBlobs(blobs [][]byte) *TxSet {
	set := NewTxSet()
	for _, blob := range blobs {
		_ = set.Add(blob)
	}
	return set
}

func (s *TxSet) Insert(tx Tx) {
	_ = s.Add(tx.Bytes())
}

func (s *TxSet) ContainsTx(tx Tx) bool {
	return s.Contains(tx.TxID())
}

func (s *TxSet) Transactions() []Tx {
	result := make([]Tx, 0, len(s.txs))
	for _, blob := range s.Txs() {
		if tx, ok := txFromBlob(blob); ok {
			result = append(result, tx)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result
}

func (s *TxSet) Clone() *TxSet {
	if s == nil {
		return NewTxSet()
	}
	return newTxSetFromBlobs(s.Txs())
}

func (s *TxSet) sortedIDs() []consensus.TxID {
	ids := make([]consensus.TxID, 0, len(s.txs))
	for id := range s.txs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	return ids
}

func (s *TxSet) ID() consensus.TxSetID {
	h := sha256.New()
	for _, id := range s.sortedIDs() {
		_, _ = h.Write(id[:])
	}
	var id consensus.TxSetID
	copy(id[:], h.Sum(nil))
	return id
}

func (s *TxSet) Txs() [][]byte {
	ids := s.sortedIDs()
	result := make([][]byte, 0, len(ids))
	for _, id := range ids {
		result = append(result, append([]byte(nil), s.txs[id]...))
	}
	return result
}

func (s *TxSet) TxIDs() []consensus.TxID {
	return s.sortedIDs()
}

func (s *TxSet) Contains(id consensus.TxID) bool {
	_, ok := s.txs[id]
	return ok
}

func (s *TxSet) Add(blob []byte) error {
	s.txs[txBlobID(blob)] = append([]byte(nil), blob...)
	return nil
}

func (s *TxSet) Remove(id consensus.TxID) error {
	delete(s.txs, id)
	return nil
}

func (s *TxSet) Size() int {
	return len(s.txs)
}

func (s *TxSet) Bytes() []byte {
	var result bytes.Buffer
	for _, id := range s.sortedIDs() {
		blob := s.txs[id]
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(blob)))
		_, _ = result.Write(size[:])
		_, _ = result.Write(blob)
	}
	return result.Bytes()
}

type Ledger struct {
	id              consensus.LedgerID
	seq             uint32
	parentID        consensus.LedgerID
	parentCloseTime time.Time
	txs             *TxSet
	txSetID         consensus.TxSetID
	closeTime       time.Time
	closeAgree      bool
	resolution      time.Duration
	ancestors       []consensus.LedgerID
}

type LedgerID = consensus.LedgerID

func MakeGenesis() *Ledger {
	empty := NewTxSet()
	genesis := &Ledger{
		txs:        empty,
		txSetID:    empty.ID(),
		closeTime:  time.Unix(protocol.RippleEpochUnix, 0).UTC(),
		closeAgree: true,
		resolution: 30 * time.Second,
	}
	genesis.ancestors = []consensus.LedgerID{genesis.id}
	return genesis
}

func (l *Ledger) computeID() consensus.LedgerID {
	h := sha256.New()
	var value [8]byte

	binary.BigEndian.PutUint32(value[:4], l.seq)
	_, _ = h.Write(value[:4])
	_, _ = h.Write(l.parentID[:])

	txSetID := l.txs.ID()
	_, _ = h.Write(txSetID[:])

	binary.BigEndian.PutUint64(value[:], uint64(l.resolution))
	_, _ = h.Write(value[:])
	binary.BigEndian.PutUint64(value[:], uint64(l.closeTime.UnixNano()))
	_, _ = h.Write(value[:])
	if l.closeAgree {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
	binary.BigEndian.PutUint64(value[:], uint64(l.parentCloseTime.UnixNano()))
	_, _ = h.Write(value[:])

	var id consensus.LedgerID
	copy(id[:], h.Sum(nil))
	return id
}

func (l *Ledger) ID() consensus.LedgerID {
	return l.id
}

func (l *Ledger) Seq() uint32 {
	return l.seq
}

func (l *Ledger) MinSeq() uint32 {
	return 0
}

func (l *Ledger) Ancestor(seq uint32) consensus.LedgerID {
	if seq >= uint32(len(l.ancestors)) {
		return consensus.LedgerID{}
	}
	return l.ancestors[seq]
}

func (l *Ledger) ParentID() consensus.LedgerID {
	return l.parentID
}

func (l *Ledger) ParentCloseTime() time.Time {
	return l.parentCloseTime
}

func (l *Ledger) Transactions() *TxSet {
	return l.txs.Clone()
}

func (l *Ledger) CloseTime() time.Time {
	return l.closeTime
}

func (l *Ledger) CloseAgree() bool {
	return l.closeAgree
}

func (l *Ledger) CloseTimeResolution() time.Duration {
	return l.resolution
}

func (l *Ledger) TxSetID() consensus.TxSetID {
	return l.txSetID
}

func (l *Ledger) Bytes() []byte {
	return append([]byte(nil), l.id[:]...)
}

func (l *Ledger) IsAncestor(other *Ledger, oracle *LedgerOracle) bool {
	if other == nil || other.seq >= l.seq {
		return false
	}
	current := l
	for current.seq > other.seq {
		current = oracle.Get(current.parentID)
		if current == nil {
			return false
		}
	}
	return current.id == other.id
}

type LedgerOracle struct {
	mu      sync.RWMutex
	ledgers map[consensus.LedgerID]*Ledger
	byKey   map[ledgerKey]*Ledger
}

type ledgerKey struct {
	seq             uint32
	parentID        consensus.LedgerID
	parentCloseTime int64
	txSetID         consensus.TxSetID
	closeTime       int64
	closeAgree      bool
	resolution      int64
}

func NewLedgerOracle() *LedgerOracle {
	genesis := MakeGenesis()
	return &LedgerOracle{
		ledgers: map[consensus.LedgerID]*Ledger{genesis.id: genesis},
		byKey:   make(map[ledgerKey]*Ledger),
	}
}

func (o *LedgerOracle) Genesis() *Ledger {
	return o.Get(MakeGenesis().ID())
}

func (o *LedgerOracle) Get(id consensus.LedgerID) *Ledger {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.ledgers[id]
}

func (o *LedgerOracle) Accept(
	parent *Ledger,
	txs *TxSet,
	closeTime time.Time,
	closeAgree bool,
	resolution time.Duration,
) *Ledger {
	if parent == nil {
		panic("csf: nil parent ledger")
	}
	if txs == nil {
		panic("csf: nil transaction set")
	}
	if resolution < time.Second || resolution%time.Second != 0 {
		panic("csf: close-time resolution must be a positive whole number of seconds")
	}
	closeAgree = closeAgree && !closeTime.IsZero()

	effective := parent.closeTime.Add(time.Second)
	if closeAgree {
		effective = effectiveCloseTime(closeTime, resolution, parent.closeTime)
	}

	cumulative := parent.txs.Clone()
	for _, blob := range txs.Txs() {
		_ = cumulative.Add(blob)
	}
	key := ledgerKey{
		seq:             parent.seq + 1,
		parentID:        parent.id,
		parentCloseTime: parent.closeTime.UnixNano(),
		txSetID:         cumulative.ID(),
		closeTime:       effective.UnixNano(),
		closeAgree:      closeAgree,
		resolution:      int64(resolution),
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if existing := o.byKey[key]; existing != nil {
		return existing
	}

	ledger := &Ledger{
		seq:             key.seq,
		parentID:        parent.id,
		parentCloseTime: parent.closeTime,
		txs:             cumulative,
		txSetID:         txs.ID(),
		closeTime:       effective,
		closeAgree:      closeAgree,
		resolution:      resolution,
	}
	ledger.id = ledger.computeID()
	ledger.ancestors = append(append(
		make([]consensus.LedgerID, 0, len(parent.ancestors)+1),
		parent.ancestors...,
	), ledger.id)
	o.ledgers[ledger.id] = ledger
	o.byKey[key] = ledger
	return ledger
}

func (o *LedgerOracle) LedgerByID(id consensus.LedgerID) (ledgertrie.Ledger, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	ledger, ok := o.ledgers[id]
	return ledger, ok
}

func effectiveCloseTime(closeTime time.Time, resolution time.Duration, parentCloseTime time.Time) time.Time {
	if closeTime.IsZero() {
		return closeTime
	}
	seconds := closeTime.Unix() - protocol.RippleEpochUnix
	step := int64(resolution / time.Second)
	seconds += step / 2
	seconds -= seconds % step
	rounded := time.Unix(protocol.RippleEpochUnix+seconds, 0).UTC()
	minimum := parentCloseTime.Add(time.Second)
	if rounded.Before(minimum) {
		return minimum
	}
	return rounded
}

func (o *LedgerOracle) Branches(ledgers []*Ledger) int {
	tips := make([]*Ledger, 0, len(ledgers))
	for _, ledger := range ledgers {
		if ledger == nil {
			continue
		}
		found := false
		for i, tip := range tips {
			earlier, later := ledger, tip
			if tip.seq < ledger.seq {
				earlier, later = tip, ledger
			}
			if later.id == earlier.id || later.IsAncestor(earlier, o) {
				tips[i] = later
				found = true
				break
			}
		}
		if !found {
			tips = append(tips, ledger)
		}
	}
	return len(tips)
}

var _ consensus.TxSet = (*TxSet)(nil)
var _ consensus.Ledger = (*Ledger)(nil)
