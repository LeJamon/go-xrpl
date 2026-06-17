// Package statecompare provides a client for reading mainnet ledgers from the
// xrpl-state-compare lab's data plane, used by the offline replay tooling to
// load seed state and transactions rather than reading fixture files.
//
// The lab keeps a small relational manifest in PostgreSQL — the queryable
// index — while the bulk immutable bytes live in object storage (MinIO/S3) as
// length-prefixed "packs" (see pack.go). A ledger header row points at one
// ledger inside a batch pack via (blob_key, blob_offset); a checkpoint row
// points at the full state of a checkpoint ledger.
package statecompare

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	_ "github.com/lib/pq" // PostgreSQL driver
)

// ErrNotFound is returned (wrapped) when a requested ledger, checkpoint or
// blob is absent, so callers can distinguish a missing record from a query
// failure with errors.Is.
var ErrNotFound = errors.New("statecompare: ledger not found")

// Client reads from the lab's PostgreSQL manifest plus its blob store.
type Client struct {
	db    *sql.DB
	blobs blobStore

	// The replay loop walks a ledger-batch pack sequentially, so memoizing the
	// most recently fetched pack object turns ~1000 redundant downloads of the
	// same object into one.
	mu        sync.Mutex
	cacheKey  string
	cacheData []byte
}

// toHash32 copies a database hash column into a fixed 32-byte array,
// erroring on a malformed length instead of silently zero-padding or
// truncating.
func toHash32(b []byte) ([32]byte, error) {
	var h [32]byte
	if len(b) != len(h) {
		return h, fmt.Errorf("expected %d-byte hash, got %d bytes", len(h), len(b))
	}
	copy(h[:], b)
	return h, nil
}

// LedgerSnapshot is a ledger's header row from the manifest.
type LedgerSnapshot struct {
	LedgerIndex         uint32
	LedgerHash          [32]byte
	ParentHash          [32]byte
	AccountHash         [32]byte
	TransactionHash     [32]byte
	TotalCoins          uint64
	CloseTime           int64
	CloseTimeResolution uint32
	CloseFlags          uint8
}

// StateEntry is one serialized ledger entry (SLE) of a checkpoint ledger.
type StateEntry struct {
	Index [32]byte
	Data  []byte
}

// Transaction is one applied transaction and its metadata.
type Transaction struct {
	// TxIndex is sfTransactionIndex from the metadata: the position the
	// transaction was applied at ledger close. GetTransactions returns the
	// slice sorted ascending by it so a single forward replay pass matches
	// mainnet's apply order.
	TxIndex  int
	TxHash   [32]byte
	TxBlob   []byte
	MetaBlob []byte
}

// Config holds the PostgreSQL manifest connection settings.
type Config struct {
	Host     string
	Port     string
	Database string
	User     string
	Password string
	// SSLMode controls libpq sslmode. Defaults to "disable" via ConfigFromEnv
	// to match local-dev setups; production deployments should set this to
	// "require" or higher via POSTGRES_SSLMODE.
	SSLMode string
}

// ConfigFromEnv creates a Config from environment variables.
// Uses the same env vars as the Python xrpl-state-compare tool.
func ConfigFromEnv() Config {
	return Config{
		Host:     getEnvOrDefault("POSTGRES_HOST", "localhost"),
		Port:     getEnvOrDefault("POSTGRES_PORT", "5432"),
		Database: getEnvOrDefault("POSTGRES_DB", "xrpl_state"),
		User:     getEnvOrDefault("POSTGRES_USER", "postgres"),
		Password: getEnvOrDefault("POSTGRES_PASSWORD", "postgres"),
		SSLMode:  getEnvOrDefault("POSTGRES_SSLMODE", "disable"),
	}
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// NewClient creates a client from the manifest and blob-store configs.
func NewClient(cfg Config, blobCfg BlobStoreConfig) (*Client, error) {
	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	connStr := fmt.Sprintf(
		"host=%s port=%s dbname=%s user=%s password=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password, sslMode,
	)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	blobs, err := newBlobStore(blobCfg)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing blob store: %w", err)
	}

	return &Client{db: db, blobs: blobs}, nil
}

// NewClientFromEnv creates a client using environment variables.
func NewClientFromEnv() (*Client, error) {
	return NewClient(ConfigFromEnv(), BlobStoreConfigFromEnv())
}

// Close closes the database connection.
func (c *Client) Close() error {
	return c.db.Close()
}

// fetchBlob returns a pack object's bytes, memoizing the most recent one. The
// returned slice is shared and must not be mutated by callers.
func (c *Client) fetchBlob(ctx context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	if key == c.cacheKey && c.cacheData != nil {
		data := c.cacheData
		c.mu.Unlock()
		return data, nil
	}
	c.mu.Unlock()

	data, err := c.blobs.get(ctx, key)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cacheKey = key
	c.cacheData = data
	c.mu.Unlock()
	return data, nil
}

// GetSnapshot retrieves a ledger header from the manifest by sequence number.
func (c *Client) GetSnapshot(ctx context.Context, seq uint32) (*LedgerSnapshot, error) {
	const query = `
		SELECT seq, ledger_hash, parent_hash, account_hash, transaction_hash,
		       total_coins, close_time, close_time_resolution, close_flags
		FROM ledgers
		WHERE seq = $1
	`

	var snapshot LedgerSnapshot
	var ledgerHash, parentHash, accountHash, txHash []byte

	err := c.db.QueryRowContext(ctx, query, seq).Scan(
		&snapshot.LedgerIndex,
		&ledgerHash,
		&parentHash,
		&accountHash,
		&txHash,
		&snapshot.TotalCoins,
		&snapshot.CloseTime,
		&snapshot.CloseTimeResolution,
		&snapshot.CloseFlags,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("ledger %d: %w", seq, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("querying snapshot: %w", err)
	}

	for _, h := range []struct {
		name string
		src  []byte
		dst  *[32]byte
	}{
		{"ledger_hash", ledgerHash, &snapshot.LedgerHash},
		{"parent_hash", parentHash, &snapshot.ParentHash},
		{"account_hash", accountHash, &snapshot.AccountHash},
		{"transaction_hash", txHash, &snapshot.TransactionHash},
	} {
		v, err := toHash32(h.src)
		if err != nil {
			return nil, fmt.Errorf("ledger %d %s: %w", seq, h.name, err)
		}
		*h.dst = v
	}

	return &snapshot, nil
}

// GetStateEntries retrieves every SLE of a checkpoint ledger by decoding its
// STATE pack. seq must be a checkpoint ledger; full state is captured only at
// checkpoints, so a non-checkpoint seq returns ErrNotFound.
func (c *Client) GetStateEntries(ctx context.Context, seq uint32) ([]StateEntry, error) {
	var blobKey string
	err := c.db.QueryRowContext(ctx,
		`SELECT blob_key FROM checkpoints WHERE seq = $1`, seq,
	).Scan(&blobKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("checkpoint %d: %w", seq, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("querying checkpoint %d: %w", seq, err)
	}

	data, err := c.blobs.get(ctx, blobKey)
	if err != nil {
		return nil, fmt.Errorf("fetching state pack %q: %w", blobKey, err)
	}
	packSeq, entries, err := unpackState(data)
	if err != nil {
		return nil, fmt.Errorf("decoding state pack %q: %w", blobKey, err)
	}
	if packSeq != uint64(seq) {
		return nil, fmt.Errorf("state pack %q is for checkpoint %d, want %d", blobKey, packSeq, seq)
	}
	return entries, nil
}

// StreamStateEntries decodes a checkpoint's STATE pack on the fly, invoking fn
// for each SLE as it is read from the blob store. Unlike GetStateEntries it
// never materializes the whole pack or the full entry slice, so seeding a
// multi-gigabyte mainnet checkpoint stays within a bounded memory footprint.
// seq must be a checkpoint ledger; a non-checkpoint seq returns ErrNotFound.
func (c *Client) StreamStateEntries(ctx context.Context, seq uint32, fn func(StateEntry) error) error {
	var blobKey string
	err := c.db.QueryRowContext(ctx,
		`SELECT blob_key FROM checkpoints WHERE seq = $1`, seq,
	).Scan(&blobKey)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("checkpoint %d: %w", seq, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("querying checkpoint %d: %w", seq, err)
	}

	r, err := c.blobs.getReader(ctx, blobKey)
	if err != nil {
		return fmt.Errorf("fetching state pack %q: %w", blobKey, err)
	}
	defer r.Close()

	packSeq, _, err := unpackStateStream(r, func(index [32]byte, data []byte) error {
		return fn(StateEntry{Index: index, Data: data})
	})
	if err != nil {
		return fmt.Errorf("decoding state pack %q: %w", blobKey, err)
	}
	if packSeq != uint64(seq) {
		return fmt.Errorf("state pack %q is for checkpoint %d, want %d", blobKey, packSeq, seq)
	}
	return nil
}

// GetTransactions retrieves the transactions of a ledger by seeking into its
// batch pack at the manifest-recorded offset.
func (c *Client) GetTransactions(ctx context.Context, seq uint32) ([]Transaction, error) {
	var blobKey sql.NullString
	var blobOffset sql.NullInt64
	err := c.db.QueryRowContext(ctx,
		`SELECT blob_key, blob_offset FROM ledgers WHERE seq = $1`, seq,
	).Scan(&blobKey, &blobOffset)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("ledger %d: %w", seq, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("querying ledger %d: %w", seq, err)
	}
	if !blobKey.Valid || !blobOffset.Valid {
		return nil, fmt.Errorf("ledger %d has no transaction blob (manifest synced without bytes): %w", seq, ErrNotFound)
	}

	data, err := c.fetchBlob(ctx, blobKey.String)
	if err != nil {
		return nil, fmt.Errorf("fetching ledger pack %q: %w", blobKey.String, err)
	}
	ledger, err := readLedgerAt(data, int(blobOffset.Int64))
	if err != nil {
		return nil, fmt.Errorf("decoding ledger pack %q at offset %d: %w", blobKey.String, blobOffset.Int64, err)
	}
	if ledger.seq != uint64(seq) {
		return nil, fmt.Errorf("ledger pack %q offset %d holds ledger %d, want %d", blobKey.String, blobOffset.Int64, ledger.seq, seq)
	}

	txs := make([]Transaction, len(ledger.txs))
	for i, t := range ledger.txs {
		txs[i] = Transaction{
			TxHash:   t.txHash,
			TxBlob:   t.txBlob,
			MetaBlob: t.metaBlob,
		}
	}
	if err := orderByTransactionIndex(txs); err != nil {
		return nil, fmt.Errorf("ordering ledger %d transactions: %w", seq, err)
	}
	return txs, nil
}

// orderByTransactionIndex sets each transaction's TxIndex from its metadata's
// sfTransactionIndex and sorts the slice into that order. A pack stores
// transactions in transaction-tree (hash) order; replaying them in that order
// applies an account's transactions out of sequence, so all but the lowest in-
// order one fail terPRE_SEQ and are dropped. sfTransactionIndex is the order in
// which the ledger close actually applied them, so a single forward pass over it
// reproduces mainnet — this mirrors rippled's replay build path, which applies
// the close-ordered tx set rather than re-running consensus's multi-pass retry.
func orderByTransactionIndex(txs []Transaction) error {
	for i := range txs {
		idx, err := metaTransactionIndex(txs[i].MetaBlob)
		if err != nil {
			return fmt.Errorf("tx %x: %w", txs[i].TxHash, err)
		}
		txs[i].TxIndex = int(idx)
	}
	sort.SliceStable(txs, func(i, j int) bool { return txs[i].TxIndex < txs[j].TxIndex })
	return nil
}

// metaTransactionIndex decodes a transaction metadata blob and returns its
// sfTransactionIndex value.
func metaTransactionIndex(meta []byte) (uint32, error) {
	if len(meta) == 0 {
		return 0, errors.New("empty metadata")
	}
	decoded, err := binarycodec.Decode(hex.EncodeToString(meta))
	if err != nil {
		return 0, fmt.Errorf("decode metadata: %w", err)
	}
	raw, ok := decoded["TransactionIndex"]
	if !ok {
		return 0, errors.New("metadata missing TransactionIndex")
	}
	switch v := raw.(type) {
	case uint32:
		return v, nil
	case int:
		return uint32(v), nil
	case int64:
		return uint32(v), nil
	case uint64:
		return uint32(v), nil
	case float64:
		return uint32(v), nil
	default:
		return 0, fmt.Errorf("metadata TransactionIndex has unexpected type %T", raw)
	}
}

// ValidateRange checks that every ledger in [from, to] is present in the
// manifest, returning the first missing sequence if any.
//
// Implemented as a single range query rather than N round-trips so validating
// a multi-thousand-ledger range stays cheap.
func (c *Client) ValidateRange(ctx context.Context, from, to uint32) (bool, uint32, error) {
	if from > to {
		return true, 0, nil
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT seq FROM ledgers
		 WHERE seq BETWEEN $1 AND $2
		 ORDER BY seq`,
		from, to,
	)
	if err != nil {
		return false, from, fmt.Errorf("querying ledger range: %w", err)
	}
	defer rows.Close()

	expected := from
	for rows.Next() {
		var idx uint32
		if err := rows.Scan(&idx); err != nil {
			return false, expected, fmt.Errorf("scanning ledger seq: %w", err)
		}
		if idx != expected {
			return false, expected, nil
		}
		expected++
	}
	if err := rows.Err(); err != nil {
		return false, expected, fmt.Errorf("iterating ledger range: %w", err)
	}
	if expected <= to {
		return false, expected, nil
	}
	return true, 0, nil
}
