package nodestore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

type CacheConfig struct {
	Enabled    bool
	MaxEntries int
	TTL        time.Duration
}

type DatabaseConfig struct {
	PositiveCache CacheConfig
	NegativeCache CacheConfig
}

type nodeCacheAccess interface {
	Get(Hash256) (*Node, bool)
	Put(*Node)
	putOwned(*Node)
	Remove(Hash256)
	Clear()
	Size() int
	Sweep() int
}

func DefaultDatabaseConfig() DatabaseConfig {
	return DatabaseConfig{
		PositiveCache: CacheConfig{
			Enabled:    true,
			MaxEntries: 2000,
			TTL:        time.Hour,
		},
		NegativeCache: CacheConfig{
			Enabled:    true,
			MaxEntries: 100000,
			TTL:        5 * time.Minute,
		},
	}
}

func (c DatabaseConfig) validate() error {
	if err := validateCacheConfig("positive", c.PositiveCache); err != nil {
		return err
	}
	return validateCacheConfig("negative", c.NegativeCache)
}

func validateCacheConfig(name string, config CacheConfig) error {
	if !config.Enabled {
		if config.MaxEntries != 0 || config.TTL != 0 {
			return fmt.Errorf("%w: %s cache is disabled but configured", ErrInvalidConfig, name)
		}
		return nil
	}
	if config.MaxEntries <= 0 {
		return fmt.Errorf("%w: %s cache max entries must be positive", ErrInvalidConfig, name)
	}
	if config.TTL <= 0 {
		return fmt.Errorf("%w: %s cache TTL must be positive", ErrInvalidConfig, name)
	}
	return nil
}

type KVDatabase struct {
	lifecycleMu sync.RWMutex
	closed      bool
	syncGate    chan struct{}

	pruneMu       sync.RWMutex
	writeMu       sync.Mutex
	store         kvstore.KeyValueStore
	cache         nodeCacheAccess
	negativeCache *negativeCache

	storeGeneration atomic.Uint64
	cacheGeneration atomic.Uint64
	stats           struct {
		reads       uint64
		fetchHits   uint64
		cacheHits   uint64
		cacheMisses uint64
		writes      uint64
		readBytes   uint64
		writeBytes  uint64
	}
}

func NewKVDatabase(store kvstore.KeyValueStore, config DatabaseConfig) (*KVDatabase, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: nil store", ErrInvalidConfig)
	}
	if err := config.validate(); err != nil {
		return nil, err
	}

	database := &KVDatabase{
		store:    store,
		syncGate: make(chan struct{}, 1),
	}
	database.syncGate <- struct{}{}
	if config.PositiveCache.Enabled {
		database.cache = newNodeCache(config.PositiveCache.MaxEntries, config.PositiveCache.TTL)
	}
	if config.NegativeCache.Enabled {
		database.negativeCache = newNegativeCache(config.NegativeCache.TTL, config.NegativeCache.MaxEntries)
	}
	return database, nil
}

func (d *KVDatabase) begin(ctx context.Context) error {
	d.lifecycleMu.RLock()
	if d.closed {
		d.lifecycleMu.RUnlock()
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		d.lifecycleMu.RUnlock()
		return err
	}
	return nil
}

// Store validates and persists one node.
func (d *KVDatabase) Store(ctx context.Context, node *Node) error {
	if err := d.begin(ctx); err != nil {
		return err
	}
	defer d.lifecycleMu.RUnlock()
	if err := validateNode(node); err != nil {
		return err
	}

	encoded := encodeNodeData(node)
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	d.pruneMu.RLock()
	err := d.store.Put(node.Hash[:], encoded)
	releaseEncodeBuf(encoded)
	if err != nil {
		d.pruneMu.RUnlock()
		return fmt.Errorf("store failed: %w", err)
	}
	if d.negativeCache != nil {
		d.storeGeneration.Add(1)
		d.negativeCache.Remove(node.Hash)
	}
	if d.cache != nil {
		d.cacheGeneration.Add(1)
	}
	atomic.AddUint64(&d.stats.writes, 1)
	atomic.AddUint64(&d.stats.writeBytes, uint64(len(node.Data)))
	if d.cache != nil {
		d.cache.Put(node)
	}
	d.pruneMu.RUnlock()
	return nil
}

func (d *KVDatabase) Fetch(ctx context.Context, hash Hash256) (*Node, error) {
	if err := d.begin(ctx); err != nil {
		return nil, err
	}
	defer d.lifecycleMu.RUnlock()

	atomic.AddUint64(&d.stats.reads, 1)
	if d.cache != nil {
		if node, found := d.cache.Get(hash); found {
			atomic.AddUint64(&d.stats.cacheHits, 1)
			atomic.AddUint64(&d.stats.fetchHits, 1)
			atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
			return node, nil
		}
		atomic.AddUint64(&d.stats.cacheMisses, 1)
	}
	cacheGeneration := d.cacheGeneration.Load()
	data, err := d.fetchBackend(hash)
	if err != nil || data == nil {
		return nil, err
	}
	node, err := decodeNodeData(hash, data)
	if err != nil {
		return nil, err
	}
	atomic.AddUint64(&d.stats.fetchHits, 1)
	atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
	if d.cache != nil && d.cacheGeneration.Load() == cacheGeneration {
		d.cache.putOwned(node)
	}
	return node, nil
}

// FetchCached returns a node only when it is in the positive cache.
func (d *KVDatabase) FetchCached(ctx context.Context, hash Hash256) (*Node, error) {
	if err := d.begin(ctx); err != nil {
		return nil, err
	}
	defer d.lifecycleMu.RUnlock()

	atomic.AddUint64(&d.stats.reads, 1)
	if d.cache == nil {
		return nil, nil
	}
	node, found := d.cache.Get(hash)
	if !found {
		atomic.AddUint64(&d.stats.cacheMisses, 1)
		return nil, nil
	}
	atomic.AddUint64(&d.stats.cacheHits, 1)
	atomic.AddUint64(&d.stats.fetchHits, 1)
	atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
	return node, nil
}

// FetchDataUncached returns validated node data without populating the positive cache.
func (d *KVDatabase) FetchDataUncached(ctx context.Context, hash Hash256) ([]byte, error) {
	if err := d.begin(ctx); err != nil {
		return nil, err
	}
	defer d.lifecycleMu.RUnlock()

	atomic.AddUint64(&d.stats.reads, 1)
	data, err := d.fetchBackend(hash)
	if err != nil || data == nil {
		return nil, err
	}
	node, err := decodeNodeData(hash, data)
	if err != nil {
		return nil, err
	}
	atomic.AddUint64(&d.stats.fetchHits, 1)
	atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
	return node.Data, nil
}

func (d *KVDatabase) fetchBackend(hash Hash256) ([]byte, error) {
	var storeGeneration uint64
	if d.negativeCache != nil {
		if d.negativeCache.IsMissing(hash) {
			return nil, nil
		}
		storeGeneration = d.storeGeneration.Load()
	}

	data, err := d.store.Get(hash[:])
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, kvstore.ErrNotFound) {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}
	if d.negativeCache != nil {
		d.negativeCache.MarkMissing(hash)
		if d.storeGeneration.Load() != storeGeneration {
			d.negativeCache.Remove(hash)
		}
	}
	return nil, nil
}

// StoreBatch validates and atomically persists a collection of nodes.
func (d *KVDatabase) StoreBatch(ctx context.Context, nodes []*Node) (err error) {
	if err := d.begin(ctx); err != nil {
		return err
	}
	defer d.lifecycleMu.RUnlock()
	for _, node := range nodes {
		if err := validateNode(node); err != nil {
			return err
		}
	}
	if len(nodes) == 0 {
		return nil
	}

	batch, err := d.store.NewBatch()
	if err != nil {
		return fmt.Errorf("create store batch: %w", err)
	}
	defer func() {
		err = errors.Join(err, batch.Close())
	}()
	for _, node := range nodes {
		encoded := encodeNodeData(node)
		putErr := batch.Put(node.Hash[:], encoded)
		releaseEncodeBuf(encoded)
		if putErr != nil {
			return fmt.Errorf("store batch failed: %w", putErr)
		}
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	d.pruneMu.RLock()
	writeErr := batch.Write()
	if writeErr != nil {
		d.pruneMu.RUnlock()
		return fmt.Errorf("store batch commit failed: %w", writeErr)
	}
	if d.negativeCache != nil {
		d.storeGeneration.Add(1)
		for _, node := range nodes {
			d.negativeCache.Remove(node.Hash)
		}
	}
	if d.cache != nil {
		d.cacheGeneration.Add(1)
	}
	for _, node := range nodes {
		atomic.AddUint64(&d.stats.writes, 1)
		atomic.AddUint64(&d.stats.writeBytes, uint64(len(node.Data)))
		if d.cache != nil {
			d.cache.Put(node)
		}
	}
	d.pruneMu.RUnlock()
	return nil
}

func (d *KVDatabase) Sweep() error {
	d.lifecycleMu.RLock()
	if d.closed {
		d.lifecycleMu.RUnlock()
		return ErrClosed
	}
	defer d.lifecycleMu.RUnlock()
	if d.cache != nil {
		d.cache.Sweep()
	}
	if d.negativeCache != nil {
		d.negativeCache.Sweep()
	}
	return nil
}

func (d *KVDatabase) Stats() Statistics {
	stats := Statistics{
		Reads:       atomic.LoadUint64(&d.stats.reads),
		FetchHits:   atomic.LoadUint64(&d.stats.fetchHits),
		CacheHits:   atomic.LoadUint64(&d.stats.cacheHits),
		CacheMisses: atomic.LoadUint64(&d.stats.cacheMisses),
		ReadBytes:   atomic.LoadUint64(&d.stats.readBytes),
		Writes:      atomic.LoadUint64(&d.stats.writes),
		WriteBytes:  atomic.LoadUint64(&d.stats.writeBytes),
	}
	if d.cache != nil {
		stats.CacheSize = uint64(d.cache.Size())
	}
	return stats
}

// Sync makes all preceding backend writes durable.
func (d *KVDatabase) Sync(ctx context.Context) error {
	d.lifecycleMu.RLock()
	if d.closed {
		d.lifecycleMu.RUnlock()
		return ErrClosed
	}
	d.lifecycleMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.syncGate:
	}

	d.lifecycleMu.RLock()
	if d.closed {
		d.lifecycleMu.RUnlock()
		d.syncGate <- struct{}{}
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		d.lifecycleMu.RUnlock()
		d.syncGate <- struct{}{}
		return err
	}

	done := make(chan error, 1)
	go func() {
		err := d.store.Sync()
		d.lifecycleMu.RUnlock()
		d.syncGate <- struct{}{}
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		select {
		case err := <-done:
			return err
		default:
			return ctx.Err()
		}
	}
}

// Close releases caches and closes the underlying store.
func (d *KVDatabase) Close() error {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if d.cache != nil {
		d.cache.Clear()
	}
	if d.negativeCache != nil {
		d.negativeCache.Clear()
	}
	return d.store.Close()
}

var _ Database = (*KVDatabase)(nil)
