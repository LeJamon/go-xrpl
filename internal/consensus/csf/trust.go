package csf

import (
	"slices"
	"sync"
)

type TrustGraph struct {
	mu    sync.RWMutex
	edges map[PeerID]map[PeerID]struct{}
}

func NewTrustGraph() *TrustGraph {
	return &TrustGraph{edges: make(map[PeerID]map[PeerID]struct{})}
}

func (g *TrustGraph) Trust(from, to PeerID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.edges[from] == nil {
		g.edges[from] = make(map[PeerID]struct{})
	}
	g.edges[from][to] = struct{}{}
}

func (g *TrustGraph) Untrust(from, to PeerID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.edges[from], to)
}

func (g *TrustGraph) Trusts(from, to PeerID) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.edges[from][to]
	return ok
}

func (g *TrustGraph) TrustedPeers(from PeerID) []PeerID {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]PeerID, 0, len(g.edges[from]))
	for peer := range g.edges[from] {
		result = append(result, peer)
	}
	slices.Sort(result)
	return result
}

func (g *TrustGraph) UNLSize(from PeerID) int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.edges[from])
}
