package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/sqlutil"
)

type ledgerRepository struct {
	db executor
}

// newLedgerRepository creates a SQLite ledger repository.
func newLedgerRepository(db executor) *ledgerRepository {
	return &ledgerRepository{db: db}
}

// GetMinLedgerSeq returns the lowest ledger sequence stored, or nil if none.
func (r *ledgerRepository) GetMinLedgerSeq(ctx context.Context) (*relationaldb.LedgerIndex, error) {
	var seq sql.NullInt64
	err := r.db.QueryRowContext(ctx, "SELECT MIN(ledger_seq) FROM ledgers").Scan(&seq)
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
	err := r.db.QueryRowContext(ctx, "SELECT MAX(ledger_seq) FROM ledgers").Scan(&seq)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_max_ledger_seq", "failed to query max ledger sequence", err)
	}
	if !seq.Valid {
		return nil, nil
	}
	result := relationaldb.LedgerIndex(seq.Int64)
	return &result, nil
}

func (r *ledgerRepository) scanLedgerInfo(row relationaldb.RowScanner) (*relationaldb.LedgerInfo, error) {
	var info relationaldb.LedgerInfo
	var hashBytes, parentHashBytes, accountHashBytes, txHashBytes []byte
	var totalCoins int64
	var closingTime, prevClosingTime int64

	err := row.Scan(
		&hashBytes, &info.Sequence, &parentHashBytes, &accountHashBytes, &txHashBytes,
		&totalCoins, &closingTime, &prevClosingTime, &info.CloseTimeRes, &info.CloseFlags)
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
	info.TotalCoins = relationaldb.Amount(totalCoins)
	info.CloseTime = time.Unix(closingTime+protocol.RippleEpochUnix, 0).UTC()
	info.ParentCloseTime = time.Unix(prevClosingTime+protocol.RippleEpochUnix, 0).UTC()

	return &info, nil
}

const ledgerSelectCols = `ledger_hash, ledger_seq, prev_hash, account_set_hash, trans_set_hash,
	total_coins, closing_time, prev_closing_time, close_time_res, close_flags`

// GetLedgerInfoBySeq returns the ledger header for the given sequence.
func (r *ledgerRepository) GetLedgerInfoBySeq(ctx context.Context, seq relationaldb.LedgerIndex) (*relationaldb.LedgerInfo, error) {
	query := `SELECT ` + ledgerSelectCols + ` FROM ledgers WHERE ledger_seq = ?`
	row := r.db.QueryRowContext(ctx, query, seq)
	info, err := r.scanLedgerInfo(row)
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
	query := `SELECT ` + ledgerSelectCols + ` FROM ledgers WHERE ledger_hash = ?`
	row := r.db.QueryRowContext(ctx, query, hash[:])
	info, err := r.scanLedgerInfo(row)
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
	row := r.db.QueryRowContext(ctx, query)
	info, err := r.scanLedgerInfo(row)
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
			  WHERE ledger_seq >= ? AND ledger_seq <= ? ORDER BY ledger_seq`

	rows, err := r.db.QueryContext(ctx, query, minSeq, maxSeq)
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
	closingTime := protocol.RippleSeconds(ledger.CloseTime)
	prevClosingTime := protocol.RippleSeconds(ledger.ParentCloseTime)

	query := `INSERT INTO ledgers (ledger_hash, ledger_seq, prev_hash, account_set_hash, trans_set_hash,
			  total_coins, closing_time, prev_closing_time, close_time_res, close_flags)
			  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			  ON CONFLICT (ledger_seq) DO UPDATE SET
			  ledger_hash = excluded.ledger_hash,
			  prev_hash = excluded.prev_hash,
			  account_set_hash = excluded.account_set_hash,
			  trans_set_hash = excluded.trans_set_hash,
			  total_coins = excluded.total_coins,
			  closing_time = excluded.closing_time,
			  prev_closing_time = excluded.prev_closing_time,
			  close_time_res = excluded.close_time_res,
			  close_flags = excluded.close_flags`

	_, err := r.db.ExecContext(ctx, query,
		ledger.Hash[:], ledger.Sequence, ledger.ParentHash[:], ledger.AccountHash[:], ledger.TransactionHash[:],
		int64(ledger.TotalCoins), closingTime, prevClosingTime, ledger.CloseTimeRes, ledger.CloseFlags)
	if err != nil {
		return relationaldb.NewQueryError("save_validated_ledger", "failed to save ledger", err)
	}
	return nil
}

func (r *ledgerRepository) deleteLedgerBySequence(ctx context.Context, seq relationaldb.LedgerIndex) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM ledgers WHERE ledger_seq = ?", seq); err != nil {
		return relationaldb.NewQueryError("unpublish_ledger", "failed to unpublish ledger", err)
	}
	return nil
}

// DeleteLedgersBySeq deletes all ledgers at or below maxSeq.
func (r *ledgerRepository) DeleteLedgersBySeq(ctx context.Context, maxSeq relationaldb.LedgerIndex) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM ledgers WHERE ledger_seq <= ?", maxSeq)
	if err != nil {
		return relationaldb.NewQueryError("delete_ledgers_by_seq", "failed to delete ledgers", err)
	}
	return nil
}
