package peermanagement

import (
	"context"
	"encoding/json"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Discovery constants.
const (
	DefaultBootCacheFile   = "peerfinder.cache"
	MaxCachedEndpoints     = 1000
	CacheEntryTTL          = 7 * 24 * time.Hour
	RecentEndpointTTL      = 5 * time.Minute
	MaxHops                = 3
	DefaultReservationFile = "peer_reservations.json"
)

// SlotState represents the connection state of a peer slot.
type SlotState int

const (
	SlotStateAccept SlotState = iota
	SlotStateConnect
	SlotStateConnected
	SlotStateActive
	SlotStateClosing
)

// String returns the string representation of the state.
func (s SlotState) String() string {
	switch s {
	case SlotStateAccept:
		return "accept"
	case SlotStateConnect:
		return "connect"
	case SlotStateConnected:
		return "connected"
	case SlotStateActive:
		return "active"
	case SlotStateClosing:
		return "closing"
	default:
		return "unknown"
	}
}

// Slot represents a peer connection slot with its state and properties.
type Slot struct {
	mu sync.RWMutex

	inbound         bool
	fixed           bool
	state           SlotState
	remoteEndpoint  net.Addr
	localEndpoint   net.Addr
	recentEndpoints *RecentEndpoints

	createdAt   time.Time
	activatedAt time.Time
}

// NewInboundSlot creates a new slot for an inbound connection.
func NewInboundSlot(localEndpoint, remoteEndpoint net.Addr, fixed bool) *Slot {
	return &Slot{
		inbound:         true,
		fixed:           fixed,
		state:           SlotStateAccept,
		remoteEndpoint:  remoteEndpoint,
		localEndpoint:   localEndpoint,
		recentEndpoints: NewRecentEndpoints(),
		createdAt:       time.Now(),
	}
}

// NewOutboundSlot creates a new slot for an outbound connection.
func NewOutboundSlot(remoteEndpoint net.Addr, fixed bool) *Slot {
	return &Slot{
		inbound:         false,
		fixed:           fixed,
		state:           SlotStateConnect,
		remoteEndpoint:  remoteEndpoint,
		recentEndpoints: NewRecentEndpoints(),
		createdAt:       time.Now(),
	}
}

// Inbound returns true if this is an inbound connection.
func (s *Slot) Inbound() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inbound
}

// Fixed returns true if this is a fixed connection.
func (s *Slot) Fixed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fixed
}

// State returns the current connection state.
func (s *Slot) State() SlotState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// SetState updates the connection state.
func (s *Slot) SetState(state SlotState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
}

// RemoteEndpoint returns the remote endpoint.
func (s *Slot) RemoteEndpoint() net.Addr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remoteEndpoint
}

// Activate transitions the slot to the active state.
func (s *Slot) Activate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = SlotStateActive
	s.activatedAt = time.Now()
}

// IsActive returns true if the slot is in the active state.
func (s *Slot) IsActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state == SlotStateActive
}

// RecentEndpoints tracks recently seen endpoints from a peer.
type RecentEndpoints struct {
	mu    sync.RWMutex
	cache map[string]*recentEntry
}

type recentEntry struct {
	Hops     uint32
	LastSeen time.Time
}

// NewRecentEndpoints creates a new RecentEndpoints tracker.
func NewRecentEndpoints() *RecentEndpoints {
	return &RecentEndpoints{
		cache: make(map[string]*recentEntry),
	}
}

// Insert records an endpoint as recently seen.
func (r *RecentEndpoints) Insert(endpoint string, hops uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[endpoint] = &recentEntry{Hops: hops, LastSeen: time.Now()}
}

// Filter returns true if we should NOT send this endpoint to the peer.
func (r *RecentEndpoints) Filter(endpoint string, hops uint32) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.cache[endpoint]
	if !exists {
		return false
	}
	return time.Since(entry.LastSeen) < RecentEndpointTTL && entry.Hops <= hops
}

// Expire removes old entries from the cache.
func (r *RecentEndpoints) Expire() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for endpoint, entry := range r.cache {
		if time.Since(entry.LastSeen) > RecentEndpointTTL {
			delete(r.cache, endpoint)
		}
	}
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
	mu       sync.RWMutex
	cache    map[string]*CachedEndpoint
	filePath string
	dirty    bool
}

// NewBootCache creates a new boot cache.
func NewBootCache(dataDir string) *BootCache {
	return &BootCache{
		cache:    make(map[string]*CachedEndpoint),
		filePath: filepath.Join(dataDir, DefaultBootCacheFile),
	}
}

// Load loads the cache from disk.
func (bc *BootCache) Load() error {
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

	bc.cache = make(map[string]*CachedEndpoint)
	now := time.Now()
	for _, entry := range entries {
		if now.Sub(entry.LastSeen) <= CacheEntryTTL {
			bc.cache[entry.Address] = entry
		}
	}
	return nil
}

// Save writes the cache to disk.
func (bc *BootCache) Save() error {
	bc.mu.RLock()
	defer bc.mu.RUnlock()

	if !bc.dirty {
		return nil
	}

	entries := make([]*CachedEndpoint, 0, len(bc.cache))
	for _, entry := range bc.cache {
		entries = append(entries, entry)
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(bc.filePath), 0755); err != nil {
		return err
	}

	bc.dirty = false
	return os.WriteFile(bc.filePath, data, 0644)
}

// Insert adds or updates an endpoint in the cache.
func (bc *BootCache) Insert(address string, port uint16) {
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

// GetEndpoints returns endpoints sorted by valence.
func (bc *BootCache) GetEndpoints(limit int) []*CachedEndpoint {
	bc.mu.RLock()
	defer bc.mu.RUnlock()

	entries := make([]*CachedEndpoint, 0, len(bc.cache))
	for _, entry := range bc.cache {
		entries = append(entries, &CachedEndpoint{
			Address:   entry.Address,
			Port:      entry.Port,
			LastSeen:  entry.LastSeen,
			Valence:   entry.Valence,
			FailCount: entry.FailCount,
		})
	}

	// Sort by valence descending
	for i := 0; i < len(entries)-1; i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].Valence > entries[i].Valence {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

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
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, exists := t.reservations[nodeID]
	return exists
}

// Insert adds or replaces a reservation and persists the table, returning the
// previous entry for the same node (nil if there was none) and any persistence
// error. Mirrors rippled's PeerReservationTable::insert_or_assign, whose DB
// write surfaces failures to the caller.
func (t *ReservationTable) Insert(r *PeerReservation) (*PeerReservation, error) {
	t.mu.Lock()
	prev := t.reservations[r.NodeID]
	t.reservations[r.NodeID] = r
	t.mu.Unlock()
	return prev, t.Save()
}

// Erase removes a reservation and persists the table, returning the removed
// entry (nil if none existed) and any persistence error. Mirrors rippled's
// PeerReservationTable::erase.
func (t *ReservationTable) Erase(nodeID string) (*PeerReservation, error) {
	t.mu.Lock()
	prev, ok := t.reservations[nodeID]
	if ok {
		delete(t.reservations, nodeID)
	}
	t.mu.Unlock()
	if !ok {
		return nil, nil
	}
	return prev, t.Save()
}

// List returns a snapshot of all reservations.
func (t *ReservationTable) List() []PeerReservation {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]PeerReservation, 0, len(t.reservations))
	for _, r := range t.reservations {
		out = append(out, *r)
	}
	return out
}

// Load reads the reservation table from disk. A missing file is not an error.
func (t *ReservationTable) Load() error {
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
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reservations = make(map[string]*PeerReservation, len(entries))
	for _, e := range entries {
		if e != nil && e.NodeID != "" {
			t.reservations[e.NodeID] = e
		}
	}
	return nil
}

// Save writes the reservation table to disk. It is a no-op when no data
// directory is configured (e.g. standalone / in-memory tests).
func (t *ReservationTable) Save() error {
	if t.filePath == "" {
		return nil
	}
	t.mu.RLock()
	entries := make([]*PeerReservation, 0, len(t.reservations))
	for _, r := range t.reservations {
		entries = append(entries, r)
	}
	t.mu.RUnlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.filePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(t.filePath, data, 0o644)
}

// Reservations exposes the reservation table (nil when no data directory is
// configured), letting the overlay consult it during peer admission.
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
}

// Discovery manages peer discovery and connection maintenance.
type Discovery struct {
	mu sync.RWMutex

	cfg Config

	peers       map[string]*DiscoveredPeer
	connected   map[PeerID]*DiscoveredPeer
	slots       map[string]*Slot
	fixedPeers  map[string]bool
	bootCache   *BootCache
	reservation *ReservationTable

	events  chan<- Event
	closeCh chan struct{}
	wg      sync.WaitGroup
}

// NewDiscovery creates a new Discovery instance.
func NewDiscovery(cfg *Config, events chan<- Event) *Discovery {
	d := &Discovery{
		cfg:        *cfg,
		peers:      make(map[string]*DiscoveredPeer),
		connected:  make(map[PeerID]*DiscoveredPeer),
		slots:      make(map[string]*Slot),
		fixedPeers: make(map[string]bool),
		events:     events,
		closeCh:    make(chan struct{}),
	}

	for _, addr := range cfg.FixedPeers {
		d.fixedPeers[addr] = true
	}

	if cfg.DataDir != "" {
		d.bootCache = NewBootCache(cfg.DataDir)
		d.reservation = NewReservationTable(cfg.DataDir)
	}

	return d
}

// Start starts the discovery service.
func (d *Discovery) Start(ctx context.Context) error {
	if d.bootCache != nil {
		d.bootCache.Load()
	}
	if d.reservation != nil {
		d.reservation.Load()
	}

	for _, addr := range d.cfg.BootstrapPeers {
		d.AddPeer(addr, 0, 0)
	}

	for _, addr := range d.cfg.FixedPeers {
		d.AddPeer(addr, 0, 0)
	}

	d.wg.Add(1)
	go d.maintenanceLoop(ctx)

	return nil
}

// Stop stops the discovery service.
func (d *Discovery) Stop() {
	close(d.closeCh)
	d.wg.Wait()

	if d.bootCache != nil {
		d.bootCache.Save()
	}
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
		existing.LastSeen = time.Now()
		return
	}

	d.peers[address] = &DiscoveredPeer{
		Address:  address,
		Hops:     hops,
		LastSeen: time.Now(),
		Source:   source,
	}
}

// MarkConnected marks a peer as connected.
func (d *Discovery) MarkConnected(address string, peerID PeerID) {
	d.mu.Lock()
	defer d.mu.Unlock()

	peer, exists := d.peers[address]
	if !exists {
		peer = &DiscoveredPeer{Address: address, LastSeen: time.Now()}
		d.peers[address] = peer
	}

	peer.Connected = true
	peer.PeerID = peerID
	d.connected[peerID] = peer
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
		}
	}
}

// ConnectedCount returns the number of connected peers.
func (d *Discovery) ConnectedCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.connected)
}

// NeedsMorePeers returns true if we should connect to more peers.
func (d *Discovery) NeedsMorePeers() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.connected) < d.cfg.MaxOutbound
}

// SelectPeersToConnect returns candidate addresses to connect to.
func (d *Discovery) SelectPeersToConnect(count int) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var candidates []string
	for _, peer := range d.peers {
		if !peer.Connected && peer.Hops <= MaxHops {
			candidates = append(candidates, peer.Address)
		}
	}

	if d.bootCache != nil {
		for _, entry := range d.bootCache.GetEndpoints(50) {
			if _, exists := d.peers[entry.Address]; !exists {
				candidates = append(candidates, entry.Address)
			}
		}
	}

	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})

	if count > 0 && count < len(candidates) {
		candidates = candidates[:count]
	}
	return candidates
}

func (d *Discovery) maintenanceLoop(ctx context.Context) {
	defer d.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.closeCh:
			return
		case <-ticker.C:
			d.prune()
		}
	}
}

func (d *Discovery) prune() {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().Add(-1 * time.Hour)
	for addr, peer := range d.peers {
		if !peer.Connected && peer.LastSeen.Before(cutoff) {
			delete(d.peers, addr)
		}
	}
}
