package list

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestFlushCacheWrites_RetriesWriteAndObservesFailure(t *testing.T) {
	var logs bytes.Buffer
	agg := newCacheFailureAggregator(t, slog.New(slog.NewTextHandler(&logs, nil)))
	path := cachePathFor(t.TempDir(), PublisherKey{0xed, 1})
	var writes int
	agg.cacheOps = cacheFileOps{
		writeFile: func(name string, body []byte, mode os.FileMode) error {
			writes++
			if writes == 1 {
				return errors.New("injected write failure")
			}
			return os.WriteFile(name, body, mode)
		},
	}
	queueCacheWrite(t, agg, path, PublisherKey{0xed, 1}, []byte("first"))
	agg.flushCacheWrites()
	if writes != 1 {
		t.Fatalf("write calls after failure: %d", writes)
	}
	if !bytes.Contains(logs.Bytes(), []byte("cache write failed")) {
		t.Fatalf("write failure was not observable: %q", logs.String())
	}
	if pendingCacheCount(agg) != 1 {
		t.Fatalf("failed write was not requeued")
	}

	agg.flushCacheWrites()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("retry did not create cache: %v", err)
	}
	if string(body) != "first" || pendingCacheCount(agg) != 0 {
		t.Fatalf("retry result: body=%q pending=%d", body, pendingCacheCount(agg))
	}
}

func TestFlushCacheWrites_RetriesRemoveAndDeletesStaleCache(t *testing.T) {
	agg := newCacheFailureAggregator(t, nil)
	pk := PublisherKey{0xed, 2}
	path := cachePathFor(t.TempDir(), pk)
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	var removes int
	agg.cacheOps = cacheFileOps{
		remove: func(name string) error {
			removes++
			if removes == 1 {
				return errors.New("injected remove failure")
			}
			return os.Remove(name)
		},
	}
	agg.mu.Lock()
	agg.cacheWriteSeq++
	agg.pendingCacheWrites[pk] = pendingCacheWrite{path: path, seq: agg.cacheWriteSeq}
	agg.mu.Unlock()
	agg.flushCacheWrites()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale cache disappeared after failed remove: %v", err)
	}
	if pendingCacheCount(agg) != 1 {
		t.Fatalf("failed remove was not requeued")
	}
	agg.flushCacheWrites()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("retry did not delete stale cache, err=%v", err)
	}
}

func TestFlushCacheWrites_RetriesRename(t *testing.T) {
	agg := newCacheFailureAggregator(t, nil)
	pk := PublisherKey{0xed, 3}
	path := cachePathFor(t.TempDir(), pk)
	var renames int
	agg.cacheOps = cacheFileOps{
		rename: func(old, new string) error {
			renames++
			if renames == 1 {
				return errors.New("injected rename failure")
			}
			return os.Rename(old, new)
		},
	}
	queueCacheWrite(t, agg, path, pk, []byte("rename"))
	agg.flushCacheWrites()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cache appeared after failed rename, err=%v", err)
	}
	if pendingCacheCount(agg) != 1 {
		t.Fatalf("failed rename was not requeued")
	}
	agg.flushCacheWrites()
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "rename" {
		t.Fatalf("rename retry result: body=%q err=%v", body, err)
	}
}

func TestFlushCacheWrites_DropsFailedOlderMutationWhenNewerQueued(t *testing.T) {
	agg := newCacheFailureAggregator(t, nil)
	pk := PublisherKey{0xed, 4}
	path := cachePathFor(t.TempDir(), pk)
	var writes int
	agg.cacheOps = cacheFileOps{
		writeFile: func(name string, body []byte, mode os.FileMode) error {
			writes++
			if writes == 1 {
				return errors.New("injected write failure")
			}
			return os.WriteFile(name, body, mode)
		},
	}
	queueCacheWrite(t, agg, path, pk, []byte("old"))
	agg.flushCacheWrites()
	if pendingCacheCount(agg) != 1 {
		t.Fatalf("failed first mutation was not requeued")
	}
	// A newer revocation supersedes the failed write. The remove succeeds and
	// the older body must never be retried or recreate the cache file.
	agg.mu.Lock()
	agg.cacheWriteSeq++
	agg.pendingCacheWrites[pk] = pendingCacheWrite{path: path, seq: agg.cacheWriteSeq}
	agg.mu.Unlock()
	agg.flushCacheWrites()
	if pendingCacheCount(agg) != 0 {
		t.Fatalf("newer mutation did not supersede failed write")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed older write recreated cache, err=%v", err)
	}
}

func TestSetCacheDirDisableDropsFailedMutationRetry(t *testing.T) {
	agg := newCacheFailureAggregator(t, nil)
	dir := t.TempDir()
	if err := agg.SetCacheDir(dir); err != nil {
		t.Fatalf("SetCacheDir: %v", err)
	}
	pk := PublisherKey{0xed, 5}
	path := cachePathFor(dir, pk)
	writes := 0
	agg.cacheOps.writeFile = func(string, []byte, os.FileMode) error {
		writes++
		return errors.New("injected write failure")
	}
	agg.mu.Lock()
	agg.cacheWriteSeq++
	agg.pendingCacheWrites[pk] = pendingCacheWrite{
		path:       path,
		body:       []byte("stale"),
		seq:        agg.cacheWriteSeq,
		generation: agg.cacheGeneration,
	}
	agg.mu.Unlock()
	agg.flushCacheWrites()
	if pendingCacheCount(agg) != 1 {
		t.Fatal("failed mutation was not queued for retry")
	}
	if err := agg.SetCacheDir(""); err != nil {
		t.Fatalf("disable cache: %v", err)
	}
	agg.flushCacheWrites()
	if writes != 1 || pendingCacheCount(agg) != 0 {
		t.Fatalf("disabled cache retried old mutation: writes=%d pending=%d", writes, pendingCacheCount(agg))
	}
}

func TestSetCacheDirWaitsForInflightWrite(t *testing.T) {
	agg := newCacheFailureAggregator(t, nil)
	dir := t.TempDir()
	if err := agg.SetCacheDir(dir); err != nil {
		t.Fatalf("SetCacheDir: %v", err)
	}
	pk := PublisherKey{0xed, 6}
	path := cachePathFor(dir, pk)
	entered := make(chan struct{})
	release := make(chan struct{})
	agg.cacheOps.writeFile = func(name string, body []byte, mode os.FileMode) error {
		close(entered)
		<-release
		return os.WriteFile(name, body, mode)
	}
	queueCacheWrite(t, agg, path, pk, []byte("inflight"))
	flushed := make(chan struct{})
	go func() {
		agg.flushCacheWrites()
		close(flushed)
	}()
	<-entered
	disabled := make(chan error, 1)
	go func() { disabled <- agg.SetCacheDir("") }()
	select {
	case err := <-disabled:
		t.Fatalf("SetCacheDir returned before in-flight write completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-flushed
	if err := <-disabled; err != nil {
		t.Fatalf("disable cache: %v", err)
	}
}

func TestFlushCacheWritesDropsStaleGenerationBeforeIO(t *testing.T) {
	agg := newCacheFailureAggregator(t, nil)
	dir := t.TempDir()
	if err := agg.SetCacheDir(dir); err != nil {
		t.Fatalf("SetCacheDir: %v", err)
	}
	pk := PublisherKey{0xed, 7}
	path := cachePathFor(dir, pk)
	writes := 0
	agg.cacheOps.writeFile = func(string, []byte, os.FileMode) error {
		writes++
		return nil
	}
	agg.mu.Lock()
	agg.cacheWriteSeq++
	agg.pendingCacheWrites[pk] = pendingCacheWrite{
		path:       path,
		body:       []byte("stale"),
		seq:        agg.cacheWriteSeq,
		generation: agg.cacheGeneration,
	}
	agg.cacheGeneration++
	agg.mu.Unlock()
	agg.flushCacheWrites()
	if writes != 0 {
		t.Fatalf("stale generation reached disk I/O: %d writes", writes)
	}
}

func newCacheFailureAggregator(t *testing.T, logger *slog.Logger) *Aggregator {
	t.Helper()
	agg, err := New(Config{
		PublisherKeys: []PublisherKey{{0xed, 1}},
		Threshold:     1,
		Clock:         func() time.Time { return time.Unix(1, 0) },
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return agg
}

func queueCacheWrite(t *testing.T, agg *Aggregator, path string, pk PublisherKey, body []byte) {
	t.Helper()
	agg.mu.Lock()
	agg.cacheWriteSeq++
	agg.pendingCacheWrites[pk] = pendingCacheWrite{path: path, body: body, seq: agg.cacheWriteSeq}
	agg.mu.Unlock()
}

func pendingCacheCount(agg *Aggregator) int {
	agg.mu.Lock()
	defer agg.mu.Unlock()
	return len(agg.pendingCacheWrites)
}
