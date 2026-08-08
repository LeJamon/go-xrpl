package list

import (
	"encoding/hex"
	"slices"
)

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

type broadcastEntry struct {
	sequence uint32
	blob     BroadcastBlob
}

// RecordPeerSequence remembers that peerID has at least sequence seq for
// pubKey. Lower sequences are ignored and zero is not a confirmed sequence.
func (a *Aggregator) RecordPeerSequence(peerID uint64, pubKey PublisherKey, seq uint32) {
	if seq == 0 {
		return
	}
	a.peerSeqMu.Lock()
	defer a.peerSeqMu.Unlock()
	m, ok := a.peerSeq[peerID]
	if !ok {
		m = make(map[PublisherKey]uint32)
		a.peerSeq[peerID] = m
	}
	if seq > m[pubKey] {
		m[pubKey] = seq
	}
}

// ForgetPeer drops every per-publisher sequence record for peerID.
func (a *Aggregator) ForgetPeer(peerID uint64) {
	a.peerSeqMu.Lock()
	defer a.peerSeqMu.Unlock()
	delete(a.peerSeq, peerID)
}

// PeerSequence returns the highest sequence known for peerID and pubKey.
func (a *Aggregator) PeerSequence(peerID uint64, pubKey PublisherKey) uint32 {
	a.peerSeqMu.Lock()
	defer a.peerSeqMu.Unlock()
	if m, ok := a.peerSeq[peerID]; ok {
		return m[pubKey]
	}
	return 0
}

// BroadcastLatest pushes the most recently accepted list for pubKey to every
// connected peer that negotiated validator-list propagation and is behind the
// publisher's current sequence. v2 peers receive only collection entries
// newer than their recorded sequence, while per-blob manifests and the
// collection-level manifest retain their distinct wire roles.
func (a *Aggregator) BroadcastLatest(pubKey PublisherKey, exceptPeer uint64) {
	a.broadcastLatest(pubKey, exceptPeer, 0)
}

func (a *Aggregator) broadcastLatest(pubKey PublisherKey, exceptPeer uint64, targetSequence uint32) {
	a.mu.Lock()
	bcaster := a.bcaster
	if bcaster == nil {
		a.mu.Unlock()
		return
	}
	s, ok := a.state[pubKey]
	if !ok || s.Sequence == 0 || s.Status == StatusRevoked ||
		len(s.RawManifest) == 0 || len(s.RawBlob) == 0 || len(s.RawSignature) == 0 {
		a.mu.Unlock()
		return
	}
	sequence := s.Sequence
	blobVersion := s.Version
	if blobVersion == 0 {
		blobVersion = 1
	}
	rawManifest := cloneWireBytes(s.RawManifest)
	listManifest := rawManifest
	if s.RawLocalManifestSet {
		listManifest = cloneOptionalWireBytes(s.RawLocalManifest, true)
	}
	rawBlob := cloneWireBytes(s.RawBlob)
	rawSignature := cloneWireBytes(s.RawSignature)
	entries := make([]broadcastEntry, 0, len(s.Remaining)+1)
	entries = append(entries, broadcastEntry{
		sequence: sequence,
		blob: BroadcastBlob{
			Manifest:  cloneOptionalWireBytes(s.RawLocalManifest, s.RawLocalManifestSet),
			Blob:      cloneWireBytes(s.RawBlob),
			Signature: cloneWireBytes(s.RawSignature),
		},
	})
	maxSeq := sequence
	if len(s.Remaining) > 0 {
		seqs := make([]uint32, 0, len(s.Remaining))
		for seq := range s.Remaining {
			seqs = append(seqs, seq)
		}
		slices.Sort(seqs)
		for _, seq := range seqs {
			rb := s.Remaining[seq]
			entries = append(entries, broadcastEntry{
				sequence: seq,
				blob: BroadcastBlob{
					Manifest:  cloneOptionalWireBytes(rb.RawLocalManifest, rb.RawLocalManifestSet),
					Blob:      cloneWireBytes(rb.RawBlob),
					Signature: cloneWireBytes(rb.RawSignature),
				},
			})
			if seq > maxSeq {
				maxSeq = seq
			}
		}
	}
	collVersion := max(blobVersion, 2)
	relaySequence := targetSequence
	if relaySequence == 0 {
		relaySequence = maxSeq
	}
	logger := a.logger
	a.mu.Unlock()

	active := bcaster.ActivePeers()
	sent := 0
	for _, peerID := range active {
		if peerID == exceptPeer {
			continue
		}
		if bcaster.PeerSupportsV2(peerID) {
			peerSequence := a.PeerSequence(peerID, pubKey)
			if peerSequence >= relaySequence {
				continue
			}
			peerBlobs := make([]BroadcastBlob, 0, len(entries))
			for _, entry := range entries {
				if peerSequence == 0 || entry.sequence > peerSequence {
					peerBlobs = append(peerBlobs, entry.blob)
				}
			}
			if len(peerBlobs) == 0 {
				continue
			}
			if err := bcaster.SendCollection(peerID, rawManifest, peerBlobs, collVersion); err != nil {
				logger.Debug("validator list collection broadcast: send failed",
					"peer", peerID,
					"publisher", hex.EncodeToString(pubKey[:]),
					"max_sequence", maxSeq,
					"error", err)
				continue
			}
			a.RecordPeerSequence(peerID, pubKey, relaySequence)
			sent++
			continue
		}
		if !bcaster.PeerSupportsVL(peerID) {
			continue
		}
		if a.PeerSequence(peerID, pubKey) >= sequence {
			continue
		}
		if err := bcaster.SendList(peerID, listManifest, rawBlob, rawSignature, supportedVersionV1); err != nil {
			logger.Debug("validator list broadcast: send failed",
				"peer", peerID,
				"publisher", hex.EncodeToString(pubKey[:]),
				"sequence", sequence,
				"error", err)
			continue
		}
		a.RecordPeerSequence(peerID, pubKey, sequence)
		sent++
	}

	if sent > 0 {
		logger.Debug("validator list broadcast",
			"publisher", hex.EncodeToString(pubKey[:]),
			"sequence", sequence,
			"remaining", len(entries)-1,
			"peers_sent", sent)
	}
}

func cloneWireBytes(raw []byte) []byte {
	if raw == nil {
		return nil
	}
	return append(make([]byte, 0, len(raw)), raw...)
}

func cloneOptionalWireBytes(raw []byte, present bool) []byte {
	if !present {
		return nil
	}
	if raw == nil {
		return []byte{}
	}
	return cloneWireBytes(raw)
}
