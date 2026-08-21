package ledger

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/skiplist"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

type lifecycleMemoryFamily struct {
	mu       sync.RWMutex
	nodes    map[[32]byte][]byte
	fetchErr error
}

func newLifecycleMemoryFamily() *lifecycleMemoryFamily {
	return &lifecycleMemoryFamily{nodes: make(map[[32]byte][]byte)}
}

func (f *lifecycleMemoryFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return bytes.Clone(f.nodes[hash]), nil
}

func (f *lifecycleMemoryFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range entries {
		f.nodes[entry.Hash] = bytes.Clone(entry.Data)
	}
	return nil
}

func (f *lifecycleMemoryFamily) setFetchError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetchErr = err
}

func lifecycleLazyMap(t *testing.T, source *shamap.SHAMap, family *lifecycleMemoryFamily) *shamap.SHAMap {
	t.Helper()
	root, err := source.Hash()
	if err != nil {
		t.Fatalf("hash source map: %v", err)
	}
	if err := source.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}); err != nil {
		t.Fatalf("store source map: %v", err)
	}
	lazy, err := shamap.NewFromRootHash(source.Type(), root, family)
	if err != nil {
		t.Fatalf("open lazy map: %v", err)
	}
	return lazy
}

func TestLedgerCloseFailureAtSkipListBoundaryIsAtomicAndRetryable(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	rolling := keylet.LedgerHashes()
	malformed := bytes.Repeat([]byte{0xff}, 16)
	if err := stateMap.Put(rolling.Key, malformed); err != nil {
		t.Fatalf("seed malformed rolling skip list: %v", err)
	}

	var parentHash [32]byte
	parentHash[0] = 0x7a
	l, err := NewOpenWithHeader(header.LedgerHeader{
		LedgerIndex: 257,
		ParentHash:  parentHash,
		Drops:       1_000,
	}, stateMap, shamap.New(shamap.TypeTransaction), drops.Fees{})
	if err != nil {
		t.Fatalf("NewOpenWithHeader: %v", err)
	}
	if err := l.AdjustDropsDestroyed(10); err != nil {
		t.Fatalf("AdjustDropsDestroyed: %v", err)
	}

	beforeHeader := l.Header()
	beforeStateRoot, err := l.StateMapHash()
	if err != nil {
		t.Fatalf("state root before close: %v", err)
	}
	beforeTxRoot, err := l.TxMapHash()
	if err != nil {
		t.Fatalf("transaction root before close: %v", err)
	}
	beforeRules := l.rules
	beforeDestroyed := l.dropsDestroyed

	if err := l.Close(time.Unix(1_700_000_000, 0), 1); err == nil {
		t.Fatal("Close accepted a malformed rolling skip list")
	}

	if got := l.Header(); got != beforeHeader {
		t.Fatalf("failed close changed header:\n got  %+v\n want %+v", got, beforeHeader)
	}
	if got, err := l.StateMapHash(); err != nil || got != beforeStateRoot {
		t.Fatalf("state root after failed close = %x, %v; want %x", got, err, beforeStateRoot)
	}
	if got, err := l.TxMapHash(); err != nil || got != beforeTxRoot {
		t.Fatalf("transaction root after failed close = %x, %v; want %x", got, err, beforeTxRoot)
	}
	if l.rules != beforeRules || l.dropsDestroyed != beforeDestroyed || !l.IsOpen() || l.IsImmutable() {
		t.Fatal("failed close changed rules, destroyed drops, lifecycle, or writability")
	}
	item, found, err := l.stateMap.Get(rolling.Key)
	if err != nil {
		t.Fatalf("read rolling skip list after failed close: %v", err)
	}
	if !found || item == nil {
		t.Fatal("failed close removed the rolling skip list")
	}
	if !bytes.Equal(item.Data(), malformed) {
		t.Fatalf("failed close changed rolling skip list to %x, want %x", item.Data(), malformed)
	}
	historical := keylet.LedgerHashesForSeq(256)
	if exists, err := l.Exists(historical); err != nil || exists {
		t.Fatalf("failed close published historical skip list: exists=%v err=%v", exists, err)
	}

	if err := l.Erase(rolling); err != nil {
		t.Fatalf("remove injected corruption: %v", err)
	}
	if err := l.Close(time.Unix(1_700_000_000, 0), 1); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if !l.IsClosed() || !l.IsImmutable() {
		t.Fatal("successful retry did not finalize the ledger")
	}
	for name, k := range map[string]keylet.Keylet{"rolling": rolling, "historical": historical} {
		if exists, err := l.Exists(k); err != nil || !exists {
			t.Fatalf("%s skip list after retry: exists=%v err=%v", name, exists, err)
		}
	}
}

func TestLedgerCloseBackendFailureIsAtomicAndRetryable(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	var ancestor [32]byte
	ancestor[0] = 1
	if err := skiplist.Write(stateMap, keylet.LedgerHashes().Key, nil, [][32]byte{ancestor}, 1); err != nil {
		t.Fatalf("seed rolling skip list: %v", err)
	}
	family := newLifecycleMemoryFamily()
	lazyState := lifecycleLazyMap(t, stateMap, family)

	l, err := NewOpenWithHeader(header.LedgerHeader{
		LedgerIndex: 3,
		ParentHash:  [32]byte{2},
		Drops:       100,
	}, shamap.New(shamap.TypeState), shamap.New(shamap.TypeTransaction), drops.Fees{})
	if err != nil {
		t.Fatalf("NewOpenWithHeader: %v", err)
	}
	l.stateMap = lazyState

	beforeHeader := l.Header()
	beforeRoot, err := l.StateMapHash()
	if err != nil {
		t.Fatalf("state root before close: %v", err)
	}
	wantErr := errors.New("injected SHAMap fetch failure")
	family.setFetchError(wantErr)
	if err := l.Close(time.Unix(1_700_000_100, 0), 0); !errors.Is(err, wantErr) {
		t.Fatalf("Close error = %v, want %v", err, wantErr)
	}
	if got := l.Header(); got != beforeHeader {
		t.Fatalf("backend failure changed header: got %+v want %+v", got, beforeHeader)
	}
	if got, err := l.StateMapHash(); err != nil || got != beforeRoot {
		t.Fatalf("state root after backend failure = %x, %v; want %x", got, err, beforeRoot)
	}
	if !l.IsOpen() || l.IsImmutable() {
		t.Fatal("backend failure changed lifecycle or writability")
	}

	family.setFetchError(nil)
	if err := l.Close(time.Unix(1_700_000_100, 0), 0); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
}
