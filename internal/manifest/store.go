package manifest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

const storeFilename = "manifests.db"

type StoredManifests struct {
	Validators [][]byte
	Publishers [][]byte
}

// Store persists the two manifest cache namespaces atomically.
type Store interface {
	Load(context.Context) (StoredManifests, error)
	Replace(context.Context, StoredManifests) error
	Close() error
}

type sqliteStore struct {
	mu sync.RWMutex
	db *sql.DB
}

type manifestNamespace uint8

const (
	validatorNamespace manifestNamespace = iota
	publisherNamespace
)

func OpenSQLiteStore(ctx context.Context, dir string) (Store, error) {
	if dir == "" {
		return nil, errors.New("manifest store: empty directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("manifest store: create directory: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, storeFilename))
	if err != nil {
		return nil, fmt.Errorf("manifest store: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	closeOnError := func(cause error) (Store, error) {
		return nil, errors.Join(cause, db.Close())
	}
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(fmt.Errorf("manifest store: ping: %w", err))
	}
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA busy_timeout=5000",
		"CREATE TABLE IF NOT EXISTS ValidatorManifests (RawData BLOB NOT NULL)",
		"CREATE TABLE IF NOT EXISTS PublisherManifests (RawData BLOB NOT NULL)",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return closeOnError(fmt.Errorf("manifest store: initialize: %w", err))
		}
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) Load(ctx context.Context) (StoredManifests, error) {
	if s == nil {
		return StoredManifests{}, errors.New("manifest store: closed")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return StoredManifests{}, errors.New("manifest store: closed")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return StoredManifests{}, fmt.Errorf("manifest store: begin load: %w", err)
	}
	defer tx.Rollback()
	validators, err := loadManifestRows(ctx, tx, validatorNamespace)
	if err != nil {
		return StoredManifests{}, fmt.Errorf("manifest store: load validators: %w", err)
	}
	publishers, err := loadManifestRows(ctx, tx, publisherNamespace)
	if err != nil {
		return StoredManifests{}, fmt.Errorf("manifest store: load publishers: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return StoredManifests{}, fmt.Errorf("manifest store: commit load: %w", err)
	}
	return StoredManifests{Validators: validators, Publishers: publishers}, nil
}

func loadManifestRows(ctx context.Context, tx *sql.Tx, namespace manifestNamespace) ([][]byte, error) {
	var query string
	switch namespace {
	case validatorNamespace:
		query = "SELECT RawData FROM ValidatorManifests"
	case publisherNamespace:
		query = "SELECT RawData FROM PublisherManifests"
	default:
		return nil, errors.New("unknown manifest namespace")
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, append([]byte(nil), raw...))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *sqliteStore) Replace(ctx context.Context, manifests StoredManifests) error {
	if s == nil {
		return errors.New("manifest store: closed")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return errors.New("manifest store: closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("manifest store: begin replace: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM ValidatorManifests"); err != nil {
		return fmt.Errorf("manifest store: clear validators: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM PublisherManifests"); err != nil {
		return fmt.Errorf("manifest store: clear publishers: %w", err)
	}
	if err := insertManifestRows(ctx, tx, validatorNamespace, manifests.Validators); err != nil {
		return fmt.Errorf("manifest store: replace validators: %w", err)
	}
	if err := insertManifestRows(ctx, tx, publisherNamespace, manifests.Publishers); err != nil {
		return fmt.Errorf("manifest store: replace publishers: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("manifest store: commit replace: %w", err)
	}
	return nil
}

func insertManifestRows(ctx context.Context, tx *sql.Tx, namespace manifestNamespace, rows [][]byte) error {
	var query string
	switch namespace {
	case validatorNamespace:
		query = "INSERT INTO ValidatorManifests (RawData) VALUES (?)"
	case publisherNamespace:
		query = "INSERT INTO PublisherManifests (RawData) VALUES (?)"
	default:
		return errors.New("unknown manifest namespace")
	}
	statement, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, raw := range rows {
		if _, err := statement.ExecContext(ctx, append([]byte(nil), raw...)); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqliteStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	if err != nil {
		return fmt.Errorf("manifest store: close: %w", err)
	}
	return nil
}
