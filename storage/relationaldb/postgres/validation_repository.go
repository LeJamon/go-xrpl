package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// ValidationRepository is the PostgreSQL-backed on-disk validation archive.
// Mirrors the SQLite backend row-for-row so RPC/forensic code sees the same
// shape regardless of deployment.
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
	sign_time, seen_time, flags, raw`

func toXRPLEpochSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix() - protocol.RippleEpochUnix
}

func fromXRPLEpochSeconds(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s+protocol.RippleEpochUnix, 0).UTC()
}

func (r *ValidationRepository) Save(ctx context.Context, v *relationaldb.ValidationRecord) error {
	if v == nil {
		return relationaldb.NewDataError("validation_save", "nil record", nil)
	}
	_, err := r.getExecutor().ExecContext(ctx, `
		INSERT INTO validations (
			ledger_seq, initial_seq, ledger_hash, node_pubkey,
			sign_time, seen_time, flags, raw
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (ledger_hash, node_pubkey) DO NOTHING
	`,
		int64(v.LedgerSeq), int64(v.InitialSeq), v.LedgerHash[:], v.NodePubKey,
		toXRPLEpochSeconds(v.SignTime), toXRPLEpochSeconds(v.SeenTime),
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
		&signTime, &seenTime, &flags, &rec.Raw,
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
		`SELECT `+validationSelectCols+` FROM validations WHERE ledger_seq = $1`, int64(seq))
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
	q := `SELECT ` + validationSelectCols + ` FROM validations WHERE node_pubkey = $1 ORDER BY ledger_seq DESC`
	args := []interface{}{nodeKey}
	if limit > 0 {
		q += ` LIMIT $2`
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
// Uses a CTID-based bounded DELETE so a single retention sweep never blocks
// the writer on an unbounded scan.
func (r *ValidationRepository) DeleteOlderThanSeq(ctx context.Context, maxSeq relationaldb.LedgerIndex, batchSize int) (int64, error) {
	var res sql.Result
	var err error
	if batchSize > 0 {
		res, err = r.getExecutor().ExecContext(ctx, `
			DELETE FROM validations WHERE ctid IN (
				SELECT ctid FROM validations WHERE ledger_seq < $1 LIMIT $2
			)
		`, int64(maxSeq), batchSize)
	} else {
		res, err = r.getExecutor().ExecContext(ctx,
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
