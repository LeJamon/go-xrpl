package postgres

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// LedgerRepository implements the LedgerRepository interface for PostgreSQL
type LedgerRepository struct {
	db *sql.DB
	tx *sql.Tx // Optional transaction context
}

// NewLedgerRepository creates a new PostgreSQL ledger repository
func NewLedgerRepository(db *sql.DB) *LedgerRepository {
	return &LedgerRepository{db: db}
}

// NewLedgerRepositoryWithTx creates a new PostgreSQL ledger repository within a transaction
func NewLedgerRepositoryWithTx(tx *sql.Tx) *LedgerRepository {
	return &LedgerRepository{tx: tx}
}

// getExecutor returns the appropriate executor (db or tx)
func (r *LedgerRepository) getExecutor() executor {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

func (r *LedgerRepository) GetMinLedgerSeq(ctx context.Context) (*relationaldb.LedgerIndex, error) {
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

func (r *LedgerRepository) GetMaxLedgerSeq(ctx context.Context) (*relationaldb.LedgerIndex, error) {
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

func (r *LedgerRepository) GetLedgerInfoBySeq(ctx context.Context, seq relationaldb.LedgerIndex) (*relationaldb.LedgerInfo, error) {
	query := `SELECT ledger_hash, ledger_seq, prev_hash, account_set_hash, trans_set_hash, 
			  total_coins, closing_time, prev_closing_time, close_time_res, close_flags
			  FROM ledgers WHERE ledger_seq = $1`

	var info relationaldb.LedgerInfo
	var hashBytes, parentHashBytes, accountHashBytes, txHashBytes []byte
	var totalCoinsStr string
	var closingTime, prevClosingTime int64

	err := r.getExecutor().QueryRowContext(ctx, query, seq).Scan(
		&hashBytes, &info.Sequence, &parentHashBytes, &accountHashBytes, &txHashBytes,
		&totalCoinsStr, &closingTime, &prevClosingTime, &info.CloseTimeRes, &info.CloseFlags)

	if err == sql.ErrNoRows {
		return nil, relationaldb.NewDataError("get_ledger_info_by_seq", "ledger not found", relationaldb.ErrLedgerNotFound)
	}
	if err != nil {
		return nil, relationaldb.NewQueryError("get_ledger_info_by_seq", "failed to query ledger", err)
	}

	// Convert data to proper formats
	copy(info.Hash[:], hashBytes)
	copy(info.ParentHash[:], parentHashBytes)
	copy(info.AccountHash[:], accountHashBytes)
	copy(info.TransactionHash[:], txHashBytes)

	// Parse total coins as decimal string to int64
	if totalCoins, err := strconv.ParseInt(totalCoinsStr, 10, 64); err == nil {
		info.TotalCoins = relationaldb.Amount(totalCoins)
	}

	// Convert rippled time format (seconds since 2000-01-01) to Go time
	info.CloseTime = time.Unix(closingTime+protocol.RippleEpochUnix, 0).UTC() // Add Ripple epoch offset
	info.ParentCloseTime = time.Unix(prevClosingTime+protocol.RippleEpochUnix, 0).UTC()

	return &info, nil
}

func (r *LedgerRepository) GetLedgerInfoByHash(ctx context.Context, hash relationaldb.Hash) (*relationaldb.LedgerInfo, error) {
	query := `SELECT ledger_hash, ledger_seq, prev_hash, account_set_hash, trans_set_hash, 
			  total_coins, closing_time, prev_closing_time, close_time_res, close_flags
			  FROM ledgers WHERE ledger_hash = $1`

	var info relationaldb.LedgerInfo
	var hashBytes, parentHashBytes, accountHashBytes, txHashBytes []byte
	var totalCoinsStr string
	var closingTime, prevClosingTime int64

	err := r.getExecutor().QueryRowContext(ctx, query, hash[:]).Scan(
		&hashBytes, &info.Sequence, &parentHashBytes, &accountHashBytes, &txHashBytes,
		&totalCoinsStr, &closingTime, &prevClosingTime, &info.CloseTimeRes, &info.CloseFlags)

	if err == sql.ErrNoRows {
		return nil, relationaldb.NewDataError("get_ledger_info_by_hash", "ledger not found", relationaldb.ErrLedgerNotFound)
	}
	if err != nil {
		return nil, relationaldb.NewQueryError("get_ledger_info_by_hash", "failed to query ledger", err)
	}

	// Convert data (same as GetLedgerInfoBySeq)
	copy(info.Hash[:], hashBytes)
	copy(info.ParentHash[:], parentHashBytes)
	copy(info.AccountHash[:], accountHashBytes)
	copy(info.TransactionHash[:], txHashBytes)

	if totalCoins, err := strconv.ParseInt(totalCoinsStr, 10, 64); err == nil {
		info.TotalCoins = relationaldb.Amount(totalCoins)
	}

	info.CloseTime = time.Unix(closingTime+protocol.RippleEpochUnix, 0).UTC()
	info.ParentCloseTime = time.Unix(prevClosingTime+protocol.RippleEpochUnix, 0).UTC()

	return &info, nil
}

func (r *LedgerRepository) GetNewestLedgerInfo(ctx context.Context) (*relationaldb.LedgerInfo, error) {
	query := `SELECT ledger_hash, ledger_seq, prev_hash, account_set_hash, trans_set_hash, 
			  total_coins, closing_time, prev_closing_time, close_time_res, close_flags
			  FROM ledgers ORDER BY ledger_seq DESC LIMIT 1`

	var info relationaldb.LedgerInfo
	var hashBytes, parentHashBytes, accountHashBytes, txHashBytes []byte
	var totalCoinsStr string
	var closingTime, prevClosingTime int64

	err := r.getExecutor().QueryRowContext(ctx, query).Scan(
		&hashBytes, &info.Sequence, &parentHashBytes, &accountHashBytes, &txHashBytes,
		&totalCoinsStr, &closingTime, &prevClosingTime, &info.CloseTimeRes, &info.CloseFlags)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, relationaldb.NewQueryError("get_newest_ledger_info", "failed to query newest ledger", err)
	}

	// Convert data (same as above)
	copy(info.Hash[:], hashBytes)
	copy(info.ParentHash[:], parentHashBytes)
	copy(info.AccountHash[:], accountHashBytes)
	copy(info.TransactionHash[:], txHashBytes)

	if totalCoins, err := strconv.ParseInt(totalCoinsStr, 10, 64); err == nil {
		info.TotalCoins = relationaldb.Amount(totalCoins)
	}

	info.CloseTime = time.Unix(closingTime+protocol.RippleEpochUnix, 0).UTC()
	info.ParentCloseTime = time.Unix(prevClosingTime+protocol.RippleEpochUnix, 0).UTC()

	return &info, nil
}

func (r *LedgerRepository) GetLimitedOldestLedgerInfo(ctx context.Context, minSeq relationaldb.LedgerIndex) (*relationaldb.LedgerInfo, error) {
	query := `SELECT ledger_hash, ledger_seq, prev_hash, account_set_hash, trans_set_hash, 
			  total_coins, closing_time, prev_closing_time, close_time_res, close_flags
			  FROM ledgers WHERE ledger_seq >= $1 ORDER BY ledger_seq ASC LIMIT 1`

	var info relationaldb.LedgerInfo
	var hashBytes, parentHashBytes, accountHashBytes, txHashBytes []byte
	var totalCoinsStr string
	var closingTime, prevClosingTime int64

	err := r.getExecutor().QueryRowContext(ctx, query, minSeq).Scan(
		&hashBytes, &info.Sequence, &parentHashBytes, &accountHashBytes, &txHashBytes,
		&totalCoinsStr, &closingTime, &prevClosingTime, &info.CloseTimeRes, &info.CloseFlags)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, relationaldb.NewQueryError("get_limited_oldest_ledger_info", "failed to query oldest ledger with limit", err)
	}

	copy(info.Hash[:], hashBytes)
	copy(info.ParentHash[:], parentHashBytes)
	copy(info.AccountHash[:], accountHashBytes)
	copy(info.TransactionHash[:], txHashBytes)

	if totalCoins, err := strconv.ParseInt(totalCoinsStr, 10, 64); err == nil {
		info.TotalCoins = relationaldb.Amount(totalCoins)
	}

	info.CloseTime = time.Unix(closingTime+protocol.RippleEpochUnix, 0).UTC()
	info.ParentCloseTime = time.Unix(prevClosingTime+protocol.RippleEpochUnix, 0).UTC()

	return &info, nil
}

func (r *LedgerRepository) GetLimitedNewestLedgerInfo(ctx context.Context, minSeq relationaldb.LedgerIndex) (*relationaldb.LedgerInfo, error) {
	query := `SELECT ledger_hash, ledger_seq, prev_hash, account_set_hash, trans_set_hash, 
			  total_coins, closing_time, prev_closing_time, close_time_res, close_flags
			  FROM ledgers WHERE ledger_seq >= $1 ORDER BY ledger_seq DESC LIMIT 1`

	var info relationaldb.LedgerInfo
	var hashBytes, parentHashBytes, accountHashBytes, txHashBytes []byte
	var totalCoinsStr string
	var closingTime, prevClosingTime int64

	err := r.getExecutor().QueryRowContext(ctx, query, minSeq).Scan(
		&hashBytes, &info.Sequence, &parentHashBytes, &accountHashBytes, &txHashBytes,
		&totalCoinsStr, &closingTime, &prevClosingTime, &info.CloseTimeRes, &info.CloseFlags)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, relationaldb.NewQueryError("get_limited_newest_ledger_info", "failed to query newest ledger with limit", err)
	}

	copy(info.Hash[:], hashBytes)
	copy(info.ParentHash[:], parentHashBytes)
	copy(info.AccountHash[:], accountHashBytes)
	copy(info.TransactionHash[:], txHashBytes)

	if totalCoins, err := strconv.ParseInt(totalCoinsStr, 10, 64); err == nil {
		info.TotalCoins = relationaldb.Amount(totalCoins)
	}

	info.CloseTime = time.Unix(closingTime+protocol.RippleEpochUnix, 0).UTC()
	info.ParentCloseTime = time.Unix(prevClosingTime+protocol.RippleEpochUnix, 0).UTC()

	return &info, nil
}

func (r *LedgerRepository) GetHashByIndex(ctx context.Context, seq relationaldb.LedgerIndex) (*relationaldb.Hash, error) {
	var hashBytes []byte
	err := r.getExecutor().QueryRowContext(ctx, "SELECT ledger_hash FROM ledgers WHERE ledger_seq = $1", seq).Scan(&hashBytes)

	if err == sql.ErrNoRows {
		return nil, relationaldb.NewDataError("get_hash_by_index", "ledger not found", relationaldb.ErrLedgerNotFound)
	}
	if err != nil {
		return nil, relationaldb.NewQueryError("get_hash_by_index", "failed to query ledger hash", err)
	}

	var hash relationaldb.Hash
	copy(hash[:], hashBytes)
	return &hash, nil
}

func (r *LedgerRepository) GetHashesByIndex(ctx context.Context, seq relationaldb.LedgerIndex) (*relationaldb.LedgerHashPair, error) {
	var ledgerHashBytes, parentHashBytes []byte
	err := r.getExecutor().QueryRowContext(ctx,
		"SELECT ledger_hash, prev_hash FROM ledgers WHERE ledger_seq = $1", seq).Scan(&ledgerHashBytes, &parentHashBytes)

	if err == sql.ErrNoRows {
		return nil, relationaldb.NewDataError("get_hashes_by_index", "ledger not found", relationaldb.ErrLedgerNotFound)
	}
	if err != nil {
		return nil, relationaldb.NewQueryError("get_hashes_by_index", "failed to query ledger hashes", err)
	}

	var pair relationaldb.LedgerHashPair
	copy(pair.LedgerHash[:], ledgerHashBytes)
	copy(pair.ParentHash[:], parentHashBytes)
	return &pair, nil
}

func (r *LedgerRepository) GetHashesByRange(ctx context.Context, minSeq, maxSeq relationaldb.LedgerIndex) (map[relationaldb.LedgerIndex]relationaldb.LedgerHashPair, error) {
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
		copy(pair.LedgerHash[:], ledgerHashBytes)
		copy(pair.ParentHash[:], parentHashBytes)
		result[seq] = pair
	}

	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("get_hashes_by_range", "error iterating rows", err)
	}

	return result, nil
}

func (r *LedgerRepository) SaveValidatedLedger(ctx context.Context, ledger *relationaldb.LedgerInfo, current bool) error {
	// Convert Go time back to rippled format (seconds since 2000-01-01)
	closingTime := ledger.CloseTime.Unix() - protocol.RippleEpochUnix
	prevClosingTime := ledger.ParentCloseTime.Unix() - protocol.RippleEpochUnix

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

func (r *LedgerRepository) DeleteLedgersBySeq(ctx context.Context, maxSeq relationaldb.LedgerIndex) error {
	_, err := r.getExecutor().ExecContext(ctx, "DELETE FROM ledgers WHERE ledger_seq <= $1", maxSeq)
	if err != nil {
		return relationaldb.NewQueryError("delete_ledgers_by_seq", "failed to delete ledgers", err)
	}

	return nil
}

func (r *LedgerRepository) GetLedgerCountMinMax(ctx context.Context) (*relationaldb.CountMinMax, error) {
	var count int64
	var minSeq, maxSeq sql.NullInt64

	query := `SELECT COUNT(*), MIN(ledger_seq), MAX(ledger_seq) FROM ledgers`
	err := r.getExecutor().QueryRowContext(ctx, query).Scan(&count, &minSeq, &maxSeq)
	if err != nil {
		return nil, relationaldb.NewQueryError("get_ledger_count_min_max", "failed to query ledger statistics", err)
	}

	result := &relationaldb.CountMinMax{
		Count: count,
	}

	if minSeq.Valid {
		result.MinLedgerSeq = relationaldb.LedgerIndex(minSeq.Int64)
	}

	if maxSeq.Valid {
		result.MaxLedgerSeq = relationaldb.LedgerIndex(maxSeq.Int64)
	}

	return result, nil
}

func (r *LedgerRepository) GetKBUsedLedger(ctx context.Context) (uint32, error) {
	var size int64
	err := r.getExecutor().QueryRowContext(ctx,
		"SELECT pg_total_relation_size('ledgers')").Scan(&size)

	if err != nil {
		return 0, relationaldb.NewQueryError("get_kb_used_ledger", "failed to get ledgers table size", err)
	}

	return uint32(size / 1024), nil
}

func (r *LedgerRepository) HasLedgerSpace(ctx context.Context) (bool, error) {
	// For PostgreSQL, we'll implement a simple check
	// In production, you'd want to check actual disk space
	return true, nil
}
