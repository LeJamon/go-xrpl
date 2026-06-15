package peermanagement

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// RelayedIndexTTL bounds how long a suppression-key → peers entry is
// kept in the reverse index. Must match the consensus router's
// messageDedupTTL so that a hash remains queryable for as long as the
// router may observe duplicates for it. If the index expired before
// the dedup window, a duplicate hitting router.handleProposal could
// find no "peers that have the message" entry and under-feed the
// slot — the exact bug B3 was filed to fix.
const RelayedIndexTTL = 30 * time.Second

// RelayedIndexMaxEntries caps memory for the reverse index under
// adversarial traffic. Sized to match the adaptor's dedup cap so both
// age out together under sustained churn.
const RelayedIndexMaxEntries = 4096

// relayedEntry is one bucket in the reverse index — the set of peers
// we know "have" a given suppression-key, plus the last-update time
// for TTL reaping.
type relayedEntry struct {
	peers  map[PeerID]struct{}
	seenAt time.Time
}

// sendAndLog sends msg to peer and logs a failure under opName:
// ErrSendBufferFull at Warn (silent drops masked TMTransaction relay loss
// in #401), other failures at Info. Shared by the broadcast / relay
// fan-out loops below.
func (o *Overlay) sendAndLog(peer *Peer, msg []byte, opName string) {
	if err := peer.Send(msg); err != nil {
		level := slog.LevelInfo
		if errors.Is(err, ErrSendBufferFull) {
			level = slog.LevelWarn
		}
		slog.Log(context.Background(), level, opName+" send failed",
			"t", "Overlay",
			"peer", peer.ID(),
			"frame_size", len(msg),
			"err", err.Error(),
		)
	}
}

// forEachConnected sends msg to every connected peer for which skip
// returns false (skip nil = no filter), logging Send failures under
// opName. Holds peersMu.RLock for the iteration and returns the peer IDs
// the frame was handed to (best-effort: included even when the Send
// errored, matching the reverse-index contract). Extracts the
// send-and-log fan-out shared by Broadcast / BroadcastExcept /
// BroadcastExceptSet / RelayFromValidator.
func (o *Overlay) forEachConnected(msg []byte, opName string, skip func(PeerID, *Peer) bool) []PeerID {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	var sent []PeerID
	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
		if skip != nil && skip(id, peer) {
			continue
		}
		o.sendAndLog(peer, msg, opName)
		sent = append(sent, id)
	}
	return sent
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
	o.forEachConnected(msg, "broadcast", nil)
	return nil
}

// BroadcastExcept sends a message to every connected peer except the
// one identified by exceptPeer. Used for gossip of peer-originated
// messages that are NOT per-validator (manifests) — the per-validator
// squelch filter in RelayFromValidator doesn't apply. Pass 0 for
// exceptPeer to fall through to a plain Broadcast.
func (o *Overlay) BroadcastExcept(exceptPeer PeerID, msg []byte) error {
	o.forEachConnected(msg, "broadcast-except", func(id PeerID, _ *Peer) bool {
		return id == exceptPeer
	})
	return nil
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
	if len(excluded) == 0 {
		return o.Broadcast(msg)
	}
	// Hold peersMu for the whole pass so the #724 starvation decision and
	// the sends observe the same connected set: a peer joining or leaving
	// between a separate "all excluded?" scan and the send loop could
	// either starve the broadcast or skip a now-eligible peer. This can't
	// reuse forEachConnected (which takes its own lock), so the
	// scan-then-send is inlined under a single RLock.
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	connected, eligible := 0, 0
	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
		connected++
		if !excluded[id] {
			eligible++
		}
	}
	// #724: if excluding would reach no one, ignore the exclusion entirely
	// rather than dropping the request on the floor.
	ignoreExclusion := eligible == 0 && connected > 0

	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
		if !ignoreExclusion && excluded[id] {
			continue
		}
		o.sendAndLog(peer, msg, "broadcast-except-set")
	}
	return nil
}

// RelayFromValidator forwards a peer-originated validator message
// (proposal or validation) to other connected peers, applying the
// per-peer squelch filter on the ORIGINATING validator's pubkey AND
// excluding the originating peer (exceptPeer). Pass 0 for exceptPeer
// when no peer should be excluded (e.g. tests that synthesize a relay).
//
// suppressionHash is the consensus-router suppression key for this
// message (same [32]byte used by the dedup cache). Every peer we
// actually send to is recorded in the reverse index so a later
// duplicate arrival from ANOTHER peer can query
// Overlay.PeersThatHave(suppressionHash) and feed the reduce-relay
// slot with the full set of known-havers.
//
// The squelch is consulted before each outbound send and expired
// squelches auto-clear via Peer.ExpireSquelch. Self-origin is handled
// by a separate code path (see Broadcast) that skips the filter
// entirely.
func (o *Overlay) RelayFromValidator(validator []byte, suppressionHash [32]byte, exceptPeer PeerID, msg []byte) error {
	// forEachConnected returns the peers we forwarded to (best-effort,
	// including any whose Send errored). Record into the reverse index
	// AFTER it releases peersMu so we never nest index-mutex inside
	// peers-mutex.
	forwarded := o.forEachConnected(msg, "relay-from-validator", func(id PeerID, peer *Peer) bool {
		return id == exceptPeer || !peer.ExpireSquelch(validator)
	})

	if len(forwarded) > 0 {
		o.recordRelayedPeers(suppressionHash, forwarded)
	}
	return nil
}

// recordRelayedPeers adds peerIDs to the reverse-index bucket for
// suppressionHash, trimming expired buckets if we hit the size cap.
// Safe for concurrent callers.
func (o *Overlay) recordRelayedPeers(suppressionHash [32]byte, peerIDs []PeerID) {
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

	// Trim if we're at capacity. A cheap TTL sweep rather than a
	// formal LRU — the index is a cache, not a hot path.
	if len(o.relayedIndex) >= RelayedIndexMaxEntries {
		cutoff := now.Add(-RelayedIndexTTL)
		for h, e := range o.relayedIndex {
			if e.seenAt.Before(cutoff) {
				delete(o.relayedIndex, h)
			}
		}
		// If that didn't free enough space (adversarial churn), drop
		// half the map — bounded worst case, same shape as the
		// messageSuppression eviction in the consensus router.
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

	entry, ok := o.relayedIndex[suppressionHash]
	if !ok {
		entry = &relayedEntry{peers: make(map[PeerID]struct{})}
		o.relayedIndex[suppressionHash] = entry
	}
	for _, id := range peerIDs {
		entry.peers[id] = struct{}{}
	}
	entry.seenAt = now
}

// PeersThatHave returns the set of peer IDs known to have the message
// whose suppression-hash is `suppressionHash`. Entries are populated
// when we relay a validator message outward (RelayFromValidator) and
// expire after RelayedIndexTTL.
//
// Returns nil when the hash is unknown or the bucket has aged out —
// callers treat both equivalently (nothing to feed the slot with
// beyond the current originPeer).
//
// Thread-safe. The returned slice is a private copy the caller may
// mutate freely.
func (o *Overlay) PeersThatHave(suppressionHash [32]byte) []PeerID {
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
	if clock().Sub(entry.seenAt) >= RelayedIndexTTL {
		delete(o.relayedIndex, suppressionHash)
		return nil
	}

	out := make([]PeerID, 0, len(entry.peers))
	for id := range entry.peers {
		out = append(out, id)
	}
	return out
}
