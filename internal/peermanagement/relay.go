package peermanagement

import (
	"math/rand/v2"
	"sync"
	"time"
)

// Reduce-relay constants live in reduce_relay_common.go
// (mirroring rippled's ReduceRelayCommon.h).

// RelayPeerState represents the state of a peer in the reduce-relay system.
type RelayPeerState int

const (
	RelayPeerCounting RelayPeerState = iota
	RelayPeerSelected
	RelayPeerSquelched
)

// String returns the string representation of RelayPeerState.
func (s RelayPeerState) String() string {
	switch s {
	case RelayPeerCounting:
		return "counting"
	case RelayPeerSelected:
		return "selected"
	case RelayPeerSquelched:
		return "squelched"
	default:
		return "unknown"
	}
}

// RelaySlotState represents the state of a validator slot.
type RelaySlotState int

const (
	RelaySlotCounting RelaySlotState = iota
	RelaySlotSelected
)

// RelayPeerInfo holds information about a peer in the reduce-relay system.
type RelayPeerInfo struct {
	State       RelayPeerState
	Count       int
	Expire      time.Time
	LastMessage time.Time
}

// SquelchCallback is called when a peer should be squelched/unsquelched.
type SquelchCallback func(validator []byte, peerID PeerID, squelch bool, duration time.Duration)

// ValidatorSlot manages peer selection for a specific validator.
type ValidatorSlot struct {
	mu sync.RWMutex

	peers            map[PeerID]*RelayPeerInfo
	considered       map[PeerID]struct{}
	reachedThreshold int
	lastSelected     time.Time
	state            RelaySlotState
	maxSelectedPeers int
	onSquelch        SquelchCallback
}

// NewValidatorSlot creates a new reduce-relay slot for a validator.
func NewValidatorSlot(maxSelected int, onSquelch SquelchCallback) *ValidatorSlot {
	if maxSelected <= 0 {
		maxSelected = MaxSelectedPeers
	}
	return &ValidatorSlot{
		peers:            make(map[PeerID]*RelayPeerInfo),
		considered:       make(map[PeerID]struct{}),
		lastSelected:     time.Now(),
		state:            RelaySlotCounting,
		maxSelectedPeers: maxSelected,
		onSquelch:        onSquelch,
	}
}

// Update processes a message from a peer.
func (s *ValidatorSlot) Update(validator []byte, peerID PeerID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	peer, exists := s.peers[peerID]

	if !exists {
		s.peers[peerID] = &RelayPeerInfo{
			State:       RelayPeerCounting,
			Count:       0,
			Expire:      now,
			LastMessage: now,
		}
		s.initCounting()
		return
	}

	if peer.State == RelayPeerSquelched && now.After(peer.Expire) {
		peer.State = RelayPeerCounting
		peer.LastMessage = now
		s.initCounting()
		return
	}

	peer.LastMessage = now

	if s.state != RelaySlotCounting || peer.State == RelayPeerSquelched {
		return
	}

	peer.Count++
	if peer.Count > MinMessageThreshold {
		s.considered[peerID] = struct{}{}
	}
	if peer.Count == MaxMessageThreshold+1 {
		s.reachedThreshold++
	}

	if time.Since(s.lastSelected) > 2*MaxUnsquelchExpireDefault {
		s.initCounting()
		return
	}

	if s.reachedThreshold == s.maxSelectedPeers {
		s.selectPeers(validator, now)
	}
}

func (s *ValidatorSlot) selectPeers(validator []byte, now time.Time) {
	candidates := make([]PeerID, 0, len(s.considered))
	for peerID := range s.considered {
		peer := s.peers[peerID]
		if peer != nil && time.Since(peer.LastMessage) < Idled {
			candidates = append(candidates, peerID)
		}
	}

	if len(candidates) < s.maxSelectedPeers {
		s.initCounting()
		return
	}

	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	// math/rand/v2 is auto-seeded.

	selected := make(map[PeerID]struct{})
	for i := 0; i < s.maxSelectedPeers && i < len(candidates); i++ {
		selected[candidates[i]] = struct{}{}
	}

	s.lastSelected = now
	squelchablePeers := len(s.peers) - s.maxSelectedPeers

	for peerID, peer := range s.peers {
		peer.Count = 0

		if _, isSelected := selected[peerID]; isSelected {
			peer.State = RelayPeerSelected
		} else if peer.State != RelayPeerSquelched {
			peer.State = RelayPeerSquelched
			duration := s.getSquelchDuration(squelchablePeers)
			peer.Expire = now.Add(duration)
			if s.onSquelch != nil {
				s.onSquelch(validator, peerID, true, duration)
			}
		}
	}

	s.considered = make(map[PeerID]struct{})
	s.reachedThreshold = 0
	s.state = RelaySlotSelected
}

// DeletePeer handles peer disconnection.
func (s *ValidatorSlot) DeletePeer(validator []byte, peerID PeerID, erase bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	peer, exists := s.peers[peerID]
	if !exists {
		return
	}

	now := time.Now()

	if peer.State == RelayPeerSelected {
		for id, p := range s.peers {
			if p.State == RelayPeerSquelched && s.onSquelch != nil {
				s.onSquelch(validator, id, false, 0)
			}
			p.State = RelayPeerCounting
			p.Count = 0
			p.Expire = now
		}
		s.considered = make(map[PeerID]struct{})
		s.reachedThreshold = 0
		s.state = RelaySlotCounting
	} else if _, inConsidered := s.considered[peerID]; inConsidered {
		if peer.Count > MaxMessageThreshold {
			s.reachedThreshold--
		}
		delete(s.considered, peerID)
	}

	// Rippled's Slot.h:479-480 unconditionally resets Count and
	// LastMessage on the deleted peer, regardless of whether it was
	// Selected or in Considered. Preserve that behavior so a later
	// re-appearance of the same peer ID starts from a clean slate
	// instead of inheriting whatever Count it had when it left.
	peer.Count = 0
	peer.LastMessage = now

	if erase {
		delete(s.peers, peerID)
	}
}

// GetSelected returns the selected peer IDs.
func (s *ValidatorSlot) GetSelected() []PeerID {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []PeerID
	for peerID, peer := range s.peers {
		if peer.State == RelayPeerSelected {
			result = append(result, peerID)
		}
	}
	return result
}

func (s *ValidatorSlot) initCounting() {
	s.state = RelaySlotCounting
	s.considered = make(map[PeerID]struct{})
	s.reachedThreshold = 0
	for _, peer := range s.peers {
		peer.Count = 0
	}
}

func (s *ValidatorSlot) getSquelchDuration(numPeers int) time.Duration {
	maxDuration := MaxUnsquelchExpireDefault
	if time.Duration(numPeers)*SquelchPerPeer > maxDuration {
		maxDuration = time.Duration(numPeers) * SquelchPerPeer
	}
	if maxDuration > MaxUnsquelchExpirePeers {
		maxDuration = MaxUnsquelchExpirePeers
	}

	minSecs := int(MinUnsquelchExpire.Seconds())
	maxSecs := int(maxDuration.Seconds())

	// rand_int(min, max) in rippled is inclusive on both ends; mirror that
	// with IntN(span+1).
	return time.Duration(minSecs+rand.IntN(maxSecs-minSecs+1)) * time.Second
}

// Relay manages reduce-relay for all validators.
type Relay struct {
	mu    sync.RWMutex
	slots map[string]*ValidatorSlot // validator pubkey hex -> slot
	cfg   *Config
	clock func() time.Time

	onSquelch SquelchCallback
	startTime time.Time
}

// NewRelay creates a new Relay manager.
func NewRelay(cfg *Config, onSquelch SquelchCallback) *Relay {
	clock := time.Now
	if cfg.Clock != nil {
		clock = cfg.Clock
	}
	return &Relay{
		slots:     make(map[string]*ValidatorSlot),
		cfg:       cfg,
		clock:     clock,
		onSquelch: onSquelch,
		startTime: clock(),
	}
}

// OnMessage handles an incoming validator message.
func (r *Relay) OnMessage(validatorKey []byte, peerID PeerID) {
	if !r.cfg.EnableReduceRelay {
		return
	}

	if r.clock().Sub(r.startTime) < WaitOnBootup {
		return
	}

	keyHex := string(validatorKey)

	r.mu.Lock()
	slot, exists := r.slots[keyHex]
	if !exists {
		slot = NewValidatorSlot(MaxSelectedPeers, r.onSquelch)
		r.slots[keyHex] = slot
	}
	r.mu.Unlock()

	slot.Update(validatorKey, peerID)
}

// RemovePeer removes a peer from all validator slots.
func (r *Relay) RemovePeer(peerID PeerID) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for key, slot := range r.slots {
		slot.DeletePeer([]byte(key), peerID, true)
	}
}

// GetSelectedPeers returns selected peers for a validator.
func (r *Relay) GetSelectedPeers(validatorKey []byte) []PeerID {
	keyHex := string(validatorKey)

	r.mu.RLock()
	slot, exists := r.slots[keyHex]
	r.mu.RUnlock()

	if !exists {
		return nil
	}
	return slot.GetSelected()
}
