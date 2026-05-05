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
// Mirrors rippled OverlayImpl::getManifestsMessage which builds a
// TMManifests carrying every ValidatorManifests entry (Manifest.cpp:1184-1212).
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

// SendLocalManifestTo sends the aggregated TMManifests frame (every
// cached validator manifest) to a single peer. Returns nil and emits
// nothing when the cache is empty or no sender is wired (test-only
// construction). Any encode error is logged and swallowed: emission is
// best-effort, the next reconnect will retry on its own.
func (r *Router) SendLocalManifestTo(peerID peermanagement.PeerID) {
	wires := r.manifestsForEmission()
	if len(wires) == 0 {
		return
	}
	sender := r.manifestEmitter()
	if sender == nil {
		return
	}
	frame, err := encodeManifestsFrame(wires...)
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

// BroadcastLocalManifest gossips the aggregated TMManifests frame to
// every currently-connected peer. Returns the number of peers the frame
// was queued for (0 when there's nothing to broadcast or no peers are
// connected) so callers can decide whether to log the emission.
func (r *Router) BroadcastLocalManifest() int {
	wires := r.manifestsForEmission()
	if len(wires) == 0 {
		return 0
	}
	sender := r.manifestEmitter()
	if sender == nil {
		return 0
	}
	peers := sender.Peers()
	if len(peers) == 0 {
		return 0
	}
	frame, err := encodeManifestsFrame(wires...)
	if err != nil {
		r.logger.Warn("failed to encode local manifest frame", "error", err)
		return 0
	}
	if err := sender.Broadcast(frame); err != nil {
		r.logger.Warn("broadcast local manifest failed", "error", err)
		return 0
	}
	return len(peers)
}

// manifestEmitter returns the sender used by SendLocalManifestTo /
// BroadcastLocalManifest. Falls back to nil when the router has neither
// a real overlay nor a test override — in that case the emission paths
// short-circuit instead of segfaulting.
func (r *Router) manifestEmitter() manifestSender {
	if r.overrideManifestSender != nil {
		return r.overrideManifestSender
	}
	if r.overlay == nil {
		return nil
	}
	return r.overlay
}

// HandlePeerConnect is the callback wired into Overlay.SetPeerConnectCallback.
// Fires once a peer has finished its handshake and joined the overlay;
// emits the cached validator manifests so the peer can resolve every
// known ephemeral signing key back to its trusted master before any
// validation arrives.
//
// Mirrors rippled PeerImp::doProtocolStart (PeerImp.cpp:851-886) which
// sends overlay_.getManifestsMessage() in the post-handshake window.
// Skip cases (cache empty, no overlay) are handled inside
// SendLocalManifestTo so this stays a thin event-loop trampoline.
func (r *Router) HandlePeerConnect(peerID peermanagement.PeerID) {
	r.SendLocalManifestTo(peerID)
}

// manifestsForEmission returns the wire bytes of every cached validator
// manifest, in arbitrary order. Centralizes the "what do we have to
// gossip?" decision so the per-peer and broadcast paths can't drift on
// the skip-case logic. Returns nil when the cache is empty (observer /
// seed-only mode, or fresh boot before any peer has gossiped anything).
func (r *Router) manifestsForEmission() [][]byte {
	if r.manifests == nil {
		return nil
	}
	return r.manifests.SerializedAll()
}
