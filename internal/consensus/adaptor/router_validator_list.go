package adaptor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
)

// handleValidatorListCollection ingests a TMValidatorListCollection
// frame, applying each blob individually with the collection's
// shared publisher manifest. When at least one blob relays the frame
// is rebroadcast to other peers.
//
// Bad-data attribution uses the worst per-blob disposition — a
// collection with one Invalid blob and several Accepted blobs gets the
// peer charged once for vl-coll-invalid rather than several times.
func (r *Router) handleValidatorListCollection(msg *peermanagement.InboundMessage) {
	if r.validatorList == nil {
		return
	}

	decoded, err := message.Decode(message.TypeValidatorListCollection, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode TMValidatorListCollection", "error", err, "peer", msg.PeerID)
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "vl-coll-decode")
		return
	}
	coll, ok := decoded.(*message.ValidatorListCollection)
	if !ok || coll == nil {
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "vl-coll-decode")
		return
	}

	// Reject v1 collections upfront ("wrong version"). Decoding once and
	// inspecting the version on the decoded message avoids a double-decode.
	if coll.Version < 2 {
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "vl-coll-wrong-version")
		return
	}

	if len(coll.Blobs) == 0 {
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "vl-coll-no-blobs")
		return
	}

	if r.messageSeen != nil {
		hash := sha512half.Sum(validatorListCollectionSemanticHash(coll))
		if firstSeen, _ := r.messageSeen.observe(hash); !firstSeen {
			r.messageSeen.recordPeer(hash, uint64(msg.PeerID))
			r.gossip.IncPeerBadData(uint64(msg.PeerID), "vl-coll-duplicate")
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

	// Record per-peer sequence using the highest blob sequence observed
	// across the collection.
	if pubKey != (validatorlist.PublisherKey{}) && maxSeq > 0 && anyRelay {
		r.validatorList.RecordPeerSequence(uint64(msg.PeerID), pubKey, maxSeq)
	}

	if anyRelay && pubKey != (validatorlist.PublisherKey{}) {
		r.validatorList.BroadcastLatest(pubKey, uint64(msg.PeerID))
	}
}

// validatorListCollectionSemanticHash is the collection counterpart of
// the message-seen cache's local-only dedup contract. Per-blob
// fields are concatenated in the order the collection presents them —
// that order is also what ApplyCollection iterates, so
// semantically-identical collections hash the same within this process.
func validatorListCollectionSemanticHash(coll *message.ValidatorListCollection) []byte {
	out := make([]byte, 0, 4+len(coll.Manifest)+64*len(coll.Blobs))
	out = appendUint32BE(out, coll.Version)
	out = appendLengthPrefixed(out, coll.Manifest)
	for _, b := range coll.Blobs {
		if b.HasManifest() {
			out = append(out, 1)
		} else {
			out = append(out, 0)
		}
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

// chargePeerForDisposition maps a Disposition's fee tier
// (Disposition.Charge) into a distinct IncPeerBadData label so operators
// get per-tier metrics:
//
//	useless data      -> "<prefix>-useless-<disposition>"
//	invalid data      -> "<prefix>-baddata-<disposition>"
//	invalid signature -> "<prefix>-badsig-<disposition>"
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
	r.gossip.IncPeerBadData(uint64(peer), prefix+"-"+tag+"-"+d.String())
}

// peerSite formats a peer-sourced site URI for the aggregator's
// per-publisher SiteURI field — emits a "host:port" string when the
// overlay is available, falling back to "peer:<id>" for tests or
// transient peer lookups.
func (r *Router) peerSite(peerID peermanagement.PeerID) string {
	if r.overlay != nil {
		if addr := r.overlay.PeerRemoteAddr(peerID); addr != "" {
			return addr
		}
	}
	return "peer:" + strconv.FormatUint(uint64(peerID), 10)
}

// routerBroadcaster is the concrete validatorlist.PeerBroadcaster
// adapter that bridges the aggregator to the overlay + frame codec.
// One instance lives for the lifetime of the router; the aggregator
// holds a reference (set via SetBroadcaster in Components bootstrap).
//
// All methods are safe for concurrent use. Returns are non-fatal — the
// aggregator logs and continues with the remaining peers.
type peerFrameSender interface {
	SendToPeer(peerID uint64, frame []byte) error
}

type routerBroadcaster struct {
	overlay *peermanagement.Overlay
	sender  peerFrameSender
	// maxCollectionFrameSize is a test seam for exercising rippled's
	// deterministic collection splitting without allocating 64 MB frames.
	// Zero uses the protocol maximum.
	maxCollectionFrameSize int
	// suppression is the optional shared hash registry. When wired,
	// SendCollection records each (hash, peer) pair after a
	// successful send so future inbound from that peer with the same
	// hash maps to a "known sender" path and the broadcast loop can
	// skip peers already known to have the content.
	suppression *messageSuppression
}

var _ validatorlist.PeerBroadcaster = (*routerBroadcaster)(nil)

// newValidatorListBroadcaster constructs a routerBroadcaster bound to
// the Router's suppression registry so SendCollection stamps
// the hash→peer association.
func (r *Router) newValidatorListBroadcaster(overlay *peermanagement.Overlay, sender peerFrameSender) *routerBroadcaster {
	return &routerBroadcaster{overlay: overlay, sender: sender, suppression: r.messageSeen}
}

// ActivePeers implements validatorlist.PeerBroadcaster.
func (b *routerBroadcaster) ActivePeers() []uint64 {
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

// SendCollection implements validatorlist.PeerBroadcaster. Encodes a
// TMValidatorListCollection carrying the publisher manifest plus the
// supplied (per-blob manifest, blob, signature) tuples and delivers
// it to peerID (single-entry collection when the publisher has no
// Remaining blobs, multi-entry when it does).
func (b *routerBroadcaster) SendCollection(peerID uint64, manifestBytes []byte, blobs []validatorlist.BroadcastBlob, version uint32) error {
	if b == nil || b.sender == nil {
		return fmt.Errorf("router broadcaster: nil sender")
	}
	maxSize := b.maxCollectionFrameSize
	if maxSize <= 0 || maxSize > message.MaxMessageSize {
		maxSize = message.MaxMessageSize
	}
	frames, err := buildValidatorListCollectionFrames(manifestBytes, blobs, version, maxSize)
	if err != nil {
		return err
	}
	for _, outgoing := range frames {
		if b.suppression != nil && b.suppression.peerHasHash(outgoing.hash, peerID) {
			continue
		}
		if err := b.sender.SendToPeer(peerID, outgoing.frame); err != nil {
			return err
		}
		if b.suppression != nil {
			b.suppression.recordPeer(outgoing.hash, peerID)
		}
	}
	return nil
}

type validatorListFrame struct {
	frame []byte
	hash  [32]byte
}

func buildValidatorListCollectionFrames(manifestBytes []byte, blobs []validatorlist.BroadcastBlob, version uint32, maxSize int) ([]validatorListFrame, error) {
	if len(blobs) == 0 {
		return nil, errors.New("validator list collection requires at least one blob")
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
	return splitValidatorListCollection(coll, maxSize, 0, len(coll.Blobs))
}

func splitValidatorListCollection(coll *message.ValidatorListCollection, maxSize, begin, end int) ([]validatorListFrame, error) {
	part := &message.ValidatorListCollection{
		Version:  coll.Version,
		Manifest: coll.Manifest,
		Blobs:    append([]message.ValidatorBlobInfo(nil), coll.Blobs[begin:end]...),
	}
	frame, err := message.EncodeFrame(part)
	if err == nil && len(frame) <= maxSize {
		return []validatorListFrame{{
			frame: frame,
			hash:  sha512half.Sum(validatorListCollectionSemanticHash(part)),
		}}, nil
	}
	if err != nil && !errors.Is(err, message.ErrMessageTooLarge) {
		return nil, fmt.Errorf("encode TMValidatorListCollection: %w", err)
	}
	if begin == end {
		if err != nil {
			return nil, fmt.Errorf("encode TMValidatorListCollection: empty collection exceeds message limit: %w", err)
		}
		return nil, fmt.Errorf("encode TMValidatorListCollection: empty collection frame is %d bytes, limit %d", len(frame), maxSize)
	}
	if end-begin == 1 {
		// A single blob cannot be split further. The protocol maximum is
		// enforced by EncodeFrame; maxSize is only a batching hint.
		if err != nil {
			return nil, fmt.Errorf("encode TMValidatorListCollection: %w", err)
		}
		return []validatorListFrame{{
			frame: frame,
			hash:  sha512half.Sum(validatorListCollectionSemanticHash(part)),
		}}, nil
	}
	mid := (begin + end) / 2
	left, err := splitValidatorListCollection(coll, maxSize, begin, mid)
	if err != nil {
		return nil, err
	}
	right, err := splitValidatorListCollection(coll, maxSize, mid, end)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}
