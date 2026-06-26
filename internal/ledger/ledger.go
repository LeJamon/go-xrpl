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

	// Snapshot the parent state map as mutable
	stateMap, err := parent.stateMap.Snapshot(true)
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot state map: %w", err)
	}

	// Create empty transaction map
	txMap := shamap.New(shamap.TypeTransaction)

	// Compute the child's close-time resolution dynamically. Rippled
	// adjusts the bin width each close based on whether the prior
	// round agreed (LedgerTiming.h:78-122, invoked from Ledger.cpp:291).
	// The parent's CloseFlags already encode previousAgree via
	// sLCF_NoConsensusTime (header.GetCloseAgree). Keeping the
	// computation in the ledger constructor matches rippled exactly
	// and lets every NewOpen callsite (consensus, service, replay,
	// test env) pick up the dynamic binning without plumbing
	// previousAgree through their signatures.
	newLedgerSeq := parent.header.LedgerIndex + 1
	newResolution := consensus.GetNextLedgerTimeResolution(
		parent.header.CloseTimeResolution,
		parent.header.GetCloseAgree(),
		newLedgerSeq,
	)

	// Create new header based on parent
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

// CloseTimeResolution returns the ledger's close time resolution in seconds.
// This value determines the granularity of close time rounding (typically 10s for genesis).
// Reference: rippled LedgerTiming.h, Env.cpp:126
func (l *Ledger) CloseTimeResolution() uint32 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.CloseTimeResolution
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

// IsImmutable reports whether the ledger has been closed and its SHAMaps
// frozen. Mirrors rippled's Ledger::isImmutable() (Ledger.h:278), which
// gates ledger-replay/proof-path serving (see
// LedgerReplayMsgHandler::processReplayDeltaRequest at
// LedgerReplayMsgHandler.cpp:197). A closed ledger has its state and tx
// SHAMaps marked immutable in Close(); IsImmutable is therefore equivalent
// to IsClosed for this implementation.
func (l *Ledger) IsImmutable() bool {
	return l.IsClosed()
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

// SkipListHashes returns the decoded rolling 256-entry LedgerHashes skip-list
// from this ledger's state map. Mirrors rippled's `ledger->read(keylet::skip())`
// at NegativeUNLVote.cpp:179. Returns (nil, nil) when the entry is absent
// (e.g. early ledgers before the skip-list has been populated).
func (l *Ledger) SkipListHashes() ([][32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return skiplist.ReadHashes(l.stateMap, keylet.LedgerHashes().Key)
}

// HashOfSeq returns the hash of ledger `seq` as recorded by this ledger,
// mirroring rippled's hashOfSeq (View.cpp:959). It resolves this ledger's own
// identity, its parent, any ancestor still inside the rolling 256-entry
// LedgerHashes skip list, and 256-aligned ancestors enshrined in the historical
// skip list. A non-256-aligned ancestor more than 256 behind is not directly
// resolvable from a single ledger (rippled reaches it via a reference ledger
// from getCandidateLedger); callers treat (zero,false) as "unresolvable from
// this ledger".
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

	// Beyond the rolling window: only 256-aligned ancestors are enshrined in
	// the historical skip list. Mirrors rippled hashOfSeq's deep branch
	// (View.cpp:1005-1018): index back from the page's LastLedgerSequence in
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

	// Check if entry exists
	exists, err := l.stateMap.Has(k.Key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrEntryNotFound
	}

	return l.stateMap.Delete(k.Key)
}

// AdjustDropsDestroyed records XRP that has been destroyed (fees)
func (l *Ledger) AdjustDropsDestroyed(drops drops.XRPAmount) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.dropsDestroyed = l.dropsDestroyed.Add(drops)
}

// AdoptState replaces this ledger's state map, transaction map, and
// destroyed-drops tally with those of src. It is the analogue of rippled's
// OpenView::apply(view) — committing a sandbox's accumulated changes back
// into the parent view in one shot (TxQ.cpp:1218 `sandbox.apply(view)`).
//
// src is expected to be a MutableSnapshot of this ledger that has since
// been mutated; on commit it surrenders ownership of its maps to the
// parent. Header and fees are unchanged (apply only touches state, the tx
// tree, and dropsDestroyed before close).
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

// LedgerSeq returns the current ledger's sequence number.
// Reference: rippled ReadView::seq().
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

	// Guard the total-drops update against unsigned underflow before
	// mutating any state: header.Drops is uint64, so an over-subtraction
	// would silently wrap to a huge value and be hashed into the header,
	// forking the chain. rippled subtracts on a signed int64 (XRPAmount),
	// where the same bug goes negative and is detectable. Under correct
	// operation the XRPNotCreated/fee invariants bound destroyed XRP well
	// below supply, so this never triggers; if it does, hard-stop before
	// any side effects to keep a latent accounting bug loud.
	if l.dropsDestroyed < 0 || uint64(l.dropsDestroyed) > l.header.Drops {
		return fmt.Errorf("ledger: drops underflow closing ledger %d: destroyed %d exceeds total %d",
			l.header.LedgerIndex, int64(l.dropsDestroyed), l.header.Drops)
	}

	// Update LedgerHashes skiplist before making state immutable.
	// Matches rippled's updateSkipList() in Ledger.cpp:878-943.
	if err := l.updateSkipList(); err != nil {
		return fmt.Errorf("failed to update skip list: %w", err)
	}

	// Make maps immutable
	if err := l.stateMap.SetImmutable(); err != nil {
		return fmt.Errorf("failed to make state map immutable: %w", err)
	}
	if err := l.txMap.SetImmutable(); err != nil {
		return fmt.Errorf("failed to make tx map immutable: %w", err)
	}

	l.header.Drops -= uint64(l.dropsDestroyed)

	// Get hashes
	accountHash, err := l.stateMap.Hash()
	if err != nil {
		return fmt.Errorf("failed to get state map hash: %w", err)
	}

	txHash, err := l.txMap.Hash()
	if err != nil {
		return fmt.Errorf("failed to get tx map hash: %w", err)
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

// UpdateNegativeUNL applies pending ValidatorToDisable /
// ValidatorToReEnable transitions on the NegativeUNL SLE, as part of
// flag-ledger processing.
//
// On a flag ledger (seq % 256 == 0), the negUNL transitions are
// processed BEFORE applying any transactions. go-xrpl previously
// skipped this step on the replay-delta path — every 256th ledger
// would fail the final hash check on networks with featureNegativeUNL
// and fall back to legacy catchup. The replay-delta Apply path now
// calls this for flag ledgers.
//
// Safe to call on any ledger. No-op when there's no NegativeUNL SLE
// or when neither ValidatorToDisable nor ValidatorToReEnable is set.
//
// Caller must NOT hold l.mu — this method acquires it internally.
func (l *Ledger) UpdateNegativeUNL() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen {
		return ErrInvalidState
	}

	return negativeunl.Apply(l.stateMap, l.header.LedgerIndex)
}

// updateSkipList updates the LedgerHashes SLE(s) in the state map.
// Called during Close() before making the state map immutable.
// Caller holds l.mu.
func (l *Ledger) updateSkipList() error {
	return skiplist.UpdateOnMap(l.stateMap, l.header.LedgerIndex, l.header.ParentHash)
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

// MutableSnapshot returns a mutable deep copy of this ledger. Unlike
// Snapshot() which returns an immutable clone, MutableSnapshot() produces
// a working copy suitable for further apply operations — the analogue of
// rippled's `std::make_shared<OpenView>(*current_)` (OpenLedger.cpp:61).
//
// The clone inherits `state` from the parent. Callers that want to apply
// transactions to the clone must ensure the parent was open: see
// OpenLedger.Modify which guards that invariant; tests use this helper
// to materialise pre-Accept fixtures whose `state` happens to be closed.
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
	return l.ForEachCtx(context.Background(), fn)
}

// ForEachCtx is the context-aware variant of ForEach. Iteration aborts
// with ctx.Err() whenever the context is cancelled, even between leaf
// callbacks (the SHAMap descent itself observes ctx).
func (l *Ledger) ForEachCtx(ctx context.Context, fn func(key [32]byte, data []byte) bool) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.stateMap.ForEachCtx(ctx, func(item *shamap.Item) bool {
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

// IterateStateFrom walks state entries in ascending key order, starting with
// the first entry whose key is strictly greater than `after`; pass the zero key
// to start from the beginning. fn is called for each entry and returns false to
// stop early. Iteration advances through the state map's upper bound (O(log n)
// seek, O(n) walk), so a resume marker pointing at a since-deleted entry
// continues from the next entry instead of rescanning from the start — and it
// never silently yields nothing the way a "skip until key == marker" scan does.
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

// DecrementKey returns key - 1, treating the 32-byte key as a big-endian
// integer (wrapping at zero). It is the companion to IterateStateFrom's
// strictly-greater (UpperBound) resume: recording DecrementKey(firstUnemittedKey)
// as a page-full marker makes the next IterateStateFrom resume exactly on that
// first un-emitted entry, whether or not the decremented value is itself a key.
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

// TxCount returns the number of transactions in the tx map.
func (l *Ledger) TxCount() uint32 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return uint32(l.txMap.Size())
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

// SerializeHeader returns the serialized ledger header bytes.
func (l *Ledger) SerializeHeader() []byte {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return header.AddRaw(l.header, true)
}

// calculateLedgerHash computes the hash of a ledger header. The canonical
// implementation lives in the header package; this thin wrapper keeps the
// existing call sites readable.
func calculateLedgerHash(h header.LedgerHeader) [32]byte {
	return header.CalculateHash(h)
}
