package sqlite

import (
	"context"
	"database/sql"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

type TransactionRepository struct {
	db *sql.DB
	tx *sql.Tx
}

func NewTransactionRepository(db *sql.DB) *TransactionRepository {
	return &TransactionRepository{db: db}
}

func NewTransactionRepositoryWithTx(tx *sql.Tx) *TransactionRepository {
	return &TransactionRepository{tx: tx}
}

func (r *TransactionRepository) getExecutor() executor {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

func (r *TransactionRepository) GetTransactionsMinLedgerSeq(ctx context.Context) (*relationaldb.LedgerIndex, error) {
	var seq sql.NullInt64
	err := r.getExecutor().QueryRowContext(ctx, "SELECT MIN(ledger_seq) FROM transactions").Scan(&seq)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_transactions_min_ledger_seq", "failed to query min transaction ledger sequence", err)
	}
	if !seq.Valid {
		return nil, nil
	}
	result := relationaldb.LedgerIndex(seq.Int64)
	return &result, nil
}

func (r *TransactionRepository) GetTransactionCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.getExecutor().QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions").Scan(&count)
	if err != nil {
		return 0, relationaldb.NewQueryError("get_transaction_count", "failed to count transactions", err)
	}
	return count, nil
}

func (r *TransactionRepository) GetTransaction(ctx context.Context, hash relationaldb.Hash, ledgerRange *relationaldb.LedgerRange) (*relationaldb.TransactionInfo, relationaldb.TxSearchResult, error) {
	query := `SELECT trans_id, ledger_seq, status, raw_txn, txn_meta
			  FROM transactions WHERE trans_id = ?`

	var info relationaldb.TransactionInfo
	var hashBytes, txnMeta []byte

	err := r.getExecutor().QueryRowContext(ctx, query, hash[:]).Scan(
		&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta)

	if err == sql.ErrNoRows {
		if ledgerRange != nil {
			var count int64
			countQuery := `SELECT COUNT(DISTINCT ledger_seq) FROM ledgers
						   WHERE ledger_seq >= ? AND ledger_seq <= ?`
			// Note: ledgers table is in a different DB file. For cross-DB queries,
			// this will only work if called outside a transaction context. Within
			// the tx DB, we return TxSearchUnknown.
			if err := r.getExecutor().QueryRowContext(ctx, countQuery, ledgerRange.Min, ledgerRange.Max).Scan(&count); err != nil {
				return nil, relationaldb.TxSearchUnknown, nil
			}
			expectedCount := int64(ledgerRange.Max - ledgerRange.Min + 1)
			if count == expectedCount {
				return nil, relationaldb.TxSearchAll, nil
			}
			return nil, relationaldb.TxSearchSome, nil
		}
		return nil, relationaldb.TxSearchUnknown, nil
	}

	if err != nil {
		return nil, relationaldb.TxSearchUnknown, relationaldb.NewQueryError("get_transaction", "failed to query transaction", err)
	}

	copy(info.Hash[:], hashBytes)
	info.TxnMeta = txnMeta

	return &info, relationaldb.TxSearchAll, nil
}

func (r *TransactionRepository) GetTxHistory(ctx context.Context, startIndex relationaldb.LedgerIndex, limit int) ([]relationaldb.TransactionInfo, error) {
	query := `SELECT trans_id, ledger_seq, status, raw_txn, txn_meta
			  FROM transactions
			  ORDER BY ledger_seq DESC
			  LIMIT ? OFFSET ?`

	rows, err := r.getExecutor().QueryContext(ctx, query, limit, startIndex)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_tx_history", "failed to query transaction history", err)
	}
	defer rows.Close()

	var results []relationaldb.TransactionInfo
	for rows.Next() {
		var info relationaldb.TransactionInfo
		var hashBytes, txnMeta []byte

		if err := rows.Scan(&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta); err != nil {
			return nil, relationaldb.NewQueryError("get_tx_history", "failed to scan row", err)
		}
		copy(info.Hash[:], hashBytes)
		info.TxnMeta = txnMeta
		results = append(results, info)
	}
	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("get_tx_history", "error iterating rows", err)
	}
	return results, nil
}

func (r *TransactionRepository) SaveTransaction(ctx context.Context, txInfo *relationaldb.TransactionInfo) error {
	query := `INSERT INTO transactions (trans_id, ledger_seq, status, raw_txn, txn_meta)
			  VALUES (?, ?, ?, ?, ?)
			  ON CONFLICT (trans_id) DO UPDATE SET
			  ledger_seq = excluded.ledger_seq,
			  status = excluded.status,
			  raw_txn = excluded.raw_txn,
			  txn_meta = excluded.txn_meta`

	_, err := r.getExecutor().ExecContext(ctx, query,
		txInfo.Hash[:], txInfo.LedgerSeq, txInfo.Status, txInfo.RawTxn, txInfo.TxnMeta)
	if err != nil {
		return relationaldb.NewQueryError("save_transaction", "failed to save transaction", err)
	}
	return nil
}

func (r *TransactionRepository) DeleteTransactionsByLedgerSeq(ctx context.Context, ledgerSeq relationaldb.LedgerIndex) error {
	if _, err := r.getExecutor().ExecContext(ctx, "DELETE FROM account_transactions WHERE ledger_seq = ?", ledgerSeq); err != nil {
		return relationaldb.NewQueryError("delete_transactions_by_ledger_seq", "failed to delete account transactions", err)
	}
	if _, err := r.getExecutor().ExecContext(ctx, "DELETE FROM transactions WHERE ledger_seq = ?", ledgerSeq); err != nil {
		return relationaldb.NewQueryError("delete_transactions_by_ledger_seq", "failed to delete transactions", err)
	}
	return nil
}

func (r *TransactionRepository) DeleteTransactionsBeforeLedgerSeq(ctx context.Context, ledgerSeq relationaldb.LedgerIndex) error {
	if _, err := r.getExecutor().ExecContext(ctx, "DELETE FROM account_transactions WHERE ledger_seq < ?", ledgerSeq); err != nil {
		return relationaldb.NewQueryError("delete_transactions_before_ledger_seq", "failed to delete account transactions", err)
	}
	if _, err := r.getExecutor().ExecContext(ctx, "DELETE FROM transactions WHERE ledger_seq < ?", ledgerSeq); err != nil {
		return relationaldb.NewQueryError("delete_transactions_before_ledger_seq", "failed to delete transactions", err)
	}
	return nil
}

func (r *TransactionRepository) GetKBUsedTransaction(ctx context.Context) (uint32, error) {
	var pageCount, pageSize int64
	if err := r.getExecutor().QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, relationaldb.NewQueryError("get_kb_used_transaction", "failed to get page count", err)
	}
	if err := r.getExecutor().QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, relationaldb.NewQueryError("get_kb_used_transaction", "failed to get page size", err)
	}
	return uint32(pageCount * pageSize / 1024), nil
}

func (r *TransactionRepository) HasTransactionSpace(ctx context.Context) (bool, error) {
	return true, nil
}
