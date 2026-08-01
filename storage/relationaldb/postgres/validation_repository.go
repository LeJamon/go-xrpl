package postgres

import (
	"context"
	"database/sql"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/sqlutil"
)

// validationRepository is the PostgreSQL-backed on-disk validation archive.
// Mirrors the SQLite backend row-for-row so RPC/forensic code sees the same
// shape regardless of deployment.
type validationRepository struct {
	db       *sqlutil.DB
	executor executor
}

// Compile-time interface check.
var _ relationaldb.ValidationRepository = (*validationRepository)(nil)

// newValidationRepository creates a PostgreSQL validation repository.
func newValidationRepository(db *sqlutil.DB) *validationRepository {
	return &validationRepository{db: db, executor: db}
}

func newValidationRepositoryWithExecutor(exec executor) *validationRepository {
	return &validationRepository{executor: exec}
}

const validationSelectCols = `ledger_seq, initial_seq, ledger_hash, node_pubkey,
	sign_time, seen_time, flags, raw`

// Save inserts a validation record, ignoring duplicates (upsert on ledger_hash + node_pubkey).
func (r *validationRepository) Save(ctx context.Context, v *relationaldb.ValidationRecord) error {
	if v == nil {
		return relationaldb.NewDataError("validation_save", "nil record", nil)
	}
	if len(v.NodePubKey) != 33 {
		return relationaldb.NewDataError("validation_save", "node public key must be 33 bytes", relationaldb.ErrInvalidData)
	}
	_, err := r.executor.ExecContext(ctx, `
		INSERT INTO validations (
			ledger_seq, initial_seq, ledger_hash, node_pubkey,
			sign_time, seen_time, flags, raw
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (ledger_hash, node_pubkey) DO NOTHING
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
	if r.db == nil {
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
	txRepo := newValidationRepositoryWithExecutor(tx)
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
	rows, err := r.executor.QueryContext(ctx,
		`SELECT `+validationSelectCols+` FROM validations WHERE ledger_seq = $1`, int64(seq))
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
	q := `SELECT ` + validationSelectCols + ` FROM validations WHERE node_pubkey = $1 ORDER BY ledger_seq DESC`
	args := []any{nodeKey}
	if limit > 0 {
		q += ` LIMIT $2`
		args = append(args, limit)
	}

	rows, err := r.executor.QueryContext(ctx, q, args...)
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
// Uses a CTID-based bounded DELETE so a single retention sweep never blocks
// the writer on an unbounded scan.
func (r *validationRepository) DeleteOlderThanSeq(ctx context.Context, maxSeq relationaldb.LedgerIndex, batchSize int) (int64, error) {
	var res sql.Result
	var err error
	if batchSize > 0 {
		res, err = r.executor.ExecContext(ctx, `
			DELETE FROM validations WHERE ctid IN (
				SELECT ctid FROM validations WHERE ledger_seq < $1 LIMIT $2
			)
		`, int64(maxSeq), batchSize)
	} else {
		res, err = r.executor.ExecContext(ctx,
			`DELETE FROM validations WHERE ledger_seq < $1`, int64(maxSeq))
	}
	if err != nil {
		return 0, relationaldb.NewQueryError("validation_delete_older", "failed to delete old validations", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, relationaldb.NewQueryError("validation_delete_older", "failed to read affected rows", err)
	}
	return n, nil
}
