package relationaldb

import (
	"context"
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
)

// ValidationRecord is one row of the on-disk validation archive. Columns
// mirror rippled's historical Validations table (DBInit.h, pre-May-2019)
// augmented with SeenTime and Flags so forensic tooling can replay the
// receive-side perspective, not just the signed payload.
//
// The signature lives inside Raw (sfSignature is part of the canonical
// STValidation wire format) — there is no separate Signature column.
// Callers that need the signature parse Raw via the binary codec.
type ValidationRecord struct {
	LedgerSeq  LedgerIndex
	InitialSeq LedgerIndex
	LedgerHash Hash
	NodePubKey []byte // 33-byte compressed pubkey
	SignTime   time.Time
	SeenTime   time.Time
	Flags      uint32
	Raw        []byte // canonical XRPL-binary STValidation blob (includes signature)
}

// ValidationRepository persists stale validations and answers historical
// queries. Backends (SQLite, PostgreSQL) guarantee idempotent writes:
// re-inserting the same (LedgerHash, NodePubKey) is a no-op, so replaying
// the same onStale stream never produces duplicate rows.
type ValidationRepository interface {
	// Save appends one row. Returns nil on duplicate-key conflict.
	Save(ctx context.Context, v *ValidationRecord) error

	// SaveBatch inserts many rows in a single transaction. Duplicates
	// within the batch are allowed; conflicts are ignored.
	SaveBatch(ctx context.Context, vs []*ValidationRecord) error

	// GetValidationsForLedger returns every archived validation for the
	// given ledger sequence. Order is unspecified.
	GetValidationsForLedger(ctx context.Context, seq LedgerIndex) ([]*ValidationRecord, error)

	// GetValidationsByValidator returns up to `limit` most-recent
	// archived validations signed by nodeKey (ordered by LedgerSeq
	// descending). limit <= 0 applies no bound.
	GetValidationsByValidator(ctx context.Context, nodeKey []byte, limit int) ([]*ValidationRecord, error)

	// GetValidationCount returns the total number of archived rows.
	GetValidationCount(ctx context.Context) (int64, error)

	// DeleteOlderThanSeq drops rows with LedgerSeq < maxSeq, bounded to
	// at most `batchSize` rows per call so long retention sweeps never
	// block the writer for an unbounded duration. Returns the number of
	// rows actually deleted. batchSize <= 0 applies no bound.
	DeleteOlderThanSeq(ctx context.Context, maxSeq LedgerIndex, batchSize int) (int64, error)
}

// RowScanner is the subset of *sql.Row / *sql.Rows used by the shared scan
// helpers, so one helper serves both single-row and multi-row queries.
type RowScanner interface {
	Scan(dest ...any) error
}

// ToXRPLEpochSeconds converts a Go time to seconds since the XRPL epoch
// (2000-01-01). The zero time maps to 0.
func ToXRPLEpochSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix() - protocol.RippleEpochUnix
}

// FromXRPLEpochSeconds converts seconds since the XRPL epoch (2000-01-01)
// to a UTC Go time. 0 maps to the zero time.
func FromXRPLEpochSeconds(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s+protocol.RippleEpochUnix, 0).UTC()
}

// ScanValidationRecord scans one validation archive row in the canonical
// column order (ledger_seq, initial_seq, ledger_hash, node_pubkey, sign_time,
// seen_time, flags, raw). Shared by the SQLite and PostgreSQL backends so the
// two cannot drift.
func ScanValidationRecord(row RowScanner) (*ValidationRecord, error) {
	var rec ValidationRecord
	var ledgerSeq, initialSeq, signTime, seenTime int64
	var flags int64
	var ledgerHash []byte

	if err := row.Scan(
		&ledgerSeq, &initialSeq, &ledgerHash, &rec.NodePubKey,
		&signTime, &seenTime, &flags, &rec.Raw,
	); err != nil {
		return nil, err
	}

	rec.LedgerSeq = LedgerIndex(ledgerSeq)
	rec.InitialSeq = LedgerIndex(initialSeq)
	copy(rec.LedgerHash[:], ledgerHash)
	rec.SignTime = FromXRPLEpochSeconds(signTime)
	rec.SeenTime = FromXRPLEpochSeconds(seenTime)
	rec.Flags = uint32(flags)
	return &rec, nil
}
