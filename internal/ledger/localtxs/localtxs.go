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
// sequence as its anchor for the age check. Expiration is the lesser
// of currentLedgerSeq + HoldLedgers and (LastLedgerSequence + 1) when
// the tx has sfLastLedgerSequence set — mirrors LocalTxs.cpp:58-65.
//
// On duplicate hash, refresh the expiration rather than no-op so a
// relay-then-RPC echo extends the hold window instead of locking it to
// whichever insertion arrived first.
func (l *LocalTxs) PushBack(currentLedgerSeq uint32, ptx openledger.PendingTx) {
	expire := currentLedgerSeq + HoldLedgers
	if ptx.LastLedgerSequence > 0 {
		if cap := ptx.LastLedgerSequence + 1; cap < expire {
			expire = cap
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.txs {
		if l.txs[i].Ptx.Hash == ptx.Hash {
			l.txs[i].ExpireLedgerSeq = expire
			return
		}
	}
	l.txs = append(l.txs, LocalTx{
		ExpireLedgerSeq: expire,
		Ptx:             ptx,
	})
}

// Sweep removes obsolete entries. Mirrors LocalTxs.cpp:142-176:
//   - drop expired entries (view.seq > expire)
//   - drop entries already in view (tx already validated)
//   - for seq-based txs: drop when the sender's AccountRoot.Sequence has
//     advanced past the tx's sequence (replacement / success / tefPAST_SEQ)
//   - for ticket-based txs: drop when the sender's sequence has advanced
//     past the ticket AND the Ticket SLE is gone (burned).
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
		if lt.Ptx.IsTicket {
			if ticketBurned(view, lt.Ptx) {
				continue
			}
		} else if seqAdvancedPast(view, lt.Ptx) {
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

// ticketBurned reports whether the Ticket the tx targets is gone from
// the view. Mirrors rippled LocalTxs.cpp:165-175: a ticket-based held tx
// is dead if the AccountRoot.Sequence has moved past the ticket value
// AND the Ticket SLE no longer exists (consumed). Both conditions
// matter: a ticket can be created (sequence advanced) but not yet
// consumed (SLE still present), in which case the held tx is still
// applicable.
func ticketBurned(view *ledger.Ledger, ptx openledger.PendingTx) bool {
	ar, ok := readAccountRoot(view, ptx.Account)
	if !ok {
		return false
	}
	if ar.Sequence <= ptx.Sequence {
		return false
	}
	exists, err := view.Exists(keylet.Ticket(ptx.Account, ptx.Sequence))
	if err != nil {
		return false
	}
	return !exists
}

func readAccountRoot(view *ledger.Ledger, accountID [20]byte) (*state.AccountRoot, bool) {
	k := keylet.Account(accountID)
	exists, err := view.Exists(k)
	if err != nil || !exists {
		return nil, false
	}
	data, err := view.Read(k)
	if err != nil {
		return nil, false
	}
	ar, err := state.ParseAccountRoot(data)
	if err != nil || ar == nil {
		return nil, false
	}
	return ar, true
}

// GetTxSet returns the current pool as a canonical-sorted slice ready
// for OpenLedger.Accept's `locals` parameter. Mirrors rippled
// LocalTxs.cpp:126 — `CanonicalTXSet tset(uint256{})` (zero salt).
//
// Sort key (zero-salt CanonicalTXSet):
//  1. account bytes
//  2. sequence
//  3. tx hash
//
// The zero-salt XOR collapses accountKey to the raw 20-byte account
// padded to 32 bytes, so this is equivalent to lexicographic compare on
// account directly. Any future caller that needs a salt-aware sort
// (e.g. the consensus build path's SHAMap-root salt) must call
// openledger.CanonicalSort on the returned slice instead.
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
