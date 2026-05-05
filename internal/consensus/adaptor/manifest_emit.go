package adaptor

import (
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// manifestSender is the slice of *peermanagement.Overlay the manifest
// emitter calls into. Defining it here keeps the production path on the
// concrete overlay (no indirection cost) while letting tests substitute
// a fake without standing up real listeners. *peermanagement.Overlay
// satisfies this interface by virtue of its existing public methods.
type manifestSender interface {
	Send(peerID peermanagement.PeerID, frame []byte) error
	Broadcast(frame []byte) error
	Peers() []peermanagement.PeerInfo
}

// encodeManifestsFrame wraps one or more wire-format manifest STObjects
// in a TMManifests frame ready for Overlay.Broadcast / Overlay.Send.
// Mirrors rippled OverlayImpl::sendEndpoints which emits manifests
// alongside endpoints in the post-handshake window — every peer
// receives the same TMManifests payload.
//
// Shared by relayManifest (single-manifest gossip from a peer) and the
// local-manifest emission paths in #372 so both produce byte-identical
// frames; rippled's PeerImp doesn't distinguish between the two on the
// wire.
func encodeManifestsFrame(serialized ...[]byte) ([]byte, error) {
	list := make([]message.Manifest, 0, len(serialized))
	for _, b := range serialized {
		if len(b) == 0 {
			continue
		}
		list = append(list, message.Manifest{STObject: b})
	}
	return encodeFrame(message.TypeManifests, &message.Manifests{List: list})
}

// SendLocalManifestTo sends our local validator manifest to a single
// peer. Returns nil and emits nothing when:
//   - the router has no overlay handle (test-only construction);
//   - we have no local validator (observer mode);
//   - the local validator is seed-only (no manifest to broadcast — the
//     legacy path where master == signing has nothing to gossip).
//
// Any encode error is logged and swallowed: emission is best-effort.
// The caller does not get a chance to retry per-peer because the next
// reconnect will just trigger another HandlePeerConnect anyway.
func (r *Router) SendLocalManifestTo(peerID peermanagement.PeerID) {
	wire := r.localManifestBytes()
	if wire == nil {
		return
	}
	sender := r.manifestEmitter()
	if sender == nil {
		return
	}
	frame, err := encodeManifestsFrame(wire)
	if err != nil {
		r.logger.Warn("failed to encode local manifest frame for peer", "error", err, "peer", peerID)
		return
	}
	if err := sender.Send(peerID, frame); err != nil {
		// Peer may have raced a disconnect between addPeer and the
		// callback. ErrPeerNotFound is benign; surface other errors at
		// debug to aid diagnosis without spamming logs on a flapping
		// peer.
		r.logger.Debug("send local manifest to peer failed", "error", err, "peer", peerID)
	}
}

// BroadcastLocalManifest gossips our local validator manifest to every
// currently-connected peer. Used by the startup one-shot broadcast and
// (in #373) by the periodic re-emission timer. Same skip cases as
// SendLocalManifestTo.
//
// Returns the number of peers the frame was queued for (0 when there's
// nothing to broadcast or no peers connected) so callers can decide
// whether to log the emission.
func (r *Router) BroadcastLocalManifest() int {
	wire := r.localManifestBytes()
	if wire == nil {
		return 0
	}
	sender := r.manifestEmitter()
	if sender == nil {
		return 0
	}
	frame, err := encodeManifestsFrame(wire)
	if err != nil {
		r.logger.Warn("failed to encode local manifest frame", "error", err)
		return 0
	}
	peers := sender.Peers()
	if len(peers) == 0 {
		return 0
	}
	if err := sender.Broadcast(frame); err != nil {
		r.logger.Warn("broadcast local manifest failed", "error", err)
		return 0
	}
	return len(peers)
}

// manifestEmitter returns the sender used by SendLocalManifestTo /
// BroadcastLocalManifest. Falls back to nil when the router has
// neither a real overlay nor a test override — in that case the
// emission paths short-circuit instead of segfaulting.
func (r *Router) manifestEmitter() manifestSender {
	if r.testManifestSender != nil {
		return r.testManifestSender
	}
	if r.overlay == nil {
		return nil
	}
	return r.overlay
}

// HandlePeerConnect is the callback wired into Overlay.SetPeerConnectCallback.
// Fires once a peer has finished its handshake and joined the overlay;
// emits our local manifest so the peer can resolve our ephemeral signing
// key back to the trusted master before our first validation arrives.
//
// Mirrors rippled OverlayImpl::sendEndpoints which always emits the
// local manifest (when one exists) immediately after a peer is added.
// Skip cases (seed-only, no overlay) are handled inside
// SendLocalManifestTo so this stays a thin event-loop trampoline.
func (r *Router) HandlePeerConnect(peerID peermanagement.PeerID) {
	r.SendLocalManifestTo(peerID)
}

// localManifestBytes returns the wire bytes of our local validator
// manifest, or nil if we have nothing to emit. Centralizes the
// "do we have a manifest to broadcast?" decision so the per-peer and
// broadcast paths can't drift on the skip-case logic.
func (r *Router) localManifestBytes() []byte {
	if r.adaptor == nil || r.adaptor.identity == nil {
		return nil
	}
	if len(r.adaptor.identity.SerializedMfst) == 0 {
		return nil
	}
	return r.adaptor.identity.SerializedMfst
}
