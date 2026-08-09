package statecompare

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

var errNotFound = errors.New("statecompare: ledger not found")

type rowScanner interface {
	Scan(dest ...any) error
}

type rowIterator interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

type manifestDB interface {
	queryRow(ctx context.Context, query string, args ...any) rowScanner
	query(ctx context.Context, query string, args ...any) (rowIterator, error)
	close() error
}

type sqlDatabase struct {
	db *sql.DB
}

func (d sqlDatabase) queryRow(ctx context.Context, query string, args ...any) rowScanner {
	return d.db.QueryRowContext(ctx, query, args...)
}

func (d sqlDatabase) query(ctx context.Context, query string, args ...any) (rowIterator, error) {
	return d.db.QueryContext(ctx, query, args...)
}

func (d sqlDatabase) close() error {
	return d.db.Close()
}

type manifestStore interface {
	snapshot(ctx context.Context, seq uint32) (*LedgerSnapshot, error)
	checkpoint(ctx context.Context, seq uint32) (checkpointManifest, error)
	validateRange(ctx context.Context, from, to uint32) (bool, uint32, error)
	Close() error
}

type checkpointManifest struct {
	seq         uint32
	blobKey     string
	accountHash [32]byte
	objectCount uint32
	sizeBytes   int64
}

type sqlManifest struct {
	db manifestDB
}

func newSQLManifest(db *sql.DB) *sqlManifest {
	return &sqlManifest{db: sqlDatabase{db: db}}
}

func (m *sqlManifest) snapshot(ctx context.Context, requested uint32) (*LedgerSnapshot, error) {
	const query = `
		SELECT seq, ledger_hash, parent_hash, account_hash, transaction_hash,
		       total_coins, close_time, close_time_resolution, close_flags,
		       tx_count, blob_key, blob_offset
		FROM ledgers
		WHERE seq = $1
	`

	var (
		seq                                         int64
		ledgerHash, parentHash, accountHash, txHash []byte
		closeResolution, closeFlags, txCount        int64
		blobKey                                     sql.NullString
		blobOffset                                  sql.NullInt64
		snapshot                                    LedgerSnapshot
	)
	err := m.db.queryRow(ctx, query, requested).Scan(
		&seq,
		&ledgerHash,
		&parentHash,
		&accountHash,
		&txHash,
		&snapshot.TotalCoins,
		&snapshot.CloseTime,
		&closeResolution,
		&closeFlags,
		&txCount,
		&blobKey,
		&blobOffset,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("ledger %d: %w", requested, errNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("querying ledger %d snapshot: %w", requested, err)
	}
	if seq != int64(requested) {
		return nil, fmt.Errorf("ledger query for %d returned sequence %d", requested, seq)
	}
	if snapshot.CloseTime < 0 || snapshot.CloseTime > math.MaxUint32 {
		return nil, fmt.Errorf("ledger %d close_time %d is outside uint32", requested, snapshot.CloseTime)
	}
	if closeResolution < 0 || closeResolution > math.MaxUint8 {
		return nil, fmt.Errorf("ledger %d close_time_resolution %d is outside uint8", requested, closeResolution)
	}
	if closeFlags < 0 || closeFlags > math.MaxUint8 {
		return nil, fmt.Errorf("ledger %d close_flags %d is outside uint8", requested, closeFlags)
	}
	if txCount < 0 || txCount > math.MaxUint32 {
		return nil, fmt.Errorf("ledger %d tx_count %d is outside uint32", requested, txCount)
	}
	if txCount > (maxLedgerPackBytes-minLedgerRecordLen)/minTransactionEntry {
		return nil, fmt.Errorf("ledger %d tx_count %d cannot fit in a ledger pack", requested, txCount)
	}
	if blobKey.Valid != blobOffset.Valid {
		return nil, fmt.Errorf("ledger %d has incomplete blob location", requested)
	}
	if blobOffset.Valid && (blobOffset.Int64 < packEnvelopeLen || blobOffset.Int64 > maxLedgerPackBytes) {
		return nil, fmt.Errorf("ledger %d blob_offset %d is outside pack bounds", requested, blobOffset.Int64)
	}

	snapshot.LedgerIndex = requested
	snapshot.CloseTimeResolution = uint32(closeResolution)
	snapshot.CloseFlags = uint8(closeFlags)
	snapshot.TransactionCount = uint32(txCount)
	snapshot.blobKey = blobKey.String
	snapshot.blobOffset = blobOffset.Int64
	snapshot.hasBlob = blobKey.Valid

	for _, hash := range []struct {
		name string
		src  []byte
		dst  *[32]byte
	}{
		{"ledger_hash", ledgerHash, &snapshot.LedgerHash},
		{"parent_hash", parentHash, &snapshot.ParentHash},
		{"account_hash", accountHash, &snapshot.AccountHash},
		{"transaction_hash", txHash, &snapshot.TransactionHash},
	} {
		value, err := toHash32(hash.src)
		if err != nil {
			return nil, fmt.Errorf("ledger %d %s: %w", requested, hash.name, err)
		}
		*hash.dst = value
	}
	return &snapshot, nil
}

func (m *sqlManifest) checkpoint(ctx context.Context, requested uint32) (checkpointManifest, error) {
	const query = `
		SELECT seq, blob_key, account_hash, object_count, size_bytes
		FROM checkpoints
		WHERE seq = $1
	`
	var (
		seq, objectCount, sizeBytes int64
		accountHash                 []byte
		checkpoint                  checkpointManifest
	)
	err := m.db.queryRow(ctx, query, requested).Scan(
		&seq,
		&checkpoint.blobKey,
		&accountHash,
		&objectCount,
		&sizeBytes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpointManifest{}, fmt.Errorf("checkpoint %d: %w", requested, errNotFound)
	}
	if err != nil {
		return checkpointManifest{}, fmt.Errorf("querying checkpoint %d: %w", requested, err)
	}
	if seq != int64(requested) {
		return checkpointManifest{}, fmt.Errorf("checkpoint query for %d returned sequence %d", requested, seq)
	}
	if objectCount < 0 || objectCount > math.MaxUint32 {
		return checkpointManifest{}, fmt.Errorf("checkpoint %d object_count %d is outside uint32", requested, objectCount)
	}
	if sizeBytes < packEnvelopeLen || sizeBytes > maxStatePackBytes {
		return checkpointManifest{}, fmt.Errorf("checkpoint %d size_bytes %d is outside [%d,%d]", requested, sizeBytes, packEnvelopeLen, maxStatePackBytes)
	}
	if objectCount > (sizeBytes-packEnvelopeLen)/minStateRecordLen {
		return checkpointManifest{}, fmt.Errorf("checkpoint %d object_count %d cannot fit in %d bytes", requested, objectCount, sizeBytes)
	}
	checkpoint.seq = requested
	checkpoint.objectCount = uint32(objectCount)
	checkpoint.sizeBytes = sizeBytes
	var hashErr error
	checkpoint.accountHash, hashErr = toHash32(accountHash)
	if hashErr != nil {
		return checkpointManifest{}, fmt.Errorf("checkpoint %d account_hash: %w", requested, hashErr)
	}
	return checkpoint, nil
}

func (m *sqlManifest) validateRange(ctx context.Context, from, to uint32) (valid bool, missing uint32, retErr error) {
	if from > to {
		return true, 0, nil
	}
	rows, err := m.db.query(ctx, `
		SELECT seq FROM ledgers
		WHERE seq BETWEEN $1 AND $2
		ORDER BY seq
	`, from, to)
	if err != nil {
		return false, from, fmt.Errorf("querying ledger range: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, rows.Close())
	}()

	expected := uint64(from)
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			return false, rangeMissing(expected), fmt.Errorf("scanning ledger sequence: %w", err)
		}
		if seq < 0 || uint64(seq) > math.MaxUint32 {
			return false, rangeMissing(expected), fmt.Errorf("ledger sequence %d is outside uint32", seq)
		}
		if uint64(seq) != expected {
			return false, rangeMissing(expected), nil
		}
		expected++
	}
	if err := rows.Err(); err != nil {
		return false, rangeMissing(expected), fmt.Errorf("iterating ledger range: %w", err)
	}
	if expected <= uint64(to) {
		return false, uint32(expected), nil
	}
	return true, 0, nil
}

func rangeMissing(expected uint64) uint32 {
	if expected > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(expected)
}

func (m *sqlManifest) Close() error {
	return m.db.close()
}
