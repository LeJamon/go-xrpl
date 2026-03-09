package csf

import (
	"sync"
)

// TrustGraph represents the trust relationships between peers.
// It is a directed graph where an edge from A to B means A trusts B
// (B is in A's UNL - Unique Node List).
type TrustGraph struct {
	mu    sync.RWMutex
	edges map[PeerID]map[PeerID]bool
}

// NewTrustGraph creates a new empty trust graph.
func NewTrustGraph() *TrustGraph {
	return &TrustGraph{
		edges: make(map[PeerID]map[PeerID]bool),
	}
}

// Trust adds a trust relationship: from trusts to.
// This means 'to' is in 'from's UNL.
func (g *TrustGraph) Trust(from, to PeerID) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.edges[from] == nil {
		g.edges[from] = make(map[PeerID]bool)
	}
	g.edges[from][to] = true
}

// Untrust removes a trust relationship.
func (g *TrustGraph) Untrust(from, to PeerID) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.edges[from] != nil {
		delete(g.edges[from], to)
	}
}

// Trusts checks if 'from' trusts 'to'.
func (g *TrustGraph) Trusts(from, to PeerID) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.edges[from] == nil {
		return false
	}
	return g.edges[from][to]
}

// TrustedPeers returns all peers that 'from' trusts (from's UNL).
func (g *TrustGraph) TrustedPeers(from PeerID) []PeerID {
	g.mu.RLock()
	defer g.mu.RUnlock()

	trusted := g.edges[from]
	if trusted == nil {
		return nil
	}

	result := make([]PeerID, 0, len(trusted))
	for peer := range trusted {
		result = append(result, peer)
	}
	return result
}

// TrustingPeers returns all peers that trust 'to' (peers with 'to' in their UNL).
func (g *TrustGraph) TrustingPeers(to PeerID) []PeerID {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var result []PeerID
	for from, trusted := range g.edges {
		if trusted[to] {
			result = append(result, from)
		}
	}
	return result
}

// UNLSize returns the number of peers in from's UNL.
func (g *TrustGraph) UNLSize(from PeerID) int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.edges[from] == nil {
		return 0
	}
	return len(g.edges[from])
}

// CanFork checks if the trust graph can lead to a fork given the minimum
// consensus percentage required. A fork can occur if there exist two groups
// of peers that don't have sufficient overlap in their trust relationships.
func (g *TrustGraph) CanFork(minConsensusPct float64) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Get all peers
	allPeers := make(map[PeerID]bool)
	for from := range g.edges {
		allPeers[from] = true
		for to := range g.edges[from] {
			allPeers[to] = true
		}
	}

	peers := make([]PeerID, 0, len(allPeers))
	for p := range allPeers {
		peers = append(peers, p)
	}

	// Check all pairs of peers
	for i := 0; i < len(peers); i++ {
		for j := i + 1; j < len(peers); j++ {
			pi, pj := peers[i], peers[j]

			// Get UNLs
			unlI := g.edges[pi]
			unlJ := g.edges[pj]

			if unlI == nil || unlJ == nil {
				continue
			}

			// Calculate overlap
			overlap := 0
			for p := range unlI {
				if unlJ[p] {
					overlap++
				}
			}

			// Check if overlap is insufficient
			// Using the formula from rippled: need > (1 - minConsensusPct) overlap
			// to prevent forks
			maxUNL := len(unlI)
			if len(unlJ) > maxUNL {
				maxUNL = len(unlJ)
			}

			// If overlap is too small relative to UNL sizes, can fork
			requiredOverlap := int(float64(maxUNL) * (1.0 - minConsensusPct) * 2)
			if overlap < requiredOverlap {
				return true
			}
		}
	}

	return false
}

// Clear removes all trust relationships.
func (g *TrustGraph) Clear() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.edges = make(map[PeerID]map[PeerID]bool)
}
