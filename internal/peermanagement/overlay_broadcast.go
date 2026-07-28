package peermanagement

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// RelayedIndexTTL matches rippled's default HashRouter hold time.
const RelayedIndexTTL = 300 * time.Second

// RelayedIndexMaxEntries caps inbound source tracking under adversarial
// traffic.
const RelayedIndexMaxEntries = 65_536

// relayedEntry holds peers that delivered a message to us. Outbound
// recipients are never added.
type relayedEntry struct {
	peers     map[PeerID]struct{}
	seenAt    time.Time
	relayedAt time.Time
}

const fanoutLogInterval = time.Second

type connectedPeer struct {
	id   PeerID
	peer *Peer
}

type fanoutResult struct {
	operation string
	attempted int
	failed    int
	critical  int
	causes    []error
}

func (r *fanoutResult) record(err error) {
	r.attempted++
	if err == nil {
		return
	}
	r.failed++
	r.causes = append(r.causes, err)
	if errors.Is(err, ErrCriticalSendQueueFull) {
		r.critical++
	}
}

func (r *fanoutResult) err() error {
	if r.failed == 0 {
		return nil
	}
	return &FanoutError{
		Operation: r.operation,
		Attempted: r.attempted,
		Failed:    r.failed,
		Critical:  r.critical,
		Err:       errors.Join(r.causes...),
	}
}

func (o *Overlay) connectedPeers() []connectedPeer {
	o.peersMu.RLock()
	peers := make([]connectedPeer, 0, len(o.peers))
	for id, peer := range o.peers {
		if peer.State() == PeerStateConnected {
			peers = append(peers, connectedPeer{id: id, peer: peer})
		}
	}
	o.peersMu.RUnlock()
	return peers
}

func sendPeer(peer *Peer, msg []byte, priority bool) error {
	if priority {
		return peer.SendPriority(msg)
	}
	return peer.Send(msg)
}

func (o *Overlay) logFanoutFailure(err error) {
	if err == nil {
		return
	}
	var summary *FanoutError
	if !errors.As(err, &summary) {
		return
	}

	now := time.Now()
	o.fanoutLogMu.Lock()
	if summary.Critical == 0 &&
		!o.fanoutLogLast.IsZero() &&
		now.Sub(o.fanoutLogLast) < fanoutLogInterval {
		o.fanoutLogSuppressed += summary.Failed
		o.fanoutLogMu.Unlock()
		return
	}
	suppressed := o.fanoutLogSuppressed
	o.fanoutLogSuppressed = 0
	o.fanoutLogLast = now
	o.fanoutLogMu.Unlock()

	level := slog.LevelInfo
	if errors.Is(summary, ErrSendBufferFull) {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, summary.Operation+" fanout enqueue failures",
		"t", "Overlay",
		"attempted", summary.Attempted,
		"failed", summary.Failed,
		"critical", summary.Critical,
		"suppressed_since_last_log", suppressed,
		"err", summary.Err,
	)
}

func (o *Overlay) forEachConnected(msg []byte, operation string, priority bool, skip func(PeerID, *Peer) bool) error {
	result := fanoutResult{operation: operation}
	for _, target := range o.connectedPeers() {
		if skip != nil && skip(target.id, target.peer) {
			continue
		}
		result.record(sendPeer(target.peer, msg, priority))
	}
	err := result.err()
	o.logFanoutFailure(err)
	return err
}

// Broadcast sends a message to all connected peers, unfiltered. Used
// for SELF-originated validator traffic (our own proposals and
// validations) and for non-validator messages (statusChange, etc.).
// The squelch filter is deliberately skipped for self-originated
// broadcasts; otherwise a peer that squelches our own pubkey would
// silence us to them.
//
// For peer-originated validator messages that need to be gossip-
// forwarded, use RelayFromValidator which applies the squelch filter
// and excludes the originating peer.
func (o *Overlay) Broadcast(msg []byte) error {
	return o.forEachConnected(msg, "broadcast", false, nil)
}

// BroadcastManifestFrames schedules one paced manifest sequence for every
// connected peer. Each peer owns its sequence lifecycle, so a slow peer cannot
// consume the ordinary queue one chunk at a time during this fan-out.
func (o *Overlay) BroadcastManifestFrames(frames [][]byte) error {
	return o.BroadcastManifestFramesExcept(0, frames)
}

func (o *Overlay) BroadcastManifestFramesExcept(exceptPeer PeerID, frames [][]byte) error {
	result := fanoutResult{operation: "broadcast-manifests"}
	for _, target := range o.connectedPeers() {
		if exceptPeer != 0 && target.id == exceptPeer {
			continue
		}
		result.record(target.peer.SendManifestFrames(frames))
	}
	err := result.err()
	o.logFanoutFailure(err)
	return err
}

// BroadcastPriority admits acquisition traffic to each peer's protected share
// of the reliable FIFO.
func (o *Overlay) BroadcastPriority(msg []byte) error {
	return o.forEachConnected(msg, "broadcast-priority", true, nil)
}

// BroadcastExcept sends a message to every connected peer except the
// one identified by exceptPeer. The per-validator squelch filter in
// RelayFromValidator doesn't apply. Pass 0 for exceptPeer to fall through
// to a plain Broadcast.
func (o *Overlay) BroadcastExcept(exceptPeer PeerID, msg []byte) error {
	return o.forEachConnected(msg, "broadcast-except", false, func(id PeerID, _ *Peer) bool {
		return id == exceptPeer
	})
}

// BroadcastExceptSet sends a message to every connected peer whose
// ID is not present in excluded. Used by tx-set acquire to skip peers
// that have repeatedly returned non-progressing TMLedgerData responses.
// This is a go-xrpl-specific outbound filter; rippled does NOT remove
// such peers from its peer set — it charges them and lets the global
// resource manager throttle them, so the peer stays eligible for the
// next broadcast. go-xrpl has no equivalent per-message resource accounting
// today, hence the explicit per-acquire exclusion. A nil or empty
// excluded map falls through to a plain Broadcast. Issue #420.
//
// Issue #724: the exclusion must never starve the broadcast. If every
// connected peer is excluded, the message would reach no one and the
// caller (tx-set missing-node acquisition) wedges in wrongLedger until
// the TTL sweep — the recurring under-load validation stall. When that
// happens, fall back to broadcasting to all connected peers, restoring
// rippled's "peer stays eligible for the next request" semantics rather
// than dropping the request on the floor.
func (o *Overlay) BroadcastExceptSet(excluded map[PeerID]bool, msg []byte) error {
	return o.broadcastExceptSet(excluded, msg, false)
}

// BroadcastPriorityExceptSet is BroadcastExceptSet using the independent
// acquisition and control queue.
func (o *Overlay) BroadcastPriorityExceptSet(excluded map[PeerID]bool, msg []byte) error {
	return o.broadcastExceptSet(excluded, msg, true)
}

func (o *Overlay) broadcastExceptSet(excluded map[PeerID]bool, msg []byte, priority bool) error {
	if len(excluded) == 0 {
		if priority {
			return o.BroadcastPriority(msg)
		}
		return o.Broadcast(msg)
	}
	peers := o.connectedPeers()
	connected, eligible := 0, 0
	for _, target := range peers {
		connected++
		if !excluded[target.id] {
			eligible++
		}
	}
	// #724: if excluding would reach no one, ignore the exclusion entirely
	// rather than dropping the request on the floor.
	ignoreExclusion := eligible == 0 && connected > 0

	result := fanoutResult{operation: "broadcast-except-set"}
	for _, target := range peers {
		if !ignoreExclusion && excluded[target.id] {
			continue
		}
		result.record(sendPeer(target.peer, msg, priority))
	}
	err := result.err()
	o.logFanoutFailure(err)
	return err
}

// RelayFromValidator forwards a peer-originated validator message
// (proposal or validation) to other connected peers, applying the
// per-peer squelch filter on the ORIGINATING validator's pubkey AND
// excluding the originating peer (exceptPeer). Pass 0 for exceptPeer
// when no peer should be excluded (e.g. tests that synthesize a relay).
//
// suppressionHash is the consensus-router suppression key for this
// message. Peers that previously delivered the same hash are atomically
// released from the inbound source index and excluded from this relay.
//
// The squelch is consulted before each outbound send and expired
// squelches auto-clear via Peer.ExpireSquelch. Self-origin is handled
// by a separate code path (see Broadcast) that skips the filter
// entirely.
func (o *Overlay) RelayFromValidator(validator []byte, suppressionHash [32]byte, exceptPeer PeerID, msg []byte) error {
	sources := o.releaseMessageSources(suppressionHash)
	sourceSet := make(map[PeerID]struct{}, len(sources))
	for _, id := range sources {
		sourceSet[id] = struct{}{}
	}

	err := o.forEachConnected(msg, "relay-from-validator", false, func(id PeerID, peer *Peer) bool {
		_, wasSource := sourceSet[id]
		return wasSource || id == exceptPeer || !peer.ExpireSquelch(validator)
	})
	for _, id := range sources {
		o.OnValidatorMessage(validator, id)
	}
	return err
}

// RecordMessageSource records an inbound peer as a source of the message.
// It must be called for every arrival, including duplicates.
func (o *Overlay) RecordMessageSource(suppressionHash [32]byte, peerID PeerID) {
	if o.relayedIndex == nil {
		return
	}
	clock := o.clockForIndex
	if clock == nil {
		clock = time.Now
	}
	now := clock()

	o.relayedIndexMu.Lock()
	defer o.relayedIndexMu.Unlock()

	entry, ok := o.relayedIndex[suppressionHash]
	if ok && now.Sub(entry.seenAt) >= RelayedIndexTTL {
		delete(o.relayedIndex, suppressionHash)
		entry = nil
		ok = false
	}

	if !ok && len(o.relayedIndex) >= RelayedIndexMaxEntries {
		cutoff := now.Add(-RelayedIndexTTL)
		for h, e := range o.relayedIndex {
			if e.seenAt.Before(cutoff) {
				delete(o.relayedIndex, h)
			}
		}
		if len(o.relayedIndex) >= RelayedIndexMaxEntries {
			i := 0
			for h := range o.relayedIndex {
				if i >= RelayedIndexMaxEntries/2 {
					break
				}
				delete(o.relayedIndex, h)
				i++
			}
		}
	}

	if !ok {
		entry = &relayedEntry{peers: make(map[PeerID]struct{})}
		o.relayedIndex[suppressionHash] = entry
	}
	entry.peers[peerID] = struct{}{}
	entry.seenAt = now
}

// PeersThatHave returns the set of peer IDs known to have the message
// whose suppression-hash is suppressionHash. The set contains only
// inbound sources and expires after RelayedIndexTTL.
//
// Returns nil when the hash is unknown or the bucket has aged out —
// callers treat both equivalently (nothing to feed the slot with
// beyond the current originPeer).
//
// Thread-safe. The returned slice is a private copy the caller may
// mutate freely.
func (o *Overlay) PeersThatHave(suppressionHash [32]byte) []PeerID {
	return o.messageSources(suppressionHash, false)
}

func (o *Overlay) releaseMessageSources(suppressionHash [32]byte) []PeerID {
	return o.messageSources(suppressionHash, true)
}

// MessageRelayedRecently reports whether this hash was relayed within the
// reduce-relay duplicate-counting window.
func (o *Overlay) MessageRelayedRecently(suppressionHash [32]byte) bool {
	if o.relayedIndex == nil {
		return false
	}
	clock := o.clockForIndex
	if clock == nil {
		clock = time.Now
	}
	now := clock()
	o.relayedIndexMu.Lock()
	defer o.relayedIndexMu.Unlock()
	entry, ok := o.relayedIndex[suppressionHash]
	if !ok || now.Sub(entry.seenAt) >= RelayedIndexTTL {
		if ok {
			delete(o.relayedIndex, suppressionHash)
		}
		return false
	}
	return !entry.relayedAt.IsZero() && now.Sub(entry.relayedAt) < Idled
}

func (o *Overlay) messageSources(suppressionHash [32]byte, release bool) []PeerID {
	if o.relayedIndex == nil {
		return nil
	}
	clock := o.clockForIndex
	if clock == nil {
		clock = time.Now
	}

	o.relayedIndexMu.Lock()
	defer o.relayedIndexMu.Unlock()

	entry, ok := o.relayedIndex[suppressionHash]
	if !ok {
		return nil
	}
	// Lazy-expire: if the bucket is older than TTL, drop it and report
	// "unknown". Keeps queries from returning stale peers after the
	// dedup window has elapsed (which would feed the slot with
	// counters the rest of the network would have dropped long ago).
	now := clock()
	if now.Sub(entry.seenAt) >= RelayedIndexTTL {
		delete(o.relayedIndex, suppressionHash)
		return nil
	}

	out := make([]PeerID, 0, len(entry.peers))
	for id := range entry.peers {
		out = append(out, id)
	}
	if release {
		entry.peers = make(map[PeerID]struct{})
		entry.relayedAt = now
		entry.seenAt = now
	}
	return out
}
