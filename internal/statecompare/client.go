// Package statecompare reads replay manifests and immutable pack objects from
// the xrpl-state-compare data plane.
package statecompare

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	txcodec "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	_ "github.com/lib/pq"
)

type Client struct {
	manifest manifestStore
	blobs    blobStore

	mu        sync.Mutex
	cacheKey  string
	cachePack *ledgerPack
}

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
	TransactionCount    uint32

	blobKey    string
	blobOffset int64
	hasBlob    bool
}

type StateEntry struct {
	Index [32]byte
	Data  []byte
}

type Transaction struct {
	TxIndex  uint32
	TxHash   [32]byte
	TxBlob   []byte
	MetaBlob []byte
}

func toHash32(value []byte) ([32]byte, error) {
	if len(value) != 32 {
		return [32]byte{}, fmt.Errorf("invalid hash length %d", len(value))
	}
	var hash [32]byte
	copy(hash[:], value)
	return hash, nil
}

func NewClientFromEnv(ctx context.Context) (*Client, error) {
	blobConfig, err := blobStoreConfigFromEnv()
	if err != nil {
		return nil, err
	}
	dsn, err := manifestDSNFromEnv()
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening manifest database: %w", err)
	}
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(connectCtx); err != nil {
		return nil, errors.Join(fmt.Errorf("connecting to manifest database: %w", err), db.Close())
	}

	blobs, err := newBlobStore(blobConfig)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("initializing blob store: %w", err), db.Close())
	}
	return newClient(newSQLManifest(db), blobs), nil
}

func manifestDSNFromEnv() (string, error) {
	port, err := strconv.Atoi(getEnvOrDefault("POSTGRES_PORT", "5432"))
	if err != nil {
		return "", fmt.Errorf("POSTGRES_PORT: %w", err)
	}
	cfg := relationaldb.NewConfig()
	cfg.Host = getEnvOrDefault("POSTGRES_HOST", "localhost")
	cfg.Port = port
	cfg.Database = getEnvOrDefault("POSTGRES_DB", "xrpl_state")
	cfg.Username = getEnvOrDefault("POSTGRES_USER", "postgres")
	cfg.Password = os.Getenv("POSTGRES_PASSWORD")
	cfg.SSLMode = getEnvOrDefault("POSTGRES_SSLMODE", "disable")
	switch cfg.SSLMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return "", fmt.Errorf("invalid POSTGRES_SSLMODE %q", cfg.SSLMode)
	}
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("invalid manifest database configuration: %w", err)
	}
	dsn, err := cfg.BuildConnectionString()
	if err != nil {
		return "", fmt.Errorf("building manifest database connection string: %w", err)
	}
	return dsn, nil
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func newClient(manifest manifestStore, blobs blobStore) *Client {
	return &Client{manifest: manifest, blobs: blobs}
}

func (c *Client) Close() error {
	return errors.Join(c.manifest.Close(), c.blobs.Close())
}

func (c *Client) Snapshot(ctx context.Context, seq uint32) (*LedgerSnapshot, error) {
	return c.manifest.snapshot(ctx, seq)
}

func (c *Client) StreamStateEntries(
	ctx context.Context,
	snapshot *LedgerSnapshot,
	fn func(StateEntry) error,
) (retErr error) {
	if snapshot == nil {
		return errors.New("statecompare: nil checkpoint snapshot")
	}
	if fn == nil {
		return errors.New("statecompare: state callback is nil")
	}
	checkpoint, err := c.manifest.checkpoint(ctx, snapshot.LedgerIndex)
	if err != nil {
		return err
	}
	if checkpoint.accountHash != snapshot.AccountHash {
		return fmt.Errorf("checkpoint %d account hash does not match ledger manifest", snapshot.LedgerIndex)
	}
	kind, keySeq, err := parseBlobKey(checkpoint.blobKey)
	if err != nil {
		return fmt.Errorf("checkpoint %d blob key: %w", snapshot.LedgerIndex, err)
	}
	if kind != kindState || keySeq != snapshot.LedgerIndex {
		return fmt.Errorf("checkpoint %d has mismatched blob key %q", snapshot.LedgerIndex, checkpoint.blobKey)
	}

	object, err := c.blobs.open(ctx, checkpoint.blobKey)
	if err != nil {
		return fmt.Errorf("opening state pack %q: %w", checkpoint.blobKey, err)
	}
	defer func() { retErr = errors.Join(retErr, object.Close()) }()
	if object.size != checkpoint.sizeBytes {
		return fmt.Errorf("state pack %q size is %d, manifest declares %d", checkpoint.blobKey, object.size, checkpoint.sizeBytes)
	}
	expected := statePackExpectation{
		seq:   snapshot.LedgerIndex,
		count: checkpoint.objectCount,
		size:  checkpoint.sizeBytes,
	}
	if err := unpackStateStream(ctx, object, expected, func(index [32]byte, data []byte) error {
		return fn(StateEntry{Index: index, Data: data})
	}); err != nil {
		return fmt.Errorf("decoding state pack %q: %w", checkpoint.blobKey, err)
	}
	return nil
}

func (c *Client) Transactions(ctx context.Context, snapshot *LedgerSnapshot) ([]Transaction, error) {
	if snapshot == nil {
		return nil, errors.New("statecompare: nil ledger snapshot")
	}
	if !snapshot.hasBlob {
		return nil, fmt.Errorf("ledger %d has no transaction blob: %w", snapshot.LedgerIndex, errNotFound)
	}
	kind, _, err := parseBlobKey(snapshot.blobKey)
	if err != nil {
		return nil, fmt.Errorf("ledger %d blob key: %w", snapshot.LedgerIndex, err)
	}
	if kind != kindLedger {
		return nil, fmt.Errorf("ledger %d has non-ledger blob key %q", snapshot.LedgerIndex, snapshot.blobKey)
	}

	pack, err := c.ledgerPack(ctx, snapshot.blobKey)
	if err != nil {
		return nil, err
	}
	record, err := pack.readLedgerAt(int(snapshot.blobOffset), snapshot.TransactionCount)
	if err != nil {
		return nil, fmt.Errorf("decoding ledger pack %q at offset %d: %w", snapshot.blobKey, snapshot.blobOffset, err)
	}
	if record.seq != snapshot.LedgerIndex {
		return nil, fmt.Errorf("ledger pack %q offset %d holds ledger %d, want %d", snapshot.blobKey, snapshot.blobOffset, record.seq, snapshot.LedgerIndex)
	}
	if err := validateLedgerHeader(record.headerBlob, snapshot); err != nil {
		return nil, fmt.Errorf("ledger %d header: %w", snapshot.LedgerIndex, err)
	}
	return validateTransactions(record.txs, snapshot)
}

func (c *Client) ledgerPack(ctx context.Context, key string) (*ledgerPack, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if key == c.cacheKey && c.cachePack != nil {
		pack := c.cachePack
		c.mu.Unlock()
		return pack, nil
	}
	c.mu.Unlock()

	object, err := c.blobs.open(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("opening ledger pack %q: %w", key, err)
	}
	if object.size < packEnvelopeLen || object.size > maxLedgerPackBytes {
		return nil, errors.Join(
			fmt.Errorf("ledger pack %q size %d is outside [%d,%d]", key, object.size, packEnvelopeLen, maxLedgerPackBytes),
			object.Close(),
		)
	}
	data, readErr := io.ReadAll(io.LimitReader(object, object.size+1))
	closeErr := object.Close()
	if readErr != nil || closeErr != nil {
		if readErr != nil {
			readErr = fmt.Errorf("reading ledger pack %q: %w", key, readErr)
		}
		return nil, errors.Join(readErr, closeErr)
	}
	if int64(len(data)) != object.size {
		return nil, fmt.Errorf("ledger pack %q size changed while reading: got %d, want %d", key, len(data), object.size)
	}
	pack, err := indexLedgerPack(data)
	if err != nil {
		return nil, fmt.Errorf("indexing ledger pack %q: %w", key, err)
	}
	_, keySeq, _ := parseBlobKey(key)
	if pack.batchStart != keySeq {
		return nil, fmt.Errorf("ledger pack %q declares batch start %d", key, pack.batchStart)
	}

	c.mu.Lock()
	c.cacheKey = key
	c.cachePack = pack
	c.mu.Unlock()
	return pack, nil
}

func validateLedgerHeader(raw []byte, snapshot *LedgerSnapshot) error {
	parsed, err := header.DeserializeHeader(raw, false)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, header.AddRaw(*parsed, false)) {
		return errors.New("non-canonical raw header")
	}
	if parsed.LedgerIndex != snapshot.LedgerIndex {
		return fmt.Errorf("sequence %d does not match manifest %d", parsed.LedgerIndex, snapshot.LedgerIndex)
	}
	if header.CalculateHash(*parsed) != snapshot.LedgerHash {
		return errors.New("ledger hash does not match manifest")
	}
	if parsed.ParentHash != snapshot.ParentHash || parsed.AccountHash != snapshot.AccountHash || parsed.TxHash != snapshot.TransactionHash {
		return errors.New("header roots do not match manifest")
	}
	if parsed.Drops != snapshot.TotalCoins {
		return fmt.Errorf("total coins %d does not match manifest %d", parsed.Drops, snapshot.TotalCoins)
	}
	if uint64(snapshot.CloseTime) > uint64(^uint32(0)) || protocol.ToRippleTime(parsed.CloseTime) != uint32(snapshot.CloseTime) {
		return errors.New("close time does not match manifest")
	}
	if uint32(parsed.CloseTimeResolution) != snapshot.CloseTimeResolution || parsed.CloseFlags != snapshot.CloseFlags {
		return errors.New("close fields do not match manifest")
	}
	return nil
}

func validateTransactions(records []txRecord, snapshot *LedgerSnapshot) ([]Transaction, error) {
	if uint64(len(records)) != uint64(snapshot.TransactionCount) {
		return nil, fmt.Errorf("ledger %d contains %d transactions, manifest declares %d", snapshot.LedgerIndex, len(records), snapshot.TransactionCount)
	}
	ordered := make([]Transaction, len(records))
	seenHashes := make(map[[32]byte]struct{}, len(records))
	seenIndices := make([]bool, len(records))
	tree := shamap.New(shamap.TypeTransaction)
	for _, record := range records {
		calculated := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), record.txBlob)
		if calculated != record.txHash {
			return nil, fmt.Errorf("ledger %d transaction hash does not match blob", snapshot.LedgerIndex)
		}
		if _, exists := seenHashes[record.txHash]; exists {
			return nil, fmt.Errorf("ledger %d contains duplicate transaction %x", snapshot.LedgerIndex, record.txHash)
		}
		seenHashes[record.txHash] = struct{}{}
		index, ok := txcodec.TransactionIndexFromMetadata(record.metaBlob)
		if !ok {
			return nil, fmt.Errorf("ledger %d transaction %x has invalid TransactionIndex", snapshot.LedgerIndex, record.txHash)
		}
		if uint64(index) >= uint64(len(records)) || seenIndices[index] {
			return nil, fmt.Errorf("ledger %d transaction %x has duplicate or out-of-range TransactionIndex %d", snapshot.LedgerIndex, record.txHash, index)
		}
		seenIndices[index] = true

		vlTx, err := txcodec.EncodeWithVL(record.txBlob)
		if err != nil {
			return nil, fmt.Errorf("encoding transaction %x: %w", record.txHash, err)
		}
		vlMeta, err := txcodec.EncodeWithVL(record.metaBlob)
		if err != nil {
			return nil, fmt.Errorf("encoding metadata for transaction %x: %w", record.txHash, err)
		}
		leaf := append(vlTx, vlMeta...)
		if err := tree.PutWithNodeType(record.txHash, leaf, shamap.NodeTypeTransactionWithMeta); err != nil {
			return nil, fmt.Errorf("building ledger %d transaction tree: %w", snapshot.LedgerIndex, err)
		}
		ordered[index] = Transaction{
			TxIndex:  index,
			TxHash:   record.txHash,
			TxBlob:   bytes.Clone(record.txBlob),
			MetaBlob: bytes.Clone(record.metaBlob),
		}
	}
	if err := tree.SetImmutable(); err != nil {
		return nil, fmt.Errorf("finalizing ledger %d transaction tree: %w", snapshot.LedgerIndex, err)
	}
	root, err := tree.Hash()
	if err != nil {
		return nil, fmt.Errorf("hashing ledger %d transaction tree: %w", snapshot.LedgerIndex, err)
	}
	if root != snapshot.TransactionHash {
		return nil, fmt.Errorf("ledger %d transaction tree root does not match manifest", snapshot.LedgerIndex)
	}
	return ordered, nil
}

func (c *Client) ValidateRange(ctx context.Context, from, to uint32) (bool, uint32, error) {
	return c.manifest.validateRange(ctx, from, to)
}
