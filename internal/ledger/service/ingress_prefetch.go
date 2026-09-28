package service

import (
	"context"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ticket"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/keylet"
)

const ingressPrefetchBudget = 100 * time.Millisecond

// Warm a small, fixed set of likely state paths before taking the apply gate.
// Never use these values to authorize or apply a transaction: after admission,
// SubmitDetailed still reads and validates against the current ledger. A ledger
// replacement during this hint-only work is therefore safe.
func (s *Service) prefetchIngressState(ptx openledger.PendingTx) {
	if s.nodeStore == nil || ptx.Parsed == nil {
		return // In-memory views need no disk-read hints.
	}
	s.mu.RLock()
	open := s.openLedgerView
	s.mu.RUnlock()
	if open == nil {
		return
	}
	current := open.Current()
	if current == nil {
		return
	}
	// Do not hold the published view's tree/ledger read locks across I/O:
	// snapshot creation and consensus need those locks too. This O(1) fork
	// is read-only here, never flushed/published, and only warms verified
	// hash-addressed children and the runtime NodeStore cache.
	view, err := current.MutableSnapshotUnflushed()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ingressPrefetchBudget)
	defer cancel()
	// The deadline bounds further hints; a backend already in a physical
	// read may not be interruptible. No apply gate is held in that case.
	for _, key := range ingressPrefetchKeys(ptx) {
		if ctx.Err() != nil {
			break
		}
		_, _ = view.ReadContext(ctx, key)
	}
}

func ingressPrefetchKeys(ptx openledger.PendingTx) []keylet.Keylet {
	keys := []keylet.Keylet{keylet.Account(ptx.Account)}
	if ptx.IsTicket {
		keys = append(keys, keylet.Ticket(ptx.Account, ptx.Sequence))
	}
	switch transaction := ptx.Parsed.(type) {
	case *payment.Payment:
		if destination, err := state.DecodeAccountID(transaction.Destination); err == nil {
			keys = append(keys, keylet.Account(destination))
		}
	case *trustset.TrustSet:
		if issuer, err := state.DecodeAccountID(transaction.LimitAmount.Issuer); err == nil {
			keys = append(keys, keylet.Account(issuer),
				keylet.Line(ptx.Account, issuer, transaction.LimitAmount.Currency),
				keylet.OwnerDir(ptx.Account), keylet.OwnerDir(issuer))
		}
	case *ticket.TicketCreate:
		keys = append(keys, keylet.OwnerDir(ptx.Account))
	}
	return keys
}
