// Package sqlutil provides guarded database executors shared by relational backends.
package sqlutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// Scanner is the row-scanning subset implemented by sql.Row and guarded rows.
type Scanner interface {
	Scan(dest ...any) error
}

// Executor is the query subset used by relational repositories.
type Executor interface {
	QueryRowContext(ctx context.Context, query string, args ...any) Scanner
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// OperationGate coordinates active operations with repository shutdown.
type OperationGate struct {
	mu        sync.RWMutex
	closing   atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

type gatedValidationRepository struct {
	gate       *OperationGate
	repository relationaldb.ValidationRepository
}

// NewGatedValidationRepository pins validation operations until manager shutdown.
func NewGatedValidationRepository(gate *OperationGate, repository relationaldb.ValidationRepository) relationaldb.ValidationRepository {
	return &gatedValidationRepository{gate: gate, repository: repository}
}

func (r *gatedValidationRepository) Save(ctx context.Context, value *relationaldb.ValidationRecord) error {
	end, err := r.gate.Begin()
	if err != nil {
		return err
	}
	defer end()
	return r.repository.Save(ctx, value)
}

func (r *gatedValidationRepository) SaveBatch(ctx context.Context, values []*relationaldb.ValidationRecord) error {
	end, err := r.gate.Begin()
	if err != nil {
		return err
	}
	defer end()
	return r.repository.SaveBatch(ctx, values)
}

func (r *gatedValidationRepository) GetValidationsForLedger(
	ctx context.Context,
	sequence relationaldb.LedgerIndex,
) ([]*relationaldb.ValidationRecord, error) {
	end, err := r.gate.Begin()
	if err != nil {
		return nil, err
	}
	defer end()
	return r.repository.GetValidationsForLedger(ctx, sequence)
}

func (r *gatedValidationRepository) GetValidationsByValidator(
	ctx context.Context,
	nodeKey []byte,
	limit int,
) ([]*relationaldb.ValidationRecord, error) {
	end, err := r.gate.Begin()
	if err != nil {
		return nil, err
	}
	defer end()
	return r.repository.GetValidationsByValidator(ctx, nodeKey, limit)
}

func (r *gatedValidationRepository) DeleteOlderThanSeq(
	ctx context.Context,
	maxSequence relationaldb.LedgerIndex,
	batchSize int,
) (int64, error) {
	end, err := r.gate.Begin()
	if err != nil {
		return 0, err
	}
	defer end()
	return r.repository.DeleteOlderThanSeq(ctx, maxSequence, batchSize)
}

// Begin registers an operation and returns its release function.
func (g *OperationGate) Begin() (func(), error) {
	if g == nil || g.closing.Load() {
		return nil, relationaldb.ErrDatabaseClosed
	}
	g.mu.RLock()
	if g.closing.Load() {
		g.mu.RUnlock()
		return nil, relationaldb.ErrDatabaseClosed
	}
	return g.mu.RUnlock, nil
}

// Close rejects new operations, waits for active operations, and closes storage.
func (g *OperationGate) Close(closeDatabase func() error) error {
	if g == nil {
		return nil
	}
	g.closeOnce.Do(func() {
		g.closing.Store(true)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.closeErr = closeDatabase()
	})
	return g.closeErr
}

type errorScanner struct {
	err error
}

func (s errorScanner) Scan(...any) error {
	return s.err
}

// DB rejects queries after its underlying database is closed.
type DB struct {
	db     *sql.DB
	closed atomic.Bool
}

// NewDB wraps db with closed-state checks.
func NewDB(db *sql.DB) *DB {
	return &DB{db: db}
}

// QueryRowContext executes a query returning at most one row.
func (db *DB) QueryRowContext(ctx context.Context, query string, args ...any) Scanner {
	if db == nil || db.closed.Load() {
		return errorScanner{err: relationaldb.ErrDatabaseClosed}
	}
	return db.db.QueryRowContext(ctx, query, args...)
}

// QueryContext executes a query returning rows.
func (db *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if db == nil || db.closed.Load() {
		return nil, relationaldb.ErrDatabaseClosed
	}
	return db.db.QueryContext(ctx, query, args...)
}

// ExecContext executes a statement.
func (db *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if db == nil || db.closed.Load() {
		return nil, relationaldb.ErrDatabaseClosed
	}
	return db.db.ExecContext(ctx, query, args...)
}

// BeginTx begins a guarded transaction.
func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	if db == nil || db.closed.Load() {
		return nil, relationaldb.ErrDatabaseClosed
	}
	tx, err := db.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return newTx(tx), nil
}

// Close closes the underlying database once.
func (db *DB) Close() error {
	if db == nil || !db.closed.CompareAndSwap(false, true) {
		return nil
	}
	return db.db.Close()
}

// Raw returns the underlying database handle.
func (db *DB) Raw() *sql.DB {
	return db.db
}

// Tx rejects operations after commit or rollback.
type Tx struct {
	tx     *sql.Tx
	active atomic.Bool
}

func newTx(tx *sql.Tx) *Tx {
	result := &Tx{tx: tx}
	result.active.Store(true)
	return result
}

// QueryRowContext executes a transaction query returning at most one row.
func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) Scanner {
	if tx == nil || !tx.active.Load() {
		return errorScanner{err: relationaldb.ErrTransactionClosed}
	}
	return tx.tx.QueryRowContext(ctx, query, args...)
}

// QueryContext executes a transaction query returning rows.
func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if tx == nil || !tx.active.Load() {
		return nil, relationaldb.ErrTransactionClosed
	}
	return tx.tx.QueryContext(ctx, query, args...)
}

// ExecContext executes a transaction statement.
func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if tx == nil || !tx.active.Load() {
		return nil, relationaldb.ErrTransactionClosed
	}
	return tx.tx.ExecContext(ctx, query, args...)
}

// Commit commits the transaction once.
func (tx *Tx) Commit() error {
	if tx == nil || !tx.active.CompareAndSwap(true, false) {
		return relationaldb.ErrTransactionClosed
	}
	return tx.tx.Commit()
}

// Rollback rolls back the transaction once.
func (tx *Tx) Rollback() error {
	if tx == nil || !tx.active.CompareAndSwap(true, false) {
		return nil
	}
	err := tx.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

// CopyExact copies a fixed-width database value or returns ErrInvalidData.
func CopyExact(dst, src []byte, field string) error {
	if len(src) != len(dst) {
		return fmt.Errorf("%w: %s has width %d, want %d", relationaldb.ErrInvalidData, field, len(src), len(dst))
	}
	copy(dst, src)
	return nil
}
