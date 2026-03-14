package csf

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// PeerID uniquely identifies a peer in the simulation.
type PeerID uint32

// Tx represents a simplified transaction for simulation.
// In the simulation, a transaction is just an integer ID.
type Tx struct {
	ID uint32
}

// TxID returns the transaction's ID as a consensus.TxID.
func (tx Tx) TxID() consensus.TxID {
	var id consensus.TxID
	binary.BigEndian.PutUint32(id[:], tx.ID)
	return id
}

// TxSet represents a set of transactions for simulation.
type TxSet struct {
	txs map[uint32]Tx
}

// NewTxSet creates a new empty transaction set.
func NewTxSet() *TxSet {
	return &TxSet{
		txs: make(map[uint32]Tx),
	}
}

// NewTxSetFrom creates a transaction set from existing transactions.
func NewTxSetFrom(txs []Tx) *TxSet {
	ts := NewTxSet()
	for _, tx := range txs {
		ts.txs[tx.ID] = tx
	}
	return ts
}

// Insert adds a transaction to the set.
func (ts *TxSet) Insert(tx Tx) {
	ts.txs[tx.ID] = tx
}

// Remove removes a transaction from the set.
func (ts *TxSet) Remove(tx Tx) {
	delete(ts.txs, tx.ID)
}

// Contains checks if a transaction is in the set.
func (ts *TxSet) Contains(tx Tx) bool {
	_, ok := ts.txs[tx.ID]
	return ok
}

// Size returns the number of transactions.
func (ts *TxSet) Size() int {
	return len(ts.txs)
}

// Transactions returns all transactions as a slice.
func (ts *TxSet) Transactions() []Tx {
	result := make([]Tx, 0, len(ts.txs))
	for _, tx := range ts.txs {
		result = append(result, tx)
	}
	// Sort for deterministic ordering
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result
}

// ID returns a hash of the transaction set.
func (ts *TxSet) ID() consensus.TxSetID {
	h := sha256.New()
	for _, tx := range ts.Transactions() {
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], tx.ID)
		h.Write(buf[:])
	}
	var id consensus.TxSetID
	copy(id[:], h.Sum(nil))
	return id
}

// Clone creates a copy of the transaction set.
func (ts *TxSet) Clone() *TxSet {
	clone := NewTxSet()
	for id, tx := range ts.txs {
		clone.txs[id] = tx
	}
	return clone
}

// Ledger represents a simplified ledger for simulation.
type Ledger struct {
	id         consensus.LedgerID
	seq        uint32
	parentID   consensus.LedgerID
	txs        *TxSet
	closeTime  time.Time
	closeAgree bool // Whether close time was agreed upon
	resolution time.Duration
}

// LedgerID is a type alias for clarity.
type LedgerID = consensus.LedgerID

// MakeGenesis creates the genesis ledger.
func MakeGenesis() *Ledger {
	l := &Ledger{
		seq:        0,
		txs:        NewTxSet(),
		closeTime:  time.Unix(0, 0),
		closeAgree: true,
		resolution: 30 * time.Second,
	}
	l.id = l.computeID()
	return l
}

// computeID calculates the ledger ID from its contents.
func (l *Ledger) computeID() consensus.LedgerID {
	h := sha256.New()

	// Include sequence
	var seqBuf [4]byte
	binary.BigEndian.PutUint32(seqBuf[:], l.seq)
	h.Write(seqBuf[:])

	// Include parent
	h.Write(l.parentID[:])

	// Include tx set hash
	txSetID := l.txs.ID()
	h.Write(txSetID[:])

	// Include close time
	var timeBuf [8]byte
	binary.BigEndian.PutUint64(timeBuf[:], uint64(l.closeTime.UnixNano()))
	h.Write(timeBuf[:])

	var id consensus.LedgerID
	copy(id[:], h.Sum(nil))
	return id
}

// ID returns the ledger's unique identifier.
func (l *Ledger) ID() consensus.LedgerID {
	return l.id
}

// Seq returns the ledger sequence number.
func (l *Ledger) Seq() uint32 {
	return l.seq
}

// ParentID returns the parent ledger's ID.
func (l *Ledger) ParentID() consensus.LedgerID {
	return l.parentID
}

// Txs returns the transaction set.
func (l *Ledger) Txs() *TxSet {
	return l.txs
}

// CloseTime returns when the ledger was closed.
func (l *Ledger) CloseTime() time.Time {
	return l.closeTime
}

// CloseAgree returns whether validators agreed on close time.
func (l *Ledger) CloseAgree() bool {
	return l.closeAgree
}

// CloseTimeResolution returns the close time resolution.
func (l *Ledger) CloseTimeResolution() time.Duration {
	return l.resolution
}

// IsAncestor checks if other is an ancestor of this ledger.
func (l *Ledger) IsAncestor(other *Ledger, oracle *LedgerOracle) bool {
	if other.seq >= l.seq {
		return false
	}

	current := l
	for current.seq > other.seq {
		parent := oracle.Get(current.parentID)
		if parent == nil {
			return false
		}
		current = parent
	}
	return current.id == other.id
}

// LedgerOracle manages unique ledger instances.
// It ensures that the same inputs always produce the same ledger.
type LedgerOracle struct {
	ledgers  map[consensus.LedgerID]*Ledger
	bySeqTxs map[ledgerKey]*Ledger
}

// ledgerKey uniquely identifies a ledger by its construction parameters.
type ledgerKey struct {
	parentID  consensus.LedgerID
	txSetID   consensus.TxSetID
	closeTime int64
}

// NewLedgerOracle creates a new ledger oracle.
func NewLedgerOracle() *LedgerOracle {
	oracle := &LedgerOracle{
		ledgers:  make(map[consensus.LedgerID]*Ledger),
		bySeqTxs: make(map[ledgerKey]*Ledger),
	}
	// Register genesis
	genesis := MakeGenesis()
	oracle.ledgers[genesis.id] = genesis
	return oracle
}

// Get retrieves a ledger by ID.
func (o *LedgerOracle) Get(id consensus.LedgerID) *Ledger {
	return o.ledgers[id]
}

// Accept creates or retrieves a ledger with the given parameters.
// This ensures the same inputs always produce the same ledger.
func (o *LedgerOracle) Accept(
	parent *Ledger,
	txs *TxSet,
	closeTime time.Time,
	closeAgree bool,
	resolution time.Duration,
) *Ledger {
	key := ledgerKey{
		parentID:  parent.id,
		txSetID:   txs.ID(),
		closeTime: closeTime.UnixNano(),
	}

	if existing, ok := o.bySeqTxs[key]; ok {
		return existing
	}

	ledger := &Ledger{
		seq:        parent.seq + 1,
		parentID:   parent.id,
		txs:        txs.Clone(),
		closeTime:  closeTime,
		closeAgree: closeAgree,
		resolution: resolution,
	}
	ledger.id = ledger.computeID()

	o.ledgers[ledger.id] = ledger
	o.bySeqTxs[key] = ledger

	return ledger
}

// Branches determines the number of distinct branches for a set of ledgers.
// Ledgers A and B are on different branches if A != B, A is not an ancestor of B,
// and B is not an ancestor of A.
func (o *LedgerOracle) Branches(ledgers []*Ledger) int {
	if len(ledgers) == 0 {
		return 0
	}

	// Tips maintains the ledgers with largest sequence number along all known chains
	tips := make([]*Ledger, 0, len(ledgers))

	// Sort by sequence (descending) to process higher sequence first
	sorted := make([]*Ledger, len(ledgers))
	copy(sorted, ledgers)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].seq > sorted[j].seq
	})

	for _, ledger := range sorted {
		// Check if this ledger is an ancestor of any tip
		isAncestorOfTip := false
		for _, tip := range tips {
			if ledger.id == tip.id || tip.IsAncestor(ledger, o) {
				isAncestorOfTip = true
				break
			}
		}

		if !isAncestorOfTip {
			// Check if any tip is an ancestor of this ledger (would replace tip)
			replaced := false
			for i, tip := range tips {
				if ledger.IsAncestor(tip, o) {
					tips[i] = ledger
					replaced = true
					break
				}
			}
			if !replaced {
				// New branch
				tips = append(tips, ledger)
			}
		}
	}

	return len(tips)
}

// Proposal represents a consensus proposal in the simulation.
type Proposal struct {
	PrevLedger consensus.LedgerID
	Position   *TxSet
	CloseTime  time.Time
	Time       SimTime
	NodeID     PeerID
	PropNum    uint32 // Position number (0, 1, 2...)
}

// ID returns a hash identifying this proposal's position.
func (p *Proposal) ID() consensus.TxSetID {
	return p.Position.ID()
}

// Validation represents a validation message in the simulation.
type Validation struct {
	LedgerID consensus.LedgerID
	Seq      uint32
	SignTime time.Time
	SeenTime time.Time
	NodeID   PeerID
	Key      PeerID // Signing key (same as NodeID in simulation)
	Full     bool
	Trusted  bool
}
