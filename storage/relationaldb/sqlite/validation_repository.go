package sqlite

import (
	"context"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/sqlutil"
)

// validationRepository is the SQLite-backed on-disk validation archive.
// Schema deliberately cohabits ledger.db — validations are heavily
// joined with the Ledgers table in rippled's forensic queries (they
// mirror ledger-seq + ledger-hash), and opening a third DB file for a
// single table would bloat the file layout without any write-concurrency
// win (SQLite serializes writes across files in the same process).
type validationRepository struct {
	db       *sqlutil.DB
	executor executor
}

// Compile-time interface check.
var _ relationaldb.ValidationRepository = (*validationRepository)(nil)

// newValidationRepository creates a SQLite validation repository.
func newValidationRepository(db *sqlutil.DB) *validationRepository {
	return &validationRepository{db: db, executor: db}
}

func (r *validationRepository) getExecutor() executor {
	return r.executor
}

const validationSelectCols = `ledger_seq, initial_seq, ledger_hash, node_pubkey,
	sign_time, seen_time, flags, raw`

// Save inserts a validation record, ignoring duplicates (upsert on ledger_hash + node_pubkey).
func (r *validationRepository) Save(ctx context.Context, v *relationaldb.ValidationRecord) error {
	if v == nil {
		return relationaldb.NewDataError("validation_save", "nil record", nil)
	}
	if len(v.NodePubKey) != 33 {
		return relationaldb.NewDataError("validation_save", "node public key must be 33 bytes", nil)
	}
	_, err := r.getExecutor().ExecContext(ctx, `
		INSERT INTO validations (
			ledger_seq, initial_seq, ledger_hash, node_pubkey,
			sign_time, seen_time, flags, raw
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ledger_hash, node_pubkey) DO NOTHING
	`,
		int64(v.LedgerSeq), int64(v.InitialSeq), v.LedgerHash[:], v.NodePubKey,
		relationaldb.ToXRPLEpochSeconds(v.SignTime), relationaldb.ToXRPLEpochSeconds(v.SeenTime),
		int64(v.Flags), v.Raw,
	)
	if err != nil {
		return relationaldb.NewQueryError("validation_save", "failed to insert validation", err)
	}
	return nil
}

// SaveBatch inserts multiple validation records in a single transaction.
func (r *validationRepository) SaveBatch(ctx context.Context, vs []*relationaldb.ValidationRecord) error {
	if len(vs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return relationaldb.NewTransactionError("validation_save_batch", "failed to begin transaction", err)
	}
	txRepo := &validationRepository{executor: tx}
	for _, v := range vs {
		if err := txRepo.Save(ctx, v); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return relationaldb.NewTransactionError("validation_save_batch", "failed to commit batch", err)
	}
	return nil
}

// GetValidationsForLedger returns all validation records for the given ledger sequence.
func (r *validationRepository) GetValidationsForLedger(ctx context.Context, seq relationaldb.LedgerIndex) ([]*relationaldb.ValidationRecord, error) {
	rows, err := r.getExecutor().QueryContext(ctx,
		`SELECT `+validationSelectCols+` FROM validations WHERE ledger_seq = ?`, int64(seq))
	if err != nil {
		return nil, relationaldb.NewQueryError("validation_get_for_ledger", "failed to query validations", err)
	}
	defer rows.Close()

	var result []*relationaldb.ValidationRecord
	for rows.Next() {
		rec, err := relationaldb.ScanValidationRecord(rows)
		if err != nil {
			return nil, relationaldb.NewQueryError("validation_get_for_ledger", "failed to scan row", err)
		}
		result = append(result, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("validation_get_for_ledger", "row iteration error", err)
	}
	return result, nil
}

// GetValidationsByValidator returns a validator's validation records newest-first,
// capped at limit (0 means no limit).
func (r *validationRepository) GetValidationsByValidator(ctx context.Context, nodeKey []byte, limit int) ([]*relationaldb.ValidationRecord, error) {
	if len(nodeKey) != 33 {
		return nil, relationaldb.NewDataError("validation_get_by_validator", "node public key must be 33 bytes", relationaldb.ErrInvalidData)
	}
	q := `SELECT ` + validationSelectCols + ` FROM validations WHERE node_pubkey = ? ORDER BY ledger_seq DESC`
	args := []any{nodeKey}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := r.getExecutor().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, relationaldb.NewQueryError("validation_get_by_validator", "failed to query validations", err)
	}
	defer rows.Close()

	var result []*relationaldb.ValidationRecord
	for rows.Next() {
		rec, err := relationaldb.ScanValidationRecord(rows)
		if err != nil {
			return nil, relationaldb.NewQueryError("validation_get_by_validator", "failed to scan row", err)
		}
		result = append(result, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("validation_get_by_validator", "row iteration error", err)
	}
	return result, nil
}

// DeleteOlderThanSeq removes up to batchSize rows with ledger_seq < maxSeq.
// A bounded DELETE keeps the retention sweep from blocking the writer on
// multi-second scans — the archive loop calls this once per flush tick.
func (r *validationRepository) DeleteOlderThanSeq(ctx context.Context, maxSeq relationaldb.LedgerIndex, batchSize int) (int64, error) {
	q := `DELETE FROM validations WHERE rowid IN (
		SELECT rowid FROM validations WHERE ledger_seq < ?`
	args := []any{int64(maxSeq)}
	if batchSize > 0 {
		q += ` LIMIT ?`
		args = append(args, batchSize)
	}
	q += `)`

	res, err := r.getExecutor().ExecContext(ctx, q, args...)
	if err != nil {
		return 0, relationaldb.NewQueryError("validation_delete_older", "failed to delete old validations", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, relationaldb.NewQueryError("validation_delete_older", "failed to read affected rows", err)
	}
	return n, nil
}
