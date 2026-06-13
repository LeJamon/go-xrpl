package replaytool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/shamap"
)

// StateSource loads the seed account-state SHAMap for a ledger. It exists so
// the SHAMap's backing — fully in-memory versus nodestore-lazy — is swappable
// without touching the replay loop, as the mainnet-replay design requires
// ("load state behind an interface, not loadInitialState's Put loop").
type StateSource interface {
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
	client     *statecompare.Client
	dir        string
	overlay    *shamap.NodeStoreFamily
	overlayDir string
	opened     []*shamap.NodeStoreFamily
}

// baseCacheMB / overlayCacheMB bound the resident node cache for the base and
// overlay pebble stores. The base is read-heavy (the whole checkpoint); the
// overlay only sees a segment's mutations.
const (
	baseCacheMB    = 1024
	overlayCacheMB = 256
)

func newNodestoreStateSource(client *statecompare.Client, dir string) (*nodestoreStateSource, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating nodestore dir: %w", err)
	}
	// The overlay is ephemeral per run: a fresh directory guarantees the
	// segment starts from the base checkpoint with no stale mutations.
	overlayDir, err := os.MkdirTemp(dir, "overlay-")
	if err != nil {
		return nil, fmt.Errorf("creating overlay dir: %w", err)
	}
	overlay, err := shamap.NewPebbleNodeStoreFamily(overlayDir, overlayCacheMB)
	if err != nil {
		os.RemoveAll(overlayDir)
		return nil, fmt.Errorf("opening overlay nodestore: %w", err)
	}
	return &nodestoreStateSource{
		client:     client,
		dir:        dir,
		overlay:    overlay,
		overlayDir: overlayDir,
		opened:     []*shamap.NodeStoreFamily{overlay},
	}, nil
}

func (s *nodestoreStateSource) Load(ctx context.Context, ledgerIndex uint32) (*shamap.SHAMap, *statecompare.LedgerSnapshot, drops.Fees, error) {
	snapshot, err := s.client.GetSnapshot(ctx, ledgerIndex)
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("getting snapshot: %w", err)
	}

	basePath := filepath.Join(s.dir, fmt.Sprintf("ckpt-%d", ledgerIndex))
	base, err := shamap.NewPebbleNodeStoreFamily(basePath, baseCacheMB)
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("opening base nodestore %s: %w", basePath, err)
	}
	s.opened = append(s.opened, base)

	stateMap, err := buildOrOpenLazyState(ctx, base, s.overlay, snapshot.AccountHash, func() ([]statecompare.StateEntry, error) {
		return s.client.GetStateEntries(ctx, ledgerIndex)
	})
	if err != nil {
		return nil, nil, drops.Fees{}, err
	}

	// Targeted lookup; lazily fetches only the FeeSettings path, not the tree.
	fees := extractFeesFromSHAMap(stateMap)
	return stateMap, snapshot, fees, nil
}

func (s *nodestoreStateSource) Close() error {
	var firstErr error
	for _, fam := range s.opened {
		if err := fam.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := os.RemoveAll(s.overlayDir); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// buildOrOpenLazyState returns a state SHAMap whose backing is a shared
// read-only base plus a writable overlay. If the base already holds the root
// node for accountHash it is opened lazily with no rebuild; otherwise the tree
// is built once from loadEntries, flushed into the base, verified against
// accountHash, and then re-opened over the base+overlay so replay mutations
// land only in the overlay.
func buildOrOpenLazyState(
	ctx context.Context,
	base, overlay shamap.Family,
	accountHash [32]byte,
	loadEntries func() ([]statecompare.StateEntry, error),
) (*shamap.SHAMap, error) {
	// Warm path: the root node is content-addressed by accountHash, so its
	// presence means the derived store was built and the root commitment
	// matches. Children are verified-by-hash as they are fetched on demand.
	root, err := base.Fetch(ctx, accountHash)
	if err != nil {
		return nil, fmt.Errorf("probing base nodestore: %w", err)
	}
	if root != nil {
		return shamap.NewFromRootHash(shamap.TypeState, accountHash, shamap.NewOverlayFamily(base, overlay))
	}

	// Cold path: build the derived nodestore once from the raw state entries.
	entries, err := loadEntries()
	if err != nil {
		return nil, fmt.Errorf("getting state entries: %w", err)
	}
	buildMap, err := shamap.NewBacked(shamap.TypeState, base)
	if err != nil {
		return nil, fmt.Errorf("creating build map: %w", err)
	}

	// Flush+release in chunks so building a ~14M-entry tree does not require
	// holding it all in the heap at once: released subtrees are re-fetched
	// from the base on demand.
	const flushChunk = 100_000
	for i, entry := range entries {
		if err := buildMap.Put(entry.Index, entry.Data); err != nil {
			return nil, fmt.Errorf("injecting entry: %w", err)
		}
		if (i+1)%flushChunk == 0 {
			if err := flushToFamily(ctx, buildMap, base); err != nil {
				return nil, err
			}
		}
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

	return shamap.NewFromRootHash(shamap.TypeState, accountHash, shamap.NewOverlayFamily(base, overlay))
}

// flushToFamily flushes the map's dirty nodes into fam, releasing child
// pointers so the heap stays bounded during a cold build.
func flushToFamily(ctx context.Context, m *shamap.SHAMap, fam shamap.Family) error {
	batch, err := m.FlushDirty(true)
	if err != nil {
		return fmt.Errorf("flushing nodes: %w", err)
	}
	if len(batch.Entries) == 0 {
		return nil
	}
	if err := fam.StoreBatch(ctx, batch.Entries); err != nil {
		return fmt.Errorf("storing nodes: %w", err)
	}
	return nil
}

// newStateSource returns the nodestore-lazy source when dir is set, otherwise
// the in-memory source.
func newStateSource(client *statecompare.Client, nodestoreDir string) (StateSource, error) {
	if nodestoreDir == "" {
		return &memoryStateSource{client: client}, nil
	}
	return newNodestoreStateSource(client, nodestoreDir)
}
