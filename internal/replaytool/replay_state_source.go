package replaytool

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/cockroachdb/pebble/vfs"
)

type stateSource interface {
	// Load returns the verified seed state map for the ledger, its snapshot,
	// and the fee schedule extracted from the state.
	Load(ctx context.Context, ledgerIndex uint32) (*shamap.SHAMap, *statecompare.LedgerSnapshot, drops.Fees, error)
	// Close releases any resources held by the source (pebble handles, the
	// ephemeral overlay directory, ...).
	Close() error
}

// memoryStateSource holds the whole state tree in the Go heap, the historical
// replay-range behaviour. It is the default when no node store is configured.
type memoryStateSource struct {
	client *statecompare.Client
}

func (s *memoryStateSource) Load(ctx context.Context, ledgerIndex uint32) (*shamap.SHAMap, *statecompare.LedgerSnapshot, drops.Fees, error) {
	return loadInitialState(ctx, s.client, ledgerIndex)
}

func (s *memoryStateSource) Close() error { return nil }

// nodestoreStateSource backs the state SHAMap with a node-local pebble
// nodestore. Each checkpoint's state is built into a durable, shared
// read-only base store once; subsequent seeds open it lazily (no rebuild),
// and a fresh per-run overlay captures the segment's mutations so the base
// stays pristine and shareable.
type nodestoreStateSource struct {
	client         *statecompare.Client
	dir            string
	baseCacheMB    int
	overlayCacheMB int
	overlay        *backend.NodeStore
	overlayDir     string
	opened         []*backend.NodeStore
	closed         bool
}

// baseNodeCacheItems / overlayNodeCacheItems size the positive node LRU (a count
// of decoded entries, independent of the Pebble block-cache MiB budget). The
// base is read-heavy (the whole checkpoint) so it warrants a far larger working
// set than the overlay, which only sees a segment's mutations. Both are generous
// but bounded so a long run does not grow the heap without limit.
const (
	baseNodeCacheItems    = 262144
	overlayNodeCacheItems = 65536
	baseCompleteMarker    = "statecompare.complete"
)

var checkpointBuildGates sync.Map

func newNodestoreStateSource(client *statecompare.Client, dir string, baseCacheMB, overlayCacheMB int) (*nodestoreStateSource, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating nodestore dir: %w", err)
	}
	// The overlay is ephemeral per run: a fresh directory guarantees the
	// segment starts from the base checkpoint with no stale mutations.
	overlayDir, err := os.MkdirTemp(dir, "overlay-")
	if err != nil {
		return nil, fmt.Errorf("creating overlay dir: %w", err)
	}
	overlay, err := backend.OpenPebble(overlayDir, overlayCacheMB, overlayNodeCacheItems)
	if err != nil {
		os.RemoveAll(overlayDir)
		return nil, fmt.Errorf("opening overlay nodestore: %w", err)
	}
	return &nodestoreStateSource{
		client:         client,
		dir:            dir,
		baseCacheMB:    baseCacheMB,
		overlayCacheMB: overlayCacheMB,
		overlay:        overlay,
		overlayDir:     overlayDir,
		opened:         []*backend.NodeStore{overlay},
	}, nil
}

func (s *nodestoreStateSource) Load(ctx context.Context, ledgerIndex uint32) (*shamap.SHAMap, *statecompare.LedgerSnapshot, drops.Fees, error) {
	snapshot, err := s.client.Snapshot(ctx, ledgerIndex)
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("getting snapshot: %w", err)
	}

	basePath := filepath.Join(s.dir, fmt.Sprintf("ckpt-%d", ledgerIndex))
	base, err := s.openOrBuildBase(ctx, basePath, snapshot.AccountHash, func(fn func(statecompare.StateEntry) error) error {
		return s.client.StreamStateEntries(ctx, snapshot, fn)
	})
	if err != nil {
		return nil, nil, drops.Fees{}, err
	}
	s.opened = append(s.opened, base)
	stateMap, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, snapshot.AccountHash, shamap.NewOverlayFamily(base, s.overlay))
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("opening checkpoint %d state root: %w", ledgerIndex, err)
	}

	// Targeted lookup; lazily fetches only the FeeSettings path, not the tree.
	fees, err := feesFromStateMap(stateMap)
	if err != nil {
		return nil, nil, drops.Fees{}, err
	}
	return stateMap, snapshot, fees, nil
}

func (s *nodestoreStateSource) openOrBuildBase(
	ctx context.Context,
	basePath string,
	accountHash [32]byte,
	streamEntries func(func(statecompare.StateEntry) error) error,
) (*backend.NodeStore, error) {
	complete, err := baseIsComplete(basePath, accountHash)
	if err != nil {
		return nil, err
	}
	if complete {
		return openVerifiedBase(ctx, basePath, s.baseCacheMB, accountHash)
	}
	buildLock, err := acquireCheckpointBuildLock(ctx, basePath)
	if err != nil {
		return nil, err
	}
	defer buildLock.Close()

	complete, err = baseIsComplete(basePath, accountHash)
	if err != nil {
		return nil, err
	}
	if complete {
		return openVerifiedBase(ctx, basePath, s.baseCacheMB, accountHash)
	}
	if _, err := os.Stat(basePath); err == nil {
		if err := os.RemoveAll(basePath); err != nil {
			return nil, fmt.Errorf("removing incomplete base nodestore %s: %w", basePath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspecting base nodestore %s: %w", basePath, err)
	}

	stagePath, err := os.MkdirTemp(s.dir, ".checkpoint-build-")
	if err != nil {
		return nil, fmt.Errorf("creating staged base nodestore: %w", err)
	}
	removeStage := true
	defer func() {
		if removeStage {
			_ = os.RemoveAll(stagePath)
		}
	}()

	stage, err := backend.OpenPebble(stagePath, s.baseCacheMB, baseNodeCacheItems)
	if err != nil {
		return nil, fmt.Errorf("opening staged base nodestore: %w", err)
	}
	_, buildErr := buildOrOpenLazyState(ctx, stage, s.overlay, accountHash, streamEntries)
	if buildErr == nil {
		buildErr = stage.Sync(ctx)
	}
	buildErr = errors.Join(buildErr, stage.Close())
	if buildErr != nil {
		return nil, fmt.Errorf("building staged base nodestore: %w", buildErr)
	}
	if err := writeBaseMarker(stagePath, accountHash); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(stagePath, basePath); err != nil {
		if complete, checkErr := baseIsComplete(basePath, accountHash); checkErr == nil && complete {
			return openVerifiedBase(ctx, basePath, s.baseCacheMB, accountHash)
		}
		return nil, fmt.Errorf("publishing base nodestore %s: %w", basePath, err)
	}
	removeStage = false
	if err := syncStateSourceDirectory(s.dir); err != nil {
		return nil, err
	}
	return openVerifiedBase(ctx, basePath, s.baseCacheMB, accountHash)
}

type checkpointBuildLock struct {
	io.Closer
	release func()
}

func (l *checkpointBuildLock) Close() error {
	err := l.Closer.Close()
	l.release()
	return err
}

func acquireCheckpointBuildLock(ctx context.Context, basePath string) (*checkpointBuildLock, error) {
	gateValue, _ := checkpointBuildGates.LoadOrStore(basePath, make(chan struct{}, 1))
	gate := gateValue.(chan struct{})
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-gate }

	lockPath := basePath + ".lock"
	for {
		lock, err := vfs.Default.Lock(lockPath)
		if err == nil {
			return &checkpointBuildLock{Closer: lock, release: release}, nil
		}
		if !checkpointLockBusy(err) {
			release()
			return nil, fmt.Errorf("locking base nodestore %s: %w", basePath, err)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			release()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func checkpointLockBusy(err error) bool {
	if runtime.GOOS == "windows" {
		return errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
	}
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAGAIN)
}

func baseIsComplete(path string, accountHash [32]byte) (bool, error) {
	marker, err := os.ReadFile(filepath.Join(path, baseCompleteMarker))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading base nodestore marker: %w", err)
	}
	return strings.TrimSpace(string(marker)) == hex.EncodeToString(accountHash[:]), nil
}

func writeBaseMarker(path string, accountHash [32]byte) error {
	markerPath := filepath.Join(path, baseCompleteMarker)
	file, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("creating base nodestore marker: %w", err)
	}
	_, writeErr := fmt.Fprintln(file, hex.EncodeToString(accountHash[:]))
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close(), syncStateSourceDirectory(path))
}

func syncStateSourceDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening directory %s for sync: %w", path, err)
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func openVerifiedBase(ctx context.Context, path string, cacheMB int, accountHash [32]byte) (*backend.NodeStore, error) {
	base, err := backend.OpenPebbleReadOnly(path, cacheMB, baseNodeCacheItems)
	if err != nil {
		return nil, fmt.Errorf("opening base nodestore %s: %w", path, err)
	}
	root, fetchErr := base.Fetch(ctx, accountHash)
	if fetchErr != nil {
		return nil, errors.Join(fmt.Errorf("verifying base nodestore %s root: %w", path, fetchErr), base.Close())
	}
	if root == nil {
		return nil, errors.Join(fmt.Errorf("verifying base nodestore %s: root is missing", path), base.Close())
	}
	return base, nil
}

func (s *nodestoreStateSource) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var closeErr error
	for _, fam := range s.opened {
		closeErr = errors.Join(closeErr, fam.Close())
	}
	return errors.Join(closeErr, os.RemoveAll(s.overlayDir))
}

// buildOrOpenLazyState returns a state SHAMap whose backing is a shared
// read-only base plus a writable overlay. If the base already holds the root
// node for accountHash it is opened lazily with no rebuild; otherwise the tree
// is built once by streaming entries from streamEntries, flushed into the base,
// verified against accountHash, and then re-opened over the base+overlay so
// replay mutations land only in the overlay.
//
// streamEntries delivers each entry through a callback rather than returning a
// slice, so a multi-gigabyte checkpoint is never held in the heap at once.
func buildOrOpenLazyState(
	ctx context.Context,
	base, overlay shamap.Family,
	accountHash [32]byte,
	streamEntries func(func(statecompare.StateEntry) error) error,
) (*shamap.SHAMap, error) {
	// Warm path: the root node is content-addressed by accountHash, so its
	// presence means the derived store was built and the root commitment
	// matches. Children are verified-by-hash as they are fetched on demand.
	root, err := base.Fetch(ctx, accountHash)
	if err != nil {
		return nil, fmt.Errorf("probing base nodestore: %w", err)
	}
	if root != nil {
		return shamap.NewFromRootHashContext(ctx, shamap.TypeState, accountHash, shamap.NewOverlayFamily(base, overlay))
	}

	// Cold path: build the derived nodestore once, streaming the raw entries.
	buildMap, err := shamap.NewBacked(shamap.TypeState, base)
	if err != nil {
		return nil, fmt.Errorf("creating build map: %w", err)
	}

	// Flush+release in chunks so building a ~14M-entry tree does not require
	// holding it all in the heap at once: released subtrees are re-fetched
	// from the base on demand.
	const flushChunk = 100_000
	n := 0
	if err := streamEntries(func(entry statecompare.StateEntry) error {
		if err := buildMap.Put(entry.Index, entry.Data); err != nil {
			return fmt.Errorf("injecting entry: %w", err)
		}
		n++
		if n%flushChunk == 0 {
			return flushToFamily(ctx, buildMap, base)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("getting state entries: %w", err)
	}
	if err := flushToFamily(ctx, buildMap, base); err != nil {
		return nil, err
	}

	// Verify-gate: the built tree root is a Merkle commitment over the whole
	// state, so a match proves the seed is complete and correct. The hash is
	// read from the retained root, not by re-walking the tree.
	builtRoot, err := buildMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("computing build root hash: %w", err)
	}
	if builtRoot != accountHash {
		return nil, fmt.Errorf("seed state account_hash mismatch: built root %x != expected %x (incomplete or corrupt state import)", builtRoot[:8], accountHash[:8])
	}

	return shamap.NewFromRootHashContext(ctx, shamap.TypeState, accountHash, shamap.NewOverlayFamily(base, overlay))
}

// flushToFamily flushes the map's dirty nodes into fam, releasing child
// pointers so the heap stays bounded during a cold build.
func flushToFamily(ctx context.Context, m *shamap.SHAMap, fam shamap.Family) error {
	if err := m.StoreDirtyAndRelease(func(entries []shamap.FlushEntry) error {
		return fam.StoreBatch(ctx, entries)
	}); err != nil {
		return fmt.Errorf("storing dirty nodes: %w", err)
	}
	return nil
}

// newStateSource returns the nodestore-lazy source when dir is set, otherwise
// the in-memory source. baseCacheMB / overlayCacheMB size the Pebble block
// caches of the nodestore base and overlay; they are ignored by the in-memory
// source.
func newStateSource(client *statecompare.Client, nodestoreDir string, baseCacheMB, overlayCacheMB int) (stateSource, error) {
	if nodestoreDir == "" {
		return &memoryStateSource{client: client}, nil
	}
	return newNodestoreStateSource(client, nodestoreDir, baseCacheMB, overlayCacheMB)
}
