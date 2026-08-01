package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/sqlutil"
)

// ledgerRepository implements the ledgerRepository interface for PostgreSQL
type ledgerRepository struct {
	executor executor
}

// newLedgerRepository creates a new PostgreSQL ledger repository
func newLedgerRepository(executor executor) *ledgerRepository {
	return &ledgerRepository{executor: executor}
}

// getExecutor returns the appropriate executor (db or tx)
func (r *ledgerRepository) getExecutor() executor {
	return r.executor
}

const ledgerSelectCols = `ledger_hash, ledger_seq, prev_hash, account_set_hash, trans_set_hash,
	total_coins, closing_time, prev_closing_time, close_time_res, close_flags`

// scanLedgerInfo scans one ledgers row in ledgerSelectCols order. total_coins
// is a DECIMAL scanned as a string; a malformed value is a returned error,
// not a silent zero.
func scanLedgerInfo(row relationaldb.RowScanner) (*relationaldb.LedgerInfo, error) {
	var info relationaldb.LedgerInfo
	var hashBytes, parentHashBytes, accountHashBytes, txHashBytes []byte
	var totalCoinsStr string
	var closingTime, prevClosingTime int64

	err := row.Scan(
		&hashBytes, &info.Sequence, &parentHashBytes, &accountHashBytes, &txHashBytes,
		&totalCoinsStr, &closingTime, &prevClosingTime, &info.CloseTimeRes, &info.CloseFlags)
	if err != nil {
		return nil, err
	}

	if err := sqlutil.CopyExact(info.Hash[:], hashBytes, "ledger_hash"); err != nil {
		return nil, err
	}
	if err := sqlutil.CopyExact(info.ParentHash[:], parentHashBytes, "prev_hash"); err != nil {
		return nil, err
	}
	if err := sqlutil.CopyExact(info.AccountHash[:], accountHashBytes, "account_set_hash"); err != nil {
		return nil, err
	}
	if err := sqlutil.CopyExact(info.TransactionHash[:], txHashBytes, "trans_set_hash"); err != nil {
		return nil, err
	}

	totalCoins, err := strconv.ParseInt(totalCoinsStr, 10, 64)
	if err != nil {
		return nil, relationaldb.NewDataError("scan_ledger_info", "malformed total_coins value", err)
	}
	info.TotalCoins = relationaldb.Amount(totalCoins)

	// Convert rippled time format (seconds since 2000-01-01) to Go time
	info.CloseTime = time.Unix(closingTime+protocol.RippleEpochUnix, 0).UTC()
	info.ParentCloseTime = time.Unix(prevClosingTime+protocol.RippleEpochUnix, 0).UTC()

	return &info, nil
}

// GetMinLedgerSeq returns the lowest ledger sequence stored, or nil if none.
func (r *ledgerRepository) GetMinLedgerSeq(ctx context.Context) (*relationaldb.LedgerIndex, error) {
	var seq sql.NullInt64
	err := r.getExecutor().QueryRowContext(ctx, "SELECT MIN(ledger_seq) FROM ledgers").Scan(&seq)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_min_ledger_seq", "failed to query min ledger sequence", err)
	}

	if !seq.Valid {
		return nil, nil
	}

	result := relationaldb.LedgerIndex(seq.Int64)
	return &result, nil
}

// GetMaxLedgerSeq returns the highest ledger sequence stored, or nil if none.
func (r *ledgerRepository) GetMaxLedgerSeq(ctx context.Context) (*relationaldb.LedgerIndex, error) {
	var seq sql.NullInt64
	err := r.getExecutor().QueryRowContext(ctx, "SELECT MAX(ledger_seq) FROM ledgers").Scan(&seq)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_max_ledger_seq", "failed to query max ledger sequence", err)
	}

	if !seq.Valid {
		return nil, nil
	}

	result := relationaldb.LedgerIndex(seq.Int64)
	return &result, nil
}

// GetLedgerInfoBySeq returns the ledger header for the given sequence.
func (r *ledgerRepository) GetLedgerInfoBySeq(ctx context.Context, seq relationaldb.LedgerIndex) (*relationaldb.LedgerInfo, error) {
	query := `SELECT ` + ledgerSelectCols + ` FROM ledgers WHERE ledger_seq = $1`
	row := r.getExecutor().QueryRowContext(ctx, query, seq)
	info, err := scanLedgerInfo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, relationaldb.NewDataError("get_ledger_info_by_seq", "ledger not found", relationaldb.ErrLedgerNotFound)
	}
	if err != nil {
		return nil, relationaldb.NewQueryError("get_ledger_info_by_seq", "failed to query ledger", err)
	}
	return info, nil
}

// GetLedgerInfoByHash returns the ledger header for the given ledger hash.
func (r *ledgerRepository) GetLedgerInfoByHash(ctx context.Context, hash relationaldb.Hash) (*relationaldb.LedgerInfo, error) {
	query := `SELECT ` + ledgerSelectCols + ` FROM ledgers WHERE ledger_hash = $1`
	row := r.getExecutor().QueryRowContext(ctx, query, hash[:])
	info, err := scanLedgerInfo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, relationaldb.NewDataError("get_ledger_info_by_hash", "ledger not found", relationaldb.ErrLedgerNotFound)
	}
	if err != nil {
		return nil, relationaldb.NewQueryError("get_ledger_info_by_hash", "failed to query ledger", err)
	}
	return info, nil
}

// GetNewestLedgerInfo returns the most recent ledger header, or nil if none.
func (r *ledgerRepository) GetNewestLedgerInfo(ctx context.Context) (*relationaldb.LedgerInfo, error) {
	query := `SELECT ` + ledgerSelectCols + ` FROM ledgers ORDER BY ledger_seq DESC LIMIT 1`
	row := r.getExecutor().QueryRowContext(ctx, query)
	info, err := scanLedgerInfo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, relationaldb.NewQueryError("get_newest_ledger_info", "failed to query newest ledger", err)
	}
	return info, nil
}

// GetHashesByRange returns the ledger and parent hashes for every sequence in
// [minSeq, maxSeq], keyed by sequence.
func (r *ledgerRepository) GetHashesByRange(ctx context.Context, minSeq, maxSeq relationaldb.LedgerIndex) (map[relationaldb.LedgerIndex]relationaldb.LedgerHashPair, error) {
	query := `SELECT ledger_seq, ledger_hash, prev_hash FROM ledgers
			  WHERE ledger_seq >= $1 AND ledger_seq <= $2 ORDER BY ledger_seq`

	rows, err := r.getExecutor().QueryContext(ctx, query, minSeq, maxSeq)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_hashes_by_range", "failed to query ledger hashes", err)
	}
	defer rows.Close()

	result := make(map[relationaldb.LedgerIndex]relationaldb.LedgerHashPair)

	for rows.Next() {
		var seq relationaldb.LedgerIndex
		var ledgerHashBytes, parentHashBytes []byte

		if err := rows.Scan(&seq, &ledgerHashBytes, &parentHashBytes); err != nil {
			return nil, relationaldb.NewQueryError("get_hashes_by_range", "failed to scan row", err)
		}

		var pair relationaldb.LedgerHashPair
		if err := sqlutil.CopyExact(pair.LedgerHash[:], ledgerHashBytes, "ledger_hash"); err != nil {
			return nil, relationaldb.NewDataError("get_hashes_by_range", "malformed ledger hash", err)
		}
		if err := sqlutil.CopyExact(pair.ParentHash[:], parentHashBytes, "prev_hash"); err != nil {
			return nil, relationaldb.NewDataError("get_hashes_by_range", "malformed parent hash", err)
		}
		result[seq] = pair
	}

	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("get_hashes_by_range", "error iterating rows", err)
	}

	return result, nil
}

// SaveValidatedLedger inserts or updates a validated ledger header (upsert on ledger_seq).
func (r *ledgerRepository) SaveValidatedLedger(ctx context.Context, ledger relationaldb.LedgerInfo) error {
	// Convert Go time back to rippled format (seconds since 2000-01-01)
	closingTime := protocol.RippleSeconds(ledger.CloseTime)
	prevClosingTime := protocol.RippleSeconds(ledger.ParentCloseTime)

	query := `INSERT INTO ledgers (ledger_hash, ledger_seq, prev_hash, account_set_hash, trans_set_hash,
			  total_coins, closing_time, prev_closing_time, close_time_res, close_flags)
			  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			  ON CONFLICT (ledger_seq) DO UPDATE SET
			  ledger_hash = EXCLUDED.ledger_hash,
			  prev_hash = EXCLUDED.prev_hash,
			  account_set_hash = EXCLUDED.account_set_hash,
			  trans_set_hash = EXCLUDED.trans_set_hash,
			  total_coins = EXCLUDED.total_coins,
			  closing_time = EXCLUDED.closing_time,
			  prev_closing_time = EXCLUDED.prev_closing_time,
			  close_time_res = EXCLUDED.close_time_res,
			  close_flags = EXCLUDED.close_flags`

	_, err := r.getExecutor().ExecContext(ctx, query,
		ledger.Hash[:], ledger.Sequence, ledger.ParentHash[:], ledger.AccountHash[:], ledger.TransactionHash[:],
		strconv.FormatInt(int64(ledger.TotalCoins), 10), closingTime, prevClosingTime, ledger.CloseTimeRes, ledger.CloseFlags)

	if err != nil {
		return relationaldb.NewQueryError("save_validated_ledger", "failed to save ledger", err)
	}

	return nil
}

// DeleteLedgersBySeq deletes all ledgers at or below maxSeq.
func (r *ledgerRepository) DeleteLedgersBySeq(ctx context.Context, maxSeq relationaldb.LedgerIndex) error {
	_, err := r.getExecutor().ExecContext(ctx, "DELETE FROM ledgers WHERE ledger_seq <= $1", maxSeq)
	if err != nil {
		return relationaldb.NewQueryError("delete_ledgers_by_seq", "failed to delete ledgers", err)
	}

	return nil
}
