package adaptor

import (
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
// at PeerImp.cpp:2033-2245 (hash-suppression dedup, charge-by-
// disposition, broadcast-on-fresh).
//
// When no aggregator is wired (standalone / no publisher trust
// configured) the frame is silently dropped — gossip carries lists for
// publishers we may not have opted into trusting, and that's not
// malicious.
func (r *Router) handleValidatorList(msg *peermanagement.InboundMessage) {
	if r.validatorList == nil {
		return
	}

	// Hash-suppression dedup (PeerImp.cpp:2051-2066). Rippled keys this
	// by `sha512Half(manifest, blobs, version)`; the goXRPL message_seen
	// tracker hashes the full wire payload, which yields the same
	// equivalence class for any byte-identical frame replay.
	if r.messageSeen != nil {
		hash := common.Sha512Half(msg.Payload)
		if firstSeen, _ := r.messageSeen.observe(hash); !firstSeen {
			r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-duplicate")
			return
		}
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

	// Reject v1 collections upfront, matching rippled PeerImp.cpp:2291-2299.
	if peeked, err := message.Decode(message.TypeValidatorListCollection, msg.Payload); err == nil {
		if coll, ok := peeked.(*message.ValidatorListCollection); ok && coll != nil && coll.Version < 2 {
			r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-wrong-version")
			return
		}
	}

	if r.messageSeen != nil {
		hash := common.Sha512Half(msg.Payload)
		if firstSeen, _ := r.messageSeen.observe(hash); !firstSeen {
			r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-duplicate")
			return
		}
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
