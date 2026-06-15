package peermanagement

import (
	"encoding/json"
	"log/slog"
	"net"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
)

// redirectEndpointCount caps the alternate addresses handed back in a
// slot-full 503 redirect. Matches rippled's
// peerfinder/detail/Tuning.h Tuning::redirectEndpointCount.
const redirectEndpointCount = 10

// redirectBodyLimit bounds how much of a non-101 handshake response body
// we read before parsing it for redirect endpoints. The peer-ips array
// is at most redirectEndpointCount "ip:port" strings, so a few KB is
// ample and caps a hostile peer's reply.
const redirectBodyLimit = 8 << 10

// writeInboundRedirect replies to a handshaked-but-unadmittable dialer
// with a 503 carrying alternate peer addresses before the caller closes
// the connection, mirroring rippled's OverlayImpl::makeRedirectResponse.
// Best-effort: a write failure is irrelevant since we are closing the
// connection regardless.
func (o *Overlay) writeInboundRedirect(tlsConn peertls.PeerConn) {
	dialer := tcpRemoteIP(tlsConn)
	peerIPs := o.collectRedirectEndpoints(dialer)

	var remoteAddr string
	if dialer != nil {
		remoteAddr = dialer.String()
	}

	resp := BuildRedirectResponse(o.cfg.UserAgent, remoteAddr, peerIPs)
	_ = resp.Write(tlsConn)
}

// collectRedirectEndpoints selects up to redirectEndpointCount alternate
// addresses for a 503 redirect, drawn from the discovered gossip set.
// Mirrors rippled's RedirectHandouts selection: only literal IP
// endpoints, never the dialer's own address, deduplicated by IP
// (port ignored), bounded by the count cap. Discovery's map iteration
// order supplies the shuffle rippled applies to its livecache.
func (o *Overlay) collectRedirectEndpoints(dialer net.IP) []string {
	out := make([]string, 0, redirectEndpointCount)
	seen := make(map[string]struct{})

	for _, e := range o.collectDiscoveredEndpoints() {
		ep, err := ParseEndpoint(e.Endpoint)
		if err != nil {
			continue
		}
		ip := net.ParseIP(ep.Host)
		if ip == nil {
			continue
		}
		if dialer != nil && ip.Equal(dialer) {
			continue
		}
		key := ip.String()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		out = append(out, e.Endpoint)
		if len(out) >= redirectEndpointCount {
			break
		}
	}

	return out
}

// ingestRedirect parses a 503 handshake-rejection body for its peer-ips
// array and hands the addresses to the dialer's onRedirect callback so a
// peer that could not admit us still points us at alternates. A missing
// callback, empty body, or malformed JSON is a no-op.
func (p *Peer) ingestRedirect(body []byte) {
	if p.onRedirect == nil || len(body) == 0 {
		return
	}
	var payload struct {
		PeerIPs []string `json:"peer-ips"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}
	if len(payload.PeerIPs) == 0 {
		return
	}
	p.onRedirect(payload.PeerIPs)
}

// ingestRedirectEndpoints feeds the addresses from a peer's 503 redirect
// into Discovery so a dialer we could not connect to actually bootstraps
// from the alternates. Mirrors rippled's ConnectAttempt::processResponse
// handing peer-ips to PeerFinder::onRedirects. Each entry is recorded as
// a one-hop candidate; non-literal addresses are skipped.
func (o *Overlay) ingestRedirectEndpoints(peerIPs []string, source PeerID) {
	for _, addr := range peerIPs {
		ep, err := ParseEndpoint(addr)
		if err != nil || net.ParseIP(ep.Host) == nil {
			continue
		}
		o.discovery.AddPeer(addr, 1, source)
	}
	slog.Debug("Ingested redirect endpoints",
		"t", "Overlay", "source", source, "count", len(peerIPs))
}
