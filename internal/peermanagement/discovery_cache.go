package peermanagement

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// CachedEndpoint represents a cached peer endpoint.
type CachedEndpoint struct {
	Address    string    `json:"address"`
	Port       uint16    `json:"port"`
	LastSeen   time.Time `json:"last_seen"`
	Valence    int       `json:"valence"`
	FailCount  int       `json:"fail_count"`
	LastFailed time.Time `json:"last_failed,omitempty"`
}

// BootCache persists known peer addresses across restarts.
type BootCache struct {
	mu        sync.RWMutex
	cache     map[string]*CachedEndpoint
	filePath  string
	dirty     bool
	writeFile atomicFileWriter
}

// NewBootCache creates a new boot cache.
func NewBootCache(dataDir string) *BootCache {
	var filePath string
	if dataDir != "" {
		filePath = filepath.Join(dataDir, DefaultBootCacheFile)
	}
	return &BootCache{
		cache:    make(map[string]*CachedEndpoint),
		filePath: filePath,
	}
}

// Load loads the cache from disk.
func (bc *BootCache) Load() error {
	if bc == nil || bc.filePath == "" {
		return nil
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()

	data, err := os.ReadFile(bc.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var entries []*CachedEndpoint
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	if entries == nil {
		return fmt.Errorf("cache must be a JSON array")
	}

	loaded := make(map[string]*CachedEndpoint, len(entries))
	now := time.Now()
	for i, entry := range entries {
		if err := validateCachedEndpoint(entry); err != nil {
			return fmt.Errorf("boot cache entry %d: %w", i, err)
		}
		if now.Sub(entry.LastSeen) <= CacheEntryTTL {
			if _, exists := loaded[entry.Address]; exists {
				return fmt.Errorf("boot cache entry %d: duplicate address %q", i, entry.Address)
			}
			loaded[entry.Address] = cloneCachedEndpoint(entry)
		}
	}
	bc.cache = loaded
	bc.dirty = false
	return nil
}

// Save writes the cache to disk. The complete snapshot and durable rename are
// serialized with mutations so a concurrent save cannot overwrite newer
// state. Once rename succeeds the in-memory snapshot matches the file even if
// the subsequent directory sync reports an uncertain durability error.
func (bc *BootCache) Save() error {
	if bc == nil || bc.filePath == "" {
		return nil
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()

	if !bc.dirty {
		return nil
	}

	entries := make([]*CachedEndpoint, 0, len(bc.cache))
	for _, entry := range bc.cache {
		if err := validateCachedEndpoint(entry); err != nil {
			return err
		}
		entries = append(entries, cloneCachedEndpoint(entry))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Address < entries[j].Address })

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}

	writer := bc.writeFile
	if writer == nil {
		writer = writeAtomicFile
	}
	committed, err := writer(bc.filePath, data, 0o600)
	if committed {
		bc.dirty = false
	}
	return err
}

// Insert adds or updates an endpoint in the cache.
func (bc *BootCache) Insert(address string, port uint16) {
	if bc == nil || strings.TrimSpace(address) == "" || port == 0 {
		return
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()

	if entry, exists := bc.cache[address]; exists {
		entry.LastSeen = time.Now()
		entry.Valence++
	} else {
		bc.cache[address] = &CachedEndpoint{
			Address:  address,
			Port:     port,
			LastSeen: time.Now(),
			Valence:  1,
		}
	}
	bc.dirty = true
}

// MarkFailed records a connection failure.
func (bc *BootCache) MarkFailed(address string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	if entry, exists := bc.cache[address]; exists {
		entry.FailCount++
		entry.LastFailed = time.Now()
		entry.Valence--
		if entry.Valence < 0 {
			entry.Valence = 0
		}
		bc.dirty = true
	}
}

// MarkSuccess records a successful connection.
func (bc *BootCache) MarkSuccess(address string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	if entry, exists := bc.cache[address]; exists {
		entry.LastSeen = time.Now()
		entry.Valence++
		entry.FailCount = 0
		bc.dirty = true
	}
}

// Endpoints returns endpoints sorted by valence.
func (bc *BootCache) Endpoints(limit int) []*CachedEndpoint {
	if bc == nil {
		return nil
	}
	bc.mu.RLock()
	defer bc.mu.RUnlock()

	entries := make([]*CachedEndpoint, 0, len(bc.cache))
	for _, entry := range bc.cache {
		entries = append(entries, cloneCachedEndpoint(entry))
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Valence > entries[j].Valence
	})

	if limit > 0 && limit < len(entries) {
		entries = entries[:limit]
	}
	return entries
}

func cloneCachedEndpoint(entry *CachedEndpoint) *CachedEndpoint {
	if entry == nil {
		return nil
	}
	copy := *entry
	return &copy
}

func validateCachedEndpoint(entry *CachedEndpoint) error {
	if entry == nil {
		return fmt.Errorf("nil endpoint")
	}
	if strings.TrimSpace(entry.Address) == "" {
		return fmt.Errorf("empty address")
	}
	if entry.Port == 0 {
		return fmt.Errorf("invalid port 0")
	}
	endpoint, err := ParseEndpoint(entry.Address)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", entry.Address, err)
	}
	if endpoint.Port != entry.Port {
		return fmt.Errorf("address port %d does not match port %d", endpoint.Port, entry.Port)
	}
	if entry.LastSeen.IsZero() {
		return fmt.Errorf("missing last_seen")
	}
	if entry.Valence < 0 || entry.FailCount < 0 {
		return fmt.Errorf("negative counters")
	}
	return nil
}

type atomicFileWriter func(path string, data []byte, mode os.FileMode) (committed bool, err error)

func writeAtomicFile(path string, data []byte, mode os.FileMode) (bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return false, err
	}
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
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	removeTemp = false
	dirFile, err := os.Open(dir)
	if err != nil {
		return true, err
	}
	syncErr := dirFile.Sync()
	closeErr := dirFile.Close()
	if syncErr != nil {
		return true, syncErr
	}
	return true, closeErr
}
