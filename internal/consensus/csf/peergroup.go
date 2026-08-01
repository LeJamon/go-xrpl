package csf

import (
	"slices"
	"sort"
)

type PeerGroup struct {
	peers []*Peer
}

func NewPeerGroup() *PeerGroup {
	return &PeerGroup{}
}

func NewPeerGroupFrom(peers []*Peer) *PeerGroup {
	result := append([]*Peer(nil), peers...)
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return &PeerGroup{peers: result}
}

func NewPeerGroupSingle(peer *Peer) *PeerGroup {
	return &PeerGroup{peers: []*Peer{peer}}
}

func (g *PeerGroup) Size() int {
	return len(g.peers)
}

func (g *PeerGroup) Get(i int) *Peer {
	return g.peers[i]
}

func (g *PeerGroup) Peers() []*Peer {
	return append([]*Peer(nil), g.peers...)
}

func (g *PeerGroup) ContainsID(id PeerID) bool {
	_, found := slices.BinarySearchFunc(g.peers, id, func(peer *Peer, id PeerID) int {
		switch {
		case peer.ID < id:
			return -1
		case peer.ID > id:
			return 1
		default:
			return 0
		}
	})
	return found
}

func (g *PeerGroup) Trust(other *PeerGroup) {
	for _, peer := range g.peers {
		for _, target := range other.peers {
			peer.Trust(target)
		}
	}
}

func (g *PeerGroup) Untrust(other *PeerGroup) {
	for _, peer := range g.peers {
		for _, target := range other.peers {
			peer.Untrust(target)
		}
	}
}

func (g *PeerGroup) Connect(other *PeerGroup, delay SimDuration) {
	forEachPair(g, other, func(peer, target *Peer) {
		peer.Connect(target, delay)
	})
}

func (g *PeerGroup) Disconnect(other *PeerGroup) {
	forEachPair(g, other, func(peer, target *Peer) {
		peer.Disconnect(target)
	})
}

func (g *PeerGroup) TrustAndConnect(other *PeerGroup, delay SimDuration) {
	g.Trust(other)
	g.Connect(other, delay)
}

func (g *PeerGroup) Union(other *PeerGroup) *PeerGroup {
	byID := make(map[PeerID]*Peer, len(g.peers)+len(other.peers))
	for _, peer := range g.peers {
		byID[peer.ID] = peer
	}
	for _, peer := range other.peers {
		byID[peer.ID] = peer
	}
	result := make([]*Peer, 0, len(byID))
	for _, peer := range byID {
		result = append(result, peer)
	}
	return NewPeerGroupFrom(result)
}

func forEachPair(a, b *PeerGroup, fn func(*Peer, *Peer)) {
	type pair struct {
		low  PeerID
		high PeerID
	}
	seen := make(map[pair]struct{})
	for _, peer := range a.peers {
		for _, target := range b.peers {
			if peer == target {
				continue
			}
			key := pair{low: peer.ID, high: target.ID}
			if key.low > key.high {
				key.low, key.high = key.high, key.low
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			fn(peer, target)
		}
	}
}
