package list

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// cacheRefreshIntervalMinutes mirrors rippled's
// `value[jss::refresh_interval] = 24 * 60` at
// rippled/src/xrpld/app/misc/detail/ValidatorList.cpp:386 — rippled is
// the only writer of these files in its model, so reads can be safely
// delayed up to 24 hours. go-xrpl does not consume the value on the
// load path (LoadCache hydrates unconditionally before site polling
// begins) but emits it so the on-disk format stays byte-compatible.
const cacheRefreshIntervalMinutes = 24 * 60

// cacheFilePrefix mirrors rippled's ValidatorList::filePrefix_ at
// rippled/src/xrpld/app/misc/detail/ValidatorList.cpp:118
// (`"cache."`). Combined with the hex-encoded publisher master key
// it yields the per-publisher cache file name.
const cacheFilePrefix = "cache."

type cacheEnvelope struct {
	Manifest       string               `json:"manifest"`
	PublicKey      string               `json:"public_key,omitempty"`
	Blob           *string              `json:"blob,omitempty"`
	Signature      *string              `json:"signature,omitempty"`
	Version        uint32               `json:"version"`
	BlobsV2        *[]cacheEnvelopeBlob `json:"blobs_v2,omitempty"`
	RefreshMinutes float64              `json:"refresh_interval,omitempty"`
}

type cacheEnvelopeBlob struct {
	Manifest  *string `json:"manifest,omitempty"`
	Blob      string  `json:"blob"`
	Signature string  `json:"signature"`
}

// pendingCacheWrite is a marshaled cache-file mutation queued under a.mu
// by writeCacheLocked / removeCacheLocked and flushed to disk by
// flushCacheWrites once the lock is released, so disk latency never stalls
// VL ingest or the validators RPC read path. body == nil encodes a delete
// (revocation). seq is a monotonic stamp (a.cacheWriteSeq) used by
// flushCacheWrites to drop a mutation that a newer one for the same
// publisher has already superseded on disk.
type pendingCacheWrite struct {
	path       string
	body       []byte
	seq        uint64
	generation uint64
}

// cacheFileOps isolates the three filesystem mutations used by the cache
// writer. The production zero value uses os, while same-package tests can
// inject deterministic failures without relying on permissions or timing.
type cacheFileOps struct {
	remove    func(string) error
	writeFile func(string, []byte, os.FileMode) error
	rename    func(string, string) error
}

// SetCacheDir wires the on-disk cache directory for accepted
// publisher lists. After each accepted ApplyList the aggregator
// writes `<dir>/cache.<pubKeyHex>` so a cold restart has up-to-date
// trust without waiting for the first poll. Mirrors rippled's
// ValidatorList::cacheValidatorFile at
// rippled/src/xrpld/app/misc/detail/ValidatorList.cpp:368-396.
//
// Passing an empty string disables on-disk caching. Safe to call
// before or after Start; takes a.mu briefly.
func (a *Aggregator) SetCacheDir(dir string) error {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create cache dir %q: %w", dir, err)
		}
	}
	a.cacheWriteMu.Lock()
	defer a.cacheWriteMu.Unlock()
	a.mu.Lock()
	if a.cacheDir != dir {
		a.cacheGeneration++
		a.pendingCacheWrites = make(map[PublisherKey]pendingCacheWrite)
	}
	a.cacheDir = dir
	a.mu.Unlock()
	return nil
}

// writeCacheLocked marshals the publisher's current accepted form and
// queues it for the deferred disk flush. Caller must hold a.mu. No-op
// when no cache directory is set or the publisher has no accepted list to
// write.
//
// The marshal happens under a.mu but the disk write does not: the bytes
// are queued in pendingCacheWrites and written by flushCacheWrites after
// the lock is released, so a slow or failing disk never stalls VL ingest
// or the validators RPC read path. The eventual write is atomic via
// tmp + rename. rippled likewise logs-and-ignores cache write failures
// (ValidatorList.cpp:390-395).
func (a *Aggregator) writeCacheLocked(s *publisherState) {
	if a.cacheDir == "" {
		return
	}
	if s == nil || s.Sequence == 0 || len(s.RawManifest) == 0 {
		return
	}
	env := cacheEnvelope{
		Manifest:       string(s.RawManifest),
		PublicKey:      hex.EncodeToString(s.MasterKey[:]),
		Version:        s.Version,
		RefreshMinutes: cacheRefreshIntervalMinutes,
	}
	if env.Version == 0 {
		env.Version = 1
	}
	needsV2 := s.Version >= 2 || s.RawLocalManifestSet || len(s.Remaining) > 0
	if needsV2 {
		// v2 shape — current + remaining, ordered by sequence ascending.
		env.Version = 2
		blobs := []cacheEnvelopeBlob{{
			Manifest:  optionalEnvelopeManifest(s.RawLocalManifest, s.RawLocalManifestSet),
			Blob:      string(s.RawBlob),
			Signature: string(s.RawSignature),
		}}
		seqs := make([]uint32, 0, len(s.Remaining))
		for seq := range s.Remaining {
			seqs = append(seqs, seq)
		}
		slices.Sort(seqs)
		for _, seq := range seqs {
			rb := s.Remaining[seq]
			blobs = append(blobs, cacheEnvelopeBlob{
				Manifest:  optionalEnvelopeManifest(rb.RawLocalManifest, rb.RawLocalManifestSet),
				Blob:      string(rb.RawBlob),
				Signature: string(rb.RawSignature),
			})
		}
		env.BlobsV2 = &blobs
	} else {
		blob := string(s.RawBlob)
		signature := string(s.RawSignature)
		env.Blob = &blob
		env.Signature = &signature
	}
	body, err := json.Marshal(env)
	if err != nil {
		a.logger.Debug("validator list: cache marshal failed",
			"publisher", hex.EncodeToString(s.MasterKey[:]),
			"error", err)
		return
	}
	a.cacheWriteSeq++
	a.pendingCacheWrites[s.MasterKey] = pendingCacheWrite{
		path:       cachePathFor(a.cacheDir, s.MasterKey),
		body:       body,
		seq:        a.cacheWriteSeq,
		generation: a.cacheGeneration,
	}
}

// removeCacheLocked queues deletion of a publisher's on-disk cache file
// (called on revocation). Caller must hold a.mu. The unlink runs in
// flushCacheWrites after the lock is released, ordered against any pending
// write for the same publisher (via seq) so a stale write cannot recreate
// a revoked publisher's cache. Missing-file errors are ignored.
func (a *Aggregator) removeCacheLocked(pk PublisherKey) {
	if a.cacheDir == "" {
		return
	}
	a.cacheWriteSeq++
	a.pendingCacheWrites[pk] = pendingCacheWrite{
		path:       cachePathFor(a.cacheDir, pk),
		body:       nil,
		seq:        a.cacheWriteSeq,
		generation: a.cacheGeneration,
	}
}

// flushCacheWrites drains the queued cache mutations and applies them to
// disk. MUST be called with a.mu NOT held: it briefly re-acquires a.mu to
// drain the queue, then performs the syscalls under cacheWriteMu. The
// per-publisher seq stamp lets a superseded mutation be dropped, so two
// concurrent flushers cannot leave a stale cache file as the winner.
// Cheap no-op when nothing is queued. Errors are logged, not surfaced — a
// failed cache mutations are requeued with their original sequence so a
// later Tick or flush can retry them without allowing an older write to
// overtake a newer mutation.
func (a *Aggregator) flushCacheWrites() {
	a.mu.Lock()
	if len(a.pendingCacheWrites) == 0 {
		a.mu.Unlock()
		return
	}
	pending := a.pendingCacheWrites
	a.pendingCacheWrites = make(map[PublisherKey]pendingCacheWrite)
	a.mu.Unlock()

	a.cacheWriteMu.Lock()
	defer a.cacheWriteMu.Unlock()
	a.mu.Lock()
	generation := a.cacheGeneration
	a.mu.Unlock()
	ops := a.cacheOps
	if ops.remove == nil {
		ops.remove = os.Remove
	}
	if ops.writeFile == nil {
		ops.writeFile = os.WriteFile
	}
	if ops.rename == nil {
		ops.rename = os.Rename
	}
	failed := make(map[PublisherKey]pendingCacheWrite)
	for pk, w := range pending {
		if w.generation != generation {
			continue
		}
		if w.seq <= a.cacheWritten[pk] {
			continue
		}
		if w.body == nil {
			if err := ops.remove(w.path); err != nil && !os.IsNotExist(err) {
				a.logger.Error("validator list: cache remove failed",
					"publisher", hex.EncodeToString(pk[:]),
					"retry", true,
					"error", err)
				failed[pk] = w
				continue
			}
			a.cacheWritten[pk] = w.seq
			continue
		}
		tmp := w.path + ".tmp"
		if err := ops.writeFile(tmp, w.body, 0o600); err != nil {
			a.logger.Error("validator list: cache write failed",
				"publisher", hex.EncodeToString(pk[:]),
				"retry", true,
				"error", err)
			if cleanupErr := ops.remove(tmp); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				a.logger.Error("validator list: cache temporary-file cleanup failed",
					"publisher", hex.EncodeToString(pk[:]),
					"error", cleanupErr)
			}
			failed[pk] = w
			continue
		}
		if err := ops.rename(tmp, w.path); err != nil {
			a.logger.Error("validator list: cache rename failed",
				"publisher", hex.EncodeToString(pk[:]),
				"retry", true,
				"error", err)
			if cleanupErr := ops.remove(tmp); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				a.logger.Error("validator list: cache temporary-file cleanup failed",
					"publisher", hex.EncodeToString(pk[:]),
					"error", cleanupErr)
			}
			failed[pk] = w
			continue
		}
		a.cacheWritten[pk] = w.seq
	}
	if len(failed) == 0 {
		return
	}
	// Keep cacheWriteMu held while checking cacheWritten and merging failed
	// entries back under a.mu. This lock order prevents a concurrent flush
	// from applying a newer mutation and then allowing an older failure to be
	// requeued behind it.
	a.mu.Lock()
	defer a.mu.Unlock()
	for pk, w := range failed {
		if w.generation != a.cacheGeneration {
			continue
		}
		if w.seq <= a.cacheWritten[pk] {
			continue
		}
		if current, ok := a.pendingCacheWrites[pk]; ok && current.seq >= w.seq {
			continue
		}
		a.pendingCacheWrites[pk] = w
	}
}

func optionalEnvelopeManifest(raw []byte, present bool) *string {
	if !present {
		return nil
	}
	value := string(raw)
	return &value
}

// LoadCache rehydrates publisher state from the on-disk cache. For
// every configured publisher, reads `<cacheDir>/cache.<pubKeyHex>` if
// present and re-applies the blob through the normal ApplyList /
// ApplyCollection pipeline so the usual signature verification and
// trust-set computation runs.
//
// Returns the number of publishers whose state was hydrated. Safe to
// call before Start; intended to be invoked once during component
// bootstrap. Failed cache reads (missing file, parse error, signature
// mismatch) are logged and skipped — a stale or tampered cache file
// MUST NOT prevent normal startup.
//
// Mirrors rippled ValidatorList::loadLists at
// rippled/src/xrpld/app/misc/detail/ValidatorList.cpp:1300-1351 +
// missingSite() drain at ValidatorSite.cpp:120-124, folded into a
// single call here because go-xrpl plumbs the file source directly to
// the aggregator rather than routing through the site poller.
func (a *Aggregator) LoadCache() int {
	a.mu.Lock()
	dir := a.cacheDir
	// Snapshot the publisher set and skip any that have already been
	// hydrated by another source (e.g. a site-poll racing the cache
	// load). Mirrors rippled's loadLists() at ValidatorList.cpp:1315-1316
	// which skips publishers whose PublisherStatus is `available`.
	pubs := make([]PublisherKey, 0, len(a.publishers))
	for k := range a.publishers {
		if st, ok := a.state[k]; ok && st != nil && st.Status == StatusAvailable {
			continue
		}
		pubs = append(pubs, k)
	}
	a.mu.Unlock()

	if dir == "" {
		return 0
	}

	loaded := 0
	for _, pk := range pubs {
		path := cachePathFor(dir, pk)
		body, err := readBoundedFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				a.logger.Debug("validator list: cache read failed",
					"publisher", hex.EncodeToString(pk[:]),
					"error", err)
			}
			continue
		}
		var env envelope
		if err := json.Unmarshal(body, &env); err != nil {
			a.logger.Debug("validator list: cache decode failed",
				"publisher", hex.EncodeToString(pk[:]),
				"error", err)
			continue
		}
		if err := env.validateShape(); err != nil {
			a.logger.Debug("validator list: invalid cache envelope",
				"publisher", hex.EncodeToString(pk[:]),
				"error", err)
			continue
		}
		uri := "file://" + path
		applied := false
		switch {
		case env.Version.Value != 1:
			disps, _, _ := a.ApplyCollection(env.toCollection(), uri)
			for _, d := range disps {
				if d.ShouldRelay() {
					applied = true
					break
				}
			}
		case env.Version.Value == 1:
			disp, _, _ := a.ApplyList(
				[]byte(env.Manifest.Value),
				[]byte(env.Blob.Value),
				[]byte(env.Signature.Value),
				env.Version.Value,
				uri,
			)
			if disp.ShouldRelay() {
				applied = true
			}
		}
		if applied {
			loaded++
		}
	}
	if loaded > 0 {
		a.logger.Info("validator list: hydrated publishers from cache",
			"count", loaded,
			"dir", dir)
	}
	return loaded
}

// cachePathFor returns the absolute cache file path for a publisher
// master key. The hex encoding is lowercase via hex.EncodeToString —
// rippled accepts case-insensitive filenames on the read path so
// either works; lowercase is the Go convention.
func cachePathFor(dir string, pk PublisherKey) string {
	name := cacheFilePrefix + hex.EncodeToString(pk[:])
	// Defensive: refuse a path with separators in the publisher hex
	// (impossible — hex is /[0-9a-f]/ — but cheap insurance).
	if strings.ContainsAny(name, "/\\") {
		name = cacheFilePrefix + "invalid"
	}
	return filepath.Join(dir, name)
}
