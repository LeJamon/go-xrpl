package csf

import (
	"math/rand"
	"sort"
)

// PeerGroup is a convenient handle for logically grouping peers together,
// and then creating trust or network relations for the group at large.
// Peer groups may also be combined to build more complex structures.
type PeerGroup struct {
	peers []*Peer
}

// NewPeerGroup creates a new empty peer group.
func NewPeerGroup() *PeerGroup {
	return &PeerGroup{
		peers: make([]*Peer, 0),
	}
}

// NewPeerGroupFrom creates a peer group from a slice of peers.
func NewPeerGroupFrom(peers []*Peer) *PeerGroup {
	// Make a copy and sort by ID for consistent set operations
	sorted := make([]*Peer, len(peers))
	copy(sorted, peers)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ID < sorted[j].ID
	})
	return &PeerGroup{peers: sorted}
}

// NewPeerGroupSingle creates a peer group with a single peer.
func NewPeerGroupSingle(peer *Peer) *PeerGroup {
	return &PeerGroup{peers: []*Peer{peer}}
}

// Size returns the number of peers in the group.
func (g *PeerGroup) Size() int {
	return len(g.peers)
}

// Get returns the peer at index i.
func (g *PeerGroup) Get(i int) *Peer {
	return g.peers[i]
}

// Peers returns all peers in the group.
func (g *PeerGroup) Peers() []*Peer {
	return g.peers
}

// Contains checks if a peer is in the group.
func (g *PeerGroup) Contains(p *Peer) bool {
	for _, peer := range g.peers {
		if peer == p {
			return true
		}
	}
	return false
}

// ContainsID checks if a peer with the given ID is in the group.
func (g *PeerGroup) ContainsID(id PeerID) bool {
	for _, peer := range g.peers {
		if peer.ID == id {
			return true
		}
	}
	return false
}

// Add adds a peer to the group.
func (g *PeerGroup) Add(p *Peer) {
	// Insert in sorted order
	idx := sort.Search(len(g.peers), func(i int) bool {
		return g.peers[i].ID >= p.ID
	})

	// Check if already present
	if idx < len(g.peers) && g.peers[idx].ID == p.ID {
		return
	}

	// Insert at position
	g.peers = append(g.peers, nil)
	copy(g.peers[idx+1:], g.peers[idx:])
	g.peers[idx] = p
}

// Remove removes a peer from the group.
func (g *PeerGroup) Remove(p *Peer) {
	idx := -1
	for i, peer := range g.peers {
		if peer == p {
			idx = i
			break
		}
	}
	if idx >= 0 {
		g.peers = append(g.peers[:idx], g.peers[idx+1:]...)
	}
}

// Trust establishes trust from all peers in this group to all peers in other.
func (g *PeerGroup) Trust(other *PeerGroup) {
	for _, p := range g.peers {
		for _, target := range other.peers {
			p.Trust(target)
		}
	}
}

// Untrust revokes trust from all peers in this group to all peers in other.
func (g *PeerGroup) Untrust(other *PeerGroup) {
	for _, p := range g.peers {
		for _, target := range other.peers {
			p.Untrust(target)
		}
	}
}

// Connect establishes network connections from all peers in this group
// to all peers in other with the given delay.
func (g *PeerGroup) Connect(other *PeerGroup, delay SimDuration) {
	for _, p := range g.peers {
		for _, target := range other.peers {
			// Cannot send messages to self
			if p != target {
				p.Connect(target, delay)
			}
		}
	}
}

// Disconnect removes network connections from all peers in this group
// to all peers in other.
func (g *PeerGroup) Disconnect(other *PeerGroup) {
	for _, p := range g.peers {
		for _, target := range other.peers {
			p.Disconnect(target)
		}
	}
}

// TrustAndConnect establishes both trust and network connections
// from all peers in this group to all peers in other.
func (g *PeerGroup) TrustAndConnect(other *PeerGroup, delay SimDuration) {
	g.Trust(other)
	g.Connect(other, delay)
}

// ConnectFromTrust creates network connections based on trust relations.
// For each peer, creates outbound connections to trusted peers.
func (g *PeerGroup) ConnectFromTrust(delay SimDuration) {
	for _, peer := range g.peers {
		for _, trustedID := range peer.trustGraph.TrustedPeers(peer.ID) {
			// Find the trusted peer in the network
			for _, target := range g.peers {
				if target.ID == trustedID && peer != target {
					peer.Connect(target, delay)
					break
				}
			}
		}
	}
}

// Union returns a new PeerGroup containing peers from both groups.
func (g *PeerGroup) Union(other *PeerGroup) *PeerGroup {
	result := NewPeerGroup()
	seen := make(map[PeerID]bool)

	for _, p := range g.peers {
		if !seen[p.ID] {
			result.peers = append(result.peers, p)
			seen[p.ID] = true
		}
	}
	for _, p := range other.peers {
		if !seen[p.ID] {
			result.peers = append(result.peers, p)
			seen[p.ID] = true
		}
	}

	// Sort for consistent ordering
	sort.Slice(result.peers, func(i, j int) bool {
		return result.peers[i].ID < result.peers[j].ID
	})

	return result
}

// Difference returns a new PeerGroup containing peers in this group but not in other.
func (g *PeerGroup) Difference(other *PeerGroup) *PeerGroup {
	result := NewPeerGroup()
	otherIDs := make(map[PeerID]bool)

	for _, p := range other.peers {
		otherIDs[p.ID] = true
	}

	for _, p := range g.peers {
		if !otherIDs[p.ID] {
			result.peers = append(result.peers, p)
		}
	}

	return result
}

// Intersection returns a new PeerGroup containing peers in both groups.
func (g *PeerGroup) Intersection(other *PeerGroup) *PeerGroup {
	result := NewPeerGroup()
	otherIDs := make(map[PeerID]bool)

	for _, p := range other.peers {
		otherIDs[p.ID] = true
	}

	for _, p := range g.peers {
		if otherIDs[p.ID] {
			result.peers = append(result.peers, p)
		}
	}

	return result
}

// ForEach calls the function for each peer in the group.
func (g *PeerGroup) ForEach(fn func(*Peer)) {
	for _, p := range g.peers {
		fn(p)
	}
}

// Filter returns a new group containing only peers matching the predicate.
func (g *PeerGroup) Filter(pred func(*Peer) bool) *PeerGroup {
	result := NewPeerGroup()
	for _, p := range g.peers {
		if pred(p) {
			result.peers = append(result.peers, p)
		}
	}
	return result
}

// IDs returns the IDs of all peers in the group.
func (g *PeerGroup) IDs() []PeerID {
	ids := make([]PeerID, len(g.peers))
	for i, p := range g.peers {
		ids[i] = p.ID
	}
	return ids
}

// -----------------------------------------------------------------------------
// Random group generation utilities

// RandomRankedGroups generates random peer groups based on peer rankings.
// More important peers (higher rank) are more likely to appear in groups.
func RandomRankedGroups(
	peers *PeerGroup,
	ranks []float64,
	numGroups int,
	sizeFunc func() int,
	rng *rand.Rand,
) []*PeerGroup {
	if len(peers.peers) != len(ranks) {
		panic("peers and ranks must have same length")
	}

	groups := make([]*PeerGroup, 0, numGroups)
	rawPeers := peers.peers

	for i := 0; i < numGroups; i++ {
		shuffled := randomWeightedShuffle(rawPeers, ranks, rng)
		size := sizeFunc()
		if size > len(shuffled) {
			size = len(shuffled)
		}
		groups = append(groups, NewPeerGroupFrom(shuffled[:size]))
	}

	return groups
}

// RandomRankedTrust generates random trust groups based on peer rankings.
func RandomRankedTrust(
	peers *PeerGroup,
	ranks []float64,
	numGroups int,
	sizeFunc func() int,
	rng *rand.Rand,
) {
	groups := RandomRankedGroups(peers, ranks, numGroups, sizeFunc, rng)

	for _, peer := range peers.peers {
		// Pick a random group for this peer's trust
		group := groups[rng.Intn(len(groups))]
		for _, target := range group.peers {
			peer.Trust(target)
		}
	}
}

// RandomRankedConnect generates random network connections based on peer rankings.
func RandomRankedConnect(
	peers *PeerGroup,
	ranks []float64,
	numGroups int,
	sizeFunc func() int,
	rng *rand.Rand,
	delay SimDuration,
) {
	groups := RandomRankedGroups(peers, ranks, numGroups, sizeFunc, rng)

	for _, peer := range peers.peers {
		// Pick a random group for this peer's connections
		group := groups[rng.Intn(len(groups))]
		for _, target := range group.peers {
			if peer != target {
				peer.Connect(target, delay)
			}
		}
	}
}

// randomWeightedShuffle shuffles peers weighted by their ranks.
// Higher ranked peers are more likely to appear earlier.
func randomWeightedShuffle(peers []*Peer, ranks []float64, rng *rand.Rand) []*Peer {
	n := len(peers)
	result := make([]*Peer, n)
	indices := make([]int, n)
	weights := make([]float64, n)

	for i := 0; i < n; i++ {
		indices[i] = i
		weights[i] = ranks[i]
	}

	for i := 0; i < n; i++ {
		// Calculate cumulative weights
		total := 0.0
		for _, w := range weights[:n-i] {
			total += w
		}

		if total == 0 {
			// All remaining weights are 0, pick uniformly
			idx := rng.Intn(n - i)
			result[i] = peers[indices[idx]]
			// Remove selected element
			indices[idx] = indices[n-i-1]
			weights[idx] = weights[n-i-1]
			continue
		}

		// Pick random weighted index
		r := rng.Float64() * total
		cumulative := 0.0
		selectedIdx := 0
		for j := 0; j < n-i; j++ {
			cumulative += weights[j]
			if r <= cumulative {
				selectedIdx = j
				break
			}
		}

		result[i] = peers[indices[selectedIdx]]

		// Remove selected element by swapping with last
		indices[selectedIdx] = indices[n-i-1]
		weights[selectedIdx] = weights[n-i-1]
	}

	return result
}

// CreateFullyConnectedGroup creates a peer group where all peers trust and
// are connected to each other.
func CreateFullyConnectedGroup(peers []*Peer, delay SimDuration) *PeerGroup {
	group := NewPeerGroupFrom(peers)
	group.TrustAndConnect(group, delay)
	return group
}

// CreateHubAndSpoke creates a network topology where a hub is connected to all spokes,
// but spokes are not connected to each other.
func CreateHubAndSpoke(hub *Peer, spokes []*Peer, delay SimDuration) (*PeerGroup, *PeerGroup) {
	hubGroup := NewPeerGroupSingle(hub)
	spokeGroup := NewPeerGroupFrom(spokes)

	// Spokes connect to hub
	spokeGroup.Connect(hubGroup, delay)
	hubGroup.Connect(spokeGroup, delay)

	// Trust relationships
	spokeGroup.Trust(hubGroup)
	hubGroup.Trust(spokeGroup)

	return hubGroup, spokeGroup
}

// CreatePartitionedNetwork creates two groups of peers that are disconnected
// from each other.
func CreatePartitionedNetwork(groupA, groupB []*Peer, delay SimDuration) (*PeerGroup, *PeerGroup) {
	a := NewPeerGroupFrom(groupA)
	b := NewPeerGroupFrom(groupB)

	// Each group is fully connected internally
	a.TrustAndConnect(a, delay)
	b.TrustAndConnect(b, delay)

	// No connections between groups
	return a, b
}
