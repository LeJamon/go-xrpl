package adaptor

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
)

// handleValidatorList ingests an inbound TMValidatorList frame, feeds
// it into the publisher-trust aggregator, and — when the disposition
// permits — rebroadcasts the original frame to other peers.
//
// Mirrors rippled's PeerImp::onMessage(TMValidatorList) at
// PeerImp.cpp:2248-2274 plus the shared onValidatorListMessage helper
// at PeerImp.cpp:2033-2245 (feature gate, hash-suppression dedup,
// charge-by-disposition, broadcast-on-fresh).
//
// When no aggregator is wired (standalone / no publisher trust
// configured) the frame is silently dropped — gossip carries lists for
// publishers we may not have opted into trusting, and that's not
// malicious.
func (r *Router) handleValidatorList(msg *peermanagement.InboundMessage) {
	if r.validatorList == nil {
		return
	}

	// Peer-feature gate. Mirrors PeerImp.cpp:2252-2260: peers that did
	// not negotiate ValidatorListPropagation should not be pushing
	// these frames; rippled charges feeUselessData and returns.
	if !r.peerSupportsValidatorListFeature(msg.PeerID) {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-unsupported-peer")
		return
	}

	decoded, err := message.Decode(message.TypeValidatorList, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode TMValidatorList", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-decode")
		return
	}
	vl, ok := decoded.(*message.ValidatorList)
	if !ok || vl == nil {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-decode")
		return
	}

	// Semantic dedup. Rippled keys this by `sha512Half(manifest, blobs,
	// version)` (PeerImp.cpp:2051) — semantic content, not wire bytes,
	// so two peers gossiping the same blob via different protobuf
	// encodings both suppress on the second arrival.
	if r.messageSeen != nil {
		hash := common.Sha512Half(validatorListSemanticHash(vl))
		if firstSeen, _ := r.messageSeen.observe(hash); !firstSeen {
			// Stamp the sender on the existing hash entry so downstream
			// rebroadcast paths skip them. Mirrors rippled
			// HashRouter::addSuppressionPeer's peer-set extension on a
			// duplicate (HashRouter.cpp:51-79).
			r.messageSeen.recordPeer(hash, uint64(msg.PeerID))
			r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-duplicate")
			return
		}
		r.messageSeen.recordPeer(hash, uint64(msg.PeerID))
	}

	disp, pubKey, seq := r.validatorList.ApplyList(vl.Manifest, vl.Blob, vl.Signature, vl.Version, r.peerSite(msg.PeerID))

	r.logger.Debug("validator list applied",
		"peer", msg.PeerID,
		"disposition", disp.String(),
		"version", vl.Version,
		"sequence", seq)

	chargePeerForDisposition(r, msg.PeerID, "vl", disp)

	// Record what the peer demonstrably has so subsequent broadcasts
	// from any source skip them. Mirrors rippled PeerImp.cpp:2102-2110
	// which updates publisherListSequences_[pubKey] for accepted /
	// expired / pending / same_sequence / known_sequence dispositions.
	if pubKey != (validatorlist.PublisherKey{}) && seq > 0 && disp.ShouldRelay() {
		r.validatorList.RecordPeerSequence(uint64(msg.PeerID), pubKey, seq)
	}

	// Relay the latest STORED accepted blob (not necessarily the
	// inbound frame) via the aggregator-owned broadcast path. The
	// aggregator skips peers already at this sequence and the
	// originating peer. Mirrors rippled applyListsAndBroadcast →
	// broadcastBlobs at ValidatorList.cpp:872-937.
	if disp.ShouldRelay() && pubKey != (validatorlist.PublisherKey{}) {
		r.validatorList.BroadcastLatest(pubKey, uint64(msg.PeerID))
	}
}

// handleValidatorListCollection ingests a TMValidatorListCollection
// (v2) frame, applying each blob individually with the collection's
// shared publisher manifest. When at least one blob relays the frame
// is rebroadcast to other peers.
//
// Bad-data attribution uses the worst per-blob disposition — a
// collection with one Invalid blob and several Accepted blobs gets the
// peer charged once for vl-coll-invalid rather than several times.
// Mirrors rippled's PeerImp.cpp:2141-2183 worstDisposition logic.
func (r *Router) handleValidatorListCollection(msg *peermanagement.InboundMessage) {
	if r.validatorList == nil {
		return
	}

	// Peer-protocol gate. Mirrors rippled PeerImp.cpp:2282-2290 which
	// gates TMValidatorListCollection on
	// `supportsFeature(ValidatorList2Propagation)`. That feature is
	// implicit at protocol >= 2.2 (PeerImp.cpp:511-514). A peer that
	// only negotiated v2.1 may send TMValidatorList (v1) but MUST NOT
	// send the collection frame.
	if !r.peerSupportsValidatorList2(msg.PeerID) {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-unsupported-peer")
		return
	}

	decoded, err := message.Decode(message.TypeValidatorListCollection, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode TMValidatorListCollection", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-decode")
		return
	}
	coll, ok := decoded.(*message.ValidatorListCollection)
	if !ok || coll == nil {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-decode")
		return
	}

	// Reject v1 collections upfront, matching rippled PeerImp.cpp:2291-2299
	// (feeInvalidData "wrong version"). Decoding once and inspecting the
	// version on the decoded message avoids the original code's
	// double-decode.
	if coll.Version < 2 {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-wrong-version")
		return
	}

	// Empty-blobs guard. Rippled charges feeHeavyBurdenPeer "no blobs"
	// at PeerImp.cpp:2042-2049 — the heaviest tier, reserved for severe
	// protocol violations.
	//
	// goXRPL's IncPeerBadData does not yet expose tiered fee weights:
	// every label increments the same counter. Two labels are used to
	// make the rippled tier difference visible in metrics so operators
	// can wire alerting on heavy-tier abuse separately, even before the
	// underlying weight machinery exists:
	//   - "vl-coll-heavy-no-blobs"   → rippled feeHeavyBurdenPeer
	//   - "vl-coll-no-blobs"          → general counter retained for
	//                                   backwards-compatible dashboards
	if len(coll.Blobs) == 0 {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-heavy-no-blobs")
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-no-blobs")
		return
	}

	if r.messageSeen != nil {
		hash := common.Sha512Half(validatorListCollectionSemanticHash(coll))
		if firstSeen, _ := r.messageSeen.observe(hash); !firstSeen {
			r.messageSeen.recordPeer(hash, uint64(msg.PeerID))
			r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-duplicate")
			return
		}
		r.messageSeen.recordPeer(hash, uint64(msg.PeerID))
	}

	dispList, pubKey, maxSeq := r.validatorList.ApplyCollection(coll, r.peerSite(msg.PeerID))

	worst := validatorlist.Accepted
	anyRelay := false
	for _, d := range dispList {
		if d.ShouldRelay() {
			anyRelay = true
		}
		if d.Severity() > worst.Severity() {
			worst = d
		}
	}

	r.logger.Debug("validator list collection applied",
		"peer", msg.PeerID,
		"blobs", len(dispList),
		"worst", worst.String(),
		"max_sequence", maxSeq)

	chargePeerForDisposition(r, msg.PeerID, "vl-coll", worst)

	if worst.IsBadData() {
		// Don't relay a frame containing a poison blob.
		return
	}

	// Record per-peer sequence using the highest blob sequence observed
	// across the collection (mirrors rippled PeerImp.cpp:2110 which
	// uses applyResult.sequence == bestDisposition's max sequence).
	if pubKey != (validatorlist.PublisherKey{}) && maxSeq > 0 && anyRelay {
		r.validatorList.RecordPeerSequence(uint64(msg.PeerID), pubKey, maxSeq)
	}

	if anyRelay && pubKey != (validatorlist.PublisherKey{}) {
		r.validatorList.BroadcastLatest(pubKey, uint64(msg.PeerID))
	}
}

// peerSupportsValidatorListFeature mirrors rippled's
// supportsFeature(ProtocolFeature::ValidatorListPropagation) check at
// PeerImp.cpp:2252. When the overlay is unavailable (tests) we err on
// the side of accepting the frame — matching the pre-fix behaviour.
func (r *Router) peerSupportsValidatorListFeature(peer peermanagement.PeerID) bool {
	if r.overlay == nil {
		return true
	}
	return r.overlay.PeerSupports(peer, peermanagement.FeatureValidatorListPropagation)
}

// peerSupportsValidatorList2 mirrors rippled's
// supportsFeature(ProtocolFeature::ValidatorList2Propagation) at
// PeerImp.cpp:511-514: returns true iff the peer's negotiated
// peer-protocol version is at least 2.2. Used to gate
// TMValidatorListCollection ingress; v2.1 peers that send a collection
// are charged feeUselessData "unsupported peer".
//
// When the overlay is unavailable (tests) we err on the side of
// accepting the frame.
func (r *Router) peerSupportsValidatorList2(peer peermanagement.PeerID) bool {
	if r.overlay == nil {
		return true
	}
	return r.overlay.PeerProtocolAtLeast(peer, 2, 2)
}

// validatorListSemanticHash builds a canonical byte stream the local
// message-seen cache uses to dedup TMValidatorList frames whose wire
// bytes happened to differ across protobuf re-encodings. The shape is
// deliberately simple (length-prefixed big-endian) and is NOT
// byte-equivalent to rippled's `sha512Half(manifest, blobs, version)`
// at PeerImp.cpp:2051 — rippled's hash flows through C++ `hash_append`
// overloads. Cross-node equivalence is not required because each node
// runs an independent seen-hash cache; the only invariant is that two
// semantically-identical inputs hash to the same value within THIS
// process.
func validatorListSemanticHash(vl *message.ValidatorList) []byte {
	out := make([]byte, 0, 4+len(vl.Manifest)+len(vl.Blob)+len(vl.Signature))
	out = appendUint32BE(out, vl.Version)
	out = appendLengthPrefixed(out, vl.Manifest)
	out = appendLengthPrefixed(out, vl.Blob)
	out = appendLengthPrefixed(out, vl.Signature)
	return out
}

// validatorListCollectionSemanticHash is the collection counterpart of
// validatorListSemanticHash — same local-only dedup contract, not
// byte-equivalent to rippled's hash. Per-blob fields are concatenated
// in the order the collection presents them — that order is also what
// ApplyCollection iterates, so semantically-identical collections hash
// the same within this process.
func validatorListCollectionSemanticHash(coll *message.ValidatorListCollection) []byte {
	out := make([]byte, 0, 4+len(coll.Manifest)+64*len(coll.Blobs))
	out = appendUint32BE(out, coll.Version)
	out = appendLengthPrefixed(out, coll.Manifest)
	for _, b := range coll.Blobs {
		out = appendLengthPrefixed(out, b.Manifest)
		out = appendLengthPrefixed(out, b.Blob)
		out = appendLengthPrefixed(out, b.Signature)
	}
	return out
}

func appendUint32BE(out []byte, v uint32) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	return append(out, buf[:]...)
}

func appendLengthPrefixed(out, data []byte) []byte {
	out = appendUint32BE(out, uint32(len(data)))
	out = append(out, data...)
	return out
}

// chargePeerForDisposition maps a Disposition's rippled fee tier
// (Disposition.Charge) into a distinct IncPeerBadData label. Mirrors
// PeerImp.cpp:2141-2183:
//
//	feeUselessData     -> "<prefix>-useless-<disposition>"
//	feeInvalidData     -> "<prefix>-baddata-<disposition>"
//	feeInvalidSignature -> "<prefix>-badsig-<disposition>"
//
// Operators get per-tier metrics matching rippled's accounting.
func chargePeerForDisposition(r *Router, peer peermanagement.PeerID, prefix string, d validatorlist.Disposition) {
	var tag string
	switch d.Charge() {
	case validatorlist.ChargeNone:
		return
	case validatorlist.ChargeUselessData:
		tag = "useless"
	case validatorlist.ChargeInvalidData:
		tag = "baddata"
	case validatorlist.ChargeInvalidSignature:
		tag = "badsig"
	default:
		return
	}
	r.adaptor.IncPeerBadData(uint64(peer), prefix+"-"+tag+"-"+d.String())
}

// peerSite formats a peer-sourced site URI for the aggregator's
// per-publisher SiteURI field. Mirrors rippled's
// `remote_address_.to_string()` used at PeerImp.cpp:2072 — emits a
// "host:port" string when the overlay is available, falling back to
// "peer:<id>" for tests or transient peer lookups.
func (r *Router) peerSite(peerID peermanagement.PeerID) string {
	if r.overlay != nil {
		if addr := r.overlay.PeerRemoteAddr(peerID); addr != "" {
			return addr
		}
	}
	return "peer:" + strconv.FormatUint(uint64(peerID), 10)
}

// RouterBroadcaster is the concrete validatorlist.PeerBroadcaster
// adapter that bridges the aggregator to the overlay + frame codec.
// One instance lives for the lifetime of the router; the aggregator
// holds a reference (set via SetBroadcaster in Components bootstrap).
//
// All three methods are safe for concurrent use: ActivePeers and
// PeerSupportsVL take the overlay's read-side locks; SendList encodes
// fresh bytes per call. Returns are non-fatal — the aggregator logs
// and continues with the next peer.
type RouterBroadcaster struct {
	overlay *peermanagement.Overlay
	sender  NetworkSender
	// suppression is the optional shared hash registry. When wired,
	// SendList / SendCollection record each (hash, peer) pair after a
	// successful send so future inbound from that peer with the same
	// hash maps to a "known sender" path and the broadcast loop can
	// skip peers already known to have the content. Mirrors rippled's
	// `hashRouter.addSuppressionPeer(hash, peer->id())` at
	// rippled/src/xrpld/app/misc/detail/ValidatorList.cpp:781 + 932.
	suppression *messageSuppression
}

// NewRouterBroadcaster wires the overlay (for peer enumeration +
// feature lookup) and the sender (for per-peer SendToPeer). Passing
// nil for either degrades the broadcaster to a silent no-op so tests
// without an overlay don't crash. Direct callers that pass through
// this constructor get a broadcaster without hash-suppression
// tracking; route through Router.NewValidatorListBroadcaster to
// plumb the shared suppression registry.
func NewRouterBroadcaster(overlay *peermanagement.Overlay, sender NetworkSender) *RouterBroadcaster {
	return &RouterBroadcaster{overlay: overlay, sender: sender}
}

// NewValidatorListBroadcaster constructs a RouterBroadcaster bound to
// the Router's suppression registry so SendList / SendCollection
// stamp the hash→peer association expected by rippled's HashRouter
// model.
func (r *Router) NewValidatorListBroadcaster(overlay *peermanagement.Overlay, sender NetworkSender) *RouterBroadcaster {
	return &RouterBroadcaster{overlay: overlay, sender: sender, suppression: r.messageSeen}
}

// ActivePeers implements validatorlist.PeerBroadcaster.
func (b *RouterBroadcaster) ActivePeers() []uint64 {
	if b == nil || b.overlay == nil {
		return nil
	}
	infos := b.overlay.Peers()
	out := make([]uint64, 0, len(infos))
	for _, p := range infos {
		out = append(out, uint64(p.ID))
	}
	return out
}

// PeerSupportsVL implements validatorlist.PeerBroadcaster.
func (b *RouterBroadcaster) PeerSupportsVL(peerID uint64) bool {
	if b == nil || b.overlay == nil {
		return false
	}
	return b.overlay.PeerSupports(peermanagement.PeerID(peerID), peermanagement.FeatureValidatorListPropagation)
}

// PeerSupportsV2 implements validatorlist.PeerBroadcaster. Mirrors
// rippled's `supportsFeature(ProtocolFeature::ValidatorList2Propagation)`
// at PeerImp.cpp:511-514 which gates on negotiated protocol >= 2.2.
func (b *RouterBroadcaster) PeerSupportsV2(peerID uint64) bool {
	if b == nil || b.overlay == nil {
		return false
	}
	return b.overlay.PeerProtocolAtLeast(peermanagement.PeerID(peerID), 2, 2)
}

// SendList implements validatorlist.PeerBroadcaster. Encodes a
// TMValidatorList carrying the supplied wire bytes verbatim and
// delivers it to peerID via the adaptor sender. blobVersion goes on
// the frame's `version` field. When wired with a suppression
// registry: short-circuits peers already known to have the content,
// and stamps the (hash, peer) pair after a successful send.
func (b *RouterBroadcaster) SendList(peerID uint64, manifestBytes, blob, signature []byte, blobVersion uint32) error {
	if b == nil || b.sender == nil {
		return fmt.Errorf("router broadcaster: nil sender")
	}
	vl := &message.ValidatorList{
		Manifest:  manifestBytes,
		Blob:      blob,
		Signature: signature,
		Version:   blobVersion,
	}
	frame, err := encodeFrame(message.TypeValidatorList, vl)
	if err != nil {
		return fmt.Errorf("encode TMValidatorList: %w", err)
	}
	hash := common.Sha512Half(validatorListSemanticHash(vl))
	if b.suppression != nil && b.suppression.peerHasHash(hash, peerID) {
		// Peer already has this content. Skip the redundant send.
		// Mirrors rippled's "skip peers already in the hash's
		// suppression peer-set" optimisation at
		// ValidatorList.cpp:909 (`if (toSkip->count(peer->id()) == 0)`).
		return nil
	}
	if err := b.sender.SendToPeer(peerID, frame); err != nil {
		return err
	}
	if b.suppression != nil {
		b.suppression.recordPeer(hash, peerID)
	}
	return nil
}

// SendCollection implements validatorlist.PeerBroadcaster. Encodes a
// TMValidatorListCollection carrying the publisher manifest plus the
// supplied (per-blob manifest, blob, signature) tuples and delivers
// it to peerID. Used by BroadcastLatest for every v2-capable peer
// (single-entry collection when the publisher has no Remaining
// blobs, multi-entry when it does).
func (b *RouterBroadcaster) SendCollection(peerID uint64, manifestBytes []byte, blobs []validatorlist.BroadcastBlob, version uint32) error {
	if b == nil || b.sender == nil {
		return fmt.Errorf("router broadcaster: nil sender")
	}
	coll := &message.ValidatorListCollection{
		Version:  version,
		Manifest: manifestBytes,
	}
	for _, blob := range blobs {
		coll.Blobs = append(coll.Blobs, message.ValidatorBlobInfo{
			Manifest:  blob.Manifest,
			Blob:      blob.Blob,
			Signature: blob.Signature,
		})
	}
	frame, err := encodeFrame(message.TypeValidatorListCollection, coll)
	if err != nil {
		return fmt.Errorf("encode TMValidatorListCollection: %w", err)
	}
	hash := common.Sha512Half(validatorListCollectionSemanticHash(coll))
	if b.suppression != nil && b.suppression.peerHasHash(hash, peerID) {
		return nil
	}
	if err := b.sender.SendToPeer(peerID, frame); err != nil {
		return err
	}
	if b.suppression != nil {
		b.suppression.recordPeer(hash, peerID)
	}
	return nil
}
