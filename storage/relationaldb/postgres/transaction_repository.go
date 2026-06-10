package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// TransactionRepository implements the TransactionRepository interface for PostgreSQL
type TransactionRepository struct {
	db *sql.DB
	tx *sql.Tx // Optional transaction context
}

// NewTransactionRepository creates a new PostgreSQL transaction repository
func NewTransactionRepository(db *sql.DB) *TransactionRepository {
	return &TransactionRepository{db: db}
}

// NewTransactionRepositoryWithTx creates a new PostgreSQL transaction repository within a transaction
func NewTransactionRepositoryWithTx(tx *sql.Tx) *TransactionRepository {
	return &TransactionRepository{tx: tx}
}

// getExecutor returns the appropriate executor (db or tx)
func (r *TransactionRepository) getExecutor() executor {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

// GetTransactionsMinLedgerSeq returns the lowest ledger sequence present in the
// transactions table, or nil if it is empty.
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

// GetTransactionCount returns the number of rows in the transactions table.
func (r *TransactionRepository) GetTransactionCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.getExecutor().QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions").Scan(&count)
	if err != nil {
		return 0, relationaldb.NewQueryError("get_transaction_count", "failed to count transactions", err)
	}

	return count, nil
}

// GetTransaction returns the transaction with the given hash. When ledgerRange is
// set and the transaction is absent, the TxSearchResult reports whether the whole
// range was searched (matching rippled's "searched all/some" semantics).
func (r *TransactionRepository) GetTransaction(ctx context.Context, hash relationaldb.Hash, ledgerRange *relationaldb.LedgerRange) (*relationaldb.TransactionInfo, relationaldb.TxSearchResult, error) {
	query := `SELECT trans_id, ledger_seq, status, raw_txn, txn_meta
			  FROM transactions WHERE trans_id = $1`

	var info relationaldb.TransactionInfo
	var hashBytes []byte
	var txnMeta sql.NullString

	err := r.getExecutor().QueryRowContext(ctx, query, hash[:]).Scan(
		&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta)

	if errors.Is(err, sql.ErrNoRows) {
		if ledgerRange != nil {
			// Check if all ledgers in range are present (rippled behavior)
			var count int64
			countQuery := `SELECT COUNT(DISTINCT ledger_seq) FROM ledgers 
						   WHERE ledger_seq >= $1 AND ledger_seq <= $2`
			if err := r.getExecutor().QueryRowContext(ctx, countQuery, ledgerRange.Min, ledgerRange.Max).Scan(&count); err != nil {
				return nil, relationaldb.TxSearchUnknown, relationaldb.NewQueryError("get_transaction", "failed to count ledgers in range", err)
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
	if txnMeta.Valid {
		info.TxnMeta = []byte(txnMeta.String)
	}

	return &info, relationaldb.TxSearchAll, nil
}

// GetTxHistory returns transactions newest-first, skipping startIndex rows and
// returning up to limit of them.
func (r *TransactionRepository) GetTxHistory(ctx context.Context, startIndex relationaldb.LedgerIndex, limit int) ([]relationaldb.TransactionInfo, error) {
	// Match rippled's getTxHistory behavior - most recent transactions
	query := `SELECT trans_id, ledger_seq, status, raw_txn, txn_meta
			  FROM transactions 
			  ORDER BY ledger_seq DESC
			  OFFSET $1 LIMIT $2`

	rows, err := r.getExecutor().QueryContext(ctx, query, startIndex, limit)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_tx_history", "failed to query transaction history", err)
	}
	defer rows.Close()

	var results []relationaldb.TransactionInfo

	for rows.Next() {
		var info relationaldb.TransactionInfo
		var hashBytes []byte
		var txnMeta sql.NullString

		if err := rows.Scan(&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta); err != nil {
			return nil, relationaldb.NewQueryError("get_tx_history", "failed to scan row", err)
		}

		copy(info.Hash[:], hashBytes)
		if txnMeta.Valid {
			info.TxnMeta = []byte(txnMeta.String)
		}
		results = append(results, info)
	}

	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("get_tx_history", "error iterating rows", err)
	}

	return results, nil
}

// SaveTransaction inserts or updates a transaction row (upsert on trans_id).
func (r *TransactionRepository) SaveTransaction(ctx context.Context, txInfo *relationaldb.TransactionInfo) error {
	query := `INSERT INTO transactions (trans_id, ledger_seq, status, raw_txn, txn_meta)
			  VALUES ($1, $2, $3, $4, $5)
			  ON CONFLICT (trans_id) DO UPDATE SET
			  ledger_seq = EXCLUDED.ledger_seq,
			  status = EXCLUDED.status,
			  raw_txn = EXCLUDED.raw_txn,
			  txn_meta = EXCLUDED.txn_meta`

	_, err := r.getExecutor().ExecContext(ctx, query,
		txInfo.Hash[:], txInfo.LedgerSeq, txInfo.Status, txInfo.RawTxn, txInfo.TxnMeta)

	if err != nil {
		return relationaldb.NewQueryError("save_transaction", "failed to save transaction", err)
	}

	return nil
}

// DeleteTransactionsByLedgerSeq deletes all transactions in the given ledger.
func (r *TransactionRepository) DeleteTransactionsByLedgerSeq(ctx context.Context, ledgerSeq relationaldb.LedgerIndex) error {
	// Note: This assumes we have a way to begin transactions within the repository
	// In a full implementation, transaction management would be handled at a higher level

	// Delete account transactions first (foreign key constraint)
	if _, err := r.getExecutor().ExecContext(ctx, "DELETE FROM account_transactions WHERE ledger_seq = $1", ledgerSeq); err != nil {
		return relationaldb.NewQueryError("delete_transactions_by_ledger_seq", "failed to delete account transactions", err)
	}

	if _, err := r.getExecutor().ExecContext(ctx, "DELETE FROM transactions WHERE ledger_seq = $1", ledgerSeq); err != nil {
		return relationaldb.NewQueryError("delete_transactions_by_ledger_seq", "failed to delete transactions", err)
	}

	return nil
}

// DeleteTransactionsBeforeLedgerSeq deletes all transactions in ledgers below ledgerSeq.
func (r *TransactionRepository) DeleteTransactionsBeforeLedgerSeq(ctx context.Context, ledgerSeq relationaldb.LedgerIndex) error {
	// Delete account transactions first
	if _, err := r.getExecutor().ExecContext(ctx, "DELETE FROM account_transactions WHERE ledger_seq < $1", ledgerSeq); err != nil {
		return relationaldb.NewQueryError("delete_transactions_before_ledger_seq", "failed to delete account transactions", err)
	}

	if _, err := r.getExecutor().ExecContext(ctx, "DELETE FROM transactions WHERE ledger_seq < $1", ledgerSeq); err != nil {
		return relationaldb.NewQueryError("delete_transactions_before_ledger_seq", "failed to delete transactions", err)
	}

	return nil
}

// GetKBUsedTransaction returns the on-disk size of the transactions table in KB.
func (r *TransactionRepository) GetKBUsedTransaction(ctx context.Context) (uint32, error) {
	var size int64
	err := r.getExecutor().QueryRowContext(ctx,
		"SELECT pg_total_relation_size('transactions') + pg_total_relation_size('account_transactions')").Scan(&size)

	if err != nil {
		return 0, relationaldb.NewQueryError("get_kb_used_transaction", "failed to get transaction tables size", err)
	}

	return uint32(size / 1024), nil
}

// HasTransactionSpace reports whether the transactions table can accept more rows.
func (r *TransactionRepository) HasTransactionSpace(ctx context.Context) (bool, error) {
	// For PostgreSQL, we'll implement a simple check
	// In production, you'd want to check actual disk space
	return true, nil
}
