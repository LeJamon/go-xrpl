package sqlite

import (
	"context"
	"database/sql"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/sqlutil"
)

// accountTransactionRepository is the SQLite-backed account-transaction repository.
type accountTransactionRepository struct {
	executor executor
}

// newAccountTransactionRepository creates a SQLite account-transaction repository.
func newAccountTransactionRepository(executor executor) *accountTransactionRepository {
	return &accountTransactionRepository{executor: executor}
}

func (r *accountTransactionRepository) getExecutor() executor {
	return r.executor
}

// GetAccountTransactionsMinLedgerSeq returns the lowest ledger sequence present
// in the account-transactions index, or nil if it is empty.
func (r *accountTransactionRepository) GetAccountTransactionsMinLedgerSeq(ctx context.Context) (*relationaldb.LedgerIndex, error) {
	var seq sql.NullInt64
	err := r.getExecutor().QueryRowContext(ctx, "SELECT MIN(ledger_seq) FROM account_transactions").Scan(&seq)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_account_transactions_min_ledger_seq", "failed to query min account transaction ledger sequence", err)
	}
	if !seq.Valid {
		return nil, nil
	}
	result := relationaldb.LedgerIndex(seq.Int64)
	return &result, nil
}

func (r *accountTransactionRepository) queryAccountTxsPage(ctx context.Context, opName string, options relationaldb.AccountTxPageOptions, orderDir string, markerCmp string) (*relationaldb.AccountTxResult, error) {
	query := `SELECT t.trans_id, t.ledger_seq, t.status, t.raw_txn, t.txn_meta, at.txn_seq
			  FROM account_transactions at
			  INNER JOIN transactions t ON t.trans_id = at.trans_id
			  WHERE at.account = ?`

	args := []any{options.Account.String()}

	if options.MinLedger > 0 {
		query += " AND at.ledger_seq >= ?"
		args = append(args, options.MinLedger)
	}
	if options.MaxLedger > 0 {
		query += " AND at.ledger_seq <= ?"
		args = append(args, options.MaxLedger)
	}

	if options.Marker != nil {
		// For ASC: > marker; for DESC: < marker
		query += " AND (at.ledger_seq " + markerCmp + " ? OR (at.ledger_seq = ? AND at.txn_seq " + markerCmp + " ?))"
		args = append(args, options.Marker.LedgerSeq, options.Marker.LedgerSeq, options.Marker.TxnSeq)
	}

	query += " ORDER BY at.ledger_seq " + orderDir + ", at.txn_seq " + orderDir

	// Fetch one extra to check for more results
	limit := int64(options.Limit) + 1
	query += " LIMIT ?"
	args = append(args, limit)

	rows, err := r.getExecutor().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, relationaldb.NewQueryError(opName, "failed to query account transactions", err)
	}
	defer rows.Close()

	var transactions []relationaldb.TransactionInfo
	for rows.Next() {
		var info relationaldb.TransactionInfo
		var hashBytes, txnMeta []byte

		if err := rows.Scan(&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta, &info.TxnSeq); err != nil {
			return nil, relationaldb.NewQueryError(opName, "failed to scan row", err)
		}
		if err := sqlutil.CopyExact(info.Hash[:], hashBytes, "trans_id"); err != nil {
			return nil, relationaldb.NewDataError(opName, "malformed transaction hash", err)
		}
		copy(info.Account[:], options.Account[:])
		info.TxnMeta = txnMeta
		transactions = append(transactions, info)
	}
	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError(opName, "error iterating rows", err)
	}

	result := &relationaldb.AccountTxResult{
		LedgerRange: relationaldb.LedgerRange{
			Min: options.MinLedger,
			Max: options.MaxLedger,
		},
		Limit: options.Limit,
	}

	if uint64(len(transactions)) > uint64(options.Limit) {
		transactions = transactions[:len(transactions)-1]
		if len(transactions) > 0 {
			lastTx := transactions[len(transactions)-1]
			result.Marker = &relationaldb.AccountTxMarker{
				LedgerSeq: lastTx.LedgerSeq,
				TxnSeq:    lastTx.TxnSeq,
			}
		}
	}

	result.Transactions = transactions
	return result, nil
}

// GetOldestAccountTxsPage returns a marker-paginated page of an account's
// transactions, oldest-first.
func (r *accountTransactionRepository) GetOldestAccountTxsPage(ctx context.Context, options relationaldb.AccountTxPageOptions) (*relationaldb.AccountTxResult, error) {
	return r.queryAccountTxsPage(ctx, "get_oldest_account_txs_page", options, "ASC", ">")
}

// GetNewestAccountTxsPage returns a marker-paginated page of an account's
// transactions, newest-first.
func (r *accountTransactionRepository) GetNewestAccountTxsPage(ctx context.Context, options relationaldb.AccountTxPageOptions) (*relationaldb.AccountTxResult, error) {
	return r.queryAccountTxsPage(ctx, "get_newest_account_txs_page", options, "DESC", "<")
}

// SaveAccountTransaction inserts or updates an account-transaction index entry.
func (r *accountTransactionRepository) SaveAccountTransaction(ctx context.Context, accountID relationaldb.AccountID, txInfo relationaldb.TransactionInfo) error {
	query := `INSERT INTO account_transactions (trans_id, account, ledger_seq, txn_seq)
			  VALUES (?, ?, ?, ?)
			  ON CONFLICT (trans_id, account) DO UPDATE SET
			  ledger_seq = excluded.ledger_seq,
			  txn_seq = excluded.txn_seq`

	_, err := r.getExecutor().ExecContext(ctx, query,
		txInfo.Hash[:], accountID.String(), txInfo.LedgerSeq, txInfo.TxnSeq)
	if err != nil {
		return relationaldb.NewQueryError("save_account_transaction", "failed to save account transaction", err)
	}
	return nil
}

func (r *accountTransactionRepository) deleteByTransactionID(ctx context.Context, transactionID relationaldb.Hash) error {
	if _, err := r.getExecutor().ExecContext(ctx, "DELETE FROM account_transactions WHERE trans_id = ?", transactionID[:]); err != nil {
		return relationaldb.NewQueryError("delete_account_transactions_by_transaction_id", "failed to delete account transactions", err)
	}
	return nil
}

func (r *accountTransactionRepository) deleteByLedgerSequence(ctx context.Context, ledgerSeq relationaldb.LedgerIndex) error {
	if _, err := r.getExecutor().ExecContext(ctx, "DELETE FROM account_transactions WHERE ledger_seq = ?", ledgerSeq); err != nil {
		return relationaldb.NewQueryError("delete_account_transactions_by_ledger_sequence", "failed to delete account transactions", err)
	}
	return nil
}

// DeleteAccountTransactionsBeforeLedgerSeq deletes index entries in ledgers below ledgerSeq.
func (r *accountTransactionRepository) DeleteAccountTransactionsBeforeLedgerSeq(ctx context.Context, ledgerSeq relationaldb.LedgerIndex) error {
	_, err := r.getExecutor().ExecContext(ctx, "DELETE FROM account_transactions WHERE ledger_seq < ?", ledgerSeq)
	if err != nil {
		return relationaldb.NewQueryError("delete_account_transactions_before_ledger_seq", "failed to delete account transactions", err)
	}
	return nil
}
