package pebble

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	cockroachpebble "github.com/cockroachdb/pebble"
)

const generationStateSuffix = ".generations.json"
const generationStateVersion = 1

type generationState struct {
	Version       int    `json:"version"`
	Writable      string `json:"writable"`
	Archive       string `json:"archive"`
	LastRotated   uint32 `json:"last_rotated,omitempty"`
	MinimumOnline uint32 `json:"minimum_online,omitempty"`
}

// RotatingStore stores new records in one Pebble generation and falls back to
// one archive generation on reads. Promote explicitly copies an archive record
// into the writable generation for online-delete preservation.
type RotatingStore struct {
	mu       sync.RWMutex
	rotateMu sync.Mutex

	basePath   string
	statePath  string
	options    Options
	blockCache *cockroachpebble.Cache

	writable      *Store
	writablePath  string
	archive       *Store
	archivePath   string
	lastRotated   uint32
	minimumOnline uint32
	syncDir       func(string) error

	closed bool
}

// HasRotationState reports whether path has a durable generation manifest.
func HasRotationState(path string) (bool, error) {
	_, err := os.Stat(filepath.Clean(path) + generationStateSuffix)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// NewRotating opens a two-generation Pebble store. An existing database at
// path becomes the initial writable generation, so enabling online deletion
// does not copy or rename an operator's current database.
func NewRotating(path string, options Options) (*RotatingStore, error) {
	if path == "" {
		return nil, errors.New("kvstore/pebble: rotating store path is empty")
	}
	resolved, err := options.Resolve()
	if err != nil {
		return nil, err
	}
	if resolved.MaxOpenFiles < MinimumRotatingOpenFiles {
		return nil, fmt.Errorf(
			"kvstore/pebble: rotating store requires at least %d max open files, got %d",
			MinimumRotatingOpenFiles,
			resolved.MaxOpenFiles,
		)
	}
	if resolved.MaxOpenFiles%2 != 0 {
		return nil, fmt.Errorf(
			"kvstore/pebble: rotating store requires an even max open files value, got %d",
			resolved.MaxOpenFiles,
		)
	}
	basePath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("kvstore/pebble: resolve rotating path: %w", err)
	}
	parent := filepath.Dir(basePath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("kvstore/pebble: create rotating parent: %w", err)
	}

	perGenerationOptions := resolved
	perGenerationOptions.MaxOpenFiles = resolved.MaxOpenFiles / 2
	r := &RotatingStore{
		basePath:  basePath,
		statePath: basePath + generationStateSuffix,
		options:   perGenerationOptions,
		syncDir:   syncDirectory,
	}

	state, found, err := r.loadState()
	if err != nil {
		return nil, err
	}
	if found {
		r.writablePath, err = r.resolveGenerationPath(state.Writable)
		if err != nil {
			return nil, err
		}
		r.archivePath, err = r.resolveGenerationPath(state.Archive)
		if err != nil {
			return nil, err
		}
		for _, generationPath := range []string{r.writablePath, r.archivePath} {
			if _, statErr := os.Stat(generationPath); statErr != nil {
				return nil, fmt.Errorf("kvstore/pebble: generation %s is unavailable: %w", generationPath, statErr)
			}
		}
		r.lastRotated = state.LastRotated
		r.minimumOnline = state.MinimumOnline
	} else {
		r.writablePath = basePath
		r.archivePath, err = r.newGenerationPath()
		if err != nil {
			return nil, err
		}
	}

	r.blockCache = cockroachpebble.NewCache(resolved.BlockCacheBytes)
	keepCache := false
	defer func() {
		if !keepCache {
			r.blockCache.Unref()
		}
	}()

	r.writable, err = newWithCache(r.writablePath, r.blockCache, r.options, false)
	if err != nil {
		if !found {
			_ = os.RemoveAll(r.archivePath)
		}
		return nil, err
	}
	r.archive, err = newWithCache(r.archivePath, r.blockCache, r.options, false)
	if err != nil {
		_ = r.writable.Close()
		if !found {
			_ = os.RemoveAll(r.archivePath)
		}
		return nil, err
	}
	if !found {
		published, saveErr := r.saveState(generationState{
			Version:  generationStateVersion,
			Writable: filepath.Base(r.writablePath),
			Archive:  filepath.Base(r.archivePath),
		})
		if saveErr != nil {
			_ = r.archive.Close()
			_ = r.writable.Close()
			if !published {
				_ = os.RemoveAll(r.archivePath)
			}
			return nil, saveErr
		}
	}
	if err := r.cleanupOrphans(); err != nil {
		_ = r.archive.Close()
		_ = r.writable.Close()
		return nil, err
	}
	keepCache = true
	return r, nil
}

// Has reports whether either generation contains key.
func (r *RotatingStore) Has(key []byte) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return false, kvstore.ErrClosed
	}
	found, err := r.writable.Has(key)
	if err != nil || found {
		return found, err
	}
	return r.archive.Has(key)
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
func (r *RotatingStore) CanRotateWithoutRefresh() (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return false, kvstore.ErrClosed
	}
	iter := r.archive.NewIterator(nil, nil)
	defer iter.Release()
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
	return r.getLocked(key, true)
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
	return r.writable.Put(key, value)
}

// Delete removes key from both generations.
func (r *RotatingStore) Delete(key []byte) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return kvstore.ErrClosed
	}
	return errors.Join(r.writable.Delete(key), r.archive.Delete(key))
}

// NewBatch returns a batch that applies operations to the rotating store.
func (r *RotatingStore) NewBatch() kvstore.Batch {
	return &rotatingBatch{store: r}
}

// NewIterator returns a merged iterator over both generations.
func (r *RotatingStore) NewIterator(prefix []byte, start []byte) kvstore.Iterator {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return &errIterator{err: kvstore.ErrClosed}
	}
	return &rotatingIterator{
		store:    r,
		writable: r.writable.NewIterator(prefix, start),
		archive:  r.archive.NewIterator(prefix, start),
	}
}

// Stat returns statistics for both generations.
func (r *RotatingStore) Stat() (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return "", kvstore.ErrClosed
	}
	writable, err := r.writable.Stat()
	if err != nil {
		return "", err
	}
	archive, err := r.archive.Stat()
	if err != nil {
		return "", err
	}
	return "writable:\n" + writable + "\narchive:\n" + archive, nil
}

// Compact compacts the requested range in both generations.
func (r *RotatingStore) Compact(start []byte, limit []byte) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return kvstore.ErrClosed
	}
	if err := r.writable.Compact(start, limit); err != nil {
		return err
	}
	return r.archive.Compact(start, limit)
}

// Sync flushes the writable generation.
func (r *RotatingStore) Sync() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return kvstore.ErrClosed
	}
	return r.writable.Sync()
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

// Rotate durably publishes a fresh writable generation before retiring the
// former archive. No operation can observe the in-memory swap before the
// generation manifest is durable.
func (r *RotatingStore) Rotate(lastRotated, minimumOnline uint32) (bool, error) {
	if lastRotated == 0 || minimumOnline == 0 {
		return false, errors.New("kvstore/pebble: rotation boundaries must be non-zero")
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

	newPath, err := r.newGenerationPath()
	if err != nil {
		return false, err
	}
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		_ = os.RemoveAll(newPath)
		return false, kvstore.ErrClosed
	}
	newWritable, err := newWithCache(newPath, r.blockCache, r.options, false)
	r.mu.RUnlock()
	if err != nil {
		_ = os.RemoveAll(newPath)
		return false, err
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = newWritable.Close()
		_ = os.RemoveAll(newPath)
		return false, kvstore.ErrClosed
	}
	if err := r.writable.Sync(); err != nil {
		r.mu.Unlock()
		_ = newWritable.Close()
		_ = os.RemoveAll(newPath)
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
		Writable:      filepath.Base(newPath),
		Archive:       filepath.Base(oldWritablePath),
		LastRotated:   lastRotated,
		MinimumOnline: minimumOnline,
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
		_ = os.RemoveAll(newPath)
		return false, saveErr
	}
	if saveErr != nil {
		r.mu.Unlock()
		closeErr := oldArchive.Close()
		return true, errors.Join(saveErr, closeErr)
	}
	r.mu.Unlock()

	cleanupErr := errors.Join(oldArchive.Close(), os.RemoveAll(oldArchivePath))
	return true, cleanupErr
}

func (r *RotatingStore) loadState() (generationState, bool, error) {
	data, err := os.ReadFile(r.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return generationState{}, false, nil
		}
		return generationState{}, false, fmt.Errorf("kvstore/pebble: read generation state: %w", err)
	}
	var state generationState
	if err := json.Unmarshal(data, &state); err != nil {
		return generationState{}, false, fmt.Errorf("kvstore/pebble: decode generation state: %w", err)
	}
	if state.Version != generationStateVersion {
		return generationState{}, false, fmt.Errorf(
			"kvstore/pebble: unsupported generation state version %d",
			state.Version,
		)
	}
	if state.Writable == "" || state.Archive == "" || state.Writable == state.Archive {
		return generationState{}, false, errors.New("kvstore/pebble: invalid generation state")
	}
	if (state.LastRotated == 0) != (state.MinimumOnline == 0) ||
		state.MinimumOnline > state.LastRotated {
		return generationState{}, false, errors.New("kvstore/pebble: invalid generation boundaries")
	}
	return state, true, nil
}

func (r *RotatingStore) saveState(state generationState) (bool, error) {
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
	if name != filepath.Base(name) || name == "." || name == string(filepath.Separator) {
		return "", fmt.Errorf("kvstore/pebble: invalid generation path %q", name)
	}
	return filepath.Join(filepath.Dir(r.basePath), name), nil
}

func (r *RotatingStore) newGenerationPath() (string, error) {
	prefix := "." + filepath.Base(r.basePath) + "-generation-"
	path, err := os.MkdirTemp(filepath.Dir(r.basePath), prefix)
	if err != nil {
		return "", fmt.Errorf("kvstore/pebble: create generation: %w", err)
	}
	return path, nil
}

func (r *RotatingStore) cleanupOrphans() error {
	pattern := filepath.Join(filepath.Dir(r.basePath), "."+filepath.Base(r.basePath)+"-generation-*")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if path == r.writablePath || path == r.archivePath {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("kvstore/pebble: remove orphan generation %s: %w", path, err)
		}
	}
	return nil
}

type rotatingBatch struct {
	store *RotatingStore
	ops   []batchOp
	size  int
	err   error
}

type batchOp struct {
	key    []byte
	value  []byte
	delete bool
}

func (b *rotatingBatch) Put(key []byte, value []byte) error {
	if b.err != nil {
		return b.err
	}
	b.ops = append(b.ops, batchOp{
		key:   append([]byte(nil), key...),
		value: append([]byte(nil), value...),
	})
	b.size += len(value)
	return nil
}

func (b *rotatingBatch) Delete(key []byte) error {
	if b.err != nil {
		return b.err
	}
	b.ops = append(b.ops, batchOp{
		key:    append([]byte(nil), key...),
		delete: true,
	})
	return nil
}

func (b *rotatingBatch) ValueSize() int { return b.size }

func (b *rotatingBatch) Write() error {
	if b.err != nil {
		return b.err
	}
	b.store.mu.RLock()
	defer b.store.mu.RUnlock()
	if b.store.closed {
		return kvstore.ErrClosed
	}
	writableBatch := b.store.writable.NewBatch()
	hasDeletes := false
	for _, op := range b.ops {
		if op.delete {
			hasDeletes = true
			if err := writableBatch.Delete(op.key); err != nil {
				return err
			}
			continue
		}
		if err := writableBatch.Put(op.key, op.value); err != nil {
			return err
		}
	}
	if err := writableBatch.Write(); err != nil {
		return err
	}
	if !hasDeletes {
		return nil
	}
	archiveBatch := b.store.archive.NewBatch()
	for _, op := range b.ops {
		if !op.delete {
			continue
		}
		if err := archiveBatch.Delete(op.key); err != nil {
			return err
		}
	}
	return archiveBatch.Write()
}

func (b *rotatingBatch) Reset() {
	b.ops = nil
	b.size = 0
	b.err = nil
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

func (i *rotatingIterator) Release() {
	if i.released {
		return
	}
	i.released = true
	i.writable.Release()
	i.archive.Release()
	i.store.mu.RUnlock()
}

var _ kvstore.RotatingStore = (*RotatingStore)(nil)
