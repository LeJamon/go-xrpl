// Outbound TMEndpoints periodic emission. Mirrors rippled's
// OverlayImpl::sendEndpoints (OverlayImpl.cpp:1348-1364) which fires
// off the once-per-second Timer::on_timer hook. rippled rate-limits the
// outbound broadcast through PeerFinder::Logic::buildEndpointsForPeers
// at Tuning::secondsPerMessage=151s; goXRPL has no PeerFinder, so we
// drive the broadcast directly from the overlay maintenance loop at the
// same outer cadence. Closes the "no recurring outbound emitter" gap
// audited in issue #497.

package peermanagement

import (
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// endpointsBroadcastInterval matches rippled's
// peerfinder/detail/Tuning.h:124 Tuning::secondsPerMessage. The same
// constant gates a peer's willingness to accept a fresh endpoints
// frame from us (Logic.h:882 whenAcceptEndpoints), so emitting more
// often than this would have every peer charge us for over-eager
// gossip.
const endpointsBroadcastInterval = 151 * time.Second

// endpointsBroadcastMaxEntries caps a single outbound frame's
// endpoints_v2 length. Matches the inbound bound rippled enforces at
// PeerImp.cpp:1206 (1024) but with a tighter ceiling — we don't need
// to push that many in a single message and a smaller cap protects
// recipients from doing 1024 IP parses on a single packet.
const endpointsBroadcastMaxEntries = 32

// sendEndpoints emits a TMEndpoints frame to every connected peer.
// Each peer receives a fresh per-peer endpoint set so the recipient
// list never includes the recipient itself (rippled's
// PeerImp::sendEndpoints does the same — the iterator at
// OverlayImpl.cpp:1362 walks the per-slot handout from
// buildEndpointsForPeers, which excludes the slot's own address).
//
// We synthesize the handout from two sources:
//   - hops=0 self-entry: our (PublicIP, ListenAddr-port) when both are
//     configured. rippled emits this from PeerFinder's wantIncoming
//     branch (Logic.h:654-669) using an unspecified address that the
//     receiver overwrites with the socket remote — that fallback
//     works because the peer wire records the source address on
//     receive. We emit the explicit value when we have it, matching
//     the post-PublicIP-known path the same comment block describes
//     as "the correct solution";
//   - hops≥1 peers: known discovered endpoints with their last-seen
//     hop count, drawn from Discovery.
//
// Per-peer caps + recipient exclusion are applied before encoding so
// each Send carries only the bytes that peer can actually use.
func (o *Overlay) sendEndpoints() {
	// Build the candidate hops≥1 pool once. Discovery is read-locked
	// internally and the snapshot is small (capped at MaxCachedEndpoints
	// entries — see discovery.go), so re-doing the walk per peer would
	// re-acquire a lock for no benefit.
	hopsGreaterEntries := o.collectDiscoveredEndpoints()

	selfEntry, hasSelf := o.localEndpointForGossip()

	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}

		// Recipient address — used to filter the recipient out of
		// its own handout. Peer.Endpoint() is the connected remote.
		recipient := peer.Endpoint().String()

		eps := make([]message.Endpointv2, 0, 1+len(hopsGreaterEntries))
		if hasSelf {
			eps = append(eps, selfEntry)
		}
		for _, e := range hopsGreaterEntries {
			if e.Endpoint == recipient {
				continue
			}
			eps = append(eps, e)
			if len(eps) >= endpointsBroadcastMaxEntries {
				break
			}
		}

		if len(eps) == 0 {
			continue
		}

		frame, err := buildEndpointsFrame(eps)
		if err != nil {
			slog.Debug("sendEndpoints frame build failed",
				"t", "Overlay", "peer", id, "err", err)
			continue
		}
		if err := peer.Send(frame); err != nil {
			slog.Debug("sendEndpoints send failed",
				"t", "Overlay", "peer", id, "err", err)
		}
	}
}

// collectDiscoveredEndpoints walks Discovery.peers and returns a list of
// gossip-eligible endpoints, each tagged with the hop count we last
// observed from. Endpoints we believe ourselves to be currently
// connected to are still eligible — the recipient peer will dedup
// against its own slot.
func (o *Overlay) collectDiscoveredEndpoints() []message.Endpointv2 {
	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()

	out := make([]message.Endpointv2, 0, len(o.discovery.peers))
	for _, p := range o.discovery.peers {
		hops := p.Hops
		if hops == 0 {
			// Our discovered entry has no explicit hop count — assume
			// a single hop. The wire format treats hops==0 as a
			// self-claim, so we must not propagate it from a third
			// party (rippled's PeerImp.cpp:1234 overwrites hops==0
			// with the socket remote — sending hops==0 here would
			// trick the recipient into believing the address is ours).
			hops = 1
		}
		out = append(out, message.Endpointv2{
			Endpoint: p.Address,
			Hops:     hops,
		})
	}
	return out
}

// localEndpointForGossip returns the (host, port) string we should
// advertise as ourselves at hops=0, when we have both. Returns ok=false
// when either piece is missing — rippled relies on PeerFinder's
// wantIncoming flag to emit a placeholder, but we'd rather not gossip
// a stale or empty self-address than have peers cache an unreachable
// one. The recipient correctly interprets an absent hops=0 entry as
// "fall back to the socket remote" (PeerImp.cpp:1234-1235).
func (o *Overlay) localEndpointForGossip() (message.Endpointv2, bool) {
	if o.cfg.PublicIP == nil {
		return message.Endpointv2{}, false
	}
	port := listenPortFromAddr(o.cfg.ListenAddr)
	if port == "" {
		return message.Endpointv2{}, false
	}
	host := o.cfg.PublicIP.String()
	return message.Endpointv2{
		Endpoint: net.JoinHostPort(host, port),
		Hops:     0,
	}, true
}

// listenPortFromAddr extracts the port number from a ListenAddr like
// ":51235" or "0.0.0.0:51235". Returns "" when the input is empty or
// the SplitHostPort call fails — the caller falls back to suppressing
// the self-entry.
func listenPortFromAddr(addr string) string {
	if addr == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if _, convErr := strconv.Atoi(port); convErr != nil {
		return ""
	}
	return port
}

// buildEndpointsFrame encodes a single TMEndpoints frame at protocol
// version 2 (the only version rippled accepts inbound — see the gate at
// PeerImp.cpp:1201).
func buildEndpointsFrame(eps []message.Endpointv2) ([]byte, error) {
	msg := &message.Endpoints{
		Version:     2,
		EndpointsV2: eps,
	}
	encoded, err := message.Encode(msg)
	if err != nil {
		return nil, err
	}
	return message.BuildWireMessage(message.TypeEndpoints, encoded)
}
