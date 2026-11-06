package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func healthyPeerServerInfo() map[string]any {
	return map[string]any{
		"validated_ledger":   map[string]any{"age": 6},
		"peers":              8,
		"server_state":       "full",
		"load_factor_server": uint32(256),
		"load_base":          uint32(256),
	}
}

func TestPeerHTTPRoutesBeforeHandshake(t *testing.T) {
	for _, tc := range []struct {
		target string
		status int
	}{
		{"/crawl", http.StatusOK},
		{"/health", http.StatusInternalServerError},
		{"/vl/unknown", http.StatusNotFound},
		{"/vl/", http.StatusBadRequest},
	} {
		t.Run(tc.target, func(t *testing.T) {
			conn, _ := runRawInboundHandshakeRequest(t, []byte("GET "+tc.target+" HTTP/1.1\r\nHost: localhost\r\n\r\n"))
			resp, err := http.ReadResponse(bufio.NewReader(&conn.writes), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.status, resp.StatusCode)
			require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
			require.True(t, resp.Close)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.True(t, json.Valid(body), "body: %q", body)
			if tc.target == "/crawl" {
				require.True(t, bytes.Contains(body, []byte(`"version":2`)))
				require.True(t, bytes.Contains(body, []byte(`"active":[]`)))
			}
		})
	}
}

func TestPeerHTTPUpgradePrecedence(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, target := range []string{"/crawl", "/vl/key"} {
			o := newLifecycleTestOverlay(t, WithListenAddr(""), WithHTTPConfig(HTTPConfig{CrawlOverlay: enabled, ValidatorList: enabled}))
			remote, err := NewIdentity()
			require.NoError(t, err)
			req, err := BuildHandshakeRequest(remote, make([]byte, 32), DefaultHandshakeConfig())
			require.NoError(t, err)
			req.URL.Path = target
			req.Host = "localhost"
			var wire bytes.Buffer
			require.NoError(t, req.Write(&wire))
			conn := &deadlinePeerConn{data: bytes.NewReader(wire.Bytes())}
			peer := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, true, o.identity, nil)
			err = o.performInboundHandshake(context.Background(), peer, conn)
			if enabled {
				require.ErrorIs(t, err, errInboundHTTPHandled)
				require.NotContains(t, conn.writes.String(), "101 Switching Protocols")
			} else {
				require.NoError(t, err)
				require.Contains(t, conn.writes.String(), "101 Switching Protocols")
			}
		}
	}
}

func TestPeerHTTPHealth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		field  string
		value  any
		status int
		info   string
	}{
		{"healthy", "", nil, 200, `{}`},
		{"ledger warning boundary", "validated_ledger", map[string]any{"age": 7}, 503, `{"validated_ledger":7}`},
		{"ledger warning upper", "validated_ledger", map[string]any{"age": 19}, 503, `{"validated_ledger":19}`},
		{"ledger critical boundary", "validated_ledger", map[string]any{"age": 20}, 500, `{"validated_ledger":20}`},
		{"missing ledger", "validated_ledger", nil, 503, `{"validated_ledger":-1}`},
		{"negative ledger age", "validated_ledger", map[string]any{"age": -2}, 503, `{"validated_ledger":-2}`},
		{"blocked", "amendment_blocked", true, 500, `{"amendment_blocked":true}`},
		{"blocked field present", "amendment_blocked", false, 500, `{"amendment_blocked":true}`},
		{"peer warning boundary", "peers", 7, 503, `{"peers":7}`},
		{"one peer", "peers", 1, 503, `{"peers":1}`},
		{"no peers", "peers", 0, 500, `{"peers":0}`},
		{"validating", "server_state", "validating", 200, `{}`},
		{"proposing", "server_state", "proposing", 200, `{}`},
		{"syncing", "server_state", "syncing", 503, `{"server_state":"syncing"}`},
		{"tracking", "server_state", "tracking", 503, `{"server_state":"tracking"}`},
		{"connected", "server_state", "connected", 503, `{"server_state":"connected"}`},
		{"disconnected", "server_state", "disconnected", 500, `{"server_state":"disconnected"}`},
		{"load healthy boundary", "load_factor_server", 25600, 200, `{}`},
		{"load warning boundary", "load_factor_server", 25728, 503, `{"load_factor":100.5}`},
		{"load warning upper", "load_factor_server", 255744, 503, `{"load_factor":999}`},
		{"load critical boundary", "load_factor_server", 256000, 500, `{"load_factor":1000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := healthyPeerServerInfo()
			if tc.field != "" {
				if tc.value == nil {
					delete(server, tc.field)
				} else {
					server[tc.field] = tc.value
				}
			}
			status, body, err := healthResponse(server)
			require.NoError(t, err)
			require.Equal(t, tc.status, status)
			encoded, err := json.Marshal(body)
			require.NoError(t, err)
			require.JSONEq(t, `{"info":`+tc.info+`}`, string(encoded))
		})
	}
	t.Run("critical dominates warnings", func(t *testing.T) {
		server := healthyPeerServerInfo()
		server["validated_ledger"] = map[string]any{"age": 30}
		server["peers"] = 1
		server["server_state"] = "syncing"
		server["load_factor_server"] = 25728
		status, body, err := healthResponse(server)
		require.NoError(t, err)
		require.Equal(t, 500, status)
		require.Len(t, body["info"], 4)
	})
}

func TestPeerHTTPCrawlSectionsAndTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  HTTPConfig
		keys []string
	}{
		{"overlay", HTTPConfig{CrawlOverlay: true}, []string{"overlay"}},
		{"server", HTTPConfig{CrawlServer: true}, []string{"server"}},
		{"counts", HTTPConfig{CrawlCounts: true}, []string{"counts"}},
		{"unl", HTTPConfig{CrawlUNL: true}, []string{"unl"}},
		{"all", HTTPConfig{CrawlOverlay: true, CrawlServer: true, CrawlCounts: true, CrawlUNL: true}, []string{"overlay", "server", "counts", "unl"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &Overlay{cfg: Config{HTTP: tc.cfg}}
			called := make(map[string]bool)
			provider := func(name string) func() map[string]any {
				return func() map[string]any { called[name] = true; return map[string]any{"source": name} }
			}
			o.SetHTTPProviders(HTTPProviders{ServerInfo: provider("server"), Counts: provider("counts"), UNL: provider("unl")})
			for _, method := range []string{"GET", "POST", "HEAD"} {
				var wire bytes.Buffer
				handled, err := o.processHTTPRequest(&http.Request{Method: method, RequestURI: "/crawl", ProtoMajor: 1, ProtoMinor: 1}, &wire)
				require.True(t, handled)
				require.NoError(t, err)
				resp, err := http.ReadResponse(bufio.NewReader(&wire), nil)
				require.NoError(t, err)
				var body map[string]any
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
				require.NoError(t, resp.Body.Close())
				require.Len(t, body, len(tc.keys)+1)
				for _, key := range tc.keys {
					require.Contains(t, body, key)
				}
			}
			require.Equal(t, tc.cfg.CrawlServer, called["server"])
			require.Equal(t, tc.cfg.CrawlCounts, called["counts"])
			require.Equal(t, tc.cfg.CrawlUNL, called["unl"])
		})
	}
	for _, target := range []string{"/crawl?x=1", "/crawl/", "/%63rawl", "/health?x=1", "/health/", "/unknown"} {
		o := &Overlay{cfg: DefaultConfig()}
		var wire bytes.Buffer
		handled, err := o.processHTTPRequest(&http.Request{RequestURI: target, ProtoMajor: 1, ProtoMinor: 1}, &wire)
		require.NoError(t, err)
		require.True(t, handled, target)
		require.Contains(t, wire.String(), "403 Forbidden")
	}
	for _, target := range []string{"/crawl", "/vl/key"} {
		o := &Overlay{}
		var wire bytes.Buffer
		handled, err := o.processHTTPRequest(&http.Request{RequestURI: target, ProtoMajor: 1, ProtoMinor: 1}, &wire)
		require.NoError(t, err)
		require.True(t, handled, target)
		resp, err := http.ReadResponse(bufio.NewReader(&wire), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, "Forbidden\r\n", string(body))
	}
}

func TestPeerHTTPCrawlPrivacy(t *testing.T) {
	o := newLifecycleTestOverlay(t, WithListenAddr(""))
	for i, inbound := range []bool{false, true, true} {
		p := NewPeer(PeerID(i), Endpoint{Host: "192.0.2.1", Port: 12345}, inbound, o.identity, nil)
		key, err := ParsePublicKeyToken(o.identity.EncodedPublicKey())
		require.NoError(t, err)
		p.remotePubKey = key
		p.createdAt = time.Now().Add(-10 * time.Second)
		p.userAgent = "rippled-test"
		p.firstLedgerSeq, p.lastLedgerSeq = 100, 200
		p.crawlPublic = i != 2
		if i == 1 {
			p.listeningPort = 51235
		}
		o.peers[p.id] = p
	}
	active := o.crawlOverlay()["active"].([]map[string]any)
	require.Len(t, active, 3)
	for _, entry := range active {
		require.Equal(t, base64.StdEncoding.EncodeToString(o.identity.PublicKey()), entry["public_key"])
		require.Equal(t, "100-200", entry["complete_ledgers"])
		require.Equal(t, "rippled-test", entry["version"])
		require.GreaterOrEqual(t, entry["uptime"], uint32(10))
		if _, public := entry["ip"]; public {
			if entry["type"] == "out" {
				require.EqualValues(t, 12345, entry["port"])
			} else {
				require.EqualValues(t, 51235, entry["port"])
			}
		} else {
			require.NotContains(t, entry, "port")
		}
	}
	p := o.peers[1]
	p.listeningPort = 0
	p.userAgent = ""
	p.firstLedgerSeq, p.lastLedgerSeq = 0, 0
	o.peers = map[PeerID]*Peer{1: p}
	entry := o.crawlOverlay()["active"].([]map[string]any)[0]
	require.Contains(t, entry, "ip")
	require.NotContains(t, entry, "port")
	require.NotContains(t, entry, "version")
	require.NotContains(t, entry, "complete_ledgers")
}

func TestPeerHTTPValidatorListTarget(t *testing.T) {
	for _, tc := range []struct {
		target  string
		status  int
		key     string
		version uint32
	}{
		{"key", 200, "key", 1},
		{"1/key", 200, "key", 1},
		{"2/key", 200, "key", 2},
		{"+2/key", 200, "key", 2},
		{"02/key", 200, "key", 2},
		{"-4294967295/key", 200, "key", 1},
		{"-4294967294/key", 200, "key", 2},
		{"-1/missing", 404, "missing", ^uint32(0)},
		{"-0/key", 400, "key", 0},
		{"0/key", 400, "key", 0},
		{"3/key", 400, "key", 3},
		{"missing", 404, "missing", 1},
		{"3/missing", 404, "missing", 3},
		{"key?x=1", 404, "key?x=1", 1},
		{"2/key/extra", 404, "key/extra", 2},
		{"", 400, "", 0},
		{"1/", 400, "", 0},
		{"/key", 400, "", 0},
		{"version/key", 400, "", 0},
		{"4294967296/key", 400, "", 0},
		{"-4294967296/key", 400, "", 0},
		{"++1/key", 400, "", 0},
		{"--1/key", 400, "", 0},
		{" 1/key", 400, "", 0},
		{"1 /key", 400, "", 0},
	} {
		t.Run(tc.target, func(t *testing.T) {
			var gotKey string
			var gotVersion uint32
			status, result := validatorListResponse(tc.target, func(key string, version uint32) (map[string]any, bool) {
				gotKey, gotVersion = key, version
				if key != "key" {
					return nil, false
				}
				if version != 1 && version != 2 {
					return nil, true
				}
				return map[string]any{"version": version}, true
			})
			require.Equal(t, tc.status, status)
			require.Equal(t, tc.key, gotKey)
			require.Equal(t, tc.version, gotVersion)
			if status == 200 {
				require.Equal(t, tc.version, result["version"])
			} else {
				require.Nil(t, result)
			}
		})
	}
}

func TestPeerHTTPCrawlHeader(t *testing.T) {
	for _, private := range []bool{false, true} {
		o := &Overlay{cfg: Config{PrivateMode: private}}
		require.Equal(t, !private, o.handshakeConfigFor().CrawlPublic)
	}
	for value, want := range map[string]bool{"": false, "private": false, "public": true, "PUBLIC": true, " public ": false, "public,private": false} {
		extras, err := ParseHandshakeExtras(http.Header{HeaderCrawl: {value}}, nil, nil)
		require.NoError(t, err)
		peer := NewPeer(1, Endpoint{}, true, nil, nil)
		peer.applyHandshakeExtras(extras)
		require.Equal(t, want, peer.crawlPublic, value)
		peer.applyHandshakeExtras(HandshakeExtras{})
		require.False(t, peer.crawlPublic)
	}
}

func TestPeerHTTPOverTLS(t *testing.T) {
	requirePeerTLSSupported(t)
	o := newLifecycleTestOverlay(t, WithPrivateMode(true))
	o.SetHTTPProviders(HTTPProviders{ServerInfo: healthyPeerServerInfo})
	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(context.Background()) }()
	t.Cleanup(func() {
		require.NoError(t, o.Stop())
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
			t.Error("overlay did not stop")
		}
	})
	select {
	case <-o.ListenerReady():
	case <-time.After(5 * time.Second):
		t.Fatal("listener not ready")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // local self-signed peer certificate
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, target := range []string{"/crawl", "/health", "/vl/unknown"} {
		resp, err := client.Get("https://" + o.ListenAddr() + target)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		if strings.HasPrefix(target, "/vl/") {
			require.Equal(t, 404, resp.StatusCode)
		} else {
			require.Equal(t, 200, resp.StatusCode)
		}
		if target == "/health" {
			require.JSONEq(t, `{"info":{}}`, string(body))
		}
	}
	require.Empty(t, o.Peers())
}
