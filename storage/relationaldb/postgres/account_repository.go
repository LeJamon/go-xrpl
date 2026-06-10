package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
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

// GetAccountTransactionsMinLedgerSeq returns the lowest ledger sequence present
// in the account-transactions index, or nil if it is empty.
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

// GetAccountTransactionCount returns the number of rows in the account-transactions index.
func (r *AccountTransactionRepository) GetAccountTransactionCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.getExecutor().QueryRowContext(ctx, "SELECT COUNT(*) FROM account_transactions").Scan(&count)
	if err != nil {
		return 0, relationaldb.NewQueryError("get_account_transaction_count", "failed to count account transactions", err)
	}

	return count, nil
}

const accountTxSelect = `SELECT t.trans_id, t.ledger_seq, t.status, t.raw_txn, t.txn_meta, at.txn_seq
		  FROM account_transactions at
		  INNER JOIN transactions t ON t.trans_id = at.trans_id
		  WHERE at.account = $1`

// scanAccountTxRows drains rows into TransactionInfo values, stamping each
// with the queried account.
func scanAccountTxRows(rows *sql.Rows, opName string, account relationaldb.AccountID) ([]relationaldb.TransactionInfo, error) {
	var results []relationaldb.TransactionInfo

	for rows.Next() {
		var info relationaldb.TransactionInfo
		var hashBytes []byte
		var txnMeta sql.NullString

		if err := rows.Scan(&hashBytes, &info.LedgerSeq, &info.Status, &info.RawTxn, &txnMeta, &info.TxnSeq); err != nil {
			return nil, relationaldb.NewQueryError(opName, "failed to scan row", err)
		}

		copy(info.Hash[:], hashBytes)
		copy(info.Account[:], account[:])
		if txnMeta.Valid {
			info.TxnMeta = []byte(txnMeta.String)
		}
		results = append(results, info)
	}

	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError(opName, "error iterating rows", err)
	}
	return results, nil
}

func (r *AccountTransactionRepository) queryAccountTxs(ctx context.Context, opName string, options relationaldb.AccountTxOptions, orderDir string) ([]relationaldb.TransactionInfo, error) {
	query := accountTxSelect
	args := []any{options.Account.String()}

	if options.MinLedger > 0 {
		args = append(args, options.MinLedger)
		query += fmt.Sprintf(" AND at.ledger_seq >= $%d", len(args))
	}

	if options.MaxLedger > 0 {
		args = append(args, options.MaxLedger)
		query += fmt.Sprintf(" AND at.ledger_seq <= $%d", len(args))
	}

	query += " ORDER BY at.ledger_seq " + orderDir + ", at.txn_seq " + orderDir

	if !options.Unlimited && options.Limit > 0 {
		args = append(args, options.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))

		if options.Offset > 0 {
			args = append(args, options.Offset)
			query += fmt.Sprintf(" OFFSET $%d", len(args))
		}
	}

	rows, err := r.getExecutor().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, relationaldb.NewQueryError(opName, "failed to query account transactions", err)
	}
	defer rows.Close()

	return scanAccountTxRows(rows, opName, options.Account)
}

// GetOldestAccountTxs returns an account's transactions oldest-first, filtered by options.
func (r *AccountTransactionRepository) GetOldestAccountTxs(ctx context.Context, options relationaldb.AccountTxOptions) ([]relationaldb.TransactionInfo, error) {
	return r.queryAccountTxs(ctx, "get_oldest_account_txs", options, "ASC")
}

// GetNewestAccountTxs returns an account's transactions newest-first, filtered by options.
func (r *AccountTransactionRepository) GetNewestAccountTxs(ctx context.Context, options relationaldb.AccountTxOptions) ([]relationaldb.TransactionInfo, error) {
	return r.queryAccountTxs(ctx, "get_newest_account_txs", options, "DESC")
}

func (r *AccountTransactionRepository) queryAccountTxsPage(ctx context.Context, opName string, options relationaldb.AccountTxPageOptions, orderDir string, markerCmp string) (*relationaldb.AccountTxResult, error) {
	query := accountTxSelect
	args := []any{options.Account.String()}

	if options.MinLedger > 0 {
		args = append(args, options.MinLedger)
		query += fmt.Sprintf(" AND at.ledger_seq >= $%d", len(args))
	}

	if options.MaxLedger > 0 {
		args = append(args, options.MaxLedger)
		query += fmt.Sprintf(" AND at.ledger_seq <= $%d", len(args))
	}

	// Marker-based pagination: for ASC pages resume after the marker (>),
	// for DESC pages resume before it (<).
	if options.Marker != nil {
		args = append(args, options.Marker.LedgerSeq, options.Marker.TxnSeq)
		seqArg, txnArg := len(args)-1, len(args)
		query += fmt.Sprintf(" AND (at.ledger_seq %s $%d OR (at.ledger_seq = $%d AND at.txn_seq %s $%d))",
			markerCmp, seqArg, seqArg, markerCmp, txnArg)
	}

	query += " ORDER BY at.ledger_seq " + orderDir + ", at.txn_seq " + orderDir

	// Fetch one extra to determine if there are more results
	args = append(args, options.Limit+1)
	query += fmt.Sprintf(" LIMIT $%d", len(args))

	rows, err := r.getExecutor().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, relationaldb.NewQueryError(opName, "failed to query account transactions", err)
	}
	defer rows.Close()

	transactions, err := scanAccountTxRows(rows, opName, options.Account)
	if err != nil {
		return nil, err
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

// GetOldestAccountTxsPage returns a marker-paginated page of an account's
// transactions, oldest-first.
func (r *AccountTransactionRepository) GetOldestAccountTxsPage(ctx context.Context, options relationaldb.AccountTxPageOptions) (*relationaldb.AccountTxResult, error) {
	return r.queryAccountTxsPage(ctx, "get_oldest_account_txs_page", options, "ASC", ">")
}

// GetNewestAccountTxsPage returns a marker-paginated page of an account's
// transactions, newest-first.
func (r *AccountTransactionRepository) GetNewestAccountTxsPage(ctx context.Context, options relationaldb.AccountTxPageOptions) (*relationaldb.AccountTxResult, error) {
	return r.queryAccountTxsPage(ctx, "get_newest_account_txs_page", options, "DESC", "<")
}

// SaveAccountTransaction inserts or updates an account-transaction index entry.
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

// DeleteAccountTransactionsBeforeLedgerSeq deletes index entries in ledgers below ledgerSeq.
func (r *AccountTransactionRepository) DeleteAccountTransactionsBeforeLedgerSeq(ctx context.Context, ledgerSeq relationaldb.LedgerIndex) error {
	_, err := r.getExecutor().ExecContext(ctx, "DELETE FROM account_transactions WHERE ledger_seq < $1", ledgerSeq)
	if err != nil {
		return relationaldb.NewQueryError("delete_account_transactions_before_ledger_seq", "failed to delete account transactions", err)
	}

	return nil
}
