// Package localtxs is goxrpl's port of rippled's app/ledger/LocalTxs.
//
// LocalTxs is a process-local pool of locally-submitted (RPC) transactions
// that need to survive Submit failure and LCL transitions until either the
// sender's AccountRoot.Sequence advances past them (success or replacement)
// or they age out (5 ledgers).
//
// Reference: rippled LocalTxs.h:65, LocalTxs.cpp:197.
package localtxs

import (
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

const holdLedgers uint32 = 5

type localTx struct {
	// ExpireLedgerSeq is the highest ledger index at which this tx is
	// still considered live. Mirrors rippled m_expire — set at push_back
	// to index + HoldLedgers and clamped by LastLedgerSequence+1 when
	// present (LocalTxs.cpp:58-65).
	ExpireLedgerSeq uint32
	// Ptx is the parsed pending tx (blob + hash + account + sequence).
	Ptx openledger.PendingTx
}

// LocalTxs is the held pool. All methods are safe for concurrent callers.
type LocalTxs struct {
	mu  sync.RWMutex
	txs map[[32]byte]localTx
}

type sweepView interface {
	Sequence() uint32
	TxExists([32]byte) (bool, error)
	Read(keylet.Keylet) ([]byte, error)
	Exists(keylet.Keylet) (bool, error)
}

func New() *LocalTxs {
	return &LocalTxs{txs: make(map[[32]byte]localTx)}
}

// PushBack records a locally-submitted tx with the current ledger
// sequence as its anchor for the age check. Expiration is the lesser
// of currentLedgerSeq + HoldLedgers and (LastLedgerSequence + 1) when
// the tx has sfLastLedgerSequence set — mirrors LocalTxs.cpp:58-65.
// Presence is what triggers the clamp (rippled uses isFieldPresent at
// :63), so a non-nil pointer with value 0 still clamps expire to 1.
func (l *LocalTxs) PushBack(currentLedgerSeq uint32, ptx openledger.PendingTx) {
	expire := currentLedgerSeq + holdLedgers
	if ptx.HasLastLedgerSequence {
		if lastExpire := ptx.LastLedgerSequence + 1; lastExpire < expire {
			expire = lastExpire
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.txs == nil {
		l.txs = make(map[[32]byte]localTx)
	}
	if existing, ok := l.txs[ptx.Hash]; ok {
		if expire > existing.ExpireLedgerSeq {
			existing.ExpireLedgerSeq = expire
			l.txs[ptx.Hash] = existing
		}
		return
	}
	l.txs[ptx.Hash] = localTx{
		ExpireLedgerSeq: expire,
		Ptx:             clonePendingTx(ptx),
	}
}

// Sweep removes obsolete entries. Mirrors LocalTxs.cpp:142-176:
//   - drop expired entries (view.seq > expire)
//   - drop entries already in view (tx already validated)
//   - for seq-based txs: drop when the sender's AccountRoot.Sequence has
//     advanced past the tx's sequence (replacement / success / tefPAST_SEQ)
//   - for ticket-based txs: drop when the sender's sequence has advanced
//     past the ticket AND the Ticket SLE is gone (burned).
func (l *LocalTxs) Sweep(view sweepView) error {
	viewValue := reflect.ValueOf(view)
	if view == nil || viewValue.Kind() == reflect.Ptr && viewValue.IsNil() {
		return errors.New("localtxs.Sweep: view is nil")
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	currentSeq := view.Sequence()
	kept := make(map[[32]byte]localTx, len(l.txs))
	for hash, lt := range l.txs {
		if currentSeq > lt.ExpireLedgerSeq {
			continue
		}
		exists, err := view.TxExists(lt.Ptx.Hash)
		if err != nil {
			return fmt.Errorf("localtxs.Sweep: inspect transaction %x: %w", hash, err)
		}
		if exists {
			continue
		}
		if lt.Ptx.IsTicket {
			burned, err := ticketBurned(view, lt.Ptx)
			if err != nil {
				return fmt.Errorf("localtxs.Sweep: inspect ticket transaction %x: %w", hash, err)
			}
			if burned {
				continue
			}
		} else {
			advanced, err := seqAdvancedPast(view, lt.Ptx)
			if err != nil {
				return fmt.Errorf("localtxs.Sweep: inspect sequence transaction %x: %w", hash, err)
			}
			if advanced {
				continue
			}
		}
		kept[hash] = lt
	}
	l.txs = kept
	return nil
}

// seqAdvancedPast reports whether view's AccountRoot.Sequence for the
// tx's sender is strictly greater than the tx's sequence. Returns false
// (keep) when the account does not exist (e.g., a yet-unfunded AccountSet
// destination — the create might still land in a later round).
func seqAdvancedPast(view sweepView, ptx openledger.PendingTx) (bool, error) {
	ar, err := state.ReadAccountRoot(view, ptx.Account)
	if err != nil {
		return false, err
	}
	if ar == nil {
		return false, nil
	}
	return ar.Sequence > ptx.Sequence, nil
}

// ticketBurned reports whether the Ticket the tx targets is gone from
// the view. Mirrors rippled LocalTxs.cpp:165-175: a ticket-based held tx
// is dead if the AccountRoot.Sequence has moved past the ticket value
// AND the Ticket SLE no longer exists (consumed). Both conditions
// matter: a ticket can be created (sequence advanced) but not yet
// consumed (SLE still present), in which case the held tx is still
// applicable.
func ticketBurned(view sweepView, ptx openledger.PendingTx) (bool, error) {
	ar, err := state.ReadAccountRoot(view, ptx.Account)
	if err != nil {
		return false, err
	}
	if ar == nil {
		return false, nil
	}
	if ar.Sequence <= ptx.Sequence {
		return false, nil
	}
	exists, err := view.Exists(keylet.Ticket(ptx.Account, ptx.Sequence))
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// GetTxSet returns the current pool as a canonical-sorted slice ready
// for OpenLedger.Accept's `locals` parameter. The zero salt mirrors
// rippled LocalTxs.cpp:126 — `CanonicalTXSet tset(uint256{})`. A future
// caller needing a salt-aware order passes its salt instead.
func (l *LocalTxs) GetTxSet() []openledger.PendingTx {
	l.mu.RLock()
	snapshot := make([]openledger.PendingTx, 0, len(l.txs))
	for _, lt := range l.txs {
		snapshot = append(snapshot, clonePendingTx(lt.Ptx))
	}
	l.mu.RUnlock()

	openledger.CanonicalSort(snapshot, [32]byte{})
	return snapshot
}

// Get returns an owned copy of a held transaction by hash. Unlike GetTxSet it
// does not sort or scan the pool, so query paths can inspect a single entry
// without turning the bounded held pool into an O(n log n) lookup.
func (l *LocalTxs) Get(hash [32]byte) (openledger.PendingTx, bool) {
	l.mu.RLock()
	entry, ok := l.txs[hash]
	if ok {
		entry.Ptx = clonePendingTx(entry.Ptx)
	}
	l.mu.RUnlock()
	if !ok {
		return openledger.PendingTx{}, false
	}
	return entry.Ptx, true
}

func (l *LocalTxs) Size() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.txs)
}

func clonePendingTx(ptx openledger.PendingTx) openledger.PendingTx {
	ptx.Blob = append([]byte(nil), ptx.Blob...)
	ptx.Parsed = nil
	return ptx
}
