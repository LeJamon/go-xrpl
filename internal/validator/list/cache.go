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

// cachedEnvelope is the on-disk shape produced by writeCacheLocked
// and consumed by LoadCache. Mirrors rippled's buildFileData layout
// at ValidatorList.cpp:304-366 — top-level manifest / version /
// public_key for v1, plus a blobs_v2 array for collections, and a
// refresh_interval added by cacheValidatorFile.
//
// PublicKey and RefreshInterval are written for byte-compatibility
// with rippled's format (line 321 + line 386) but are not consumed
// by LoadCache: the publisher is identified by the file name, and
// refresh cadence is driven by the site poller, not the cache.
type cachedEnvelope struct {
	Manifest        string            `json:"manifest"`
	PublicKey       string            `json:"public_key,omitempty"`
	Blob            string            `json:"blob,omitempty"`
	Signature       string            `json:"signature,omitempty"`
	Version         uint32            `json:"version"`
	BlobsV2         []cachedBlobEntry `json:"blobs_v2,omitempty"`
	RefreshInterval uint32            `json:"refresh_interval,omitempty"`
}

type cachedBlobEntry struct {
	Manifest  string `json:"manifest,omitempty"`
	Blob      string `json:"blob"`
	Signature string `json:"signature"`
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
	if dir == "" {
		a.mu.Lock()
		a.cacheDir = ""
		a.mu.Unlock()
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cache dir %q: %w", dir, err)
	}
	a.mu.Lock()
	a.cacheDir = dir
	a.mu.Unlock()
	return nil
}

// writeCacheLocked persists the publisher's current accepted form to
// disk. Caller must hold a.mu. No-op when no cache directory is set
// or the publisher has no accepted list to write.
//
// Atomic via tmp + rename so a partial write cannot be observed by a
// concurrent LoadCache. Errors are logged but not surfaced — a failed
// cache write is recoverable; rippled does the same (logs and
// ignores) at ValidatorList.cpp:390-395.
func (a *Aggregator) writeCacheLocked(s *PublisherState) {
	if a.cacheDir == "" {
		return
	}
	if s == nil || s.Sequence == 0 || len(s.RawManifest) == 0 {
		return
	}
	env := cachedEnvelope{
		Manifest:        string(s.RawManifest),
		PublicKey:       hex.EncodeToString(s.MasterKey[:]),
		Version:         s.Version,
		RefreshInterval: cacheRefreshIntervalMinutes,
	}
	if env.Version == 0 {
		env.Version = 1
	}
	if len(s.Remaining) > 0 {
		// v2 shape — current + remaining, ordered by sequence ascending.
		env.Version = 2
		env.BlobsV2 = append(env.BlobsV2, cachedBlobEntry{
			Blob:      string(s.RawBlob),
			Signature: string(s.RawSignature),
		})
		seqs := make([]uint32, 0, len(s.Remaining))
		for seq := range s.Remaining {
			seqs = append(seqs, seq)
		}
		slices.Sort(seqs)
		for _, seq := range seqs {
			rb := s.Remaining[seq]
			env.BlobsV2 = append(env.BlobsV2, cachedBlobEntry{
				Blob:      string(rb.RawBlob),
				Signature: string(rb.RawSignature),
			})
		}
	} else {
		env.Blob = string(s.RawBlob)
		env.Signature = string(s.RawSignature)
	}
	body, err := json.Marshal(env)
	if err != nil {
		a.logger.Debug("validator list: cache marshal failed",
			"publisher", hex.EncodeToString(s.MasterKey[:]),
			"error", err)
		return
	}
	path := cachePathFor(a.cacheDir, s.MasterKey)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		a.logger.Debug("validator list: cache write failed",
			"publisher", hex.EncodeToString(s.MasterKey[:]),
			"error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		a.logger.Debug("validator list: cache rename failed",
			"publisher", hex.EncodeToString(s.MasterKey[:]),
			"error", err)
		_ = os.Remove(tmp)
	}
}

// removeCacheLocked deletes the on-disk cache file for a publisher
// (called on revocation). Caller must hold a.mu. Missing-file errors
// are silently ignored.
func (a *Aggregator) removeCacheLocked(pk PublisherKey) {
	if a.cacheDir == "" {
		return
	}
	if err := os.Remove(cachePathFor(a.cacheDir, pk)); err != nil && !os.IsNotExist(err) {
		a.logger.Debug("validator list: cache remove failed",
			"publisher", hex.EncodeToString(pk[:]),
			"error", err)
	}
}

// LoadCache rehydrates publisher state from the on-disk cache. For
// every configured publisher, reads `<cacheDir>/cache.<pubKeyHex>` if
// present and re-applies the blob through the normal ApplyList /
// applyCachedCollection pipeline so the usual signature verification
// and trust-set computation runs.
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
		body, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				a.logger.Debug("validator list: cache read failed",
					"publisher", hex.EncodeToString(pk[:]),
					"error", err)
			}
			continue
		}
		var env cachedEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			a.logger.Debug("validator list: cache decode failed",
				"publisher", hex.EncodeToString(pk[:]),
				"error", err)
			continue
		}
		if env.Version == 0 || env.Manifest == "" {
			continue
		}
		uri := "file://" + path
		applied := false
		if len(env.BlobsV2) > 0 {
			if a.applyCachedCollection(env.Version, []byte(env.Manifest), env.BlobsV2, uri) {
				applied = true
			}
		} else if env.Blob != "" && env.Signature != "" {
			disp, _, _ := a.ApplyList(
				[]byte(env.Manifest),
				[]byte(env.Blob),
				[]byte(env.Signature),
				env.Version,
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

// applyCachedCollection feeds a parsed cachedEnvelope.blobs_v2 through
// the per-blob apply path (signature verify + state machine). Returns
// true if at least one blob applied to ShouldRelay disposition.
func (a *Aggregator) applyCachedCollection(version uint32, manifestBytes []byte, blobs []cachedBlobEntry, siteURI string) bool {
	if !isSupportedVersion(version) || len(blobs) == 0 {
		return false
	}
	if len(blobs) > MaxSupportedBlobs {
		return false
	}
	any := false
	for _, b := range blobs {
		mf := []byte(b.Manifest)
		if len(mf) == 0 {
			mf = manifestBytes
		}
		disp, _, _ := a.ApplyList(mf, []byte(b.Blob), []byte(b.Signature), version, siteURI)
		if disp.ShouldRelay() {
			any = true
		}
	}
	return any
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
