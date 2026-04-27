package relationaldb

import (
	"context"
	"time"
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
