package sqlite

import (
	"context"
	"database/sql"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

type AccountTransactionRepository struct {
	db *sql.DB
	tx *sql.Tx
}

func NewAccountTransactionRepository(db *sql.DB) *AccountTransactionRepository {
	return &AccountTransactionRepository{db: db}
}

func NewAccountTransactionRepositoryWithTx(tx *sql.Tx) *AccountTransactionRepository {
	return &AccountTransactionRepository{tx: tx}
}

func (r *AccountTransactionRepository) getExecutor() executor {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

func (r *AccountTransactionRepository) GetAccountTransactionsMinLedgerSeq(ctx context.Context) (*relationaldb.LedgerIndex, error) {
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

func (r *AccountTransactionRepository) GetAccountTransactionCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.getExecutor().QueryRowContext(ctx, "SELECT COUNT(*) FROM account_transactions").Scan(&count)
	if err != nil {
		return 0, relationaldb.NewQueryError("get_account_transaction_count", "failed to count account transactions", err)
	}
	return count, nil
}

func (r *AccountTransactionRepository) queryAccountTxs(ctx context.Context, opName string, options relationaldb.AccountTxOptions, orderDir string) ([]relationaldb.TransactionInfo, error) {
	query := `SELECT t.trans_id, t.ledger_seq, t.status, t.raw_txn, t.txn_meta, at.txn_seq
			  FROM account_transactions at
			  INNER JOIN transactions t ON t.trans_id = at.trans_id
			  WHERE at.account = ?`

	args := []interface{}{options.Account.String()}

	if options.MinLedger > 0 {
		query += " AND at.ledger_seq >= ?"
		args = append(args, options.MinLedger)
	}
	if options.MaxLedger > 0 {
		query += " AND at.ledger_seq <= ?"
		args = append(args, options.MaxLedger)
	}

	query += " ORDER BY at.ledger_seq " + orderDir + ", at.txn_seq " + orderDir

	if !options.Unlimited && options.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, options.Limit)
		if options.Offset > 0 {
			query += " OFFSET ?"
			args = append(args, options.Offset)
		}
	}

	rows, err := r.getExecutor().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, relationaldb.NewQueryError(opName, "failed to query account transactions", err)
	}
	defer rows.Close()

	var results []relationaldb.TransactionInfo
	for rows.Next() {
		var info relationaldb.TransactionInfo
		var hashBytes, txnMeta []byte

		if err := rows.Scan(&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta, &info.TxnSeq); err != nil {
			return nil, relationaldb.NewQueryError(opName, "failed to scan row", err)
		}
		copy(info.Hash[:], hashBytes)
		copy(info.Account[:], options.Account[:])
		info.TxnMeta = txnMeta
		results = append(results, info)
	}
	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError(opName, "error iterating rows", err)
	}
	return results, nil
}

func (r *AccountTransactionRepository) GetOldestAccountTxs(ctx context.Context, options relationaldb.AccountTxOptions) ([]relationaldb.TransactionInfo, error) {
	return r.queryAccountTxs(ctx, "get_oldest_account_txs", options, "ASC")
}

func (r *AccountTransactionRepository) GetNewestAccountTxs(ctx context.Context, options relationaldb.AccountTxOptions) ([]relationaldb.TransactionInfo, error) {
	return r.queryAccountTxs(ctx, "get_newest_account_txs", options, "DESC")
}

func (r *AccountTransactionRepository) queryAccountTxsPage(ctx context.Context, opName string, options relationaldb.AccountTxPageOptions, orderDir string, markerCmp string) (*relationaldb.AccountTxResult, error) {
	query := `SELECT t.trans_id, t.ledger_seq, t.status, t.raw_txn, t.txn_meta, at.txn_seq
			  FROM account_transactions at
			  INNER JOIN transactions t ON t.trans_id = at.trans_id
			  WHERE at.account = ?`

	args := []interface{}{options.Account.String()}

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
	limit := options.Limit + 1
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
		copy(info.Hash[:], hashBytes)
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

	if len(transactions) > int(options.Limit) {
		transactions = transactions[:options.Limit]
		lastTx := transactions[len(transactions)-1]
		result.Marker = &relationaldb.AccountTxMarker{
			LedgerSeq: lastTx.LedgerSeq,
			TxnSeq:    lastTx.TxnSeq,
		}
	}

	result.Transactions = transactions
	return result, nil
}

func (r *AccountTransactionRepository) GetOldestAccountTxsPage(ctx context.Context, options relationaldb.AccountTxPageOptions) (*relationaldb.AccountTxResult, error) {
	return r.queryAccountTxsPage(ctx, "get_oldest_account_txs_page", options, "ASC", ">")
}

func (r *AccountTransactionRepository) GetNewestAccountTxsPage(ctx context.Context, options relationaldb.AccountTxPageOptions) (*relationaldb.AccountTxResult, error) {
	return r.queryAccountTxsPage(ctx, "get_newest_account_txs_page", options, "DESC", "<")
}

func (r *AccountTransactionRepository) SaveAccountTransaction(ctx context.Context, accountID relationaldb.AccountID, txInfo *relationaldb.TransactionInfo) error {
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

func (r *AccountTransactionRepository) DeleteAccountTransactionsBeforeLedgerSeq(ctx context.Context, ledgerSeq relationaldb.LedgerIndex) error {
	_, err := r.getExecutor().ExecContext(ctx, "DELETE FROM account_transactions WHERE ledger_seq < ?", ledgerSeq)
	if err != nil {
		return relationaldb.NewQueryError("delete_account_transactions_before_ledger_seq", "failed to delete account transactions", err)
	}
	return nil
}
