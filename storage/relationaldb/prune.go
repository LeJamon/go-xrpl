package relationaldb

import (
	"context"
	"fmt"
)

// LedgerPruner deletes ledger and transaction index rows below a retention
// boundary, in batches, mirroring rippled's SHAMapStoreImp::clearSql over the
// Ledgers, Transactions and AccountTransactions tables. It is the relational
// half of online_delete rotation.
type LedgerPruner struct {
	mgr   RepositoryManager
	batch int
}

// NewLedgerPruner builds a pruner over mgr. batch caps how many sequences each
// delete step spans, bounding the size of any single delete statement; a
// non-positive batch deletes in one step.
func NewLedgerPruner(mgr RepositoryManager, batch int) *LedgerPruner {
	return &LedgerPruner{mgr: mgr, batch: batch}
}

// DeleteLedgersBefore removes every Ledgers, Transactions and
// AccountTransactions row with a ledger sequence strictly below boundary.
//
// Each table is pruned independently from its own current minimum sequence,
// matching rippled's clearSql: it looks up the table's lowest LedgerSeq, skips
// the table when nothing lies below the boundary, then deletes up the range in
// batch-sized steps. The whole operation is best-effort and idempotent — a
// later pass with the same or a higher boundary simply finds less to delete.
func (p *LedgerPruner) DeleteLedgersBefore(ctx context.Context, boundary uint32) error {
	if p == nil || p.mgr == nil || boundary == 0 {
		return nil
	}

	if err := p.clear(ctx, boundary,
		p.mgr.Ledger().GetMinLedgerSeq,
		// Ledgers delete is inclusive (<= maxSeq); pass boundary-1 to delete
		// strictly below the boundary, keeping the boundary ledger itself.
		func(ctx context.Context, upTo uint32) error {
			return p.mgr.Ledger().DeleteLedgersBySeq(ctx, LedgerIndex(upTo-1))
		},
	); err != nil {
		return fmt.Errorf("prune ledgers: %w", err)
	}

	if err := p.clear(ctx, boundary,
		p.mgr.Transaction().GetTransactionsMinLedgerSeq,
		func(ctx context.Context, upTo uint32) error {
			return p.mgr.Transaction().DeleteTransactionsBeforeLedgerSeq(ctx, LedgerIndex(upTo))
		},
	); err != nil {
		return fmt.Errorf("prune transactions: %w", err)
	}

	if err := p.clear(ctx, boundary,
		p.mgr.AccountTransaction().GetAccountTransactionsMinLedgerSeq,
		func(ctx context.Context, upTo uint32) error {
			return p.mgr.AccountTransaction().DeleteAccountTransactionsBeforeLedgerSeq(ctx, LedgerIndex(upTo))
		},
	); err != nil {
		return fmt.Errorf("prune account transactions: %w", err)
	}

	return nil
}

// clear deletes rows below boundary in batch-sized steps, starting from the
// table's current minimum sequence. getMin returns the lowest sequence still
// present (nil when the table is empty); deleteUpTo deletes rows strictly below
// the supplied (exclusive) sequence.
func (p *LedgerPruner) clear(
	ctx context.Context,
	boundary uint32,
	getMin func(context.Context) (*LedgerIndex, error),
	deleteUpTo func(context.Context, uint32) error,
) error {
	m, err := getMin(ctx)
	if err != nil {
		return err
	}
	if m == nil {
		return nil
	}
	min := uint32(*m)
	if min >= boundary {
		return nil
	}

	for min < boundary {
		upTo := boundary
		if p.batch > 0 {
			if step := min + uint32(p.batch); step < boundary {
				upTo = step
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := deleteUpTo(ctx, upTo); err != nil {
			return err
		}
		min = upTo
	}
	return nil
}
