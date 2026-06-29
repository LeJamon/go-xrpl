package ledger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/negativeunl"
	"github.com/LeJamon/go-xrpl/internal/ledger/skiplist"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
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
	Sequence() uint32

	// Hash returns the ledger hash (only valid for closed ledgers)
	Hash() [32]byte

	ParentHash() [32]byte

	CloseTime() time.Time

	// TotalDrops returns the total XRP in existence
	TotalDrops() uint64

	State() State

	Read(k keylet.Keylet) ([]byte, error)

	Exists(k keylet.Keylet) (bool, error)

	GetFees() drops.Fees
}

// Writer provides write access to ledger state
type Writer interface {
	Insert(k keylet.Keylet, data []byte) error

	Update(k keylet.Keylet, data []byte) error

	Erase(k keylet.Keylet) error

	// AdjustDropsDestroyed records XRP destroyed as fees.
	AdjustDropsDestroyed(drops drops.XRPAmount)
}

// Ledger represents a single ledger in the chain
type Ledger struct {
	mu sync.RWMutex

	stateMap *shamap.SHAMap
	txMap    *shamap.SHAMap

	header header.LedgerHeader

	fees drops.Fees

	state State

	// Drops destroyed in this ledger (transaction fees)
	dropsDestroyed drops.XRPAmount
}

// NewOpen creates a new open ledger based on a parent ledger
func NewOpen(parent *Ledger, closeTime time.Time) (*Ledger, error) {
	if parent == nil {
		return nil, errors.New("parent ledger cannot be nil")
	}

	stateMap, err := parent.stateMap.Snapshot(true)
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot state map: %w", err)
	}

	txMap := shamap.New(shamap.TypeTransaction)

	// Recompute close-time resolution per close from the parent's previousAgree
	// (encoded in its CloseFlags) — matches rippled and avoids plumbing
	// previousAgree through every NewOpen caller.
	newLedgerSeq := parent.header.LedgerIndex + 1
	newResolution := consensus.GetNextLedgerTimeResolution(
		parent.header.CloseTimeResolution,
		parent.header.GetCloseAgree(),
		newLedgerSeq,
	)

	newHeader := header.LedgerHeader{
		LedgerIndex:         newLedgerSeq,
		ParentHash:          parent.header.Hash,
		ParentCloseTime:     parent.header.CloseTime,
		CloseTime:           closeTime,
		CloseTimeResolution: newResolution,
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

// NewOpenWithHeader creates an open ledger with the exact header values provided
// (testing/replay scenarios that control all header fields).
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

func (l *Ledger) Sequence() uint32 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.LedgerIndex
}

func (l *Ledger) Hash() [32]byte {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.Hash
}

func (l *Ledger) ParentHash() [32]byte {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.ParentHash
}

func (l *Ledger) CloseTime() time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.CloseTime
}

func (l *Ledger) ParentCloseTime() time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.ParentCloseTime
}

// CloseTimeResolution returns the close-time resolution in seconds (granularity of
// close-time rounding).
func (l *Ledger) CloseTimeResolution() uint32 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.CloseTimeResolution
}

func (l *Ledger) TotalDrops() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.Drops
}

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

func (l *Ledger) GetFees() drops.Fees {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.fees
}

func (l *Ledger) IsOpen() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state == StateOpen
}

// IsClosed reports whether the ledger is closed (validated counts as closed).
func (l *Ledger) IsClosed() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state == StateClosed || l.state == StateValidated
}

// IsImmutable reports whether the ledger is closed and its SHAMaps frozen.
// Equivalent to IsClosed here: Close() marks both maps immutable.
func (l *Ledger) IsImmutable() bool {
	return l.IsClosed()
}

func (l *Ledger) IsValidated() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state == StateValidated
}

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

// SkipListHashes returns the decoded rolling 256-entry LedgerHashes skip-list,
// or (nil, nil) when absent (early ledgers before it is populated).
func (l *Ledger) SkipListHashes() ([][32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return skiplist.ReadHashes(l.stateMap, keylet.LedgerHashes().Key)
}

// HashOfSeq returns the hash of ledger seq as recorded by this ledger. It resolves
// this ledger's identity, its parent, any ancestor inside the rolling 256-entry
// skip list, and 256-aligned ancestors in the historical skip list. A non-256-aligned
// ancestor more than 256 behind is unresolvable from one ledger → (zero, false).
func (l *Ledger) HashOfSeq(seq uint32) ([32]byte, bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	lseq := l.header.LedgerIndex
	if seq == 0 || seq > lseq {
		return [32]byte{}, false, nil
	}
	if seq == lseq {
		return l.header.Hash, true, nil
	}
	if seq == lseq-1 {
		return l.header.ParentHash, true, nil
	}

	// Rolling 256: this ledger's skip list holds hashes for seqs
	// [lseq-len .. lseq-1], so hash(seq) sits at index len-diff.
	_, hashes, _, err := skiplist.ReadLedgerHashesSLE(l.stateMap, keylet.LedgerHashes().Key)
	if err != nil {
		return [32]byte{}, false, err
	}
	if diff := lseq - seq; diff <= uint32(len(hashes)) {
		return hashes[uint32(len(hashes))-diff], true, nil
	}

	// Beyond the rolling window only 256-aligned ancestors are enshrined in the
	// historical skip list: index back from the page's LastLedgerSequence in
	// 256-ledger strides.
	if seq&0xff != 0 {
		return [32]byte{}, false, nil
	}
	_, histHashes, lastSeq, err := skiplist.ReadLedgerHashesSLE(l.stateMap, keylet.LedgerHashesForSeq(seq).Key)
	if err != nil {
		return [32]byte{}, false, err
	}
	if lastSeq >= seq {
		if d := (lastSeq - seq) >> 8; uint32(len(histHashes)) > d {
			return histHashes[uint32(len(histHashes))-d-1], true, nil
		}
	}
	return [32]byte{}, false, nil
}

func (l *Ledger) Exists(k keylet.Keylet) (bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.stateMap.Has(k.Key)
}

func (l *Ledger) Insert(k keylet.Keylet, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}

	exists, err := l.stateMap.Has(k.Key)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("entry already exists")
	}

	return l.stateMap.Put(k.Key, data)
}

func (l *Ledger) Update(k keylet.Keylet, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}

	exists, err := l.stateMap.Has(k.Key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrEntryNotFound
	}

	return l.stateMap.Put(k.Key, data)
}

func (l *Ledger) Erase(k keylet.Keylet) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}

	exists, err := l.stateMap.Has(k.Key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrEntryNotFound
	}

	return l.stateMap.Delete(k.Key)
}

func (l *Ledger) AdjustDropsDestroyed(drops drops.XRPAmount) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.dropsDestroyed = l.dropsDestroyed.Add(drops)
}

// AdoptState replaces this ledger's state map, tx map, and destroyed-drops tally
// with src's — committing a mutated MutableSnapshot back into the parent in one
// shot (rippled OpenView::apply). Header and fees are unchanged.
func (l *Ledger) AdoptState(src *Ledger) error {
	if src == nil {
		return errors.New("ledger: AdoptState from nil source")
	}

	src.mu.RLock()
	stateMap := src.stateMap
	txMap := src.txMap
	dropsDestroyed := src.dropsDestroyed
	src.mu.RUnlock()

	l.mu.Lock()
	l.stateMap = stateMap
	l.txMap = txMap
	l.dropsDestroyed = dropsDestroyed
	l.mu.Unlock()
	return nil
}

func (l *Ledger) AddTransaction(txHash [32]byte, txData []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}

	return l.txMap.Put(txHash, txData)
}

// AddTransactionWithMeta adds a tx with metadata, using NodeTypeTransactionWithMeta
// for correct tx-tree hashing.
func (l *Ledger) AddTransactionWithMeta(txHash [32]byte, txWithMetaData []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrLedgerImmutable
	}

	return l.txMap.PutWithNodeType(txHash, txWithMetaData, shamap.NodeTypeTransactionWithMeta)
}

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

func (l *Ledger) HasTransaction(txHash [32]byte) (bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.txMap.Has(txHash)
}

// TxExists reports whether a tx with the given hash is already in this ledger.
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

// LedgerSeq aliases Sequence for the ReadView interface.
func (l *Ledger) LedgerSeq() uint32 {
	return l.Sequence()
}

// Close closes the ledger, making it immutable
func (l *Ledger) Close(closeTime time.Time, closeFlags uint8) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrInvalidState
	}

	// Guard total-drops against unsigned underflow before mutating state:
	// header.Drops is uint64, so over-subtraction would wrap to a huge value,
	// be hashed into the header, and fork the chain. Invariants bound destroyed
	// XRP below supply, so this never fires under correct operation.
	if l.dropsDestroyed < 0 || uint64(l.dropsDestroyed) > l.header.Drops {
		return fmt.Errorf("ledger: drops underflow closing ledger %d: destroyed %d exceeds total %d",
			l.header.LedgerIndex, int64(l.dropsDestroyed), l.header.Drops)
	}

	// Update LedgerHashes skiplist before making state immutable.
	if err := l.updateSkipList(); err != nil {
		return fmt.Errorf("failed to update skip list: %w", err)
	}

	if err := l.stateMap.SetImmutable(); err != nil {
		return fmt.Errorf("failed to make state map immutable: %w", err)
	}
	if err := l.txMap.SetImmutable(); err != nil {
		return fmt.Errorf("failed to make tx map immutable: %w", err)
	}

	l.header.Drops -= uint64(l.dropsDestroyed)

	accountHash, err := l.stateMap.Hash()
	if err != nil {
		return fmt.Errorf("failed to get state map hash: %w", err)
	}

	txHash, err := l.txMap.Hash()
	if err != nil {
		return fmt.Errorf("failed to get tx map hash: %w", err)
	}

	l.header.AccountHash = accountHash
	l.header.TxHash = txHash
	l.header.CloseTime = closeTime
	l.header.CloseFlags = closeFlags
	l.header.Accepted = true

	l.header.Hash = calculateLedgerHash(l.header)

	l.state = StateClosed

	return nil
}

// UpdateNegativeUNL applies pending ValidatorToDisable / ValidatorToReEnable
// transitions on the NegativeUNL SLE during flag-ledger (seq%256==0) processing,
// before any transactions are applied. No-op on any other ledger or when neither
// transition field is set. Caller must NOT hold l.mu — it acquires it internally.
func (l *Ledger) UpdateNegativeUNL() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrInvalidState
	}

	return negativeunl.Apply(l.stateMap, l.header.LedgerIndex)
}

// updateSkipList updates the LedgerHashes SLE(s) in the state map. Caller holds l.mu.
func (l *Ledger) updateSkipList() error {
	return skiplist.UpdateOnMap(l.stateMap, l.header.LedgerIndex, l.header.ParentHash)
}

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

// MutableSnapshot returns a mutable deep copy suitable for further apply
// operations (unlike the immutable Snapshot). The clone inherits state from the
// parent; callers applying txs must ensure the parent was open (see OpenLedger.Modify).
func (l *Ledger) MutableSnapshot() (*Ledger, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	stateMapCopy, err := l.stateMap.Snapshot(true)
	if err != nil {
		return nil, err
	}
	txMapCopy, err := l.txMap.Snapshot(true)
	if err != nil {
		return nil, err
	}
	return &Ledger{
		stateMap:       stateMapCopy,
		txMap:          txMapCopy,
		header:         l.header,
		fees:           l.fees,
		state:          l.state,
		dropsDestroyed: l.dropsDestroyed,
	}, nil
}

func (l *Ledger) StateMapHash() ([32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.stateMap.Hash()
}

func (l *Ledger) TxMapHash() ([32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.txMap.Hash()
}

// ForEach calls fn for each state entry (key, data); return false to stop early.
func (l *Ledger) ForEach(fn func(key [32]byte, data []byte) bool) error {
	return l.ForEachCtx(context.Background(), fn)
}

// ForEachCtx is the context-aware ForEach; iteration aborts with ctx.Err() even
// between leaf callbacks (the SHAMap descent observes ctx).
func (l *Ledger) ForEachCtx(ctx context.Context, fn func(key [32]byte, data []byte) bool) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.stateMap.ForEachCtx(ctx, func(item *shamap.Item) bool {
		return fn(item.Key(), item.Data())
	})
}

// Succ returns the first state entry with key > the given key (O(log n) UpperBound).
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

// IterateStateFrom walks state entries in ascending key order starting strictly
// after `after` (zero key = from the beginning); fn returns false to stop. Using
// strictly-greater UpperBound means a resume marker pointing at a since-deleted
// entry continues from the next entry instead of rescanning or yielding nothing.
func (l *Ledger) IterateStateFrom(ctx context.Context, after [32]byte, fn func(key [32]byte, data []byte) bool) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	it := l.stateMap.UpperBound(after)
	for it.Valid() {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := it.Item()
		if item == nil {
			break
		}
		if !fn(item.Key(), item.Data()) {
			return nil
		}
		it.Next()
	}
	return it.Err()
}

// DecrementKey returns key-1 as a big-endian 32-byte integer (wrapping at zero).
// Recording DecrementKey(firstUnemittedKey) as a page marker makes the next
// IterateStateFrom resume exactly on that first un-emitted entry.
func DecrementKey(key [32]byte) [32]byte {
	out := key
	for i := 31; i >= 0; i-- {
		if out[i] > 0 {
			out[i]--
			return out
		}
		out[i] = 0xFF
	}
	return out
}

// ForEachTransaction calls fn for each tx (hash, data); return false to stop early.
func (l *Ledger) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.txMap.ForEach(func(item *shamap.Item) bool {
		return fn(item.Key(), item.Data())
	})
}

func (l *Ledger) TxCount() uint32 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return uint32(l.txMap.Size())
}

// StateMapSnapshot returns a mutable snapshot of the state map (e.g. for chaining
// one block's output into the next during continuous replay).
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

func (l *Ledger) SerializeHeader() []byte {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return header.AddRaw(l.header, true)
}

// calculateLedgerHash hashes a header via the canonical header.CalculateHash.
func calculateLedgerHash(h header.LedgerHeader) [32]byte {
	return header.CalculateHash(h)
}
