package peermanagement

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
)

type PeerReservation struct {
	NodeID      string `json:"node_id"`
	Description string `json:"description,omitempty"`
}

type ReservationTable struct {
	mu           sync.RWMutex
	reservations map[string]*PeerReservation
	filePath     string
	writeFile    atomicFileWriter
}

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
// previous entry for the same node and any persistence error.
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
// entry and any persistence error.
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
// directory is configured.
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
