package adaptor

import (
	"strconv"

	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	validatorlist "github.com/LeJamon/goXRPLd/internal/validator/list"
)

// handleValidatorList ingests an inbound TMValidatorList frame, feeds
// it into the publisher-trust aggregator, and — on Accepted —
// rebroadcasts the original frame to other peers. Mirrors rippled's
// PeerImp::onMessage(TMValidatorList) at PeerImp.cpp:2248-2274 +
// ValidatorList::applyListsAndBroadcast at ValidatorList.cpp:940-995.
//
// When no aggregator is wired (standalone / no publisher trust
// configured) the frame is silently dropped — gossip carries lists for
// publishers we may not have opted into trusting, and that's not
// malicious.
//
// Decode failures and verification failures attribute "vl-decode" /
// "vl-invalid" bad-data to the sender, matching the manifest handler's
// pattern of charging only on structural / cryptographic failures
// (Untrusted publishers and Stale / SameSequence dispositions are
// silent).
func (r *Router) handleValidatorList(msg *peermanagement.InboundMessage) {
	if r.validatorList == nil {
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

	disp, _ := r.validatorList.ApplyList(vl.Manifest, vl.Blob, vl.Signature, vl.Version, peerSite(msg.PeerID))

	r.logger.Debug("validator list applied",
		"peer", msg.PeerID,
		"disposition", disp.String(),
		"version", vl.Version)

	if disp.IsBadData() {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-"+disp.String())
		return
	}

	if disp.ShouldRelay() && r.overlay != nil {
		frame, err := encodeFrame(message.TypeValidatorList, vl)
		if err != nil {
			r.logger.Warn("failed to encode TMValidatorList relay frame", "error", err)
			return
		}
		_ = r.overlay.BroadcastExcept(msg.PeerID, frame)
	}
}

// handleValidatorListCollection ingests a TMValidatorListCollection
// (v2) frame, applying each blob individually with the collection's
// shared publisher manifest. On any Accepted blob the frame is
// rebroadcast (matching rippled's per-collection broadcast policy).
//
// Bad-data attribution is summarized to the worst disposition across
// blobs — a collection with one Invalid and several Accepted gets the
// peer charged once for vl-collection-invalid rather than several
// times.
func (r *Router) handleValidatorListCollection(msg *peermanagement.InboundMessage) {
	if r.validatorList == nil {
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

	dispList, _ := r.validatorList.ApplyCollection(coll, peerSite(msg.PeerID))

	worst := validatorlist.Accepted
	anyAccepted := false
	for _, d := range dispList {
		if d == validatorlist.Accepted {
			anyAccepted = true
		}
		if dispositionRank(d) > dispositionRank(worst) {
			worst = d
		}
	}

	r.logger.Debug("validator list collection applied",
		"peer", msg.PeerID,
		"blobs", len(dispList),
		"worst", worst.String())

	if worst.IsBadData() {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "vl-coll-"+worst.String())
		// Don't relay a frame containing a poison blob.
		return
	}

	if anyAccepted && r.overlay != nil {
		frame, err := encodeFrame(message.TypeValidatorListCollection, coll)
		if err != nil {
			r.logger.Warn("failed to encode TMValidatorListCollection relay frame", "error", err)
			return
		}
		_ = r.overlay.BroadcastExcept(msg.PeerID, frame)
	}
}

// dispositionRank returns a "worse-is-larger" ordering used to pick the
// summary disposition for a v2 collection. Keep in sync with the table
// in validatorlist.bestDisposition — they describe the same ordering
// but here we go "worst wins" because the router uses it to attribute
// bad-data to the sender, whereas the poller uses "best wins" for RPC.
func dispositionRank(d validatorlist.Disposition) int {
	switch d {
	case validatorlist.Accepted:
		return 0
	case validatorlist.Expired:
		return 1
	case validatorlist.Pending:
		return 2
	case validatorlist.SameSequence:
		return 3
	case validatorlist.KnownSequence:
		return 4
	case validatorlist.Stale:
		return 5
	case validatorlist.Untrusted:
		return 6
	case validatorlist.Invalid:
		return 7
	case validatorlist.UnsupportedVersion:
		return 8
	case validatorlist.Malformed:
		return 9
	default:
		return -1
	}
}

// peerSite formats a peer-sourced site URI for the aggregator's
// per-publisher SiteURI field. Distinct from HTTP-polled URIs so the
// RPC can tell at a glance where a publisher's most recent list came
// from.
func peerSite(peerID peermanagement.PeerID) string {
	return "peer:" + strconv.FormatUint(uint64(peerID), 10)
}
