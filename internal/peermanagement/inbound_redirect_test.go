package peermanagement

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redirectBody decodes a 503 redirect response body into its peer-ips.
func redirectBody(t *testing.T, resp *http.Response) []string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var payload struct {
		PeerIPs []string `json:"peer-ips"`
	}
	require.NoError(t, json.Unmarshal(raw, &payload))
	return payload.PeerIPs
}

// TestBuildRedirectResponse verifies the slot-full 503 carries the
// rippled-shaped headers and a peer-ips JSON body. Mirrors rippled's
// OverlayImpl::makeRedirectResponse.
func TestBuildRedirectResponse(t *testing.T) {
	ips := []string{"192.0.2.1:51235", "203.0.113.5:51235"}
	resp := BuildRedirectResponse("go-xrpl/test", "198.51.100.9", ips)
	require.NotNil(t, resp)

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "go-xrpl/test", resp.Header.Get(HeaderServer))
	assert.Equal(t, "198.51.100.9", resp.Header.Get("Remote-Address"))
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.Equal(t, "close", resp.Header.Get(HeaderConnection))

	assert.Equal(t, ips, redirectBody(t, resp))
}

// An empty redirect set still serializes as [] (not null), matching
// rippled's Json::arrayValue default.
func TestBuildRedirectResponse_EmptyList(t *testing.T) {
	resp := BuildRedirectResponse("", "", nil)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Empty(t, resp.Header.Get(HeaderServer))
	assert.Empty(t, resp.Header.Get("Remote-Address"))
	assert.Equal(t, []string{}, redirectBody(t, resp))
}

// TestCollectRedirectEndpoints mirrors rippled's RedirectHandouts
// selection: the dialer's own address is excluded, addresses are
// deduplicated by IP (port ignored), and the list is capped at
// redirectEndpointCount.
func TestCollectRedirectEndpoints(t *testing.T) {
	o := &Overlay{discovery: &Discovery{peers: make(map[string]*DiscoveredPeer)}}

	dialer := net.ParseIP("192.0.2.50")

	// The dialer's own address — must never be handed back to it.
	o.discovery.AddPeer("192.0.2.50:51235", 1, 0)
	// Two entries sharing an IP — only one survives the IP dedup.
	o.discovery.AddPeer("203.0.113.7:51235", 1, 0)
	o.discovery.AddPeer("203.0.113.7:3000", 1, 0)
	// A non-IP entry is not redirectable.
	o.discovery.AddPeer("example.com:51235", 1, 0)
	o.discovery.AddPeer("198.51.100.1:51235", 1, 0)

	got := o.collectRedirectEndpoints(dialer)

	seenIP := make(map[string]struct{})
	for _, addr := range got {
		ep, err := ParseEndpoint(addr)
		require.NoError(t, err)
		ip := net.ParseIP(ep.Host)
		require.NotNil(t, ip, "redirect must only contain literal IPs")
		require.False(t, ip.Equal(dialer), "must not redirect dialer to itself")
		_, dup := seenIP[ip.String()]
		require.False(t, dup, "must not repeat an IP")
		seenIP[ip.String()] = struct{}{}
	}
	// 203.0.113.7 (deduped) + 198.51.100.1 — example.com and the dialer
	// are filtered out.
	assert.Len(t, got, 2)
}

func TestCollectRedirectEndpoints_Cap(t *testing.T) {
	o := &Overlay{discovery: &Discovery{peers: make(map[string]*DiscoveredPeer)}}
	for i := 0; i < redirectEndpointCount+5; i++ {
		o.discovery.AddPeer(net.JoinHostPort(
			net.IPv4(203, 0, 113, byte(i+1)).String(), "51235"), 1, 0)
	}
	assert.Len(t, o.collectRedirectEndpoints(nil), redirectEndpointCount)
}

// TestIngestRedirectEndpoints verifies a dialer feeds a peer's redirect
// addresses into Discovery as one-hop candidates, skipping non-literals.
func TestIngestRedirectEndpoints(t *testing.T) {
	o := &Overlay{discovery: &Discovery{peers: make(map[string]*DiscoveredPeer)}}

	o.ingestRedirectEndpoints([]string{
		"192.0.2.10:51235",
		"example.com:51235", // hostname → skipped
		"garbage",           // unparseable → skipped
		"203.0.113.5:51235",
	}, PeerID(7))

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 2)
	for _, addr := range []string{"192.0.2.10:51235", "203.0.113.5:51235"} {
		p, ok := o.discovery.peers[addr]
		require.True(t, ok, "expected %s ingested", addr)
		assert.Equal(t, uint32(1), p.Hops)
		assert.Equal(t, PeerID(7), p.Source)
	}
}

// TestConnectAsIncludesPeer mirrors rippled's onHandoff Connect-As gate:
// admit only when some comma-token case-insensitively equals "peer".
func TestConnectAsIncludesPeer(t *testing.T) {
	for header, want := range map[string]bool{
		"peer":          true,
		"Peer":          true,
		"PEER":          true,
		" peer ":        true,
		"crawler,peer":  true,
		"peer, crawler": true,
		"":              false,
		"crawler":       false,
		"peerish":       false,
	} {
		assert.Equalf(t, want, connectAsIncludesPeer(header), "Connect-As %q", header)
	}
}

// An untrusted emitter cannot make us record more than maxRedirectIngest
// addresses from a single 503 body, matching rippled's Tuning::maxRedirects.
func TestIngestRedirectEndpoints_Cap(t *testing.T) {
	o := &Overlay{discovery: &Discovery{peers: make(map[string]*DiscoveredPeer)}}
	addrs := make([]string, 0, maxRedirectIngest+10)
	for i := 0; i < maxRedirectIngest+10; i++ {
		addrs = append(addrs, net.JoinHostPort(
			net.IPv4(10, 0, byte(i/256), byte(i%256)).String(), "51235"))
	}
	o.ingestRedirectEndpoints(addrs, PeerID(1))

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	assert.Len(t, o.discovery.peers, maxRedirectIngest)
}

// A redirect address is filed in the lower-trust boot cache, not the live
// gossip set we re-advertise — matching rippled's onRedirects -> bootcache_.
func TestAddRedirectCandidate_BootCacheRouting(t *testing.T) {
	d := &Discovery{
		peers:     make(map[string]*DiscoveredPeer),
		bootCache: NewBootCache(t.TempDir()),
	}
	d.AddRedirectCandidate("192.0.2.20:51235", PeerID(3))

	d.mu.RLock()
	assert.Empty(t, d.peers, "redirect must not enter the gossip set when a boot cache exists")
	d.mu.RUnlock()

	eps := d.bootCache.GetEndpoints(10)
	require.Len(t, eps, 1)
	assert.Equal(t, "192.0.2.20:51235", eps[0].Address)
}

// Without a boot cache (no DataDir) a redirect falls back to the discovered
// set as a one-hop candidate so it stays usable for connection.
func TestAddRedirectCandidate_FallbackWhenNoBootCache(t *testing.T) {
	d := &Discovery{peers: make(map[string]*DiscoveredPeer)}
	d.AddRedirectCandidate("192.0.2.21:51235", PeerID(4))

	d.mu.RLock()
	defer d.mu.RUnlock()
	p, ok := d.peers["192.0.2.21:51235"]
	require.True(t, ok)
	assert.Equal(t, uint32(1), p.Hops)
	assert.Equal(t, PeerID(4), p.Source)
}

// TestPeerIngestRedirect verifies the dialer-side parse of a 503 body
// invokes onRedirect with the peer-ips, and is a safe no-op otherwise.
func TestPeerIngestRedirect(t *testing.T) {
	t.Run("valid body invokes callback", func(t *testing.T) {
		var got []string
		p := &Peer{onRedirect: func(ips []string) { got = ips }}
		p.ingestRedirect([]byte(`{"peer-ips":["192.0.2.1:51235","203.0.113.5:51235"]}`))
		assert.Equal(t, []string{"192.0.2.1:51235", "203.0.113.5:51235"}, got)
	})

	t.Run("nil callback does not panic", func(t *testing.T) {
		p := &Peer{}
		assert.NotPanics(t, func() {
			p.ingestRedirect([]byte(`{"peer-ips":["192.0.2.1:51235"]}`))
		})
	})

	for name, body := range map[string]string{
		"empty body":       "",
		"malformed json":   "{not json",
		"empty peer-ips":   `{"peer-ips":[]}`,
		"missing peer-ips": `{"other":1}`,
	} {
		t.Run(name+" is a no-op", func(t *testing.T) {
			called := false
			p := &Peer{onRedirect: func([]string) { called = true }}
			p.ingestRedirect([]byte(body))
			assert.False(t, called)
		})
	}
}
