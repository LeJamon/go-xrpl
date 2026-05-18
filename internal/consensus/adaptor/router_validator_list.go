package adaptor

import (
	"encoding/binary"
	"strconv"

	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	validatorlist "github.com/LeJamon/goXRPLd/internal/validator/list"
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
			r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-duplicate")
			return
		}
	}

	disp, _ := r.validatorList.ApplyList(vl.Manifest, vl.Blob, vl.Signature, vl.Version, peerSite(msg.PeerID))

	r.logger.Debug("validator list applied",
		"peer", msg.PeerID,
		"disposition", disp.String(),
		"version", vl.Version)

	chargePeerForDisposition(r, msg.PeerID, "vl", disp)

	if disp.ShouldRelay() && r.overlay != nil {
		frame, encErr := encodeFrame(message.TypeValidatorList, vl)
		if encErr != nil {
			r.logger.Warn("failed to encode TMValidatorList relay frame", "error", encErr)
			return
		}
		_ = r.overlay.BroadcastExcept(msg.PeerID, frame)
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

	// Peer-feature gate. Mirrors PeerImp.cpp:2282-2290.
	if !r.peerSupportsValidatorListFeature(msg.PeerID) {
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
	// protocol violations. Surface it as a distinct label so operators
	// can see this is not a generic bad-signature case.
	if len(coll.Blobs) == 0 {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-no-blobs")
		return
	}

	if r.messageSeen != nil {
		hash := common.Sha512Half(validatorListCollectionSemanticHash(coll))
		if firstSeen, _ := r.messageSeen.observe(hash); !firstSeen {
			r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-duplicate")
			return
		}
	}

	dispList, _ := r.validatorList.ApplyCollection(coll, peerSite(msg.PeerID))

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
		"worst", worst.String())

	chargePeerForDisposition(r, msg.PeerID, "vl-coll", worst)

	if worst.IsBadData() {
		// Don't relay a frame containing a poison blob.
		return
	}

	if anyRelay && r.overlay != nil {
		frame, encErr := encodeFrame(message.TypeValidatorListCollection, coll)
		if encErr != nil {
			r.logger.Warn("failed to encode TMValidatorListCollection relay frame", "error", encErr)
			return
		}
		_ = r.overlay.BroadcastExcept(msg.PeerID, frame)
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

// validatorListSemanticHash builds the canonical byte stream that
// stands in for rippled's `sha512Half(manifest, blobs, version)` at
// PeerImp.cpp:2051. The shape is fixed across protobuf re-encodings so
// dedup catches semantically-identical replays even when the wire bytes
// differ.
func validatorListSemanticHash(vl *message.ValidatorList) []byte {
	out := make([]byte, 0, 4+len(vl.Manifest)+len(vl.Blob)+len(vl.Signature))
	out = appendUint32BE(out, vl.Version)
	out = appendLengthPrefixed(out, vl.Manifest)
	out = appendLengthPrefixed(out, vl.Blob)
	out = appendLengthPrefixed(out, vl.Signature)
	return out
}

// validatorListCollectionSemanticHash is the collection counterpart of
// validatorListSemanticHash. Per-blob fields are concatenated in the
// order the collection presents them — that order is also what
// ApplyCollection iterates, so semantically-identical collections hash
// the same.
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
// per-publisher SiteURI field. Distinct from HTTP-polled URIs so the
// RPC can tell at a glance where a publisher's most recent list came
// from.
func peerSite(peerID peermanagement.PeerID) string {
	return "peer:" + strconv.FormatUint(uint64(peerID), 10)
}
