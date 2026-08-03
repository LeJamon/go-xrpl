package peermanagement

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
)

// Discovery constants.
const (
	DefaultBootCacheFile   = "peerfinder.cache"
	MaxCachedEndpoints     = 1000
	CacheEntryTTL          = 7 * 24 * time.Hour
	MaxHops                = 3
	DefaultReservationFile = "peer_reservations.json"

	// MaxDiscoveredPeers caps the peer-discovery set. AddPeer evicts the
	// least-recently-seen non-connected, non-configured entry before exceeding
	// it, so gossiped endpoint announcements cannot grow d.peers without
	// bound (issue #1170). Sized well above any real network's reachable
	// peer count; live connections, fixed peers, and bootstrap peers are never
	// evicted.
	MaxDiscoveredPeers = 8192

	recentConnectAttempt = time.Minute
)

var fixedConnectBackoff = [...]time.Duration{
	time.Minute,
	time.Minute,
	2 * time.Minute,
	3 * time.Minute,
	5 * time.Minute,
	8 * time.Minute,
	13 * time.Minute,
	21 * time.Minute,
	34 * time.Minute,
	55 * time.Minute,
}

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
		// Rename made the new snapshot visible. Keep memory aligned with that
		// snapshot even when directory durability is uncertain.
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
		entries = append(entries, &CachedEndpoint{
			Address:    entry.Address,
			Port:       entry.Port,
			LastSeen:   entry.LastSeen,
			Valence:    entry.Valence,
			FailCount:  entry.FailCount,
			LastFailed: entry.LastFailed,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Valence > entries[j].Valence
	})

	if limit > 0 && limit < len(entries) {
		entries = entries[:limit]
	}
	return entries
}

// PeerReservation represents a reserved peer slot.
type PeerReservation struct {
	NodeID      string `json:"node_id"`
	Description string `json:"description,omitempty"`
}

// ReservationTable manages peer reservations.
type ReservationTable struct {
	mu           sync.RWMutex
	reservations map[string]*PeerReservation
	filePath     string
	writeFile    atomicFileWriter
}

// NewReservationTable creates a new reservation table.
func NewReservationTable(dataDir string) *ReservationTable {
	var filePath string
	if dataDir != "" {
		filePath = filepath.Join(dataDir, DefaultReservationFile)
	}
	return &ReservationTable{
		reservations: make(map[string]*PeerReservation),
		filePath:     filePath,
	}
}

// Contains returns true if the node has a reservation.
func (t *ReservationTable) Contains(nodeID string) bool {
	if t == nil {
		return false
	}
	canonical, err := canonicalNodePublicKey(nodeID)
	if err != nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, exists := t.reservations[canonical]
	return exists
}

// Insert adds or replaces a reservation and persists the table, returning the
// previous entry for the same node (nil if there was none) and any persistence
// error. Mirrors rippled's PeerReservationTable::insert_or_assign, whose DB
// write surfaces failures to the caller.
func (t *ReservationTable) Insert(r *PeerReservation) (*PeerReservation, error) {
	if t == nil {
		return nil, fmt.Errorf("reservation table is nil")
	}
	normalized, err := normalizeReservation(r)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reservations == nil {
		t.reservations = make(map[string]*PeerReservation)
	}
	prev := cloneReservation(t.reservations[normalized.NodeID])
	t.reservations[normalized.NodeID] = normalized
	committed, err := t.saveLocked()
	if err != nil && !committed {
		if prev == nil {
			delete(t.reservations, normalized.NodeID)
		} else {
			t.reservations[normalized.NodeID] = prev
		}
		return prev, err
	}
	return prev, err
}

// Erase removes a reservation and persists the table, returning the removed
// entry (nil if none existed) and any persistence error. Mirrors rippled's
// PeerReservationTable::erase.
func (t *ReservationTable) Erase(nodeID string) (*PeerReservation, error) {
	if t == nil {
		return nil, fmt.Errorf("reservation table is nil")
	}
	canonical, err := canonicalNodePublicKey(nodeID)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, ok := t.reservations[canonical]
	if ok {
		delete(t.reservations, canonical)
	}
	if !ok {
		return nil, nil
	}
	committed, err := t.saveLocked()
	if err != nil && !committed {
		t.reservations[canonical] = prev
		return cloneReservation(prev), err
	}
	return cloneReservation(prev), err
}

// List returns a snapshot of all reservations.
func (t *ReservationTable) List() []PeerReservation {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]PeerReservation, 0, len(t.reservations))
	for _, r := range t.reservations {
		if r != nil {
			out = append(out, *cloneReservation(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// Load reads the reservation table from disk. A missing file is not an error.
func (t *ReservationTable) Load() error {
	if t == nil {
		return fmt.Errorf("reservation table is nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.filePath == "" {
		return nil
	}
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var entries []*PeerReservation
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	if entries == nil {
		return fmt.Errorf("reservations must be a JSON array")
	}
	loaded := make(map[string]*PeerReservation, len(entries))
	for i, entry := range entries {
		normalized, err := normalizeReservation(entry)
		if err != nil {
			return fmt.Errorf("reservation entry %d: %w", i, err)
		}
		if _, exists := loaded[normalized.NodeID]; exists {
			return fmt.Errorf("reservation entry %d: duplicate node id %q", i, normalized.NodeID)
		}
		loaded[normalized.NodeID] = normalized
	}
	t.reservations = loaded
	return nil
}

// Save writes the reservation table to disk. It is a no-op when no data
// directory is configured (e.g. standalone / in-memory tests).
func (t *ReservationTable) Save() error {
	if t == nil {
		return fmt.Errorf("reservation table is nil")
	}
	if t.filePath == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := t.saveLocked()
	return err
}

func (t *ReservationTable) saveLocked() (bool, error) {
	if t.filePath == "" {
		return true, nil
	}
	entries := make([]*PeerReservation, 0, len(t.reservations))
	seen := make(map[string]struct{}, len(t.reservations))
	for _, r := range t.reservations {
		normalized, err := normalizeReservation(r)
		if err != nil {
			return false, err
		}
		if _, exists := seen[normalized.NodeID]; exists {
			return false, fmt.Errorf("duplicate node id %q", normalized.NodeID)
		}
		seen[normalized.NodeID] = struct{}{}
		entries = append(entries, normalized)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].NodeID < entries[j].NodeID })

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return false, err
	}
	writer := t.writeFile
	if writer == nil {
		writer = writeAtomicFile
	}
	return writer(t.filePath, data, 0o600)
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

func cloneReservation(r *PeerReservation) *PeerReservation {
	if r == nil {
		return nil
	}
	copy := *r
	return &copy
}

func normalizeReservation(r *PeerReservation) (*PeerReservation, error) {
	if r == nil {
		return nil, fmt.Errorf("nil reservation")
	}
	canonical, err := canonicalNodePublicKey(r.NodeID)
	if err != nil {
		return nil, fmt.Errorf("invalid node id %q: %w", r.NodeID, err)
	}
	normalized := cloneReservation(r)
	normalized.NodeID = canonical
	return normalized, nil
}

func canonicalNodePublicKey(nodeID string) (string, error) {
	if strings.TrimSpace(nodeID) != nodeID || nodeID == "" {
		return "", fmt.Errorf("empty or whitespace-padded node id")
	}
	raw, err := addresscodec.DecodeNodePublicKey(nodeID)
	if err != nil {
		return "", err
	}
	if len(raw) != 33 || (raw[0] != 0xED && raw[0] != 0x02 && raw[0] != 0x03) {
		return "", fmt.Errorf("invalid node public key type or length")
	}
	canonical, err := addresscodec.EncodeNodePublicKey(raw)
	if err != nil {
		return "", err
	}
	return canonical, nil
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

// Reservations exposes the reservation table backing the peer_reservations_*
// RPCs and consulted at inbound admission (nil when no data directory is
// configured).
func (d *Discovery) Reservations() *ReservationTable {
	return d.reservation
}

// DiscoveredPeer stores information about a discovered peer.
type DiscoveredPeer struct {
	Address   string
	Hops      uint32
	LastSeen  time.Time
	Connected bool
	PeerID    PeerID
	Source    PeerID

	compressionKnown    bool
	supportsCompression bool

	// Position in Discovery.lru; guarded by Discovery.mu.
	lruEntry *list.Element
}

type bootstrapSourceObservation struct {
	projected     time.Duration
	cooldownUntil time.Time
}

func (o bootstrapSourceObservation) coolingDown(now time.Time) bool {
	return now.Before(o.cooldownUntil)
}

func (o bootstrapSourceObservation) viable() bool {
	return o.projected > 0 && o.projected <= bootstrapTargetDuration
}

type bootstrapSourceStatus struct {
	known    int
	unviable int
	episode  uint64
}

func (s bootstrapSourceStatus) allUnviable() bool {
	return s.known > 0 && s.known == s.unviable
}

type connectAttempt struct {
	inFlight    bool
	failures    int
	nextAttempt time.Time
}

type connectAttemptResult uint8

const (
	connectAttemptReleased connectAttemptResult = iota
	connectAttemptSucceeded
	connectAttemptFailed
)

// Discovery manages peer discovery and connection maintenance.
type Discovery struct {
	mu sync.RWMutex

	cfg Config

	peers map[string]*DiscoveredPeer
	// lru orders d.peers by last-seen recency (front = most recent) so
	// eviction at MaxDiscoveredPeers pops from the stale end instead of
	// scanning the whole map. Every peers insert/delete updates it.
	lru             list.List
	connected       map[PeerID]*DiscoveredPeer
	fixedPeers      map[string]bool
	persistentPeers map[string]bool
	// Fixed-peer backoff is endpoint keyed; recent-attempt suppression is
	// host keyed so changing the port cannot bypass it.
	connectAttempts  map[string]*connectAttempt
	recentAttempts   map[string]time.Time
	bootstrapSources map[string]bootstrapSourceObservation
	bootstrapEpisode uint64
	bootCache        *BootCache
	reservation      *ReservationTable
	lookupIP         func(context.Context, string) ([]net.IPAddr, error)

	events   chan<- Event
	cancel   context.CancelFunc
	stopped  bool
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewDiscovery creates a new Discovery instance.
func NewDiscovery(cfg *Config, events chan<- Event) *Discovery {
	discoveryCfg := *cfg
	discoveryCfg.BootstrapPeers = append([]string(nil), cfg.BootstrapPeers...)
	discoveryCfg.FixedPeers = append([]string(nil), cfg.FixedPeers...)
	d := &Discovery{
		cfg:              discoveryCfg,
		peers:            make(map[string]*DiscoveredPeer),
		connected:        make(map[PeerID]*DiscoveredPeer),
		fixedPeers:       make(map[string]bool),
		persistentPeers:  make(map[string]bool),
		connectAttempts:  make(map[string]*connectAttempt),
		recentAttempts:   make(map[string]time.Time),
		bootstrapSources: make(map[string]bootstrapSourceObservation),
		events:           events,
		lookupIP:         net.DefaultResolver.LookupIPAddr,
	}
	if d.cfg.Clock == nil {
		d.cfg.Clock = time.Now
	}

	for _, addr := range discoveryCfg.FixedPeers {
		d.fixedPeers[addr] = true
		d.persistentPeers[addr] = true
	}
	for _, addr := range discoveryCfg.BootstrapPeers {
		d.persistentPeers[addr] = true
	}

	if cfg.DataDir != "" {
		d.bootCache = NewBootCache(cfg.DataDir)
		d.reservation = NewReservationTable(cfg.DataDir)
	}

	return d
}

func (d *Discovery) now() time.Time {
	if d.cfg.Clock != nil {
		return d.cfg.Clock()
	}
	return time.Now()
}

// Start starts the discovery service.
func (d *Discovery) Start(ctx context.Context) error {
	if d.bootCache != nil {
		if err := d.bootCache.Load(); err != nil {
			return fmt.Errorf("load peer boot cache %q: %w", d.bootCache.filePath, err)
		}
	}
	if d.reservation != nil {
		if err := d.reservation.Load(); err != nil {
			return fmt.Errorf("load peer reservations %q: %w", d.reservation.filePath, err)
		}
	}

	bootstrapPeers, fixedPeers := d.resolveConfiguredPeers(ctx)
	d.mu.Lock()
	for _, addr := range d.cfg.BootstrapPeers {
		delete(d.persistentPeers, addr)
	}
	for _, addr := range d.cfg.FixedPeers {
		delete(d.fixedPeers, addr)
		delete(d.persistentPeers, addr)
	}
	d.cfg.BootstrapPeers = bootstrapPeers
	d.cfg.FixedPeers = fixedPeers
	for _, addr := range bootstrapPeers {
		d.persistentPeers[addr] = true
	}
	for _, addr := range fixedPeers {
		d.fixedPeers[addr] = true
		d.persistentPeers[addr] = true
	}
	d.mu.Unlock()

	for _, addr := range bootstrapPeers {
		d.AddPeer(addr, 0, 0)
	}

	for _, addr := range fixedPeers {
		d.AddPeer(addr, 0, 0)
	}

	// Start can run concurrently with Stop (async Overlay.Run vs fast
	// shutdown): cancel is handed over under mu, and a Stop that already
	// ran wins — the maintenance loop must not start after it.
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return nil
	}
	ctx, d.cancel = context.WithCancel(ctx) //nolint:gosec // G118: cancel stored in struct field, called on Stop
	d.wg.Add(1)
	d.mu.Unlock()

	go d.maintenanceLoop(ctx)

	return nil
}

func (d *Discovery) resolveConfiguredPeers(ctx context.Context) ([]string, []string) {
	bootstrapSeen := make(map[string]struct{})
	bootstrap := make([]string, 0, len(d.cfg.BootstrapPeers))
	for _, configured := range d.cfg.BootstrapPeers {
		for _, endpoint := range d.resolveConfiguredPeer(ctx, configured) {
			if _, exists := bootstrapSeen[endpoint]; exists {
				continue
			}
			bootstrapSeen[endpoint] = struct{}{}
			bootstrap = append(bootstrap, endpoint)
		}
	}

	fixedSeen := make(map[string]struct{})
	fixed := make([]string, 0, len(d.cfg.FixedPeers))
	for _, configured := range d.cfg.FixedPeers {
		for _, endpoint := range d.resolveConfiguredPeer(ctx, configured) {
			if _, exists := fixedSeen[endpoint]; exists {
				continue
			}
			fixedSeen[endpoint] = struct{}{}
			fixed = append(fixed, endpoint)
			break
		}
	}

	return bootstrap, fixed
}

func (d *Discovery) resolveConfiguredPeer(ctx context.Context, configured string) []string {
	endpoint, err := ParseEndpoint(configured)
	if err != nil {
		slog.Warn("Configured peer endpoint is invalid; skipping",
			"t", "Discovery", "address", configured, "err", err)
		return nil
	}
	if ip := net.ParseIP(endpoint.Host); ip != nil {
		return []string{(Endpoint{Host: ip.String(), Port: endpoint.Port}).String()}
	}

	lookupIP := d.lookupIP
	if lookupIP == nil {
		lookupIP = net.DefaultResolver.LookupIPAddr
	}
	addresses, err := lookupIP(ctx, endpoint.Host)
	if err != nil {
		slog.Warn("Configured peer hostname could not be resolved; skipping",
			"t", "Discovery", "address", configured, "err", err)
		return nil
	}

	seen := make(map[string]struct{}, len(addresses))
	resolved := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if address.IP == nil {
			continue
		}
		value := (Endpoint{Host: address.IP.String(), Port: endpoint.Port}).String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		resolved = append(resolved, value)
	}
	return resolved
}

// Stop stops the discovery service by cancelling its context. Idempotent:
// guarded by sync.Once so a defensive double-shutdown is a no-op.
func (d *Discovery) Stop() {
	d.stopOnce.Do(func() {
		d.mu.Lock()
		d.stopped = true
		cancel := d.cancel
		d.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		d.wg.Wait()

		if d.bootCache != nil {
			if err := d.bootCache.Save(); err != nil {
				slog.Warn("Peer boot cache could not be saved", "t", "Discovery", "path", d.bootCache.filePath, "err", err)
			}
		}
	})
}

// AddPeer adds a discovered peer.
func (d *Discovery) AddPeer(address string, hops uint32, source PeerID) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if existing, exists := d.peers[address]; exists {
		if hops < existing.Hops {
			existing.Hops = hops
			existing.Source = source
		}
		existing.LastSeen = d.now()
		d.lru.MoveToFront(existing.lruEntry)
		return
	}

	if len(d.peers) >= MaxDiscoveredPeers && !d.evictOldestLocked() {
		// At the ceiling with every entry a live or fixed peer we must
		// keep — refuse the new gossiped address rather than grow unbounded.
		return
	}

	d.insertPeerLocked(&DiscoveredPeer{
		Address:  address,
		Hops:     hops,
		LastSeen: d.now(),
		Source:   source,
	})
}

// insertPeerLocked adds p to the peer map and the recency list as the
// most recently seen entry. Caller holds d.mu.
func (d *Discovery) insertPeerLocked(p *DiscoveredPeer) {
	p.lruEntry = d.lru.PushFront(p)
	d.peers[p.Address] = p
}

// evictOldestLocked removes the least-recently-seen discardable entry to
// make room under MaxDiscoveredPeers, returning false when every entry is
// a connected or fixed peer that must be retained. Caller holds d.mu.
func (d *Discovery) evictOldestLocked() bool {
	for e := d.lru.Back(); e != nil; e = e.Prev() {
		p := e.Value.(*DiscoveredPeer)
		if p.Connected || d.persistentPeers[p.Address] {
			continue
		}
		d.lru.Remove(e)
		delete(d.peers, p.Address)
		delete(d.connectAttempts, p.Address)
		return true
	}
	return false
}

// AddRedirectCandidate records an address learned from a peer's 503
// redirect. rippled files redirect addresses into the lower-trust boot
// cache (Logic::onRedirects -> bootcache_), NOT the live cache it
// re-advertises, so a redirected address becomes a reconnect seed but is
// never gossiped onward as if we had observed it live. When no boot cache
// is configured (no DataDir) we fall back to the discovered set as a
// one-hop candidate so the address stays usable for connection.
func (d *Discovery) AddRedirectCandidate(address string, source PeerID) {
	ep, err := ParseEndpoint(address)
	if err != nil {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Lock order d.mu -> bc.mu matches MarkConnected / SelectPeersToConnect.
	if d.bootCache != nil {
		d.bootCache.Insert(address, ep.Port)
		return
	}

	if _, exists := d.peers[address]; !exists {
		d.insertPeerLocked(&DiscoveredPeer{
			Address:  address,
			Hops:     1,
			LastSeen: d.now(),
			Source:   source,
		})
	}
}

// MarkConnected marks a peer as connected.
func (d *Discovery) MarkConnected(address string, peerID PeerID) {
	d.mu.Lock()
	defer d.mu.Unlock()

	peer, exists := d.peers[address]
	if !exists {
		peer = &DiscoveredPeer{Address: address, LastSeen: d.now()}
		d.insertPeerLocked(peer)
	}

	peer.Connected = true
	peer.PeerID = peerID
	d.connected[peerID] = peer
	d.markConnectSucceededLocked(address)

	// Feed the boot cache with addresses we successfully connected to, so a
	// restart can reconnect to known-good peers (GetEndpoints feeds
	// SelectPeersToConnect). MarkConnected only ever sees outbound,
	// connectable addresses. Lock order d.mu -> bc.mu matches
	// SelectPeersToConnect.
	if d.bootCache != nil {
		if ep, err := ParseEndpoint(address); err == nil {
			d.bootCache.Insert(address, ep.Port)
			d.bootCache.MarkSuccess(address)
		}
	}
}

// MarkDisconnected marks a peer as disconnected.
func (d *Discovery) MarkDisconnected(peerID PeerID) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if peer, exists := d.connected[peerID]; exists {
		peer.Connected = false
		peer.PeerID = 0
		delete(d.connected, peerID)
	}
}

// SyncConnectedState reconciles Discovery's view of connected peers
// against the Overlay's actual outbound peer set. Any d.peers entry
// currently marked Connected whose address is NOT in actualConnected
// is flipped back to Connected=false so it becomes a candidate for
// reconnection.
//
// goxrpl-specific infrastructure: no direct rippled counterpart.
// rippled's overlay tracks peer-add/peer-remove transitions via
// OverlayImpl::activate / OverlayImpl::onPeerDestroy under a single
// strand and doesn't need an out-of-band reconcile step. goxrpl's
// Discovery sits behind an event bus that can drop or coalesce
// transitions under load, so we reconcile against the overlay's
// authoritative peer set here.
//
// This guards against the PeerID-keyed MarkDisconnected path missing
// some disconnect events (event-bus races, inbound-only peers
// transitioning, double-disconnect dedupe in removePeer). Without
// this, fixed peers can stay marked Connected=true in d.peers even
// after their TCP connection drops, so SelectPeersToConnect filters
// them out and autoconnect reports `candidates=0 needed=N` forever —
// observed in the 5-node soak when goxrpl-1 lost a single rippled
// connection and never re-established it (iter23/24).
func (d *Discovery) SyncConnectedState(actualConnected map[string]struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for addr, peer := range d.peers {
		if peer.Connected {
			if _, stillConnected := actualConnected[addr]; !stillConnected {
				peer.Connected = false
				if peer.PeerID != 0 {
					delete(d.connected, peer.PeerID)
					peer.PeerID = 0
				}
			}
		}
	}
}

// SyncConnectedHosts marks any d.peers entry whose host is in the
// live host set as Connected=true, even if its full address (with
// listener port) was never seen by MarkConnected. This covers fixed
// peers for which we only have an INBOUND connection: the inbound's
// ephemeral source port won't match the fixed-peer config's listener
// port, but the host IP matches.
//
// goxrpl-specific infrastructure: no direct rippled counterpart.
// rippled correlates inbound peers against fixed-peer configuration
// at the OverlayImpl::checkStopped / autoConnect layer using the
// remote endpoint's host directly; goxrpl's Discovery keys peers by
// the full "host:port" string, so a separate host-level reconcile
// is needed to recognise an inbound as covering a fixed entry.
//
// Without this, autoconnect repeatedly dials addresses we already
// have inbound connections from. Each redial completes TLS, then the
// remote rejects via its post-handshake isConnectedTo guard and
// closes — surfacing as `failed to read header: unexpected EOF` on
// our side. Forever flap. Root cause of the issue #470 fixed-peer
// soak stall.
func (d *Discovery) SyncConnectedHosts(hosts map[string]struct{}) {
	if len(hosts) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, peer := range d.peers {
		if peer.Connected {
			continue
		}
		host, _, err := net.SplitHostPort(peer.Address)
		if err != nil {
			continue
		}
		if _, covered := hosts[host]; covered {
			peer.Connected = true
			d.markConnectSucceededLocked(peer.Address)
		}
	}
}

// ForEachDiscovered calls fn for each currently-known discovered peer
// (address + last-observed hop count) under the discovery read lock. fn
// must not block or re-enter Discovery. Lets callers (e.g. the overlay's
// TMEndpoints gossip) read the discovered set through an accessor instead
// of reaching into the Discovery internals directly.
func (d *Discovery) ForEachDiscovered(fn func(address string, hops uint32)) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, p := range d.peers {
		fn(p.Address, p.Hops)
	}
}

// ConnectedCount returns the number of connected peers.
func (d *Discovery) ConnectedCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.connected)
}

func (d *Discovery) IsFixed(address string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.fixedPeers[address]
}

// NeedsMorePeers returns true if we should connect to more peers.
func (d *Discovery) NeedsMorePeers() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.connected) < d.cfg.MaxOutbound
}

// SelectPeersToConnect returns candidate addresses to connect to.
func (d *Discovery) SelectPeersToConnect(count int) []string {
	return d.selectPeersToConnect(count, false)
}

func (d *Discovery) selectPeersToConnect(count int, bootstrap bool) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	for host, until := range d.recentAttempts {
		if !now.Before(until) {
			delete(d.recentAttempts, host)
		}
	}
	var fixedCandidates []string
	var candidates []string
	seenHosts := make(map[string]struct{})
	for _, peer := range d.peers {
		if peer.Connected {
			host := connectAttemptHost(peer.Address)
			seenHosts[host] = struct{}{}
			if !bootstrap && count > 0 {
				d.recentAttempts[host] = now.Add(recentConnectAttempt)
			}
		}
	}
	eligible := func(address string) bool {
		host := connectAttemptHost(address)
		if bootstrap && d.bootstrapSources[host].coolingDown(now) {
			return false
		}
		if _, duplicate := seenHosts[host]; duplicate {
			return false
		}
		if until, suppressed := d.recentAttempts[host]; suppressed && now.Before(until) {
			return false
		}
		for attemptedAddress, attempted := range d.connectAttempts {
			if attempted.inFlight && connectAttemptHost(attemptedAddress) == host {
				return false
			}
		}
		attempt := d.connectAttempts[address]
		if attempt != nil && (attempt.inFlight || now.Before(attempt.nextAttempt)) {
			return false
		}
		seenHosts[host] = struct{}{}
		return true
	}
	for address := range d.fixedPeers {
		peer := d.peers[address]
		if peer != nil && !peer.Connected && eligible(address) {
			fixedCandidates = append(fixedCandidates, address)
		}
	}
	if !d.cfg.PrivateMode {
		seen := make(map[string]struct{})
		for _, peer := range d.peers {
			seen[peer.Address] = struct{}{}
			if d.fixedPeers[peer.Address] {
				continue
			}
			if !peer.Connected && peer.Hops <= MaxHops && eligible(peer.Address) {
				candidates = append(candidates, peer.Address)
			}
		}

		if d.bootCache != nil {
			for _, entry := range d.bootCache.Endpoints(50) {
				if _, exists := seen[entry.Address]; !exists && eligible(entry.Address) {
					candidates = append(candidates, entry.Address)
					seen[entry.Address] = struct{}{}
				}
			}
		}

		rand.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
		if !bootstrap && count > 0 && count < len(candidates) {
			candidates = candidates[:count]
		} else if count <= 0 {
			candidates = nil
		}
	}
	candidates = append(fixedCandidates, candidates...)
	if bootstrap {
		d.rankCompressionCandidatesLocked(candidates)
		if count <= 0 && len(fixedCandidates) > 0 {
			for _, address := range candidates {
				if d.fixedPeers[address] {
					candidates = []string{address}
					break
				}
			}
		} else if count <= 0 {
			candidates = nil
		} else if len(candidates) > count {
			candidates = candidates[:count]
		}
	}
	for _, address := range candidates {
		attempt := d.connectAttempts[address]
		if attempt == nil {
			attempt = &connectAttempt{}
			if d.connectAttempts == nil {
				d.connectAttempts = make(map[string]*connectAttempt)
			}
			d.connectAttempts[address] = attempt
		}
		attempt.inFlight = true
		if d.recentAttempts == nil {
			d.recentAttempts = make(map[string]time.Time)
		}
		d.recentAttempts[connectAttemptHost(address)] = now.Add(recentConnectAttempt)
	}
	return candidates
}

func (d *Discovery) rankCompressionCandidatesLocked(candidates []string) {
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	compressionRank := func(address string) int {
		peer := d.peers[address]
		if peer != nil && peer.compressionKnown && peer.supportsCompression {
			return 0
		}
		if peer == nil || !peer.compressionKnown {
			return 1
		}
		return 2
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		leftCompression, rightCompression := compressionRank(left), compressionRank(right)
		if leftCompression != rightCompression {
			return leftCompression < rightCompression
		}

		leftObservation := d.bootstrapSources[connectAttemptHost(left)]
		rightObservation := d.bootstrapSources[connectAttemptHost(right)]
		leftViable := leftObservation.viable()
		rightViable := rightObservation.viable()
		if leftViable != rightViable {
			return leftViable
		}
		if leftViable && leftObservation.projected != rightObservation.projected {
			return leftObservation.projected < rightObservation.projected
		}
		if d.fixedPeers[left] != d.fixedPeers[right] {
			return d.fixedPeers[left]
		}
		return false
	})
}

func (d *Discovery) observeBootstrapSource(address string, projected time.Duration) {
	if projected <= 0 {
		return
	}

	host := connectAttemptHost(address)
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.bootstrapSources == nil {
		d.bootstrapSources = make(map[string]bootstrapSourceObservation)
	}
	now := d.now()
	current := d.bootstrapSources[host]
	wasCooling := current.coolingDown(now)
	current.projected = projected
	if projected > bootstrapTargetDuration {
		cooldownUntil := now.Add(bootstrapPartialRetry)
		if current.cooldownUntil.Before(cooldownUntil) {
			current.cooldownUntil = cooldownUntil
		}
	} else {
		current.cooldownUntil = time.Time{}
	}
	if wasCooling != current.coolingDown(now) {
		d.bootstrapEpisode++
	}
	d.bootstrapSources[host] = current
}

func (d *Discovery) bootstrapSourceSummary() bootstrapSourceStatus {
	d.mu.RLock()
	defer d.mu.RUnlock()

	hosts := make(map[string]struct{})
	for address := range d.fixedPeers {
		if d.peers[address] != nil {
			hosts[connectAttemptHost(address)] = struct{}{}
		}
	}
	if !d.cfg.PrivateMode {
		seen := make(map[string]struct{})
		for _, peer := range d.peers {
			seen[peer.Address] = struct{}{}
			if !d.fixedPeers[peer.Address] && peer.Hops <= MaxHops {
				hosts[connectAttemptHost(peer.Address)] = struct{}{}
			}
		}
		if d.bootCache != nil {
			for _, entry := range d.bootCache.Endpoints(50) {
				if _, exists := seen[entry.Address]; !exists {
					hosts[connectAttemptHost(entry.Address)] = struct{}{}
				}
			}
		}
	}

	now := d.now()
	status := bootstrapSourceStatus{known: len(hosts), episode: d.bootstrapEpisode}
	for host := range hosts {
		if d.bootstrapSources[host].coolingDown(now) {
			status.unviable++
		}
	}
	return status
}

func (d *Discovery) markNegotiatedCompression(address string, enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	peer := d.peers[address]
	if peer == nil {
		peer = &DiscoveredPeer{Address: address, LastSeen: d.now()}
		d.insertPeerLocked(peer)
	}
	peer.compressionKnown = true
	peer.supportsCompression = enabled
}

func (d *Discovery) delayConnectRetry(address string, delay time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	host := connectAttemptHost(address)
	now := d.now()
	retryAt := now.Add(delay)
	if current := d.recentAttempts[host]; current.Before(retryAt) {
		d.recentAttempts[host] = retryAt
	}
	if d.bootstrapSources == nil {
		d.bootstrapSources = make(map[string]bootstrapSourceObservation)
	}
	observation := d.bootstrapSources[host]
	wasCooling := observation.coolingDown(now)
	if observation.cooldownUntil.Before(retryAt) {
		observation.cooldownUntil = retryAt
		d.bootstrapSources[host] = observation
	}
	if !wasCooling && observation.coolingDown(now) {
		d.bootstrapEpisode++
	}
}

// finishConnectAttempt releases an autoconnect reservation. Network failures
// advance fixed peers through rippled's bounded Fibonacci-minute schedule;
// every other result retains the one-minute recent-attempt suppression set at
// selection time.
func (d *Discovery) finishConnectAttempt(address string, result connectAttemptResult) {
	d.mu.Lock()
	defer d.mu.Unlock()

	attempt := d.connectAttempts[address]
	if attempt == nil {
		return
	}
	attempt.inFlight = false
	if !d.fixedPeers[address] {
		delete(d.connectAttempts, address)
		return
	}
	if result == connectAttemptSucceeded {
		attempt.failures = 0
		attempt.nextAttempt = time.Time{}
		return
	}
	if result != connectAttemptFailed {
		return
	}
	attempt.failures = min(attempt.failures+1, len(fixedConnectBackoff)-1)
	attempt.nextAttempt = d.now().Add(fixedConnectBackoff[attempt.failures])
}

func (d *Discovery) markConnectSucceededLocked(address string) {
	if !d.fixedPeers[address] {
		delete(d.connectAttempts, address)
		return
	}
	attempt := d.connectAttempts[address]
	if attempt == nil {
		attempt = &connectAttempt{}
		if d.connectAttempts == nil {
			d.connectAttempts = make(map[string]*connectAttempt)
		}
		d.connectAttempts[address] = attempt
	}
	attempt.inFlight = false
	attempt.failures = 0
	attempt.nextAttempt = time.Time{}
}

func connectAttemptHost(address string) string {
	ep, err := ParseEndpoint(address)
	if err != nil {
		return strings.ToLower(address)
	}
	if ip := net.ParseIP(ep.Host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(ep.Host)
}

func (d *Discovery) maintenanceLoop(ctx context.Context) {
	defer d.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.prune()
		}
	}
}

func (d *Discovery) prune() {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := d.now().Add(-1 * time.Hour)
	for addr, peer := range d.peers {
		if !peer.Connected && !d.persistentPeers[addr] && peer.LastSeen.Before(cutoff) {
			d.lru.Remove(peer.lruEntry)
			delete(d.peers, addr)
			delete(d.connectAttempts, addr)
		}
	}
}
