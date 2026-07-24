package adaptor

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"google.golang.org/protobuf/encoding/protowire"
)

// manifestSender is the slice of *peermanagement.Overlay the manifest
// emitter calls into. Defining it here keeps the production path on the
// concrete overlay (no indirection cost) while letting tests substitute
// a fake without standing up real listeners. *peermanagement.Overlay
// satisfies this interface by virtue of its existing public methods.
type manifestSender interface {
	SendManifestFrames(peerID peermanagement.PeerID, frames [][]byte) error
	BroadcastManifestFrames(frames [][]byte) error
	BroadcastManifestFramesExcept(peerID peermanagement.PeerID, frames [][]byte) error
	Peers() []peermanagement.PeerInfo
}

const (
	manifestFrameTargetSize = 1 << 20
	manifestFrameMaxEntries = 100
)

// encodeManifestsFrame wraps one or more wire-format manifest STObjects
// in a TMManifests frame ready for Overlay.Broadcast / Overlay.Send.
//
// Shared by inbound manifest relay and the local-manifest emission paths
// so both produce byte-identical frames;
// peers don't distinguish between the two on the wire.
func encodeManifestsFrame(serialized ...[]byte) ([]byte, error) {
	list := make([]message.Manifest, 0, len(serialized))
	for _, b := range serialized {
		if len(b) == 0 {
			continue
		}
		list = append(list, message.Manifest{STObject: b})
	}
	return message.EncodeFrame(&message.Manifests{List: list})
}

func encodeManifestFrames(serialized ...[]byte) ([][]byte, error) {
	frames := make([][]byte, 0, 1)
	chunk := make([][]byte, 0)
	chunkSize := message.HeaderSizeUncompressed

	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		frame, err := encodeManifestsFrame(chunk...)
		if err != nil {
			return err
		}
		frames = append(frames, frame)
		chunk = chunk[:0]
		chunkSize = message.HeaderSizeUncompressed
		return nil
	}

	for _, wire := range serialized {
		if len(wire) == 0 {
			continue
		}
		innerSize := 1 + protowire.SizeBytes(len(wire))
		entrySize := 1 + protowire.SizeBytes(innerSize)
		if message.HeaderSizeUncompressed+entrySize > manifestFrameTargetSize {
			return nil, fmt.Errorf("manifest size %d exceeds frame target %d", len(wire), manifestFrameTargetSize)
		}
		if len(chunk) != 0 && (len(chunk) >= manifestFrameMaxEntries || chunkSize+entrySize > manifestFrameTargetSize) {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		chunk = append(chunk, wire)
		chunkSize += entrySize
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return frames, nil
}

// SendLocalManifestTo sends the aggregated TMManifests frames (every
// cached validator manifest) to a single peer. Returns nil and emits
// nothing when the cache is empty or no sender is wired (test-only
// construction). Any encode error is logged and swallowed: emission is
// best-effort, the next reconnect will retry on its own.
func (r *Router) SendLocalManifestTo(peerID peermanagement.PeerID) {
	frames := r.cachedManifestFrames()
	if len(frames) == 0 {
		return
	}
	sender := r.manifestEmitter()
	if sender == nil {
		return
	}
	if err := sender.SendManifestFrames(peerID, frames); err != nil {
		// Peer may have raced a disconnect between addPeer and the
		// callback. ErrPeerNotFound / ErrConnectionClosed are benign;
		// surface at debug to aid diagnosis without spamming logs on a
		// flapping peer.
		r.logger.Debug("send local manifest to peer failed", "error", err, "peer", peerID)
	}
}

// BroadcastLocalManifest gossips the aggregated TMManifests frames to
// every currently-connected peer. Returns the number of peers the frames
// were queued for (0 when there's nothing to broadcast or no peers are
// connected) so callers can decide whether to log the emission.
func (r *Router) BroadcastLocalManifest() int {
	frames := r.cachedManifestFrames()
	if len(frames) == 0 {
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
	if err := sender.BroadcastManifestFrames(frames); err != nil {
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

// HandlePeerConnect queues peer admission onto the Router goroutine.
func (r *Router) HandlePeerConnect(peerID peermanagement.PeerID) {
	r.pendingPeerConnects.Store(peerID, struct{}{})
	select {
	case r.peerConnectWake <- struct{}{}:
	default:
	}
}

func (r *Router) drainPeerConnects() {
	r.pendingPeerConnects.Range(func(key, _ any) bool {
		peerID := key.(peermanagement.PeerID)
		if _, loaded := r.pendingPeerConnects.LoadAndDelete(peerID); loaded {
			r.handlePeerConnect(peerID)
		}
		return true
	})
}

func (r *Router) handlePeerConnect(peerID peermanagement.PeerID) {
	if r.peerSessions != nil && !r.peerSessions.IsPeerConnected(peerID) {
		return
	}
	r.reconcilePeerAvailability()
	if hints, ok := r.peerSessions.(peerLedgerHintView); ok && r.adaptor != nil {
		if closed, exists := hints.PeerClosedLedger(peerID); exists {
			r.adaptor.UpdatePeerLCL(uint64(peerID), closed)
		}
	}
	r.addPeerToActiveAcquisitions(uint64(peerID))
	r.SendLocalManifestTo(peerID)
}

func (r *Router) addPeerToActiveAcquisitions(peerID uint64) {
	if r.fetchTracker == nil || peerID == 0 {
		return
	}
	for _, ledger := range r.fetchTracker.Active() {
		if !ledger.AddPeerBounded(peerID, acquisitionPeerStart) {
			continue
		}
		if ledger.State() == inbound.StateWantBase {
			r.requestLedgerBaseFromPeer(ledger, peerID, "failed to request ledger base from added peer")
			continue
		}
		if r.submitAcquisitionWork(ledger, acquisitionWorkEvent{kind: acquisitionWorkAdded, peerID: peerID}) {
			continue
		}
		ledger.RemovePeer(peerID)
	}
}

// cachedManifestFrames returns the encoded TMManifests frames for the
// current state of the manifest cache, building it on demand and
// reusing it across calls until the cache's Sequence advances — so a
// burst of post-handshake emissions reuses the same encoded bytes
// instead of re-walking the cache per peer.
//
// Returns nil when the cache is unwired, empty, or fails to encode.
// Encode failures are NOT cached so a transient error doesn't pin a
// stale frame; the next caller re-attempts.
func (r *Router) cachedManifestFrames() [][]byte {
	if r.manifests == nil {
		return nil
	}

	// Read sequence outside the frame lock so we never nest the cache's
	// RLock under our own mutex. A racing increment between this read
	// and the lock acquisition just causes the next caller to rebuild —
	// not a correctness issue.
	seq := r.manifests.Sequence()

	r.manifestFrameMu.Lock()
	defer r.manifestFrameMu.Unlock()

	if r.manifestFrameBuilt && r.manifestFrameSeq == seq {
		return r.manifestFrames
	}

	wires := r.manifests.SerializedAll()
	if len(wires) == 0 {
		// Empty cache — cache that fact too so the next call doesn't
		// re-walk byMaster only to find it still empty.
		r.manifestFrames = nil
		r.manifestFrameSeq = seq
		r.manifestFrameBuilt = true
		return nil
	}

	frames, err := encodeManifestFrames(wires...)
	if err != nil {
		r.logger.Warn("failed to encode local manifest frame", "error", err)
		return nil
	}

	r.manifestFrames = frames
	r.manifestFrameSeq = seq
	r.manifestFrameBuilt = true
	return frames
}
