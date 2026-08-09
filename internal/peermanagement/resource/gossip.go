package resource

type GossipItem struct {
	Address string
	Balance uint32
}

type Gossip struct {
	Items []GossipItem
}
