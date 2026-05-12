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
	"bytes"
	"sort"
	"sync"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/openledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// HoldLedgers mirrors rippled LocalTxs.h:40 — after this many ledgers a
// tx that hasn't applied is dropped from the held pool.
const HoldLedgers uint32 = 5

// LocalTx is one entry in the local held-tx pool. Mirrors the rippled
// LocalTx wrapper (LocalTxs.cpp:53-104).
type LocalTx struct {
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
	mu  sync.Mutex
	txs []LocalTx
}

func New() *LocalTxs { return &LocalTxs{} }

// PushBack records a locally-submitted tx with the current ledger
// sequence as its anchor for the age check. Duplicates (same tx hash)
// are no-ops — rippled's std::list::emplace_back would happily double-
// insert, but we de-dupe here because every successful Submit publishes
// a new Current() so a relay-then-RPC echo would otherwise push twice.
// Mirrors LocalTxs.cpp:115-121.
func (l *LocalTxs) PushBack(currentLedgerSeq uint32, ptx openledger.PendingTx) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.txs {
		if l.txs[i].Ptx.Hash == ptx.Hash {
			return
		}
	}
	l.txs = append(l.txs, LocalTx{
		ExpireLedgerSeq: currentLedgerSeq + HoldLedgers,
		Ptx:             ptx,
	})
}

// Sweep removes obsolete entries. Mirrors LocalTxs.cpp:142-176:
//   - drop expired entries (view.seq > expire)
//   - drop entries already in view (tx already validated)
//   - drop entries whose sender's AccountRoot.Sequence has advanced past
//     the tx's sequence (replacement / success / tefPAST_SEQ)
//
// Ticket-based txs survive the seq check until they age out (we don't
// yet read Ticket SLEs from a view; the age cap bounds the held window).
func (l *LocalTxs) Sweep(view *ledger.Ledger) {
	if view == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	currentSeq := view.Sequence()
	kept := l.txs[:0]
	for _, lt := range l.txs {
		if currentSeq > lt.ExpireLedgerSeq {
			continue
		}
		if view.TxExists(lt.Ptx.Hash) {
			continue
		}
		if seqAdvancedPast(view, lt.Ptx) {
			continue
		}
		kept = append(kept, lt)
	}
	// Zero the tail so we don't pin dropped tx blobs in memory.
	for i := len(kept); i < len(l.txs); i++ {
		l.txs[i] = LocalTx{}
	}
	l.txs = kept
}

// seqAdvancedPast reports whether view's AccountRoot.Sequence for the
// tx's sender is strictly greater than the tx's sequence. Returns false
// (keep) when the account does not exist (e.g., a yet-unfunded AccountSet
// destination — the create might still land in a later round).
func seqAdvancedPast(view *ledger.Ledger, ptx openledger.PendingTx) bool {
	k := keylet.Account(ptx.Account)
	exists, err := view.Exists(k)
	if err != nil || !exists {
		return false
	}
	data, err := view.Read(k)
	if err != nil {
		return false
	}
	ar, err := state.ParseAccountRoot(data)
	if err != nil || ar == nil {
		return false
	}
	return ar.Sequence > ptx.Sequence
}

// GetTxSet returns the current pool as a canonical-sorted slice ready
// for OpenLedger.Accept's `locals` parameter.
//
// Sort key matches rippled CanonicalTXSet with zero salt
// (LocalTxs.cpp:126 `CanonicalTXSet tset(uint256{})`):
//  1. account bytes
//  2. sequence
//  3. tx hash
//
// With zero salt the accountKey XOR collapses to the raw 20-byte account
// padded to 32 bytes — equivalent to lexicographic compare on the
// account directly.
func (l *LocalTxs) GetTxSet() []openledger.PendingTx {
	l.mu.Lock()
	snapshot := make([]openledger.PendingTx, len(l.txs))
	for i, lt := range l.txs {
		snapshot[i] = lt.Ptx
	}
	l.mu.Unlock()

	sort.SliceStable(snapshot, func(i, j int) bool {
		if c := bytes.Compare(snapshot[i].Account[:], snapshot[j].Account[:]); c != 0 {
			return c < 0
		}
		if snapshot[i].Sequence != snapshot[j].Sequence {
			return snapshot[i].Sequence < snapshot[j].Sequence
		}
		return bytes.Compare(snapshot[i].Hash[:], snapshot[j].Hash[:]) < 0
	})
	return snapshot
}

func (l *LocalTxs) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.txs)
}
