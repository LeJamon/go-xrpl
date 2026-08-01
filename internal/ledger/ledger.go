package ledger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/skiplist"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

// Common errors for ledger operations
var (
	ErrLedgerImmutable = errors.New("ledger is immutable")
	ErrLedgerNotClosed = errors.New("ledger is not closed")
	ErrEntryExists     = errors.New("ledger entry already exists")
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

	Read(k keylet.Keylet) ([]byte, error)

	Exists(k keylet.Keylet) (bool, error)

	Fees() drops.Fees
}

// Writer provides write access to ledger state
type Writer interface {
	Insert(k keylet.Keylet, data []byte) error

	Update(k keylet.Keylet, data []byte) error

	Erase(k keylet.Keylet) error

	// AdjustDropsDestroyed records XRP destroyed as fees.
	AdjustDropsDestroyed(drops drops.XRPAmount) error
}

// AtomicWriter can commit a group of ledger and destroyed-drop changes as one
// unit. The supplied writer is valid only for the duration of apply.
type AtomicWriter interface {
	Writer
	ApplyAtomically(apply func(Writer) error) error
}

// Ledger represents a single ledger in the chain
type Ledger struct {
	mu sync.RWMutex

	stateMap *shamap.SHAMap
	txMap    *shamap.SHAMap

	header header.LedgerHeader

	fees drops.Fees

	state State

	writable bool

	// Drops destroyed in this ledger (transaction fees)
	dropsDestroyed drops.XRPAmount

	// rules is fixed while an open ledger applies transactions and is replaced
	// with the resulting amendment state when that ledger closes.
	rules *amendment.Rules
}

var _ AtomicWriter = (*Ledger)(nil)

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
	return uint32(l.header.CloseTimeResolution)
}

func (l *Ledger) TotalDrops() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header.Drops
}

// Header returns a copy of the ledger header
func (l *Ledger) Header() header.LedgerHeader {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.header
}

func (l *Ledger) Fees() drops.Fees {
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

// IsImmutable reports whether the ledger is read-only.
func (l *Ledger) IsImmutable() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return !l.writable
}

func (l *Ledger) IsValidated() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state == StateValidated
}

func (l *Ledger) Read(k keylet.Keylet) ([]byte, error) {
	return l.ReadContext(context.Background(), k)
}

func (l *Ledger) ReadContext(ctx context.Context, k keylet.Keylet) ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	item, found, err := l.stateMap.GetContext(ctx, k.Key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	data := item.Data()
	if !state.MatchesKeyletType(k, data) {
		return nil, nil
	}
	return data, nil
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
	return l.HashOfSeqContext(context.Background(), seq)
}

func (l *Ledger) HashOfSeqContext(ctx context.Context, seq uint32) ([32]byte, bool, error) {
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
	_, hashes, lastSeq, err := skiplist.ReadLedgerHashesSLEContext(ctx, l.stateMap, keylet.LedgerHashes().Key)
	if err != nil {
		return [32]byte{}, false, err
	}
	if len(hashes) != 0 {
		if lseq == 0 || lastSeq != lseq-1 {
			return [32]byte{}, false, fmt.Errorf("rolling LedgerHashes ends at %d, want %d", lastSeq, lseq-1)
		}
		wantLen := min(int(lastSeq), 256)
		if len(hashes) != wantLen {
			return [32]byte{}, false, fmt.Errorf("rolling LedgerHashes cardinality %d, want %d", len(hashes), wantLen)
		}
		firstSeq := lastSeq - uint32(len(hashes)) + 1
		if seq >= firstSeq && seq <= lastSeq {
			return hashes[seq-firstSeq], true, nil
		}
	}

	// Beyond the rolling window only 256-aligned ancestors are enshrined in the
	// historical skip list: index back from the page's LastLedgerSequence in
	// 256-ledger strides.
	if seq&0xff != 0 {
		return [32]byte{}, false, nil
	}
	_, histHashes, lastSeq, err := skiplist.ReadLedgerHashesSLEContext(ctx, l.stateMap, keylet.LedgerHashesForSeq(seq).Key)
	if err != nil {
		return [32]byte{}, false, err
	}
	if len(histHashes) == 0 {
		return [32]byte{}, false, nil
	}
	if lastSeq&0xff != 0 || lastSeq>>16 != seq>>16 {
		return [32]byte{}, false, fmt.Errorf("historical LedgerHashes has invalid last sequence %d for ledger %d", lastSeq, seq)
	}
	firstSeq := (lastSeq >> 16) << 16
	if firstSeq == 0 {
		firstSeq = 256
	}
	wantLen := int((lastSeq-firstSeq)>>8) + 1
	if len(histHashes) != wantLen {
		return [32]byte{}, false, fmt.Errorf("historical LedgerHashes cardinality %d, want %d", len(histHashes), wantLen)
	}
	if seq >= firstSeq && seq <= lastSeq {
		return histHashes[(seq-firstSeq)>>8], true, nil
	}
	return [32]byte{}, false, nil
}

// Rules returns the transaction rules fixed for this ledger.
func (l *Ledger) Rules() *amendment.Rules {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.rules
}

// LedgerSeq aliases Sequence for the ReadView interface.
func (l *Ledger) LedgerSeq() uint32 {
	return l.Sequence()
}
