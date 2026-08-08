package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/require"
)

func TestValidateHandshakeRequestEnvelope(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	req, err := BuildHandshakeRequest(id, make([]byte, 32), DefaultHandshakeConfig())
	require.NoError(t, err)

	tests := []struct {
		name string
		edit func(*http.Request)
	}{
		{name: "method", edit: func(r *http.Request) { r.Method = http.MethodPost }},
		{name: "version", edit: func(r *http.Request) { r.ProtoMajor, r.ProtoMinor, r.Proto = 1, 0, "HTTP/1.0" }},
		{name: "connection token", edit: func(r *http.Request) { r.Header.Set(HeaderConnection, "keep-alive") }},
		{name: "upgrade missing", edit: func(r *http.Request) { r.Header.Del(HeaderUpgrade) }},
		{name: "upgrade malformed", edit: func(r *http.Request) { r.Header.Set(HeaderUpgrade, "not-an-xrpl-version") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			copyReq := req.Clone(context.Background())
			copyReq.Header = req.Header.Clone()
			tc.edit(copyReq)
			require.ErrorIs(t, validateHandshakeRequest(copyReq), ErrInvalidHandshake)
		})
	}
	require.NoError(t, validateHandshakeRequest(req))
	missingPublicKey := req.Clone(context.Background())
	missingPublicKey.Header = req.Header.Clone()
	missingPublicKey.Header.Del(HeaderPublicKey)
	require.NoError(t, validateHandshakeRequest(missingPublicKey))
	withBody := req.Clone(context.Background())
	withBody.Header = req.Header.Clone()
	withBody.ContentLength = 1
	require.NoError(t, validateHandshakeRequest(withBody), "positive Content-Length is accepted by the dynamic-body upgrade predicate")
	withTransferEncoding := req.Clone(context.Background())
	withTransferEncoding.Header = req.Header.Clone()
	withTransferEncoding.TransferEncoding = []string{"chunked"}
	require.NoError(t, validateHandshakeRequest(withTransferEncoding), "transfer encoding is accepted by the dynamic-body upgrade predicate")
	missingConnectAs := req.Clone(context.Background())
	missingConnectAs.Header = req.Header.Clone()
	missingConnectAs.Header.Del(HeaderConnectAs)
	require.NoError(t, validateHandshakeRequest(missingConnectAs), "Connect-As is handled by admission redirect logic")
	for _, version := range []struct {
		major int
		minor int
		proto string
	}{
		{major: 1, minor: 2, proto: "HTTP/1.2"},
		{major: 2, minor: 0, proto: "HTTP/2.0"},
	} {
		copyReq := req.Clone(context.Background())
		copyReq.Header = req.Header.Clone()
		copyReq.ProtoMajor, copyReq.ProtoMinor, copyReq.Proto = version.major, version.minor, version.proto
		require.NoErrorf(t, validateHandshakeRequest(copyReq), "HTTP version %s", version.proto)
	}
}

func TestValidateHandshakeResponseEnvelope(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	resp := BuildHandshakeResponse(&http.Request{Header: make(http.Header)}, id, make([]byte, 32), DefaultHandshakeConfig(), "XRPL/2.2")

	tests := []struct {
		name string
		edit func(*http.Response)
	}{
		{name: "status", edit: func(r *http.Response) { r.StatusCode = http.StatusOK }},
		{name: "version", edit: func(r *http.Response) { r.ProtoMajor, r.ProtoMinor, r.Proto = 1, 0, "HTTP/1.0" }},
		{name: "connection token", edit: func(r *http.Response) { r.Header.Set(HeaderConnection, "close") }},
		{name: "upgrade version", edit: func(r *http.Response) { r.Header.Set(HeaderUpgrade, SupportedProtocolVersions()) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			copyResp := *resp
			copyResp.Header = resp.Header.Clone()
			tc.edit(&copyResp)
			require.ErrorIs(t, validateHandshakeResponse(&copyResp), ErrInvalidHandshake)
		})
	}
	require.NoError(t, validateHandshakeResponse(resp))
	for _, version := range []struct {
		major int
		minor int
		proto string
	}{
		{major: 1, minor: 2, proto: "HTTP/1.2"},
		{major: 2, minor: 0, proto: "HTTP/2.0"},
	} {
		copyResp := *resp
		copyResp.Header = resp.Header.Clone()
		copyResp.ProtoMajor, copyResp.ProtoMinor, copyResp.Proto = version.major, version.minor, version.proto
		require.NoErrorf(t, validateHandshakeResponse(&copyResp), "HTTP version %s", version.proto)
	}
}

func TestHandshakeHeaderReaderBoundsAndPreservesFrames(t *testing.T) {
	header := "GET / HTTP/1.1\r\nConnection: Upgrade\r\nUpgrade: XRPL/2.2\r\n\r\n"
	const payload = "\x01\x02\x03"
	reader := &handshakeHeaderReader{r: strings.NewReader(header + payload), limit: maxHandshakeHeaderBytes}
	buffered := bufio.NewReader(reader)
	req, err := http.ReadRequest(buffered)
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, req.Method)
	got, err := io.ReadAll(buffered)
	require.NoError(t, err)
	require.Equal(t, []byte(payload), got)

	oversized := &handshakeHeaderReader{
		r:     strings.NewReader("GET / HTTP/1.1\r\nX-Bloat: " + strings.Repeat("x", maxHandshakeHeaderBytes) + "\r\n"),
		limit: maxHandshakeHeaderBytes,
	}
	_, err = http.ReadRequest(bufio.NewReader(oversized))
	require.ErrorIs(t, err, ErrHandshakeHeadersTooLarge)
}

func TestHandshakeHeaderReaderAcceptsLFOnlyAndPreservesFrames(t *testing.T) {
	header := "GET / HTTP/1.1\nConnection: Upgrade\nUpgrade: XRPL/2.2\n\n"
	const payload = "\x01\x02\x03"
	reader := &handshakeHeaderReader{r: strings.NewReader(header + payload), limit: maxHandshakeHeaderBytes}
	buffered := bufio.NewReader(reader)
	req, err := http.ReadRequest(buffered)
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, req.Method)
	got, err := io.ReadAll(buffered)
	require.NoError(t, err)
	require.Equal(t, []byte(payload), got)
	require.True(t, reader.done)
}

func TestHandshakeHeaderReaderExactBoundary(t *testing.T) {
	prefix := "GET / HTTP/1.1\r\nConnection: Upgrade\r\nUpgrade: XRPL/2.2\r\n"
	suffix := "\r\n"
	makeHeader := func(total int) string {
		target := total - len(prefix) - len(suffix)
		line := "X-Pad: x\r\n"
		lines := target / len(line)
		remaining := target - lines*len(line)
		const minLine = len("X-Pad: \r\n")
		if remaining > 0 && remaining < minLine {
			lines--
			remaining += len(line)
		}
		padding := strings.Repeat(line, lines)
		if remaining > 0 {
			padding += "X-Pad: " + strings.Repeat("x", remaining-minLine) + "\r\n"
		}
		return prefix + padding + suffix
	}

	exactHeader := makeHeader(maxHandshakeHeaderBytes)
	require.Len(t, exactHeader, maxHandshakeHeaderBytes)
	exactReader := &handshakeHeaderReader{r: strings.NewReader(exactHeader), limit: maxHandshakeHeaderBytes}
	_, err := http.ReadRequest(bufio.NewReader(exactReader))
	require.NoError(t, err)

	tooLargeHeader := makeHeader(maxHandshakeHeaderBytes + 1)
	require.Len(t, tooLargeHeader, maxHandshakeHeaderBytes+1)
	tooLargeReader := &handshakeHeaderReader{r: strings.NewReader(tooLargeHeader), limit: maxHandshakeHeaderBytes}
	_, err = io.ReadAll(tooLargeReader)
	require.ErrorIs(t, err, ErrHandshakeHeadersTooLarge)
}

type closeErrorBody struct {
	io.Reader
}

func (closeErrorBody) Close() error { return errors.New("body close failed") }

var (
	errBodyReadPastLimit   = errors.New("body read past limit")
	errBodyCloseWouldDrain = errors.New("body close would drain unread bytes")
)

type handshakeBodyProbe struct {
	io.ReadCloser
	expected int64
	readN    int64
	closeN   int
}

func (b *handshakeBodyProbe) Read(p []byte) (int, error) {
	if b.readN >= b.expected {
		return 0, errBodyReadPastLimit
	}
	remaining := b.expected - b.readN
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.readN += int64(n)
	return n, err
}

func (b *handshakeBodyProbe) Close() error {
	b.closeN++
	if b.readN < b.expected {
		return errBodyCloseWouldDrain
	}
	return b.ReadCloser.Close()
}

func parseHandshakeBodyRequest(t *testing.T, wire string) *http.Request {
	t.Helper()
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(wire)))
	require.NoError(t, err)
	return req
}

func TestReadHandshakeBodyBoundsAndErrors(t *testing.T) {
	exact := parseHandshakeBodyRequest(t, fmt.Sprintf(
		"GET / HTTP/1.1\r\nContent-Length: %d\r\n\r\n%s",
		maxHandshakeBodyBytes,
		strings.Repeat("x", maxHandshakeBodyBytes),
	))
	exactProbe := &handshakeBodyProbe{
		ReadCloser: exact.Body,
		expected:   maxHandshakeBodyBytes,
	}
	exact.Body = exactProbe
	require.NoError(t, readHandshakeBody(exact))
	require.Equal(t, int64(maxHandshakeBodyBytes), exactProbe.readN)
	require.Equal(t, 1, exactProbe.closeN)

	fixedTooLarge := parseHandshakeBodyRequest(t, fmt.Sprintf(
		"GET / HTTP/1.1\r\nContent-Length: %d\r\n\r\n%s",
		maxHandshakeBodyBytes+1,
		strings.Repeat("x", maxHandshakeBodyBytes+1),
	))
	fixedProbe := &handshakeBodyProbe{
		ReadCloser: fixedTooLarge.Body,
		expected:   maxHandshakeBodyBytes + 1,
	}
	fixedTooLarge.Body = fixedProbe
	require.ErrorIs(t, readHandshakeBody(fixedTooLarge), ErrHandshakeBodyTooLarge)
	require.Zero(t, fixedProbe.readN, "oversized fixed bodies are rejected before reading")
	require.Zero(t, fixedProbe.closeN, "oversized fixed bodies are left for connection close")

	chunkedTooLarge := parseHandshakeBodyRequest(t, fmt.Sprintf(
		"GET / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n",
		maxHandshakeBodyBytes+1,
		strings.Repeat("x", maxHandshakeBodyBytes+1),
	))
	chunkedProbe := &handshakeBodyProbe{
		ReadCloser: chunkedTooLarge.Body,
		expected:   maxHandshakeBodyBytes + 1,
	}
	chunkedTooLarge.Body = chunkedProbe
	require.ErrorIs(t, readHandshakeBody(chunkedTooLarge), ErrHandshakeBodyTooLarge)
	require.Equal(t, int64(maxHandshakeBodyBytes+1), chunkedProbe.readN,
		"chunked bodies are read only through the first byte over the limit")
	require.Zero(t, chunkedProbe.closeN, "oversized chunked bodies are not drained by Close")

	truncatedWire := "GET / HTTP/1.1\r\nContent-Length: 3\r\n\r\nx"
	truncated, err := http.ReadRequest(bufio.NewReader(strings.NewReader(truncatedWire)))
	require.NoError(t, err)
	require.ErrorIs(t, readHandshakeBody(truncated), io.ErrUnexpectedEOF)

	validChunkedWire := "GET / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nbody\r\n0\r\n\r\n"
	validChunked, err := http.ReadRequest(bufio.NewReader(strings.NewReader(validChunkedWire)))
	require.NoError(t, err)
	require.NoError(t, readHandshakeBody(validChunked))

	chunkedWire := "GET / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\nZ\r\nnot-a-chunk\r\n0\r\n\r\n"
	chunked, err := http.ReadRequest(bufio.NewReader(strings.NewReader(chunkedWire)))
	require.NoError(t, err)
	require.Error(t, readHandshakeBody(chunked))

	closeErr := &http.Request{Body: closeErrorBody{Reader: strings.NewReader("body")}, ContentLength: 4}
	require.Error(t, readHandshakeBody(closeErr))
}

type deadlinePeerConn struct {
	data      *bytes.Reader
	deadlines []time.Time
	writes    bytes.Buffer
}

var _ peertls.PeerConn = (*deadlinePeerConn)(nil)

func (c *deadlinePeerConn) Read(p []byte) (int, error) { return c.data.Read(p) }
func (c *deadlinePeerConn) Write(p []byte) (int, error) {
	return c.writes.Write(p)
}
func (c *deadlinePeerConn) Close() error         { return nil }
func (c *deadlinePeerConn) LocalAddr() net.Addr  { return &net.TCPAddr{} }
func (c *deadlinePeerConn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.ParseIP("192.0.2.1")} }
func (c *deadlinePeerConn) SetDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	return nil
}
func (c *deadlinePeerConn) SetReadDeadline(time.Time) error        { return nil }
func (c *deadlinePeerConn) SetWriteDeadline(time.Time) error       { return nil }
func (c *deadlinePeerConn) HandshakeContext(context.Context) error { return nil }
func (c *deadlinePeerConn) SharedValue() ([]byte, error)           { return make([]byte, 32), nil }

func TestInboundHandshakeInstallsAndResetsDeadline(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	o := &Overlay{cfg: DefaultConfig(), identity: id}
	peer := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, true, id, nil)
	conn := &deadlinePeerConn{data: bytes.NewReader([]byte("not an HTTP request\r\n"))}
	err = o.performInboundHandshake(context.Background(), peer, conn)
	require.Error(t, err)
	require.GreaterOrEqual(t, len(conn.deadlines), 2)
	require.False(t, conn.deadlines[0].IsZero())
	require.True(t, conn.deadlines[len(conn.deadlines)-1].IsZero())
}

func TestMissingConnectAsUsesRedirect(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	req, err := BuildHandshakeRequest(id, make([]byte, 32), DefaultHandshakeConfig())
	require.NoError(t, err)
	req.Header.Del(HeaderConnectAs)
	req.Header.Del(HeaderPublicKey)
	req.Header.Del(HeaderSessionSignature)
	var wire bytes.Buffer
	require.NoError(t, WriteRawHandshakeRequest(&wire, req))

	o := newLifecycleTestOverlay(t, WithListenAddr(""))
	peer := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, true, o.identity, nil)
	conn := &deadlinePeerConn{data: bytes.NewReader(wire.Bytes())}
	err = o.performInboundHandshake(context.Background(), peer, conn)
	require.ErrorIs(t, err, errInboundRejected)
	require.Contains(t, conn.writes.String(), "HTTP/1.1 503 Service Unavailable")
	require.NotContains(t, conn.writes.String(), "HTTP/1.1 400 Bad Request")
}

func TestMalformedUpgradeUsesEnvelopeErrorBeforeRedirect(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	req, err := BuildHandshakeRequest(id, make([]byte, 32), DefaultHandshakeConfig())
	require.NoError(t, err)
	req.Header.Set(HeaderUpgrade, "not-an-xrpl-version")
	req.Header.Set(HeaderConnectAs, "crawler")
	var wire bytes.Buffer
	require.NoError(t, WriteRawHandshakeRequest(&wire, req))

	o := newLifecycleTestOverlay(t, WithListenAddr(""))
	peer := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, true, o.identity, nil)
	conn := &deadlinePeerConn{data: bytes.NewReader(wire.Bytes())}
	err = o.performInboundHandshake(context.Background(), peer, conn)
	require.Error(t, err)
	require.NotErrorIs(t, err, errInboundRejected)
	require.Contains(t, conn.writes.String(), "HTTP/1.1 400 Bad Request")
}

func runInboundHandshakeRequest(t *testing.T, edit func(*http.Request)) (*deadlinePeerConn, error) {
	t.Helper()
	remoteID, err := NewIdentity()
	require.NoError(t, err)
	req, err := BuildHandshakeRequest(remoteID, make([]byte, 32), DefaultHandshakeConfig())
	require.NoError(t, err)
	edit(req)
	var wire bytes.Buffer
	require.NoError(t, WriteRawHandshakeRequest(&wire, req))
	return runRawInboundHandshakeRequest(t, wire.Bytes())
}

func runRawInboundHandshakeRequest(t *testing.T, wire []byte) (*deadlinePeerConn, error) {
	t.Helper()
	o := newLifecycleTestOverlay(t, WithListenAddr(""))
	peer := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, true, o.identity, nil)
	conn := &deadlinePeerConn{data: bytes.NewReader(wire)}
	err := o.performInboundHandshake(context.Background(), peer, conn)
	return conn, err
}

func TestNegotiationPrecedesAuthentication(t *testing.T) {
	conn, err := runInboundHandshakeRequest(t, func(req *http.Request) {
		req.Header.Set(HeaderUpgrade, "XRPL/9.9")
		req.Header.Del(HeaderPublicKey)
		req.Header.Del(HeaderSessionSignature)
	})
	require.Error(t, err)
	require.Contains(t, conn.writes.String(), "HTTP/1.1 400 Bad Request")
	require.Contains(t, conn.writes.String(), "unable to agree on a protocol version")
	require.NotContains(t, conn.writes.String(), "missing Public-Key")
}

func TestInboundBodyErrorPrecedesAdmission(t *testing.T) {
	remoteID, err := NewIdentity()
	require.NoError(t, err)
	req, err := BuildHandshakeRequest(remoteID, make([]byte, 32), DefaultHandshakeConfig())
	require.NoError(t, err)
	req.Header.Del(HeaderConnectAs)
	req.Header.Del(HeaderPublicKey)
	req.Header.Del(HeaderSessionSignature)
	req.ContentLength = 3
	req.Body = io.NopCloser(strings.NewReader("abc"))
	var wire bytes.Buffer
	require.NoError(t, req.Write(&wire))
	truncated := append([]byte(nil), wire.Bytes()[:wire.Len()-2]...)

	o := newLifecycleTestOverlay(t, WithListenAddr(""))
	peer := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, true, o.identity, nil)
	conn := &deadlinePeerConn{data: bytes.NewReader(truncated)}
	err = o.performInboundHandshake(context.Background(), peer, conn)
	require.Error(t, err)
	require.Contains(t, conn.writes.String(), "HTTP/1.1 400 Bad Request")
	require.NotContains(t, conn.writes.String(), "HTTP/1.1 503 Service Unavailable")
}

func TestInboundOversizedBodyWritesBadRequest(t *testing.T) {
	tests := []struct {
		name string
		wire string
	}{
		{
			name: "fixed content length",
			wire: fmt.Sprintf(
				"GET / HTTP/1.1\r\nContent-Length: %d\r\n\r\n%s",
				maxHandshakeBodyBytes+1,
				strings.Repeat("x", maxHandshakeBodyBytes+1),
			),
		},
		{
			name: "chunked body",
			wire: fmt.Sprintf(
				"GET / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n",
				maxHandshakeBodyBytes+1,
				strings.Repeat("x", maxHandshakeBodyBytes+1),
			),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := runRawInboundHandshakeRequest(t, []byte(tc.wire))
			require.Error(t, err)
			require.Contains(t, conn.writes.String(), "HTTP/1.1 400 Bad Request")
		})
	}
}

func TestInboundVerificationErrorsWriteBadRequest(t *testing.T) {
	tests := []struct {
		name string
		edit func(*http.Request)
	}{
		{name: "server domain", edit: func(req *http.Request) { req.Header.Set(HeaderServerDomain, "-bad.example.com") }},
		{name: "network id", edit: func(req *http.Request) { req.Header.Set(HeaderNetworkID, "not-a-number") }},
		{name: "network time", edit: func(req *http.Request) { req.Header.Set(HeaderNetworkTime, "not-a-time") }},
		{name: "public key", edit: func(req *http.Request) { req.Header.Del(HeaderPublicKey) }},
		{name: "session signature", edit: func(req *http.Request) { req.Header.Del(HeaderSessionSignature) }},
		{name: "local ip", edit: func(req *http.Request) { req.Header.Set(HeaderLocalIP, "not-an-ip") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := runInboundHandshakeRequest(t, tc.edit)
			require.Error(t, err)
			require.Contains(t, conn.writes.String(), "HTTP/1.1 400 Bad Request")
		})
	}
}

func TestOutboundResourceAdmissionPrecedesDial(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			connection.Close()
			accepted <- struct{}{}
		}
	}()

	o := newLifecycleTestOverlay(t, WithListenAddr(""))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.Run(ctx) }()
	select {
	case <-o.ListenerReady():
	case <-time.After(2 * time.Second):
		t.Fatal("overlay did not become ready")
	}

	addr := listener.Addr().String()
	consumer := o.resourceManager.NewOutboundEndpoint(addr)
	for {
		if consumer.Charge(resource.NewCharge(resource.DropThreshold+1, "test"), "") == resource.Drop {
			break
		}
	}
	consumer.Release()
	if err := o.Connect(addr); !errors.Is(err, ErrEndpointBanned) {
		t.Fatalf("Connect error = %v, want ErrEndpointBanned before dial", err)
	}
	select {
	case <-accepted:
		t.Fatal("banned endpoint was dialed before resource admission")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("overlay Run did not stop")
	}
}

func TestResolveOutboundEndpointSupportsHostnames(t *testing.T) {
	o := &Overlay{discovery: &Discovery{}}
	o.discovery.lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		require.Equal(t, "peer.example", host)
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.80")}}, nil
	}

	endpoint, err := o.resolveOutboundEndpoint(t.Context(), Endpoint{Host: "peer.example", Port: 51235})
	require.NoError(t, err)
	require.Equal(t, Endpoint{Host: "192.0.2.80", Port: 51235}, endpoint)
}
