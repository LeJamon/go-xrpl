package pebble

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	cockroachpebble "github.com/cockroachdb/pebble"
)

const generationStateSuffix = ".generations.json"
const legacyGenerationStateVersion = 1
const generationStateVersion = 2
const generationMarkerName = ".goxrpl-generation.json"
const generationMarkerVersion = 1
const rotatingStoreMutationStripes = 256

var errGenerationNotOwned = errors.New("kvstore/pebble: generation is not owned by this store")

// ErrLegacyRotationState reports a version-1 rotation manifest that must be
// explicitly migrated while the store is offline.
var ErrLegacyRotationState = errors.New("kvstore/pebble: legacy rotation state requires explicit migration")

type generationState struct {
	Version       int    `json:"version"`
	OwnerID       string `json:"owner_id"`
	Writable      string `json:"writable"`
	Archive       string `json:"archive"`
	LastRotated   uint32 `json:"last_rotated,omitempty"`
	MinimumOnline uint32 `json:"minimum_online,omitempty"`
}

type legacyGenerationState struct {
	Version       int    `json:"version"`
	Writable      string `json:"writable"`
	Archive       string `json:"archive"`
	LastRotated   uint32 `json:"last_rotated,omitempty"`
	MinimumOnline uint32 `json:"minimum_online,omitempty"`
}

type generationMarker struct {
	Version int    `json:"version"`
	OwnerID string `json:"owner_id"`
}

func validateGenerationState(state generationState) error {
	if state.Version != generationStateVersion {
		return fmt.Errorf(
			"kvstore/pebble: unsupported generation state version %d",
			state.Version,
		)
	}
	if err := validateGenerationName(state.Writable); err != nil {
		return fmt.Errorf("kvstore/pebble: invalid writable generation: %w", err)
	}
	if err := validateGenerationName(state.Archive); err != nil {
		return fmt.Errorf("kvstore/pebble: invalid archive generation: %w", err)
	}
	if state.Writable == state.Archive {
		return errors.New("kvstore/pebble: writable and archive generations must differ")
	}
	if err := validateOwnerID(state.OwnerID); err != nil {
		return err
	}
	return validateGenerationBoundaries(state.LastRotated, state.MinimumOnline)
}

func validateLegacyGenerationState(state legacyGenerationState) error {
	if state.Version != legacyGenerationStateVersion {
		return fmt.Errorf(
			"kvstore/pebble: unsupported legacy generation state version %d",
			state.Version,
		)
	}
	if err := validateGenerationName(state.Writable); err != nil {
		return fmt.Errorf("kvstore/pebble: invalid writable generation: %w", err)
	}
	if err := validateGenerationName(state.Archive); err != nil {
		return fmt.Errorf("kvstore/pebble: invalid archive generation: %w", err)
	}
	if state.Writable == state.Archive {
		return errors.New("kvstore/pebble: writable and archive generations must differ")
	}
	return validateGenerationBoundaries(state.LastRotated, state.MinimumOnline)
}

func validateGenerationName(name string) error {
	if !filepath.IsLocal(name) || name != filepath.Base(name) || name == "." {
		return fmt.Errorf("invalid generation path %q", name)
	}
	return nil
}

func validateOwnerID(ownerID string) error {
	decoded, err := hex.DecodeString(ownerID)
	if err != nil || len(decoded) != 16 || ownerID != strings.ToLower(ownerID) {
		return errors.New("kvstore/pebble: invalid generation owner")
	}
	return nil
}

func newOwnerID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("kvstore/pebble: generate owner ID: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

func validateGenerationBoundaries(lastRotated, minimumOnline uint32) error {
	if (lastRotated == 0) != (minimumOnline == 0) || minimumOnline > lastRotated {
		return errors.New("kvstore/pebble: invalid generation boundaries")
	}
	return nil
}

// RotatingStore stores new records in one Pebble generation and falls back to
// one archive generation on reads. Promote explicitly copies an archive record
// into the writable generation for online-delete preservation.
type RotatingStore struct {
	// Lock order is mu, archiveMu when needed, then mutation stripes in
	// ascending order. Put-only operations skip archiveMu.
	mu        sync.RWMutex
	archiveMu sync.RWMutex
	rotateMu  sync.Mutex
	mutations [rotatingStoreMutationStripes]sync.Mutex

	basePath              string
	statePath             string
	options               Options
	blockCache            *cockroachpebble.Cache
	ownerID               string
	unpublishedBaseMarker bool

	writable       *Store
	writablePath   string
	archive        *Store
	archivePath    string
	lastRotated    uint32
	minimumOnline  uint32
	syncDir        func(string) error
	syncGeneration func(*Store) error

	closed bool
}

// HasRotationState reports whether path has a durable generation manifest.
func HasRotationState(path string) (bool, error) {
	basePath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false, fmt.Errorf("kvstore/pebble: resolve rotating path: %w", err)
	}
	info, err := os.Lstat(basePath + generationStateSuffix)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("kvstore/pebble: generation state must not be a symlink")
		}
		if !info.Mode().IsRegular() {
			return false, errors.New("kvstore/pebble: generation state is not a regular file")
		}
		return true, nil
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	if err := rejectUnmanifestedGenerations(basePath); err != nil {
		return false, err
	}
	return false, nil
}

func rejectUnmanifestedGenerations(basePath string) error {
	marked, err := hasGenerationMarker(basePath)
	if err != nil {
		return fmt.Errorf("kvstore/pebble: inspect unmanifested base generation: %w", err)
	}
	if marked {
		return errors.New("kvstore/pebble: generation state is missing but the base generation has an ownership marker")
	}

	parent := filepath.Dir(basePath)
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("kvstore/pebble: inspect unmanifested generations: %w", err)
	}
	prefix := "." + filepath.Base(basePath) + "-generation-"
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		generationPath := filepath.Join(parent, entry.Name())
		marked, err := hasGenerationMarker(generationPath)
		if err != nil {
			return fmt.Errorf("kvstore/pebble: inspect unmanifested generation %s: %w", generationPath, err)
		}
		if marked {
			return fmt.Errorf(
				"kvstore/pebble: generation state is missing but generation %s has an ownership marker",
				generationPath,
			)
		}
	}
	return nil
}

func hasGenerationMarker(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("generation path must not be a symlink")
	}
	if !info.IsDir() {
		return false, errors.New("generation path is not a directory")
	}
	_, found, err := readGenerationMarker(path)
	return found, err
}

// MigrateLegacyRotationState upgrades a version-1 rotation manifest with
// durable ownership markers. The store must be offline, and the operator must
// verify that both manifest paths belong to this store before calling it.
func MigrateLegacyRotationState(path string) error {
	if path == "" {
		return errors.New("kvstore/pebble: rotating store path is empty")
	}
	basePath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("kvstore/pebble: resolve rotating path: %w", err)
	}
	r := &RotatingStore{
		basePath:  basePath,
		statePath: basePath + generationStateSuffix,
		syncDir:   syncDirectory,
	}
	data, err := readGenerationStateFile(r.statePath)
	if err != nil {
		return err
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return fmt.Errorf("kvstore/pebble: decode generation state: %w", err)
	}
	if header.Version == generationStateVersion {
		state, found, err := r.loadState()
		if err != nil {
			return err
		}
		if !found {
			return errors.New("kvstore/pebble: generation state is unavailable")
		}
		r.ownerID = state.OwnerID
		for _, name := range []string{state.Writable, state.Archive} {
			generationPath, err := r.resolveGenerationPath(name)
			if err != nil {
				return err
			}
			if err := r.verifyOwnedGeneration(generationPath); err != nil {
				return fmt.Errorf("kvstore/pebble: verify generation %s: %w", generationPath, err)
			}
		}
		return nil
	}
	if header.Version != legacyGenerationStateVersion {
		return fmt.Errorf(
			"kvstore/pebble: unsupported generation state version %d",
			header.Version,
		)
	}

	var legacy legacyGenerationState
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("kvstore/pebble: decode legacy generation state: %w", err)
	}
	if err := validateLegacyGenerationState(legacy); err != nil {
		return err
	}
	paths := make([]string, 0, 2)
	for _, name := range []string{legacy.Writable, legacy.Archive} {
		generationPath, err := r.resolveGenerationPath(name)
		if err != nil {
			return err
		}
		paths = append(paths, generationPath)
	}

	markers := make([]generationMarker, len(paths))
	marked := make([]bool, len(paths))
	for i, generationPath := range paths {
		marker, found, err := readGenerationMarker(generationPath)
		if err != nil {
			return fmt.Errorf("kvstore/pebble: inspect legacy generation %s: %w", generationPath, err)
		}
		markers[i] = marker
		marked[i] = found
	}
	for i := range markers {
		if !marked[i] {
			continue
		}
		if r.ownerID == "" {
			r.ownerID = markers[i].OwnerID
			continue
		}
		if r.ownerID != markers[i].OwnerID {
			return errors.New("kvstore/pebble: legacy generations have conflicting ownership markers")
		}
	}
	if r.ownerID == "" {
		r.ownerID, err = newOwnerID()
		if err != nil {
			return err
		}
	}
	for i, generationPath := range paths {
		if marked[i] {
			continue
		}
		if err := r.writeGenerationMarker(generationPath); err != nil {
			return fmt.Errorf("kvstore/pebble: mark legacy generation %s: %w", generationPath, err)
		}
	}
	for _, generationPath := range paths {
		if err := r.verifyOwnedGeneration(generationPath); err != nil {
			return fmt.Errorf("kvstore/pebble: verify migrated generation %s: %w", generationPath, err)
		}
	}
	_, err = r.saveState(generationState{
		Version:       generationStateVersion,
		OwnerID:       r.ownerID,
		Writable:      legacy.Writable,
		Archive:       legacy.Archive,
		LastRotated:   legacy.LastRotated,
		MinimumOnline: legacy.MinimumOnline,
	})
	if err != nil {
		return fmt.Errorf("kvstore/pebble: publish migrated generation state: %w", err)
	}
	return nil
}

// NewRotating opens a two-generation Pebble store. An existing database at
// path becomes the initial writable generation, so enabling online deletion
// does not copy or rename an operator's current database.
func NewRotating(path string, options Options) (*RotatingStore, error) {
	if path == "" {
		return nil, errors.New("kvstore/pebble: rotating store path is empty")
	}
	resolved, perGenerationOptions, err := resolveRotatingOptions(options)
	if err != nil {
		return nil, err
	}
	r, found, err := prepareRotatingStore(path, perGenerationOptions)
	if err != nil {
		return nil, err
	}
	if err := r.openGenerations(resolved.BlockCacheBytes, found, newWithCache); err != nil {
		return nil, err
	}
	return r, nil
}

func prepareRotatingStore(path string, options Options) (*RotatingStore, bool, error) {
	basePath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, false, fmt.Errorf("kvstore/pebble: resolve rotating path: %w", err)
	}
	r := &RotatingStore{
		basePath:  basePath,
		statePath: basePath + generationStateSuffix,
		options:   options,
		syncDir:   syncDirectory,
		syncGeneration: func(store *Store) error {
			return store.Sync()
		},
	}

	state, found, err := r.loadState()
	if err != nil {
		return nil, false, err
	}
	if !found {
		if err := rejectUnmanifestedGenerations(basePath); err != nil {
			return nil, false, err
		}
	}
	parent := filepath.Dir(basePath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, false, fmt.Errorf("kvstore/pebble: create rotating parent: %w", err)
	}
	if found {
		r.ownerID = state.OwnerID
		r.writablePath, err = r.resolveGenerationPath(state.Writable)
		if err != nil {
			return nil, false, err
		}
		r.archivePath, err = r.resolveGenerationPath(state.Archive)
		if err != nil {
			return nil, false, err
		}
		for _, generationPath := range []string{r.writablePath, r.archivePath} {
			if verifyErr := r.verifyOwnedGeneration(generationPath); verifyErr != nil {
				return nil, false, fmt.Errorf(
					"kvstore/pebble: generation %s is unavailable: %w",
					generationPath,
					verifyErr,
				)
			}
		}
		r.lastRotated = state.LastRotated
		r.minimumOnline = state.MinimumOnline
	} else {
		r.writablePath = basePath
		r.ownerID, err = r.prepareInitialGeneration()
		if err != nil {
			return nil, false, err
		}
		r.archivePath, err = r.newGenerationPath()
		if err != nil {
			return nil, false, errors.Join(err, r.rollbackBaseMarker())
		}
	}
	return r, found, nil
}

type generationOpener func(string, *cockroachpebble.Cache, Options) (*Store, error)

func (r *RotatingStore) openGenerations(
	blockCacheBytes int64,
	existingState bool,
	openGeneration generationOpener,
) (resultErr error) {
	published := existingState
	r.blockCache = cockroachpebble.NewCache(blockCacheBytes)
	defer func() {
		if resultErr != nil {
			r.blockCache.Unref()
			if !published {
				resultErr = errors.Join(resultErr, r.rollbackBaseMarker())
			}
		}
	}()

	var err error
	r.writable, err = openGeneration(r.writablePath, r.blockCache, r.options)
	if err != nil {
		var cleanupErr error
		if !existingState {
			cleanupErr = r.removeOwnedGeneration(r.archivePath)
		}
		return errors.Join(err, cleanupErr)
	}
	r.archive, err = openGeneration(r.archivePath, r.blockCache, r.options)
	if err != nil {
		cleanupErr := r.writable.Close()
		if !existingState {
			cleanupErr = errors.Join(cleanupErr, r.removeOwnedGeneration(r.archivePath))
		}
		return errors.Join(err, cleanupErr)
	}
	if !existingState {
		statePublished, saveErr := r.saveState(generationState{
			Version:  generationStateVersion,
			OwnerID:  r.ownerID,
			Writable: filepath.Base(r.writablePath),
			Archive:  filepath.Base(r.archivePath),
		})
		if statePublished {
			published = true
			r.unpublishedBaseMarker = false
		}
		if saveErr != nil {
			cleanupErr := errors.Join(r.archive.Close(), r.writable.Close())
			if !statePublished {
				cleanupErr = errors.Join(cleanupErr, r.removeOwnedGeneration(r.archivePath))
			}
			return errors.Join(saveErr, cleanupErr)
		}
	}
	if err := r.cleanupOrphans(); err != nil {
		_ = r.archive.Close()
		_ = r.writable.Close()
		return err
	}
	return nil
}

// Get returns key from the writable generation or falls back to the archive.
func (r *RotatingStore) Get(key []byte) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, kvstore.ErrClosed
	}
	return r.getLocked(key, false)
}

// CanRotateWithoutRefresh reports whether the archive is empty.
func (r *RotatingStore) CanRotateWithoutRefresh() (canRotate bool, resultErr error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return false, kvstore.ErrClosed
	}
	iter, err := r.archive.NewIterator(nil, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, iter.Close())
	}()
	hasRecords := iter.Next()
	if err := iter.Error(); err != nil {
		return false, err
	}
	return !hasRecords, nil
}

// Promote fetches key and copies an archive hit into the writable generation.
func (r *RotatingStore) Promote(key []byte) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, kvstore.ErrClosed
	}
	mutation := &r.mutations[mutationStripe(key)]
	mutation.Lock()
	defer mutation.Unlock()
	return r.getLocked(key, true)
}

// PromoteBatch resolves and promotes a bounded hash-sorted group.
func (r *RotatingStore) PromoteBatch(
	keys [][]byte,
	maxBytes int,
) (promotions []kvstore.Promotion, stats kvstore.PromotionStats, resultErr error) {
	stats.Requested = len(keys)
	if len(keys) == 0 {
		return nil, stats, nil
	}
	if maxBytes <= 0 {
		return nil, stats, errors.New("kvstore/pebble: promotion byte limit must be positive")
	}

	sorted := make([][]byte, len(keys))
	for i, key := range keys {
		sorted[i] = append([]byte(nil), key...)
	}
	sort.SliceStable(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, stats, kvstore.ErrClosed
	}
	// Archive deletion must wait through commit, but ordinary writable stores
	// can proceed while archive blocks are read and decompressed.
	r.archiveMu.RLock()
	defer r.archiveMu.RUnlock()
	prefetched, err := r.prefetchPromotion(sorted, maxBytes)
	if err != nil {
		return nil, stats, err
	}
	sorted = sorted[:len(prefetched)]
	lockedMutations := r.lockMutations(sorted)
	defer r.unlockMutations(&lockedMutations)

	writable, err := r.writable.newPointIterator()
	if err != nil {
		return nil, stats, err
	}
	defer func() { resultErr = errors.Join(resultErr, writable.Close()) }()

	batch, err := r.writable.NewBatch()
	if err != nil {
		return nil, stats, err
	}
	defer func() { resultErr = errors.Join(resultErr, batch.Close()) }()

	promotions = make([]kvstore.Promotion, 0, len(sorted))
	for index, key := range sorted {
		remaining := maxBytes - stats.BufferedBytes
		value, found, tooLarge, err := writable.get(key, remaining, len(promotions) == 0)
		if err != nil {
			return nil, stats, err
		}
		if tooLarge {
			break
		}
		if found {
			stats.WritableHits++
			stats.BufferedBytes += len(value)
			promotions = append(promotions, kvstore.Promotion{
				Key: append([]byte(nil), key...), Value: value, Found: true,
			})
			stats.Consumed++
			continue
		}
		stats.WritableMisses++
		candidate := prefetched[index]
		value, found, err = candidate.value, candidate.found, candidate.err
		tooLarge = found && len(value) > remaining && len(promotions) > 0
		if err != nil {
			return nil, stats, err
		}
		if tooLarge {
			break
		}
		if !found {
			stats.ArchiveMisses++
			promotions = append(promotions, kvstore.Promotion{Key: append([]byte(nil), key...)})
			stats.Consumed++
			continue
		}
		stats.ArchiveHits++
		stats.BufferedBytes += len(value)
		if err := batch.Put(key, value); err != nil {
			return nil, stats, err
		}
		stats.Promoted++
		stats.PromotedBytes += len(value)
		promotions = append(promotions, kvstore.Promotion{
			Key: append([]byte(nil), key...), Value: value, Found: true,
		})
		stats.Consumed++
	}
	if stats.Promoted > 0 {
		if err := batch.Write(); err != nil {
			return nil, stats, fmt.Errorf("kvstore/pebble: promote archive batch: %w", err)
		}
		stats.Batches = 1
	}
	return promotions, stats, nil
}

type promotionPrefetch struct {
	value []byte
	found bool
	err   error
}

// The prefetched payload and the final results each have their own byte budget.
// A shorter prefix is valid even when writable precedence would leave room for
// more results; the caller retries the remaining hashes.
func (r *RotatingStore) prefetchPromotion(keys [][]byte, maxBytes int) ([]promotionPrefetch, error) {
	archive, err := r.archive.newPointIterator()
	if err != nil {
		return nil, err
	}
	records := make([]promotionPrefetch, 0, len(keys))
	buffered := 0
	for _, key := range keys {
		value, found, tooLarge, readErr := archive.get(key, maxBytes-buffered, len(records) == 0)
		if tooLarge {
			break
		}
		records = append(records, promotionPrefetch{value: value, found: found, err: readErr})
		if readErr != nil {
			break
		}
		buffered += len(value)
	}
	closeErr := archive.Close()
	if len(records) > 0 && records[len(records)-1].err != nil {
		// Lazy read errors also reach Close. Keep the error with its key so a
		// newer writable value can take precedence over the failed archive read.
		last := &records[len(records)-1]
		last.err = errors.Join(last.err, closeErr)
		return records, nil
	}
	return records, closeErr
}

// CacheMetrics returns a point-in-time snapshot of the shared block cache.
func (r *RotatingStore) CacheMetrics() kvstore.CacheMetrics {
	metrics := r.blockCache.Metrics()
	return kvstore.CacheMetrics{Hits: metrics.Hits, Misses: metrics.Misses}
}

// IOMetrics returns a point-in-time snapshot of Pebble persistence counters.
func (r *RotatingStore) IOMetrics() kvstore.IOMetrics {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.writable == nil || r.archive == nil {
		return kvstore.IOMetrics{}
	}
	var metrics kvstore.IOMetrics
	addIOMetrics(&metrics, r.writable.db.Metrics())
	addIOMetrics(&metrics, r.archive.db.Metrics())
	return metrics
}

func addIOMetrics(result *kvstore.IOMetrics, metrics *cockroachpebble.Metrics) {
	result.LogicalBytesWritten += metrics.WAL.BytesIn
	result.WALBytesWritten += metrics.WAL.BytesWritten
	result.MemTableBytes += metrics.MemTable.Size
	for _, level := range metrics.Levels {
		result.FlushBytesWritten += level.BytesFlushed
		result.CompactionBytesRead += level.BytesRead
		result.CompactionBytesWritten += level.BytesCompacted
		if level.Size > 0 {
			result.SSTableBytes += uint64(level.Size)
		}
	}
}

func (r *RotatingStore) lockMutations(keys [][]byte) [rotatingStoreMutationStripes]bool {
	var selected [rotatingStoreMutationStripes]bool
	for _, key := range keys {
		selected[mutationStripe(key)] = true
	}
	for index := range selected {
		if selected[index] {
			r.mutations[index].Lock()
		}
	}
	return selected
}

func (r *RotatingStore) unlockMutations(selected *[rotatingStoreMutationStripes]bool) {
	for index := len(selected) - 1; index >= 0; index-- {
		if selected[index] {
			r.mutations[index].Unlock()
		}
	}
}

func mutationStripe(key []byte) int {
	const (
		offset = uint32(2166136261)
		prime  = uint32(16777619)
	)
	hash := offset
	for _, value := range key {
		hash ^= uint32(value)
		hash *= prime
	}
	return int(hash % rotatingStoreMutationStripes)
}

func (r *RotatingStore) getLocked(key []byte, promote bool) ([]byte, error) {
	data, err := r.writable.Get(key)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, kvstore.ErrNotFound) {
		return nil, err
	}
	data, err = r.archive.Get(key)
	if err != nil {
		return nil, err
	}
	if promote {
		if err := r.writable.Put(key, data); err != nil {
			return nil, fmt.Errorf("kvstore/pebble: promote archive record: %w", err)
		}
	}
	return data, nil
}

// Put writes key and value to the writable generation.
func (r *RotatingStore) Put(key []byte, value []byte) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return kvstore.ErrClosed
	}
	mutation := &r.mutations[mutationStripe(key)]
	mutation.Lock()
	defer mutation.Unlock()
	return r.writable.Put(key, value)
}

// NewBatch returns a batch that applies operations to the rotating store.
func (r *RotatingStore) NewBatch() (kvstore.Batch, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, kvstore.ErrClosed
	}
	return &rotatingBatch{store: r}, nil
}

// NewIterator returns a merged iterator over both generations.
func (r *RotatingStore) NewIterator(prefix []byte, start []byte) (kvstore.Iterator, error) {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return nil, kvstore.ErrClosed
	}
	writable, err := r.writable.NewIterator(prefix, start)
	if err != nil {
		r.mu.RUnlock()
		return nil, err
	}
	archive, err := r.archive.NewIterator(prefix, start)
	if err != nil {
		closeErr := writable.Close()
		r.mu.RUnlock()
		return nil, errors.Join(err, closeErr)
	}
	return &rotatingIterator{
		store:    r,
		writable: writable,
		archive:  archive,
	}, nil
}

// Sync flushes both generations.
func (r *RotatingStore) Sync() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return kvstore.ErrClosed
	}
	return errors.Join(
		r.syncGeneration(r.writable),
		r.syncGeneration(r.archive),
	)
}

// Close closes both generations.
func (r *RotatingStore) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	err := errors.Join(r.writable.Close(), r.archive.Close())
	r.blockCache.Unref()
	return err
}

// RotationState returns the boundary committed with the active generation pair.
func (r *RotatingStore) RotationState() (uint32, uint32) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastRotated, r.minimumOnline
}

// RotationIdentity returns a path-free snapshot of the durable manifest.
func (r *RotatingStore) RotationIdentity() (kvstore.RotationIdentity, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return kvstore.RotationIdentity{}, kvstore.ErrClosed
	}
	owner, err := hex.DecodeString(r.ownerID)
	if err != nil || len(owner) != 16 {
		return kvstore.RotationIdentity{}, errors.New("kvstore/pebble: invalid rotation owner ID")
	}
	identity := kvstore.RotationIdentity{
		WritableID:    sha256.Sum256([]byte(filepath.Base(r.writablePath))),
		ArchiveID:     sha256.Sum256([]byte(filepath.Base(r.archivePath))),
		LastRotated:   r.lastRotated,
		MinimumOnline: r.minimumOnline,
	}
	copy(identity.OwnerID[:], owner)
	return identity, nil
}

// Rotate durably publishes a fresh writable generation before retiring the
// former archive. No operation can observe the in-memory swap before the
// generation manifest is durable.
func (r *RotatingStore) Rotate(lastRotated, minimumOnline uint32) (bool, error) {
	if lastRotated == 0 || minimumOnline == 0 {
		return false, errors.New("kvstore/pebble: rotation boundaries must be non-zero")
	}
	if err := validateGenerationBoundaries(lastRotated, minimumOnline); err != nil {
		return false, err
	}
	r.rotateMu.Lock()
	defer r.rotateMu.Unlock()

	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return false, kvstore.ErrClosed
	}
	completed := r.lastRotated
	completedMinimum := r.minimumOnline
	r.mu.RUnlock()
	if lastRotated <= completed {
		if lastRotated == completed && minimumOnline != completedMinimum {
			return false, fmt.Errorf(
				"kvstore/pebble: rotation boundary %d has minimum online %d, not %d",
				lastRotated,
				completedMinimum,
				minimumOnline,
			)
		}
		return true, nil
	}
	if completedMinimum != 0 && minimumOnline < completedMinimum {
		return false, fmt.Errorf(
			"kvstore/pebble: minimum online %d precedes committed minimum %d",
			minimumOnline,
			completedMinimum,
		)
	}

	newPath, err := r.newGenerationPath()
	if err != nil {
		return false, err
	}
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		_ = r.removeOwnedGeneration(newPath)
		return false, kvstore.ErrClosed
	}
	newWritable, err := newWithCache(newPath, r.blockCache, r.options)
	r.mu.RUnlock()
	if err != nil {
		_ = r.removeOwnedGeneration(newPath)
		return false, err
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = newWritable.Close()
		_ = r.removeOwnedGeneration(newPath)
		return false, kvstore.ErrClosed
	}
	if err := r.writable.Sync(); err != nil {
		r.mu.Unlock()
		_ = newWritable.Close()
		_ = r.removeOwnedGeneration(newPath)
		return false, err
	}

	oldArchive := r.archive
	oldArchivePath := r.archivePath
	oldWritable := r.writable
	oldWritablePath := r.writablePath
	oldLastRotated := r.lastRotated
	oldMinimumOnline := r.minimumOnline

	r.writable = newWritable
	r.writablePath = newPath
	r.archive = oldWritable
	r.archivePath = oldWritablePath
	r.lastRotated = lastRotated
	r.minimumOnline = minimumOnline
	state := generationState{
		Version:       generationStateVersion,
		OwnerID:       r.ownerID,
		Writable:      filepath.Base(newPath),
		Archive:       filepath.Base(oldWritablePath),
		LastRotated:   lastRotated,
		MinimumOnline: minimumOnline,
	}
	if err := validateGenerationState(state); err != nil {
		r.writable = oldWritable
		r.writablePath = oldWritablePath
		r.archive = oldArchive
		r.archivePath = oldArchivePath
		r.lastRotated = oldLastRotated
		r.minimumOnline = oldMinimumOnline
		r.mu.Unlock()
		_ = newWritable.Close()
		_ = r.removeOwnedGeneration(newPath)
		return false, err
	}
	published, saveErr := r.saveState(state)
	if saveErr != nil && !published {
		r.writable = oldWritable
		r.writablePath = oldWritablePath
		r.archive = oldArchive
		r.archivePath = oldArchivePath
		r.lastRotated = oldLastRotated
		r.minimumOnline = oldMinimumOnline
		r.mu.Unlock()
		_ = newWritable.Close()
		_ = r.removeOwnedGeneration(newPath)
		return false, saveErr
	}
	if saveErr != nil {
		r.mu.Unlock()
		closeErr := oldArchive.Close()
		return true, errors.Join(saveErr, closeErr)
	}
	r.mu.Unlock()

	cleanupErr := errors.Join(oldArchive.Close(), r.removeOwnedGeneration(oldArchivePath))
	return true, cleanupErr
}

func (r *RotatingStore) loadState() (generationState, bool, error) {
	data, err := readGenerationStateFile(r.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return generationState{}, false, nil
		}
		return generationState{}, false, err
	}
	var state generationState
	if err := json.Unmarshal(data, &state); err != nil {
		return generationState{}, false, fmt.Errorf("kvstore/pebble: decode generation state: %w", err)
	}
	if state.Version == legacyGenerationStateVersion {
		return generationState{}, false, fmt.Errorf(
			"%w: close the store, verify both manifest generations, then run the offline rotation-state migration for %q",
			ErrLegacyRotationState,
			r.basePath,
		)
	}
	if err := validateGenerationState(state); err != nil {
		return generationState{}, false, err
	}
	return state, true, nil
}

func readGenerationStateFile(statePath string) ([]byte, error) {
	info, err := os.Lstat(statePath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("kvstore/pebble: generation state must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("kvstore/pebble: generation state is not a regular file")
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("kvstore/pebble: read generation state: %w", err)
	}
	return data, nil
}

func (r *RotatingStore) saveState(state generationState) (bool, error) {
	if err := validateGenerationState(state); err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(r.statePath)
	tmp, err := os.CreateTemp(dir, ".pebble-generations-*")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, r.statePath); err != nil {
		return false, err
	}
	return true, r.syncDir(dir)
}

func syncDirectory(dir string) error {
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	return dirFile.Sync()
}

func (r *RotatingStore) resolveGenerationPath(name string) (string, error) {
	if err := validateGenerationName(name); err != nil {
		return "", fmt.Errorf("kvstore/pebble: invalid generation path %q", name)
	}
	parent := filepath.Clean(filepath.Dir(r.basePath))
	resolved := filepath.Clean(filepath.Join(parent, name))
	relative, err := filepath.Rel(parent, resolved)
	if err != nil || !filepath.IsLocal(relative) || relative != name || filepath.Dir(resolved) != parent {
		return "", fmt.Errorf("kvstore/pebble: invalid generation path %q", name)
	}
	baseName := filepath.Base(r.basePath)
	if name != baseName && !strings.HasPrefix(name, "."+baseName+"-generation-") {
		return "", fmt.Errorf("kvstore/pebble: invalid generation path %q", name)
	}
	return resolved, nil
}

func (r *RotatingStore) newGenerationPath() (string, error) {
	prefix := "." + filepath.Base(r.basePath) + "-generation-"
	path, err := os.MkdirTemp(filepath.Dir(r.basePath), prefix)
	if err != nil {
		return "", fmt.Errorf("kvstore/pebble: create generation: %w", err)
	}
	if err := r.writeGenerationMarker(path); err != nil {
		if r.verifyOwnedGeneration(path) == nil {
			_ = r.removeOwnedGeneration(path)
		} else {
			_ = os.Remove(path)
		}
		return "", err
	}
	return path, nil
}

func (r *RotatingStore) cleanupOrphans() error {
	parent := filepath.Dir(r.basePath)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	prefix := "." + filepath.Base(r.basePath) + "-generation-"
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		path := filepath.Join(parent, entry.Name())
		if path == r.writablePath || path == r.archivePath {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("kvstore/pebble: orphan generation %s must not be a symlink", path)
		}
		if err := r.removeOwnedGeneration(path); err != nil && !errors.Is(err, errGenerationNotOwned) {
			return fmt.Errorf("kvstore/pebble: remove orphan generation %s: %w", path, err)
		}
	}
	return nil
}

func (r *RotatingStore) prepareInitialGeneration() (string, error) {
	info, err := os.Lstat(r.basePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("kvstore/pebble: inspect initial generation: %w", err)
		}
		if err := os.Mkdir(r.basePath, 0o755); err != nil {
			return "", fmt.Errorf("kvstore/pebble: create initial generation: %w", err)
		}
	} else {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("kvstore/pebble: initial generation must not be a symlink")
		}
		if !info.IsDir() {
			return "", errors.New("kvstore/pebble: initial generation is not a directory")
		}
	}

	_, found, err := readGenerationMarker(r.basePath)
	if err != nil {
		return "", err
	}
	if found {
		return "", errors.New("kvstore/pebble: generation state is missing but the base generation has an ownership marker")
	}
	ownerID, err := newOwnerID()
	if err != nil {
		return "", err
	}
	r.ownerID = ownerID
	r.unpublishedBaseMarker = true
	if err := r.writeGenerationMarker(r.basePath); err != nil {
		return "", errors.Join(err, r.rollbackBaseMarker())
	}
	return ownerID, nil
}

func (r *RotatingStore) rollbackBaseMarker() error {
	if !r.unpublishedBaseMarker {
		return nil
	}
	marker, found, err := readGenerationMarker(r.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := syncDirectory(filepath.Dir(r.basePath)); err != nil {
				return fmt.Errorf("kvstore/pebble: sync initial generation marker rollback: %w", err)
			}
			r.unpublishedBaseMarker = false
			return nil
		}
		return fmt.Errorf("kvstore/pebble: inspect initial generation marker rollback: %w", err)
	}
	if !found {
		return r.syncBaseMarkerRemoval()
	}
	if marker.OwnerID != r.ownerID {
		return errGenerationNotOwned
	}
	if err := os.Remove(filepath.Join(r.basePath, generationMarkerName)); err != nil {
		if os.IsNotExist(err) {
			return r.syncBaseMarkerRemoval()
		}
		return fmt.Errorf("kvstore/pebble: remove initial generation marker: %w", err)
	}
	return r.syncBaseMarkerRemoval()
}

func (r *RotatingStore) syncBaseMarkerRemoval() error {
	if err := errors.Join(
		syncDirectory(r.basePath),
		syncDirectory(filepath.Dir(r.basePath)),
	); err != nil {
		return fmt.Errorf("kvstore/pebble: sync initial generation marker rollback: %w", err)
	}
	r.unpublishedBaseMarker = false
	return nil
}

func (r *RotatingStore) writeGenerationMarker(path string) error {
	if err := validateOwnerID(r.ownerID); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("kvstore/pebble: inspect generation directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("kvstore/pebble: generation path must be a real directory")
	}
	data, err := json.Marshal(generationMarker{
		Version: generationMarkerVersion,
		OwnerID: r.ownerID,
	})
	if err != nil {
		return err
	}
	markerPath := filepath.Join(path, generationMarkerName)
	file, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("kvstore/pebble: create generation marker: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(markerPath)
		return fmt.Errorf("kvstore/pebble: write generation marker: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(markerPath)
		return fmt.Errorf("kvstore/pebble: sync generation marker: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(markerPath)
		return fmt.Errorf("kvstore/pebble: close generation marker: %w", err)
	}
	if err := syncDirectory(path); err != nil {
		return fmt.Errorf("kvstore/pebble: sync generation directory: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("kvstore/pebble: sync generation parent: %w", err)
	}
	return nil
}

func readGenerationMarker(path string) (generationMarker, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return generationMarker{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return generationMarker{}, false, errors.New("kvstore/pebble: generation path must not be a symlink")
	}
	if !info.IsDir() {
		return generationMarker{}, false, errors.New("kvstore/pebble: generation path is not a directory")
	}
	markerPath := filepath.Join(path, generationMarkerName)
	markerInfo, err := os.Lstat(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return generationMarker{}, false, nil
		}
		return generationMarker{}, false, fmt.Errorf("kvstore/pebble: inspect generation marker: %w", err)
	}
	if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
		return generationMarker{}, false, errors.New("kvstore/pebble: generation marker must be a regular file")
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return generationMarker{}, false, fmt.Errorf("kvstore/pebble: read generation marker: %w", err)
	}
	var marker generationMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return generationMarker{}, false, fmt.Errorf("kvstore/pebble: decode generation marker: %w", err)
	}
	if marker.Version != generationMarkerVersion {
		return generationMarker{}, false, fmt.Errorf(
			"kvstore/pebble: unsupported generation marker version %d",
			marker.Version,
		)
	}
	if err := validateOwnerID(marker.OwnerID); err != nil {
		return generationMarker{}, false, err
	}
	return marker, true, nil
}

func (r *RotatingStore) verifyOwnedGeneration(path string) error {
	marker, found, err := readGenerationMarker(path)
	if err != nil {
		return err
	}
	if !found || marker.OwnerID != r.ownerID {
		return errGenerationNotOwned
	}
	return nil
}

func (r *RotatingStore) removeOwnedGeneration(path string) error {
	resolved, err := r.resolveGenerationPath(filepath.Base(path))
	if err != nil || resolved != filepath.Clean(path) {
		return errGenerationNotOwned
	}
	if err := r.verifyOwnedGeneration(path); err != nil {
		return errors.Join(errGenerationNotOwned, err)
	}
	return os.RemoveAll(path)
}

type rotatingBatch struct {
	store  *RotatingStore
	ops    []batchOp
	size   int
	closed bool
}

type batchOp struct {
	key    []byte
	value  []byte
	delete bool
}

func (b *rotatingBatch) Put(key []byte, value []byte) error {
	if b.closed {
		return kvstore.ErrClosed
	}
	b.ops = append(b.ops, batchOp{
		key:   append([]byte(nil), key...),
		value: append([]byte(nil), value...),
	})
	b.size += len(value)
	return nil
}

func (b *rotatingBatch) Delete(key []byte) error {
	if b.closed {
		return kvstore.ErrClosed
	}
	b.ops = append(b.ops, batchOp{
		key:    append([]byte(nil), key...),
		delete: true,
	})
	return nil
}

func (b *rotatingBatch) ValueSize() int { return b.size }

func (b *rotatingBatch) Write() (resultErr error) {
	if b.closed {
		return kvstore.ErrClosed
	}
	keys := make([][]byte, len(b.ops))
	for index := range b.ops {
		keys[index] = b.ops[index].key
	}
	b.store.mu.RLock()
	defer b.store.mu.RUnlock()
	if b.store.closed {
		return kvstore.ErrClosed
	}
	hasDeletes := false
	for _, op := range b.ops {
		if op.delete {
			hasDeletes = true
			break
		}
	}
	if hasDeletes {
		b.store.archiveMu.Lock()
		defer b.store.archiveMu.Unlock()
	}
	lockedMutations := b.store.lockMutations(keys)
	defer b.store.unlockMutations(&lockedMutations)
	writableBatch, err := b.store.writable.NewBatch()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, writableBatch.Close())
	}()
	for _, op := range b.ops {
		if op.delete {
			if err := writableBatch.Delete(op.key); err != nil {
				return err
			}
			continue
		}
		if err := writableBatch.Put(op.key, op.value); err != nil {
			return err
		}
	}
	if hasDeletes {
		archiveBatch, err := b.store.archive.NewBatch()
		if err != nil {
			return err
		}
		for _, op := range b.ops {
			if !op.delete {
				continue
			}
			if err := archiveBatch.Delete(op.key); err != nil {
				return errors.Join(err, archiveBatch.Close())
			}
		}
		if err := archiveBatch.Write(); err != nil {
			return errors.Join(err, archiveBatch.Close())
		}
		if err := archiveBatch.Close(); err != nil {
			return err
		}
	}
	if err := writableBatch.Write(); err != nil {
		return err
	}
	return nil
}

func (b *rotatingBatch) Reset() {
	if b.closed {
		return
	}
	b.ops = nil
	b.size = 0
}

func (b *rotatingBatch) Close() error {
	if b.closed {
		return nil
	}
	b.ops = nil
	b.size = 0
	b.closed = true
	return nil
}

type rotatingIterator struct {
	store         *RotatingStore
	writable      kvstore.Iterator
	archive       kvstore.Iterator
	writableKey   []byte
	archiveKey    []byte
	writableValid bool
	archiveValid  bool
	started       bool
	key           []byte
	value         []byte
	released      bool
}

func (i *rotatingIterator) Next() bool {
	if i.released {
		return false
	}
	if !i.started {
		i.started = true
		i.advanceWritable()
		i.advanceArchive()
	}

	if !i.writableValid && !i.archiveValid {
		return false
	}
	if !i.archiveValid || i.writableValid && bytes.Compare(i.writableKey, i.archiveKey) <= 0 {
		i.key = i.writableKey
		i.value = i.writable.Value()
		duplicate := i.archiveValid && bytes.Equal(i.writableKey, i.archiveKey)
		i.advanceWritable()
		if duplicate {
			i.advanceArchive()
		}
		return true
	}

	i.key = i.archiveKey
	i.value = i.archive.Value()
	i.advanceArchive()
	return true
}

func (i *rotatingIterator) advanceWritable() {
	i.writableValid = i.writable.Next()
	if i.writableValid {
		i.writableKey = i.writable.Key()
	} else {
		i.writableKey = nil
	}
}

func (i *rotatingIterator) advanceArchive() {
	i.archiveValid = i.archive.Next()
	if i.archiveValid {
		i.archiveKey = i.archive.Key()
	} else {
		i.archiveKey = nil
	}
}

func (i *rotatingIterator) Key() []byte   { return append([]byte(nil), i.key...) }
func (i *rotatingIterator) Value() []byte { return append([]byte(nil), i.value...) }

func (i *rotatingIterator) Error() error {
	if i.released {
		return nil
	}
	return errors.Join(i.writable.Error(), i.archive.Error())
}

func (i *rotatingIterator) Close() error {
	if i.released {
		return nil
	}
	i.released = true
	err := errors.Join(i.writable.Close(), i.archive.Close())
	i.store.mu.RUnlock()
	return err
}

var _ kvstore.RotatingStore = (*RotatingStore)(nil)
