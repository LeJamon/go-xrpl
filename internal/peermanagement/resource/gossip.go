package resource

// GossipItem describes one consumer's balance for export.
type GossipItem struct {
	Address string
	Balance uint32
}

// Gossip is a snapshot of consumer balances suitable for sharing across a cluster.
type Gossip struct {
	Items []GossipItem
}
