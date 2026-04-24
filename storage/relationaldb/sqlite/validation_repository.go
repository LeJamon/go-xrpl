package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// ValidationRepository is the SQLite-backed on-disk validation archive.
// Schema deliberately cohabits ledger.db — validations are heavily
// joined with the Ledgers table in rippled's forensic queries (they
// mirror ledger-seq + ledger-hash), and opening a third DB file for a
// single table would bloat the file layout without any write-concurrency
// win (SQLite serializes writes across files in the same process).
type ValidationRepository struct {
	db *sql.DB
	tx *sql.Tx
}

// Compile-time interface check.
var _ relationaldb.ValidationRepository = (*ValidationRepository)(nil)

func NewValidationRepository(db *sql.DB) *ValidationRepository {
	return &ValidationRepository{db: db}
}

func NewValidationRepositoryWithTx(tx *sql.Tx) *ValidationRepository {
	return &ValidationRepository{tx: tx}
}

func (r *ValidationRepository) getExecutor() executor {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

const validationSelectCols = `ledger_seq, initial_seq, ledger_hash, node_pubkey,
	signature, sign_time, seen_time, flags, raw`

// xrplEpochOffset is the difference between Unix epoch (1970-01-01) and
// the XRPL epoch (2000-01-01). Times stored in the archive are XRPL-epoch
// seconds to stay wire-compatible with the serialized SignTime field.
const xrplEpochOffset int64 = 946684800

func toXRPLEpochSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix() - xrplEpochOffset
}

func fromXRPLEpochSeconds(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s+xrplEpochOffset, 0).UTC()
}

func (r *ValidationRepository) Save(ctx context.Context, v *relationaldb.ValidationRecord) error {
	if v == nil {
		return relationaldb.NewDataError("validation_save", "nil record", nil)
	}
	_, err := r.getExecutor().ExecContext(ctx, `
		INSERT INTO validations (
			ledger_seq, initial_seq, ledger_hash, node_pubkey,
			signature, sign_time, seen_time, flags, raw
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ledger_hash, node_pubkey) DO NOTHING
	`,
		int64(v.LedgerSeq), int64(v.InitialSeq), v.LedgerHash[:], v.NodePubKey,
		v.Signature, toXRPLEpochSeconds(v.SignTime), toXRPLEpochSeconds(v.SeenTime),
		int64(v.Flags), v.Raw,
	)
	if err != nil {
		return relationaldb.NewQueryError("validation_save", "failed to insert validation", err)
	}
	return nil
}

func (r *ValidationRepository) SaveBatch(ctx context.Context, vs []*relationaldb.ValidationRecord) error {
	if len(vs) == 0 {
		return nil
	}
	// Run as a single transaction when we're not already inside one —
	// rippled's doStaleWrite batches a whole vector per commit for the
	// same reason (amortize fsync).
	if r.tx != nil {
		for _, v := range vs {
			if err := r.Save(ctx, v); err != nil {
				return err
			}
		}
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return relationaldb.NewTransactionError("validation_save_batch", "failed to begin transaction", err)
	}
	txRepo := NewValidationRepositoryWithTx(tx)
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

func (r *ValidationRepository) scanRow(row interface {
	Scan(dest ...interface{}) error
}) (*relationaldb.ValidationRecord, error) {
	var rec relationaldb.ValidationRecord
	var ledgerSeq, initialSeq, signTime, seenTime int64
	var flags int64
	var ledgerHash []byte

	if err := row.Scan(
		&ledgerSeq, &initialSeq, &ledgerHash, &rec.NodePubKey,
		&rec.Signature, &signTime, &seenTime, &flags, &rec.Raw,
	); err != nil {
		return nil, err
	}

	rec.LedgerSeq = relationaldb.LedgerIndex(ledgerSeq)
	rec.InitialSeq = relationaldb.LedgerIndex(initialSeq)
	copy(rec.LedgerHash[:], ledgerHash)
	rec.SignTime = fromXRPLEpochSeconds(signTime)
	rec.SeenTime = fromXRPLEpochSeconds(seenTime)
	rec.Flags = uint32(flags)
	return &rec, nil
}

func (r *ValidationRepository) GetValidationsForLedger(ctx context.Context, seq relationaldb.LedgerIndex) ([]*relationaldb.ValidationRecord, error) {
	rows, err := r.getExecutor().QueryContext(ctx,
		`SELECT `+validationSelectCols+` FROM validations WHERE ledger_seq = ?`, int64(seq))
	if err != nil {
		return nil, relationaldb.NewQueryError("validation_get_for_ledger", "failed to query validations", err)
	}
	defer rows.Close()

	var result []*relationaldb.ValidationRecord
	for rows.Next() {
		rec, err := r.scanRow(rows)
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

func (r *ValidationRepository) GetValidationsByValidator(ctx context.Context, nodeKey []byte, limit int) ([]*relationaldb.ValidationRecord, error) {
	q := `SELECT ` + validationSelectCols + ` FROM validations WHERE node_pubkey = ? ORDER BY ledger_seq DESC`
	args := []interface{}{nodeKey}
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
		rec, err := r.scanRow(rows)
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

func (r *ValidationRepository) GetValidationCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.getExecutor().QueryRowContext(ctx, `SELECT COUNT(*) FROM validations`).Scan(&count)
	if err != nil {
		return 0, relationaldb.NewQueryError("validation_count", "failed to count validations", err)
	}
	return count, nil
}

// DeleteOlderThanSeq removes up to batchSize rows with ledger_seq < maxSeq.
// A bounded DELETE keeps the retention sweep from blocking the writer on
// multi-second scans — the archive loop calls this once per flush tick.
func (r *ValidationRepository) DeleteOlderThanSeq(ctx context.Context, maxSeq relationaldb.LedgerIndex, batchSize int) (int64, error) {
	q := `DELETE FROM validations WHERE rowid IN (
		SELECT rowid FROM validations WHERE ledger_seq < ?`
	args := []interface{}{int64(maxSeq)}
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
