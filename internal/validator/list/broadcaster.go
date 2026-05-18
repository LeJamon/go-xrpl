package list

// PeerBroadcaster is the minimal overlay+encoder surface the aggregator
// uses to push the most recently accepted list for a publisher out to
// connected peers. Implemented by the router so that validator/list
// stays free of any peermanagement / message-codec dependency.
//
// Sends are issued at TMValidatorList (v1, single-blob) granularity:
// goXRPL's aggregator never stores pending blobs, so the canonical
// accepted form for any publisher is always a single (manifest, blob,
// signature) triple — and v1 is the wire-version every VL-capable
// peer can decode, which lets us defer ValidatorList2Propagation
// feature-negotiation work without losing relay coverage.
type PeerBroadcaster interface {
	// ActivePeers returns the IDs of every connected, handshake-
	// complete peer. The aggregator iterates this set on each
	// BroadcastLatest call; order is unspecified.
	ActivePeers() []uint64

	// PeerSupportsVL reports whether `peerID` negotiated
	// ValidatorListPropagation at handshake. Mirrors rippled's
	// peer->supportsFeature(ProtocolFeature::ValidatorListPropagation)
	// gate in PeerImp.cpp:2252-2260. Peers that didn't advertise the
	// feature are silently skipped.
	PeerSupportsVL(peerID uint64) bool

	// SendList delivers a TMValidatorList frame to peerID carrying
	// the supplied wire bytes verbatim. blobVersion is recorded on
	// the frame so the receiver knows which blob schema to parse.
	// Returns any send error; the aggregator logs and continues
	// with the remaining peers rather than aborting the broadcast.
	SendList(peerID uint64, manifest, blob, signature []byte, blobVersion uint32) error
}
