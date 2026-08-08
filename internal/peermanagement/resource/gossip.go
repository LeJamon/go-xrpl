package resource

// GossipItem describes one consumer's balance for export. Mirrors
// rippled's ripple::Resource::Gossip::Item.
type GossipItem struct {
	Address string
	Balance uint32
}

// Gossip is a snapshot of consumer balances suitable for sharing across
// a cluster. Mirrors rippled's ripple::Resource::Gossip.
type Gossip struct {
	Items []GossipItem
}
