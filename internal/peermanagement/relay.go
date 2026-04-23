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

// IgnoredSquelchCallback is called when a peer keeps relaying a
// validator's messages after being placed in the Squelched state —
// i.e., the peer is ignoring a TMSquelch we previously sent it.
//
// Mirrors rippled's ignored_squelch_callback at Slot.h:112-113,
// invoked inside Slot::update at Slot.h:329-331. The overlay uses
// this signal to charge the offending peer's reputation (bad-data
// balance) so sustained squelch-violations eventually evict the peer
// rather than leaving squelch enforcement purely advisory.
//
// Invoked from the hot receive path, OUTSIDE any slot mutex —
// implementations must be non-blocking. The callback carries only the
// peerID because the charge is per-peer: a peer can ignore the squelch
// for multiple validators, and each event should land on the same
// peer's balance.
type IgnoredSquelchCallback func(peerID PeerID)

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

	// onIgnoredSquelch is fired when an incoming validator message
	// originates from a peer currently in RelayPeerSquelched state —
	// i.e., the peer is ignoring a TMSquelch we previously sent it.
	// Set once at slot construction by Relay.OnMessage (which
	// propagates it from Overlay wiring); leaving it nil simply
	// disables the charge. Invoked OUTSIDE s.mu to avoid inverting
	// lock order with the overlay's peers map.
	onIgnoredSquelch IgnoredSquelchCallback
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
	// ignoredSquelch is set under s.mu when we detect the peer is
	// still Squelched, then invoked AFTER releasing the lock. Lock
	// ordering: never call into overlay-registered callbacks while
	// holding the slot mutex — the overlay's IncPeerBadData takes its
	// own peers map lock and we must not invert that order.
	var (
		fireIgnoredSquelch bool
		ignoredCb          IgnoredSquelchCallback
	)
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		if fireIgnoredSquelch && ignoredCb != nil {
			ignoredCb(peerID)
		}
	}()

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
		// Squelch window has elapsed — transition the peer back to
		// Counting and return. No ignored-squelch charge: the peer
		// isn't ignoring us anymore, the squelch simply expired on its
		// own schedule. Matches rippled Slot.h:307-314 where the
		// callback is NOT invoked on expired-squelch traffic.
		peer.State = RelayPeerCounting
		peer.LastMessage = now
		s.initCounting()
		return
	}

	peer.LastMessage = now

	// G4: the peer is still Squelched and the squelch has NOT expired
	// — yet it relayed a validator message anyway. Mirrors rippled's
	// Slot::update at Slot.h:329-331 which calls the
	// ignored_squelch_callback here. Stage the invocation; the defer
	// above fires it after we release s.mu so the callback can freely
	// take the overlay's peers map lock without risking inversion.
	if peer.State == RelayPeerSquelched {
		fireIgnoredSquelch = true
		ignoredCb = s.onIgnoredSquelch
	}

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

// deleteIdlePeers evicts every peer in this slot whose LastMessage is
// older than Idled from `now`, and — if the remaining peer count drops
// below maxSelectedPeers while the slot was in RelaySlotSelected —
// demotes the slot back to RelaySlotCounting so future Updates can
// retry selection.
//
// Mirrors rippled's Slot::deleteIdlePeer (Slot.h:262-283). The
// per-peer eviction path reuses the DeletePeer logic under this
// slot's own lock to keep the peer-map transitions consistent with
// the normal disconnect path (Selected-peer removal cascades into
// unsquelching the rest of the slot and resetting slot state).
//
// Called under Relay.mu write-lock by Relay.deleteIdlePeers, but
// takes this slot's own mu independently — the two locks always
// nest in the order (relay, slot) just as RemovePeer does.
func (s *ValidatorSlot) deleteIdlePeers(validator []byte, now time.Time) {
	s.mu.Lock()

	// First pass: collect the IDs to evict under lock. We cannot call
	// DeletePeer (which takes s.mu) while holding it, so do the per-
	// peer eviction inline here mirroring DeletePeer's semantics.
	var toEvict []PeerID
	for id, peer := range s.peers {
		if now.Sub(peer.LastMessage) > Idled {
			toEvict = append(toEvict, id)
		}
	}

	// Track whether any Selected peer was among the evicted — if so,
	// rippled's deletePeer cascade unsquelches the rest of the slot
	// and resets state. We collect the unsquelch callbacks to fire
	// AFTER we release s.mu, matching the DeletePeer ordering above.
	type unsquelchCall struct{ peerID PeerID }
	var unsquelches []unsquelchCall

	for _, id := range toEvict {
		peer, exists := s.peers[id]
		if !exists {
			continue
		}

		if peer.State == RelayPeerSelected {
			// Cascade: unsquelch all other Squelched peers, reset
			// every remaining peer to Counting, and demote the slot.
			// Matches rippled Slot.h:457-471.
			for k, v := range s.peers {
				if k == id {
					continue
				}
				if v.State == RelayPeerSquelched {
					unsquelches = append(unsquelches, unsquelchCall{peerID: k})
				}
				v.State = RelayPeerCounting
				v.Count = 0
				v.Expire = now
			}
			s.considered = make(map[PeerID]struct{})
			s.reachedThreshold = 0
			s.state = RelaySlotCounting
		} else if _, inConsidered := s.considered[id]; inConsidered {
			if peer.Count > MaxMessageThreshold {
				s.reachedThreshold--
			}
			delete(s.considered, id)
		}

		peer.Count = 0
		peer.LastMessage = now
		delete(s.peers, id)
	}

	// Safety-net per G2 spec: if the slot was still in Selected state
	// after the walk (e.g., only Squelched peers were evicted) and the
	// remaining peer count dropped below maxSelectedPeers, demote to
	// Counting so the selection state machine can retry with whatever
	// peers are left.
	if s.state == RelaySlotSelected && len(s.peers) < s.maxSelectedPeers {
		s.initCounting()
	}

	callback := s.onSquelch
	s.mu.Unlock()

	// Fire unsquelch callbacks outside the lock — matches rippled's
	// "after peers_.erase(it)" ordering at Slot.h:485-487.
	if callback != nil {
		for _, u := range unsquelches {
			callback(validator, u.peerID, false, 0)
		}
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

	onSquelch        SquelchCallback
	onIgnoredSquelch IgnoredSquelchCallback
	startTime        time.Time
}

// NewRelay creates a new Relay manager with no ignored-squelch
// callback. Equivalent to NewRelayWithIgnoredCallback(cfg, onSquelch,
// nil). Existing callers (and tests that don't exercise G4) keep
// using this.
func NewRelay(cfg *Config, onSquelch SquelchCallback) *Relay {
	return NewRelayWithIgnoredCallback(cfg, onSquelch, nil)
}

// NewRelayWithIgnoredCallback creates a Relay that additionally
// invokes onIgnoredSquelch(peerID) every time a peer in the Squelched
// state relays a validator's message. The callback is propagated to
// every ValidatorSlot the Relay creates. Pass nil to disable the
// charge (falls back to NewRelay semantics).
func NewRelayWithIgnoredCallback(
	cfg *Config,
	onSquelch SquelchCallback,
	onIgnoredSquelch IgnoredSquelchCallback,
) *Relay {
	clock := time.Now
	if cfg.Clock != nil {
		clock = cfg.Clock
	}
	return &Relay{
		slots:            make(map[string]*ValidatorSlot),
		cfg:              cfg,
		clock:            clock,
		onSquelch:        onSquelch,
		onIgnoredSquelch: onIgnoredSquelch,
		startTime:        clock(),
	}
}

// OnMessage handles an incoming validator message.
func (r *Relay) OnMessage(validatorKey []byte, peerID PeerID) {
	// R6.3: gate on either the specific VPRR flag OR the legacy
	// omnibus flag. Pre-R6.3 only checked EnableReduceRelay, which
	// meant an operator who set only EnableVPReduceRelay=true (after
	// R5.12 split the flags) would advertise VPRR in handshake but
	// leave this engine dormant, silently disabling reduce-relay for
	// the whole node. Relay is the validator-proposal slot machine
	// (mtSQUELCH for TMProposeSet/TMValidation), so VPRR is the
	// correct gate; TXRR governs tx-relay which is handled elsewhere.
	if !r.cfg.EnableVPReduceRelay && !r.cfg.EnableReduceRelay {
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
		// Propagate the ignored-squelch callback so every slot this
		// Relay hands out shares one reputation-charge sink. Set
		// before Update is called so even the first inbound message
		// from a (hypothetically pre-squelched) peer lands on it.
		slot.onIgnoredSquelch = r.onIgnoredSquelch
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

// deleteIdlePeers is the periodic sweep that evicts peers whose
// last-message timestamp is older than Idled from every validator
// slot, demotes slots that dropped below MaxSelectedPeers while in
// Selected state back to Counting, and drops slots that lost all
// peers entirely.
//
// Mirrors rippled's Slot::deleteIdlePeer (Slot.h:262-283) + its
// aggregator Slots::deleteIdlePeers (Slot.h:821-839). Without this
// sweep r.slots only shrinks on explicit RemovePeer — a selected peer
// that silently stops relaying is never demoted back to Counting, so
// the slot permanently points at a dead source and the relay never
// retries selection for that validator.
//
// Takes `now` explicitly so tests can drive the sweep clock
// deterministically; callers in production pass time.Now().
func (r *Relay) deleteIdlePeers(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key, slot := range r.slots {
		slot.deleteIdlePeers([]byte(key), now)

		// After the per-slot walk, drop the slot entirely if it has
		// zero peers left — otherwise r.slots would leak entries for
		// validators we no longer hear from.
		slot.mu.RLock()
		empty := len(slot.peers) == 0
		slot.mu.RUnlock()
		if empty {
			delete(r.slots, key)
		}
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
