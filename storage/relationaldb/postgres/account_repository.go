package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// AccountTransactionRepository implements the AccountTransactionRepository interface for PostgreSQL
type AccountTransactionRepository struct {
	db *sql.DB
	tx *sql.Tx // Optional transaction context
}

// NewAccountTransactionRepository creates a new PostgreSQL account transaction repository
func NewAccountTransactionRepository(db *sql.DB) *AccountTransactionRepository {
	return &AccountTransactionRepository{db: db}
}

// NewAccountTransactionRepositoryWithTx creates a new PostgreSQL account transaction repository within a transaction
func NewAccountTransactionRepositoryWithTx(tx *sql.Tx) *AccountTransactionRepository {
	return &AccountTransactionRepository{tx: tx}
}

// getExecutor returns the appropriate executor (db or tx)
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

func (r *AccountTransactionRepository) GetOldestAccountTxs(ctx context.Context, options relationaldb.AccountTxOptions) ([]relationaldb.TransactionInfo, error) {
	// Build query based on rippled's getOldestAccountTxs logic
	query := `SELECT t.trans_id, t.ledger_seq, t.status, t.raw_txn, t.txn_meta, at.txn_seq
			  FROM account_transactions at
			  INNER JOIN transactions t ON t.trans_id = at.trans_id
			  WHERE at.account = $1`

	args := []any{options.Account.String()}
	argCount := 1

	if options.MinLedger > 0 {
		argCount++
		query += fmt.Sprintf(" AND at.ledger_seq >= $%d", argCount)
		args = append(args, options.MinLedger)
	}

	if options.MaxLedger > 0 {
		argCount++
		query += fmt.Sprintf(" AND at.ledger_seq <= $%d", argCount)
		args = append(args, options.MaxLedger)
	}

	query += " ORDER BY at.ledger_seq ASC, at.txn_seq ASC"

	if !options.Unlimited && options.Limit > 0 {
		argCount++
		query += fmt.Sprintf(" LIMIT $%d", argCount)
		args = append(args, options.Limit)

		if options.Offset > 0 {
			argCount++
			query += fmt.Sprintf(" OFFSET $%d", argCount)
			args = append(args, options.Offset)
		}
	}

	rows, err := r.getExecutor().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_oldest_account_txs", "failed to query account transactions", err)
	}
	defer rows.Close()

	var results []relationaldb.TransactionInfo

	for rows.Next() {
		var info relationaldb.TransactionInfo
		var hashBytes []byte
		var txnMeta sql.NullString

		if err := rows.Scan(&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta, &info.TxnSeq); err != nil {
			return nil, relationaldb.NewQueryError("get_oldest_account_txs", "failed to scan row", err)
		}

		copy(info.Hash[:], hashBytes)
		copy(info.Account[:], options.Account[:])
		if txnMeta.Valid {
			info.TxnMeta = []byte(txnMeta.String)
		}
		results = append(results, info)
	}

	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("get_oldest_account_txs", "error iterating rows", err)
	}

	return results, nil
}

func (r *AccountTransactionRepository) GetNewestAccountTxs(ctx context.Context, options relationaldb.AccountTxOptions) ([]relationaldb.TransactionInfo, error) {
	// Same as GetOldestAccountTxs but with DESC order
	query := `SELECT t.trans_id, t.ledger_seq, t.status, t.raw_txn, t.txn_meta, at.txn_seq
			  FROM account_transactions at
			  INNER JOIN transactions t ON t.trans_id = at.trans_id
			  WHERE at.account = $1`

	args := []any{options.Account.String()}
	argCount := 1

	if options.MinLedger > 0 {
		argCount++
		query += fmt.Sprintf(" AND at.ledger_seq >= $%d", argCount)
		args = append(args, options.MinLedger)
	}

	if options.MaxLedger > 0 {
		argCount++
		query += fmt.Sprintf(" AND at.ledger_seq <= $%d", argCount)
		args = append(args, options.MaxLedger)
	}

	query += " ORDER BY at.ledger_seq DESC, at.txn_seq DESC"

	if !options.Unlimited && options.Limit > 0 {
		argCount++
		query += fmt.Sprintf(" LIMIT $%d", argCount)
		args = append(args, options.Limit)

		if options.Offset > 0 {
			argCount++
			query += fmt.Sprintf(" OFFSET $%d", argCount)
			args = append(args, options.Offset)
		}
	}

	rows, err := r.getExecutor().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_newest_account_txs", "failed to query account transactions", err)
	}
	defer rows.Close()

	var results []relationaldb.TransactionInfo

	for rows.Next() {
		var info relationaldb.TransactionInfo
		var hashBytes []byte
		var txnMeta sql.NullString

		if err := rows.Scan(&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta, &info.TxnSeq); err != nil {
			return nil, relationaldb.NewQueryError("get_newest_account_txs", "failed to scan row", err)
		}

		copy(info.Hash[:], hashBytes)
		copy(info.Account[:], options.Account[:])
		if txnMeta.Valid {
			info.TxnMeta = []byte(txnMeta.String)
		}
		results = append(results, info)
	}

	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("get_newest_account_txs", "error iterating rows", err)
	}

	return results, nil
}

func (r *AccountTransactionRepository) GetOldestAccountTxsPage(ctx context.Context, options relationaldb.AccountTxPageOptions) (*relationaldb.AccountTxResult, error) {
	// Build paginated query with marker support (based on rippled's implementation)
	query := `SELECT t.trans_id, t.ledger_seq, t.status, t.raw_txn, t.txn_meta, at.txn_seq
			  FROM account_transactions at
			  INNER JOIN transactions t ON t.trans_id = at.trans_id
			  WHERE at.account = $1`

	args := []any{options.Account.String()}
	argCount := 1

	if options.MinLedger > 0 {
		argCount++
		query += fmt.Sprintf(" AND at.ledger_seq >= $%d", argCount)
		args = append(args, options.MinLedger)
	}

	if options.MaxLedger > 0 {
		argCount++
		query += fmt.Sprintf(" AND at.ledger_seq <= $%d", argCount)
		args = append(args, options.MaxLedger)
	}

	// Add marker-based pagination
	if options.Marker != nil {
		argCount++
		query += fmt.Sprintf(" AND (at.ledger_seq > $%d OR (at.ledger_seq = $%d AND at.txn_seq > $%d))",
			argCount, argCount, argCount+1)
		args = append(args, options.Marker.LedgerSeq, options.Marker.TxnSeq)
		argCount++
	}

	query += " ORDER BY at.ledger_seq ASC, at.txn_seq ASC"

	// Fetch one extra to determine if there are more results
	limit := options.Limit + 1
	argCount++
	query += fmt.Sprintf(" LIMIT $%d", argCount)
	args = append(args, limit)

	rows, err := r.getExecutor().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_oldest_account_txs_page", "failed to query account transactions", err)
	}
	defer rows.Close()

	var transactions []relationaldb.TransactionInfo

	for rows.Next() {
		var info relationaldb.TransactionInfo
		var hashBytes []byte
		var txnMeta sql.NullString

		if err := rows.Scan(&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta, &info.TxnSeq); err != nil {
			return nil, relationaldb.NewQueryError("get_oldest_account_txs_page", "failed to scan row", err)
		}

		copy(info.Hash[:], hashBytes)
		copy(info.Account[:], options.Account[:])
		if txnMeta.Valid {
			info.TxnMeta = []byte(txnMeta.String)
		}
		transactions = append(transactions, info)
	}

	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("get_oldest_account_txs_page", "error iterating rows", err)
	}

	result := &relationaldb.AccountTxResult{
		LedgerRange: relationaldb.LedgerRange{
			Min: options.MinLedger,
			Max: options.MaxLedger,
		},
		Limit: options.Limit,
	}

	// Check if there are more results
	if len(transactions) > int(options.Limit) {
		// Remove the extra transaction and set marker
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

func (r *AccountTransactionRepository) GetNewestAccountTxsPage(ctx context.Context, options relationaldb.AccountTxPageOptions) (*relationaldb.AccountTxResult, error) {
	// Similar to GetOldestAccountTxsPage but with DESC order and reverse marker logic
	query := `SELECT t.trans_id, t.ledger_seq, t.status, t.raw_txn, t.txn_meta, at.txn_seq
			  FROM account_transactions at
			  INNER JOIN transactions t ON t.trans_id = at.trans_id
			  WHERE at.account = $1`

	args := []any{options.Account.String()}
	argCount := 1

	if options.MinLedger > 0 {
		argCount++
		query += fmt.Sprintf(" AND at.ledger_seq >= $%d", argCount)
		args = append(args, options.MinLedger)
	}

	if options.MaxLedger > 0 {
		argCount++
		query += fmt.Sprintf(" AND at.ledger_seq <= $%d", argCount)
		args = append(args, options.MaxLedger)
	}

	// Add marker-based pagination (reverse logic for DESC order)
	if options.Marker != nil {
		argCount++
		query += fmt.Sprintf(" AND (at.ledger_seq < $%d OR (at.ledger_seq = $%d AND at.txn_seq < $%d))",
			argCount, argCount, argCount+1)
		args = append(args, options.Marker.LedgerSeq, options.Marker.TxnSeq)
		argCount++
	}

	query += " ORDER BY at.ledger_seq DESC, at.txn_seq DESC"

	// Fetch one extra to determine if there are more results
	limit := options.Limit + 1
	argCount++
	query += fmt.Sprintf(" LIMIT $%d", argCount)
	args = append(args, limit)

	rows, err := r.getExecutor().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_newest_account_txs_page", "failed to query account transactions", err)
	}
	defer rows.Close()

	var transactions []relationaldb.TransactionInfo

	for rows.Next() {
		var info relationaldb.TransactionInfo
		var hashBytes []byte
		var txnMeta sql.NullString

		if err := rows.Scan(&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta, &info.TxnSeq); err != nil {
			return nil, relationaldb.NewQueryError("get_newest_account_txs_page", "failed to scan row", err)
		}

		copy(info.Hash[:], hashBytes)
		copy(info.Account[:], options.Account[:])
		if txnMeta.Valid {
			info.TxnMeta = []byte(txnMeta.String)
		}
		transactions = append(transactions, info)
	}

	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("get_newest_account_txs_page", "error iterating rows", err)
	}

	result := &relationaldb.AccountTxResult{
		LedgerRange: relationaldb.LedgerRange{
			Min: options.MinLedger,
			Max: options.MaxLedger,
		},
		Limit: options.Limit,
	}

	// Check if there are more results
	if len(transactions) > int(options.Limit) {
		// Remove the extra transaction and set marker
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

func (r *AccountTransactionRepository) SaveAccountTransaction(ctx context.Context, accountID relationaldb.AccountID, txInfo *relationaldb.TransactionInfo) error {
	query := `INSERT INTO account_transactions (trans_id, account, ledger_seq, txn_seq)
			  VALUES ($1, $2, $3, $4)
			  ON CONFLICT (trans_id, account) DO UPDATE SET
			  ledger_seq = EXCLUDED.ledger_seq,
			  txn_seq = EXCLUDED.txn_seq`

	_, err := r.getExecutor().ExecContext(ctx, query,
		txInfo.Hash[:], accountID.String(), txInfo.LedgerSeq, txInfo.TxnSeq)

	if err != nil {
		return relationaldb.NewQueryError("save_account_transaction", "failed to save account transaction", err)
	}

	return nil
}

func (r *AccountTransactionRepository) DeleteAccountTransactionsBeforeLedgerSeq(ctx context.Context, ledgerSeq relationaldb.LedgerIndex) error {
	_, err := r.getExecutor().ExecContext(ctx, "DELETE FROM account_transactions WHERE ledger_seq < $1", ledgerSeq)
	if err != nil {
		return relationaldb.NewQueryError("delete_account_transactions_before_ledger_seq", "failed to delete account transactions", err)
	}

	return nil
}
