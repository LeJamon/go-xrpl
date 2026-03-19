package ledger

import (
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/shamap"
)

// Common errors for ledger operations
var (
	ErrLedgerImmutable = errors.New("ledger is immutable")
	ErrLedgerNotClosed = errors.New("ledger is not closed")
	ErrEntryNotFound   = errors.New("ledger entry not found")
	ErrInvalidState    = errors.New("invalid ledger state")
)

// State represents the current state of a ledger
type State int

const (
	// StateOpen indicates the ledger is open for modifications
	StateOpen State = iota
	// StateClosed indicates the ledger has been closed but not yet validated
	StateClosed
	// StateValidated indicates the ledger has been validated
	StateValidated
)

// String returns a string representation of the state
func (s State) String() string {
	switch s {
	case StateOpen:
		return "open"
	case StateClosed:
		return "closed"
	case StateValidated:
		return "validated"
	default:
		return "unknown"
	}
}

// Reader provides read-only access to ledger state
type Reader interface {
	// Sequence returns the ledger sequence number
	Sequence() uint32

	// Hash returns the ledger hash (only valid for closed ledgers)
	Hash() [32]byte

	// ParentHash returns the parent ledger hash
	ParentHash() [32]byte

	// CloseTime returns the ledger close time
	CloseTime() time.Time

	// TotalDrops returns the total XRP in existence
	TotalDrops() uint64

	// State returns the current ledger state
	State() State

	// Read reads a ledger entry by its keylet
	Read(k keylet.Keylet) ([]byte, error)

	// Exists checks if a ledger entry exists
	Exists(k keylet.Keylet) (bool, error)

	// GetFees returns the current fee settings
	GetFees() drops.Fees
}

// Writer provides write access to ledger state
type Writer interface {
	// Insert adds a new ledger entry
	Insert(k keylet.Keylet, data []byte) error

	// Update modifies an existing ledger entry
	Update(k keylet.Keylet, data []byte) error

	// Erase removes a ledger entry
	Erase(k keylet.Keylet) error

	// AdjustDropsDestroyed records XRP that has been destroyed (fees)
	AdjustDropsDestroyed(drops drops.XRPAmount)
}

// Ledger represents a single ledger in the chain
type Ledger struct {
	mu sync.RWMutex

	// Core data structures
	stateMap *shamap.SHAMap // Account state tree
	txMap    *shamap.SHAMap // Transaction tree

	// Header information
	header header.LedgerHeader

	// Fee configuration
	fees drops.Fees

	// Current state
	state State

	// Drops destroyed in this ledger (transaction fees)
	dropsDestroyed drops.XRPAmount
}

// NewOpen creates a new open ledger based on a parent ledger
func NewOpen(parent *Ledger, closeTime time.Time) (*Ledger, error) {
	if parent == nil {
		return nil, errors.New("parent ledger cannot be nil")
	}

	// Snapshot the parent state map as mutable
	stateMap, err := parent.stateMap.Snapshot(true)
	if err != nil {
		return nil, errors.New("failed to snapshot state map: " + err.Error())
	}

	// Create empty transaction map
	txMap, err := shamap.New(shamap.TypeTransaction)
	if err != nil {
		return nil, errors.New("failed to create tx map: " + err.Error())
	}

	// Create new header based on parent
	newHeader := header.LedgerHeader{
		LedgerIndex:         parent.header.LedgerIndex + 1,
		ParentHash:          parent.header.Hash,
		ParentCloseTime:     parent.header.CloseTime,
		CloseTime:           closeTime,
		CloseTimeResolution: parent.header.CloseTimeResolution,
		Drops:               parent.header.Drops,
		// Hash, TxHash, AccountHash will be set when closed
	}

	return &Ledger{
		stateMap:       stateMap,
		txMap:          txMap,
		header:         newHeader,
		fees:           parent.fees,
		state:          StateOpen,
		dropsDestroyed: 0,
	}, nil
}

// FromGenesis creates a Ledger from a genesis creation result
func FromGenesis(
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
) *Ledger {
	return &Ledger{
		stateMap: stateMap,
		txMap:    txMap,
		header:   hdr,
		fees:     fees,
		state:    StateValidated, // Genesis is immediately validated
	}
}

// NewFromHeader creates a closed/validated ledger from a deserialized header
// and existing state/tx maps. Used during initial sync to adopt a peer's ledger.
func NewFromHeader(
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
) *Ledger {
	return &Ledger{
		stateMap: stateMap,
		txMap:    txMap,
		header:   hdr,
		fees:     fees,
		state:    StateValidated,
	}
}

// NewOpenWithHeader creates an open ledger with the exact header values provided.
// This is useful for testing/replay scenarios where you want to control all header fields.
func NewOpenWithHeader(
	hdr header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	fees drops.Fees,
) *Ledger {
	return &Ledger{
		stateMap: stateMap,
		txMap:    txMap,
		header:   hdr,
		fees:     fees,
		state:    StateOpen,
	}
}

// Sequence returns the ledger sequence number
func (l *Ledger) Sequence() uint32 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.LedgerIndex
}

// Hash returns the ledger hash
func (l *Ledger) Hash() [32]byte {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.Hash
}

// ParentHash returns the parent ledger hash
func (l *Ledger) ParentHash() [32]byte {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.ParentHash
}

// CloseTime returns the ledger close time
func (l *Ledger) CloseTime() time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.CloseTime
}

// ParentCloseTime returns the parent ledger's close time
func (l *Ledger) ParentCloseTime() time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.ParentCloseTime
}

// TotalDrops returns the total XRP in existence
func (l *Ledger) TotalDrops() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.Drops
}

// State returns the current ledger state
func (l *Ledger) State() State {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state
}

// Header returns a copy of the ledger header
func (l *Ledger) Header() header.LedgerHeader {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header
}

// GetFees returns the current fee settings
func (l *Ledger) GetFees() drops.Fees {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.fees
}

// IsOpen returns true if the ledger is open for modifications
func (l *Ledger) IsOpen() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state == StateOpen
}

// IsClosed returns true if the ledger is closed
func (l *Ledger) IsClosed() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state == StateClosed || l.state == StateValidated
}

// IsValidated returns true if the ledger is validated
func (l *Ledger) IsValidated() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state == StateValidated
}

// Read reads a ledger entry by its keylet
func (l *Ledger) Read(k keylet.Keylet) ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	item, found, err := l.stateMap.Get(k.Key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}

	return item.Data(), nil
}

// Exists checks if a ledger entry exists
func (l *Ledger) Exists(k keylet.Keylet) (bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.stateMap.Has(k.Key)
}

// Insert adds a new ledger entry
func (l *Ledger) Insert(k keylet.Keylet, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}

	// Check if entry already exists
	exists, err := l.stateMap.Has(k.Key)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("entry already exists")
	}

	return l.stateMap.Put(k.Key, data)
}

// Update modifies an existing ledger entry
func (l *Ledger) Update(k keylet.Keylet, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}

	// Check if entry exists
	exists, err := l.stateMap.Has(k.Key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrEntryNotFound
	}

	return l.stateMap.Put(k.Key, data)
}

// Erase removes a ledger entry
func (l *Ledger) Erase(k keylet.Keylet) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}

	return l.stateMap.Delete(k.Key)
}

// AdjustDropsDestroyed records XRP that has been destroyed (fees)
func (l *Ledger) AdjustDropsDestroyed(drops drops.XRPAmount) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.dropsDestroyed = l.dropsDestroyed.Add(drops)
}

// AddTransaction adds a transaction to the transaction tree
func (l *Ledger) AddTransaction(txHash [32]byte, txData []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}

	return l.txMap.Put(txHash, txData)
}

// AddTransactionWithMeta adds a transaction with metadata to the transaction tree
// This uses NodeTypeTransactionWithMeta for proper transaction tree hashing
func (l *Ledger) AddTransactionWithMeta(txHash [32]byte, txWithMetaData []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}

	return l.txMap.PutWithNodeType(txHash, txWithMetaData, shamap.NodeTypeTransactionWithMeta)
}

// GetTransaction retrieves a transaction by its hash
func (l *Ledger) GetTransaction(txHash [32]byte) ([]byte, bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	item, found, err := l.txMap.Get(txHash)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}

	return item.Data(), true, nil
}

// HasTransaction checks if a transaction exists in this ledger
func (l *Ledger) HasTransaction(txHash [32]byte) (bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.txMap.Has(txHash)
}

// TxExists returns true if a transaction with the given hash has already been
// applied to this ledger. Delegates to the transaction SHAMap.
// Reference: rippled ReadView::txExists()
func (l *Ledger) TxExists(txID [32]byte) bool {
	exists, err := l.HasTransaction(txID)
	if err != nil {
		return false
	}
	return exists
}

// Rules returns nil — the base ledger doesn't carry amendment rules.
func (l *Ledger) Rules() *amendment.Rules {
	return nil
}

// Close closes the ledger, making it immutable
func (l *Ledger) Close(closeTime time.Time, closeFlags uint8) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrInvalidState
	}

	// Make maps immutable
	if err := l.stateMap.SetImmutable(); err != nil {
		return errors.New("failed to make state map immutable: " + err.Error())
	}
	if err := l.txMap.SetImmutable(); err != nil {
		return errors.New("failed to make tx map immutable: " + err.Error())
	}

	// Update drops (subtract destroyed)
	l.header.Drops -= uint64(l.dropsDestroyed)

	// Get hashes
	accountHash, err := l.stateMap.Hash()
	if err != nil {
		return errors.New("failed to get state map hash: " + err.Error())
	}

	txHash, err := l.txMap.Hash()
	if err != nil {
		return errors.New("failed to get tx map hash: " + err.Error())
	}

	// Update header
	l.header.AccountHash = accountHash
	l.header.TxHash = txHash
	l.header.CloseTime = closeTime
	l.header.CloseFlags = closeFlags
	l.header.Accepted = true

	// Calculate ledger hash
	l.header.Hash = calculateLedgerHash(l.header)

	l.state = StateClosed

	return nil
}

// SetValidated marks the ledger as validated
func (l *Ledger) SetValidated() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateClosed {
		return ErrLedgerNotClosed
	}

	l.header.Validated = true
	l.state = StateValidated

	return nil
}

// Snapshot creates an immutable copy of this ledger
func (l *Ledger) Snapshot() (*Ledger, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	stateMapCopy, err := l.stateMap.Snapshot(false)
	if err != nil {
		return nil, err
	}

	txMapCopy, err := l.txMap.Snapshot(false)
	if err != nil {
		return nil, err
	}

	return &Ledger{
		stateMap: stateMapCopy,
		txMap:    txMapCopy,
		header:   l.header,
		fees:     l.fees,
		state:    l.state,
	}, nil
}

// StateMapHash returns the state map hash
func (l *Ledger) StateMapHash() ([32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.stateMap.Hash()
}

// TxMapHash returns the transaction map hash
func (l *Ledger) TxMapHash() ([32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.txMap.Hash()
}

// ForEach iterates over all state entries and calls fn for each.
// If fn returns false, iteration stops early.
// The callback receives the entry key and data.
func (l *Ledger) ForEach(fn func(key [32]byte, data []byte) bool) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.stateMap.ForEach(func(item *shamap.Item) bool {
		return fn(item.Key(), item.Data())
	})
}

// Succ returns the first state entry with key > the given key.
// Uses SHAMap's UpperBound for O(log n) lookup.
// Reference: rippled ReadView::succ()
func (l *Ledger) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	it := l.stateMap.UpperBound(key)
	if it.Valid() {
		item := it.Item()
		if item != nil {
			return item.Key(), item.Data(), true, nil
		}
	}
	if err := it.Err(); err != nil {
		return [32]byte{}, nil, false, err
	}
	return [32]byte{}, nil, false, nil
}

// ForEachTransaction iterates over all transactions in the ledger and calls fn for each.
// If fn returns false, iteration stops early.
// The callback receives the transaction hash and data.
func (l *Ledger) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.txMap.ForEach(func(item *shamap.Item) bool {
		return fn(item.Key(), item.Data())
	})
}

// StateMapSnapshot returns a mutable snapshot of the state map.
// This is useful for continuous replay where the state from one block
// becomes the input for the next block.
func (l *Ledger) StateMapSnapshot() (*shamap.SHAMap, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.stateMap.Snapshot(true)
}

// TxMapSnapshot returns a mutable snapshot of the transaction map.
func (l *Ledger) TxMapSnapshot() (*shamap.SHAMap, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.txMap.Snapshot(true)
}

// SetStateMapFamily sets the Family on the state map, enabling backed mode
// with lazy loading and efficient snapshots.
func (l *Ledger) SetStateMapFamily(family shamap.Family) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stateMap.SetFamily(family)
}

// SerializeHeader returns the serialized ledger header bytes
func (l *Ledger) SerializeHeader() []byte {
	l.mu.RLock()
	defer l.mu.RUnlock()

	data, err := header.AddRaw(l.header, true)
	if err != nil {
		return nil
	}
	return data
}

const rippleEpochUnix int64 = 946684800

// calculateLedgerHash computes the hash of a ledger header
// This is duplicated from genesis package to avoid circular imports
func calculateLedgerHash(h header.LedgerHeader) [32]byte {
	var data []byte

	data = append(data, protocol.HashPrefixLedgerMaster.Bytes()...)

	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, h.LedgerIndex)
	data = append(data, seqBytes...)

	dropsBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(dropsBytes, h.Drops)
	data = append(data, dropsBytes...)

	data = append(data, h.ParentHash[:]...)
	data = append(data, h.TxHash[:]...)
	data = append(data, h.AccountHash[:]...)

	parentCloseBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(parentCloseBytes, uint32(h.ParentCloseTime.Unix()-rippleEpochUnix))
	data = append(data, parentCloseBytes...)

	closeTimeBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(closeTimeBytes, uint32(h.CloseTime.Unix()-rippleEpochUnix))
	data = append(data, closeTimeBytes...)

	data = append(data, byte(h.CloseTimeResolution))
	data = append(data, h.CloseFlags)

	return common.Sha512Half(data)
}
