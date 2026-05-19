package list

// PeerBroadcaster is the minimal overlay+encoder surface the aggregator
// uses to push the most recently accepted list (current + any
// verified-but-future Remaining blobs) for a publisher out to
// connected peers. Implemented by the router so that validator/list
// stays free of any peermanagement / message-codec dependency.
//
// Two send entry points distinguish the rippled wire shapes:
//   - SendList: TMValidatorList (v1) carrying a single accepted blob.
//     Used for peers that did not negotiate ValidatorList2Propagation.
//   - SendCollection: TMValidatorListCollection (v2) carrying current
//     plus any Remaining blobs. Used for any peer that negotiated v2,
//     even when the publisher has no Remaining blobs (in which case
//     the collection has a single entry — current).
//
// The aggregator picks the entry point per peer via PeerSupportsV2,
// matching rippled's sendValidatorList at
// rippled/src/xrpld/app/misc/detail/ValidatorList.cpp:752-757 which
// selects messageVersion based on the peer feature alone.
type PeerBroadcaster interface {
	// ActivePeers returns the IDs of every connected, handshake-
	// complete peer. The aggregator iterates this set on each
	// BroadcastLatest call; order is unspecified.
	ActivePeers() []uint64

	// PeerSupportsVL reports whether `peerID` negotiated
	// ValidatorListPropagation at handshake. Mirrors rippled's
	// peer->supportsFeature(ProtocolFeature::ValidatorListPropagation)
	// gate in PeerImp.cpp:2252-2260.
	PeerSupportsVL(peerID uint64) bool

	// PeerSupportsV2 reports whether `peerID` negotiated
	// ValidatorList2Propagation (implicitly at peer-protocol >= 2.2).
	// Mirrors rippled PeerImp.cpp:511-514.
	PeerSupportsV2(peerID uint64) bool

	// SendList delivers a TMValidatorList (v1) frame to peerID carrying
	// the supplied wire bytes verbatim. blobVersion is recorded on the
	// frame's `version` field. Returns any send error; the aggregator
	// logs and continues with the remaining peers.
	SendList(peerID uint64, manifest, blob, signature []byte, blobVersion uint32) error

	// SendCollection delivers a TMValidatorListCollection (v2) frame
	// carrying the publisher manifest plus an ordered slice of
	// (per-blob optional manifest, blob, signature) tuples. Used for
	// every v2-capable recipient (the slice has a single current entry
	// when the publisher has no Remaining blobs). Returns any send
	// error.
	SendCollection(peerID uint64, manifest []byte, blobs []BroadcastBlob, version uint32) error
}

// BroadcastBlob is one entry inside a TMValidatorListCollection frame.
// The aggregator constructs a slice of these from the publisher's
// current + Remaining state for v2 broadcasts.
type BroadcastBlob struct {
	// Manifest is the per-blob manifest override; empty for blobs that
	// use the collection's shared publisher manifest.
	Manifest []byte
	// Blob is the base64-encoded blob bytes as originally received.
	Blob []byte
	// Signature is the hex-encoded blob signature.
	Signature []byte
}
