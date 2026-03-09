package csf

import (
	"sync"
)

// Link represents a network connection between two peers.
type Link struct {
	Inbound     bool
	Delay       SimDuration
	Established SimTime
}

// BasicNetwork simulates a peer-to-peer network with configurable delays.
// Messages sent on a link are delivered after the configured delay.
type BasicNetwork struct {
	mu        sync.RWMutex
	scheduler *Scheduler
	links     map[PeerID]map[PeerID]*Link
}

// NewBasicNetwork creates a new simulated network.
func NewBasicNetwork(scheduler *Scheduler) *BasicNetwork {
	return &BasicNetwork{
		scheduler: scheduler,
		links:     make(map[PeerID]map[PeerID]*Link),
	}
}

// Connect establishes a bidirectional connection between two peers.
// Messages sent between them will be delayed by the given duration.
func (n *BasicNetwork) Connect(from, to PeerID, delay SimDuration) bool {
	if from == to {
		return false
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	// Check if already connected
	if n.links[from] != nil && n.links[from][to] != nil {
		return false
	}

	now := n.scheduler.Now()

	// Create outbound link from -> to
	if n.links[from] == nil {
		n.links[from] = make(map[PeerID]*Link)
	}
	n.links[from][to] = &Link{
		Inbound:     false,
		Delay:       delay,
		Established: now,
	}

	// Create inbound link to -> from
	if n.links[to] == nil {
		n.links[to] = make(map[PeerID]*Link)
	}
	n.links[to][from] = &Link{
		Inbound:     true,
		Delay:       delay,
		Established: now,
	}

	return true
}

// Disconnect removes the connection between two peers.
func (n *BasicNetwork) Disconnect(from, to PeerID) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.links[from] == nil || n.links[from][to] == nil {
		return false
	}

	delete(n.links[from], to)
	if n.links[to] != nil {
		delete(n.links[to], from)
	}

	return true
}

// IsConnected checks if two peers are connected.
func (n *BasicNetwork) IsConnected(from, to PeerID) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.links[from] == nil {
		return false
	}
	return n.links[from][to] != nil
}

// GetDelay returns the delay for messages from one peer to another.
// Returns 0 and false if not connected.
func (n *BasicNetwork) GetDelay(from, to PeerID) (SimDuration, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.links[from] == nil {
		return 0, false
	}
	link := n.links[from][to]
	if link == nil {
		return 0, false
	}
	return link.Delay, true
}

// Peers returns all peers that the given peer is connected to.
func (n *BasicNetwork) Peers(id PeerID) []PeerID {
	n.mu.RLock()
	defer n.mu.RUnlock()

	peerLinks := n.links[id]
	if peerLinks == nil {
		return nil
	}

	result := make([]PeerID, 0, len(peerLinks))
	for peer := range peerLinks {
		result = append(result, peer)
	}
	return result
}

// Send schedules a message to be delivered after the link delay.
// The handler is called when the message "arrives" at the destination.
// Returns false if the peers are not connected.
func (n *BasicNetwork) Send(from, to PeerID, handler func()) bool {
	delay, ok := n.GetDelay(from, to)
	if !ok {
		return false
	}

	// Schedule delivery after delay
	n.scheduler.In(delay, func() {
		// Check still connected at delivery time
		if n.IsConnected(from, to) {
			handler()
		}
	})

	return true
}

// Broadcast sends a message to all connected peers.
// Returns the number of peers the message was sent to.
func (n *BasicNetwork) Broadcast(from PeerID, handler func(to PeerID)) int {
	peers := n.Peers(from)
	for _, to := range peers {
		peer := to // Capture for closure
		n.Send(from, peer, func() {
			handler(peer)
		})
	}
	return len(peers)
}
