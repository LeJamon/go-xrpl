package peermanagement

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
)

// redirectEndpointCount caps the alternate addresses handed back in a
// slot-full 503 redirect. Matches rippled's
// peerfinder/detail/Tuning.h Tuning::redirectEndpointCount.
const redirectEndpointCount = 10

// maxRedirectIngest caps how many addresses we accept from a single 503
// redirect body. The emitter is untrusted, so we apply our own bound
// rather than relying on its count cap. Matches rippled's
// peerfinder/detail/Tuning.h Tuning::maxRedirects.
const maxRedirectIngest = 30

// redirectBodyLimit bounds how much of a non-101 handshake response body
// we read before parsing it for redirect endpoints. The peer-ips array
// is at most redirectEndpointCount "ip:port" strings, so a few KB is
// ample and caps a hostile peer's reply.
const redirectBodyLimit = 8 << 10

// errInboundRejected signals handleInbound that performInboundHandshake
// already emitted the appropriate rejection (a 503 redirect for a
// non-peer Connect-As or slot-full dialer, or a silent close for a
// duplicate) in lieu of the 101 upgrade. The handshake itself did not
// fail, so the connection is closed without an EventPeerFailed event.
var errInboundRejected = errors.New("inbound connection rejected before upgrade")

// connectAsIncludesPeer reports whether a Connect-As header advertises
// the "peer" role. rippled splits the header on commas and admits the
// connection only when some token case-insensitively equals "peer";
// anything else (a crawler, an empty/absent header) is redirected.
func connectAsIncludesPeer(header string) bool {
	for _, tok := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "peer") {
			return true
		}
	}
	return false
}

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

	resp := BuildRedirectResponse(o.cfg.UserAgent, remoteAddr, peerIPs) //nolint:bodyclose // locally-built response serialized via Write; nothing to close
	_ = resp.Write(tlsConn)
}

// collectRedirectEndpoints selects up to redirectEndpointCount alternate
// addresses for a 503 redirect, drawn from the discovered gossip set.
// Mirrors rippled's RedirectHandouts selection: only literal IP
// endpoints, never the dialer's own address, deduplicated by IP
// (port ignored), bounded by the count cap. Selection order follows
// Go's randomized map iteration rather than rippled's explicit livecache
// shuffle; both yield a non-deterministic subset.
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

// ingestRedirectEndpoints files the addresses from a peer's 503 redirect
// as reconnect candidates so a dialer we could not connect to still
// bootstraps from the alternates. Mirrors rippled's
// ConnectAttempt::processResponse handing peer-ips to
// PeerFinder::onRedirects, which records them in the lower-trust boot
// cache (capped at Tuning::maxRedirects) rather than the live gossip set
// it re-advertises. The emitter is untrusted, so entries are bounded by
// maxRedirectIngest and non-literal addresses are skipped.
func (o *Overlay) ingestRedirectEndpoints(peerIPs []string, source PeerID) {
	accepted := 0
	for _, addr := range peerIPs {
		if accepted >= maxRedirectIngest {
			break
		}
		ep, err := ParseEndpoint(addr)
		if err != nil || net.ParseIP(ep.Host) == nil {
			continue
		}
		o.discovery.AddRedirectCandidate(addr, source)
		accepted++
	}
	slog.Debug("Ingested redirect endpoints",
		"t", "Overlay", "source", source, "count", accepted)
}
