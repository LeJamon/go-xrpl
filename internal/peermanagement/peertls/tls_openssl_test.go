//go:build cgo

package peertls

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls/shim"
)

func generateTestCert(t testing.TB) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             now,
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
	return
}

func newTestConnPair(t testing.TB) (PeerConn, PeerConn) {
	t.Helper()
	clientCert, clientKey := generateTestCert(t)
	serverCert, serverKey := generateTestCert(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	acceptResult := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		acceptResult <- struct {
			conn net.Conn
			err  error
		}{conn: conn, err: acceptErr}
	}()
	clientWire, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		t.Fatalf("Dial: %v", err)
	}
	accepted := <-acceptResult
	_ = listener.Close()
	if accepted.err != nil {
		_ = clientWire.Close()
		t.Fatalf("Accept: %v", accepted.err)
	}
	serverWire := accepted.conn

	clientConn, err := Client(clientWire, &Config{CertPEM: clientCert, KeyPEM: clientKey})
	if err != nil {
		_ = clientWire.Close()
		_ = serverWire.Close()
		t.Fatalf("Client: %v", err)
	}
	serverConn, err := newConn(serverWire, &Config{CertPEM: serverCert, KeyPEM: serverKey}, true)
	if err != nil {
		_ = clientConn.Close()
		_ = serverWire.Close()
		t.Fatalf("server conn: %v", err)
	}
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- serverConn.HandshakeContext(ctx)
	}()
	if err := clientConn.HandshakeContext(ctx); err != nil {
		t.Fatalf("client HandshakeContext: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server HandshakeContext: %v", err)
	}
	return clientConn, serverConn
}

// TestHandshake_SessionSigRoundTrip drives a client and server peertls
// connection through a full handshake over a TCP loopback and asserts
// that both sides compute identical SharedValue bytes.
func TestHandshake_SessionSigRoundTrip(t *testing.T) {
	clientCert, clientKey := generateTestCert(t)
	serverCert, serverKey := generateTestCert(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	serverConfig := &Config{
		CertPEM:    serverCert,
		KeyPEM:     serverKey,
		CipherList: "TLSv1.2:!CBC:!DSS:!PSK:!eNULL:!aNULL",
	}
	wrapped, err := NewListener(ln, serverConfig)
	if err != nil {
		_ = ln.Close()
		t.Fatalf("NewListener: %v", err)
	}
	defer wrapped.Close()
	serverConfig.CertPEM = []byte("mutated")
	serverConfig.KeyPEM = []byte("mutated")
	serverConfig.CipherList = "not-a-real-cipher"

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	tcpClient, err := dialer.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer tcpClient.Close()

	clientConn, err := Client(tcpClient, &Config{CertPEM: clientCert, KeyPEM: clientKey})
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	defer clientConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		conn net.Conn
		err  error
	}
	srvCh := make(chan result, 1)
	go func() {
		c, e := wrapped.Accept()
		if e != nil {
			srvCh <- result{nil, e}
			return
		}
		pc := c.(PeerConn)
		if e := pc.HandshakeContext(ctx); e != nil {
			srvCh <- result{nil, e}
			return
		}
		srvCh <- result{c, nil}
	}()

	if err := clientConn.HandshakeContext(ctx); err != nil {
		t.Fatalf("client HandshakeContext: %v", err)
	}
	srvRes := <-srvCh
	if srvRes.err != nil {
		t.Fatalf("server handshake: %v", srvRes.err)
	}
	defer srvRes.conn.Close()

	clientSV, err := clientConn.SharedValue()
	if err != nil {
		t.Fatalf("client SharedValue: %v", err)
	}
	serverSV, err := srvRes.conn.(PeerConn).SharedValue()
	if err != nil {
		t.Fatalf("server SharedValue: %v", err)
	}
	if len(clientSV) != 32 || len(serverSV) != 32 {
		t.Fatalf("expected 32-byte shared values, got client=%d server=%d", len(clientSV), len(serverSV))
	}
	for i := range clientSV {
		if clientSV[i] != serverSV[i] {
			t.Fatalf("shared values differ at byte %d: client=%x server=%x", i, clientSV, serverSV)
		}
	}
}

// TestHandshake_ConcurrentReadWrite drives concurrent Read on one
// goroutine and Write on another over a peertls connection, and also
// verifies that Close while Read is parked unblocks the reader. Catches
// the deadlock and full-duplex starvation patterns where a single
// mutex protected all of Read/Write/Close.
func TestHandshake_ConcurrentReadWrite(t *testing.T) {
	clientCert, clientKey := generateTestCert(t)
	serverCert, serverKey := generateTestCert(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	wrapped, err := NewListener(ln, &Config{CertPEM: serverCert, KeyPEM: serverKey})
	if err != nil {
		_ = ln.Close()
		t.Fatalf("NewListener: %v", err)
	}
	defer wrapped.Close()

	tcpClient, err := (&net.Dialer{Timeout: 2 * time.Second}).Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	clientConn, err := Client(tcpClient, &Config{CertPEM: clientCert, KeyPEM: clientKey})
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srvCh := make(chan PeerConn, 1)
	srvErrCh := make(chan error, 1)
	go func() {
		c, e := wrapped.Accept()
		if e != nil {
			srvErrCh <- e
			return
		}
		pc := c.(PeerConn)
		if e := pc.HandshakeContext(ctx); e != nil {
			srvErrCh <- e
			return
		}
		srvCh <- pc
	}()

	if err := clientConn.HandshakeContext(ctx); err != nil {
		t.Fatalf("client HandshakeContext: %v", err)
	}
	var serverConn PeerConn
	select {
	case serverConn = <-srvCh:
	case e := <-srvErrCh:
		t.Fatalf("server handshake: %v", e)
	}

	const payload = "ping ping ping ping ping ping"
	const rounds = 32

	// Server echoes whatever it reads. Run in a goroutine.
	echoDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := serverConn.Read(buf)
			if rerr != nil {
				echoDone <- rerr
				return
			}
			if _, werr := serverConn.Write(buf[:n]); werr != nil {
				echoDone <- werr
				return
			}
		}
	}()

	// On the client, write and read concurrently. If a single mutex
	// serialized Read+Write the reader would starve and the writer
	// would deadlock once the underlying TCP buffer filled.
	var wg sync.WaitGroup
	wg.Add(2)

	writeErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		for range rounds {
			if _, err := clientConn.Write([]byte(payload)); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	readErr := make(chan error, 1)
	var totalRead int
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		want := len(payload) * rounds
		for totalRead < want {
			n, err := clientConn.Read(buf)
			if err != nil {
				readErr <- err
				return
			}
			if !bytes.Contains(bytes.Repeat([]byte(payload), rounds), buf[:n]) {
				readErr <- io.ErrUnexpectedEOF
				return
			}
			totalRead += n
		}
		readErr <- nil
	}()

	// Reasonable upper bound — the actual exchange should finish in ms.
	deadline := time.After(5 * time.Second)
	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("client Write: %v", err)
		}
	case <-deadline:
		t.Fatalf("write goroutine timed out — likely full-duplex starvation")
	}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("client Read: %v", err)
		}
	case <-deadline:
		t.Fatalf("read goroutine timed out — likely full-duplex starvation")
	}

	if totalRead != len(payload)*rounds {
		t.Fatalf("read %d bytes, want %d", totalRead, len(payload)*rounds)
	}

	// Close while the server's echo loop is parked in Read. Close must
	// unblock it promptly (deadlock guard).
	closeReturned := make(chan struct{})
	go func() {
		_ = clientConn.Close()
		close(closeReturned)
	}()
	select {
	case <-closeReturned:
	case <-time.After(2 * time.Second):
		t.Fatalf("Close blocked — likely Read holding the SSL mutex")
	}

	// Server's Read should now return an error promptly too.
	select {
	case <-echoDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("server echo goroutine did not unblock after client Close")
	}
	_ = serverConn.Close()
	wg.Wait()
}

func TestHandshake_LargeConcurrentRoundTrip(t *testing.T) {
	clientConn, serverConn := newTestConnPair(t)
	deadline := time.Now().Add(10 * time.Second)
	if err := clientConn.SetDeadline(deadline); err != nil {
		t.Fatalf("client SetDeadline: %v", err)
	}
	if err := serverConn.SetDeadline(deadline); err != nil {
		t.Fatalf("server SetDeadline: %v", err)
	}

	clientPayload := make([]byte, 1<<20+137)
	serverPayload := make([]byte, 1<<20+311)
	for i := range clientPayload {
		clientPayload[i] = byte(i*31 + 7)
	}
	for i := range serverPayload {
		serverPayload[i] = byte(i*17 + 11)
	}

	errCh := make(chan error, 4)
	go func() {
		n, err := clientConn.Write(clientPayload)
		if err == nil && n != len(clientPayload) {
			err = io.ErrShortWrite
		}
		if err != nil {
			err = fmt.Errorf("client Write: %w", err)
		}
		errCh <- err
	}()
	go func() {
		n, err := serverConn.Write(serverPayload)
		if err == nil && n != len(serverPayload) {
			err = io.ErrShortWrite
		}
		if err != nil {
			err = fmt.Errorf("server Write: %w", err)
		}
		errCh <- err
	}()
	go func() {
		got := make([]byte, len(serverPayload))
		_, err := io.ReadFull(clientConn, got)
		if err == nil && !bytes.Equal(got, serverPayload) {
			err = errors.New("client received corrupted payload")
		}
		if err != nil {
			err = fmt.Errorf("client Read: %w", err)
		}
		errCh <- err
	}()
	go func() {
		got := make([]byte, len(clientPayload))
		_, err := io.ReadFull(serverConn, got)
		if err == nil && !bytes.Equal(got, clientPayload) {
			err = errors.New("server received corrupted payload")
		}
		if err != nil {
			err = fmt.Errorf("server Read: %w", err)
		}
		errCh <- err
	}()

	var operationErrors []error
	for range 4 {
		if err := <-errCh; err != nil {
			operationErrors = append(operationErrors, err)
		}
	}
	for _, err := range operationErrors {
		t.Error(err)
	}
}

func BenchmarkPeerConnWrite(b *testing.B) {
	cases := []struct {
		name string
		size int
	}{
		{name: "16KiB", size: 16 << 10},
		{name: "64KiB", size: 64 << 10},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			clientConn, serverConn := newTestConnPair(b)
			payload := bytes.Repeat([]byte{0x5a}, tc.size)
			readBuf := make([]byte, 64<<10)
			start := make(chan struct{})
			readDone := make(chan error, 1)
			go func() {
				<-start
				remaining := int64(b.N) * int64(tc.size)
				for remaining > 0 {
					buf := readBuf
					if remaining < int64(len(buf)) {
						buf = buf[:remaining]
					}
					n, err := serverConn.Read(buf)
					remaining -= int64(n)
					if err != nil {
						readDone <- err
						return
					}
				}
				readDone <- nil
			}()

			b.SetBytes(int64(tc.size))
			b.ReportAllocs()
			close(start)
			b.ResetTimer()
			for range b.N {
				n, err := clientConn.Write(payload)
				if err != nil {
					b.Fatal(err)
				}
				if n != len(payload) {
					b.Fatal(io.ErrShortWrite)
				}
			}
			if err := <-readDone; err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
		})
	}
}

type engineResult struct {
	n   int
	err error
	out []byte
}

type scriptedEngine struct {
	handshakes []engineResult
	reads      []engineResult
	writes     []engineResult
	shutdowns  []engineResult
	bioWrites  []engineResult
	out        []byte
	in         []byte
	freed      bool
}

func (e *scriptedEngine) pop(steps *[]engineResult) engineResult {
	if len(*steps) == 0 {
		return engineResult{}
	}
	r := (*steps)[0]
	*steps = (*steps)[1:]
	e.out = append(e.out, r.out...)
	return r
}

func (e *scriptedEngine) Handshake() error { return e.pop(&e.handshakes).err }
func (e *scriptedEngine) Read(p []byte) (int, error) {
	r := e.pop(&e.reads)
	if r.n > 0 {
		copy(p, bytes.Repeat([]byte{'p'}, r.n))
	}
	return r.n, r.err
}
func (e *scriptedEngine) Write([]byte) (int, error) {
	r := e.pop(&e.writes)
	return r.n, r.err
}
func (e *scriptedEngine) Shutdown() error { return e.pop(&e.shutdowns).err }
func (e *scriptedEngine) BIORead(p []byte) (int, error) {
	if len(e.out) == 0 {
		return 0, nil
	}
	n := copy(p, e.out)
	e.out = e.out[n:]
	return n, nil
}
func (e *scriptedEngine) BIOWrite(p []byte) (int, error) {
	r := engineResult{n: len(p)}
	if len(e.bioWrites) > 0 {
		r = e.pop(&e.bioWrites)
	}
	if r.n > 0 && r.n <= len(p) {
		e.in = append(e.in, p[:r.n]...)
	}
	return r.n, r.err
}
func (e *scriptedEngine) GetFinished(p []byte) int {
	copy(p, bytes.Repeat([]byte{1}, 12))
	return 12
}
func (e *scriptedEngine) GetPeerFinished(p []byte) int {
	copy(p, bytes.Repeat([]byte{2}, 12))
	return 12
}
func (e *scriptedEngine) Free() { e.freed = true }

type blockingReadEngine struct {
	scriptedEngine
	readStarted chan struct{}
	releaseRead chan struct{}
}

func (e *blockingReadEngine) Read(p []byte) (int, error) {
	close(e.readStarted)
	<-e.releaseRead
	e.out = append(e.out, []byte("control")...)
	p[0] = 'p'
	return 1, nil
}

type rawRead struct {
	data []byte
	err  error
}

type scriptedRawConn struct {
	reads     []rawRead
	writes    []engineResult
	written   []byte
	deadlines []time.Time
	closed    bool
}

func (c *scriptedRawConn) Read(p []byte) (int, error) {
	if len(c.reads) == 0 {
		return 0, io.EOF
	}
	r := c.reads[0]
	c.reads = c.reads[1:]
	return copy(p, r.data), r.err
}
func (c *scriptedRawConn) Write(p []byte) (int, error) {
	r := engineResult{n: len(p)}
	if len(c.writes) > 0 {
		r = c.writes[0]
		c.writes = c.writes[1:]
	}
	if r.n > 0 && r.n <= len(p) {
		c.written = append(c.written, p[:r.n]...)
	}
	return r.n, r.err
}
func (c *scriptedRawConn) Close() error                     { c.closed = true; return nil }
func (c *scriptedRawConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *scriptedRawConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *scriptedRawConn) SetReadDeadline(time.Time) error  { return nil }
func (c *scriptedRawConn) SetWriteDeadline(time.Time) error { return nil }
func (c *scriptedRawConn) SetDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	return nil
}

type testAddr string

func (a testAddr) Network() string { return string(a) }
func (a testAddr) String() string  { return string(a) }

func readyScriptedConn(raw net.Conn, engine tlsEngine) *conn {
	c := newConnWithEngine(raw, engine)
	c.state = stateReady
	return c
}

type refreshableTimeoutError struct{}

func (refreshableTimeoutError) Error() string   { return "temporary timeout" }
func (refreshableTimeoutError) Timeout() bool   { return true }
func (refreshableTimeoutError) Temporary() bool { return true }

func TestReadDefersRawTerminalErrorAndPreservesPartialBIOInput(t *testing.T) {
	rawErr := errors.New("raw read failed")
	for _, tc := range []struct {
		name string
		raw  error
		want error
	}{
		{name: "EOF", raw: io.EOF, want: io.ErrUnexpectedEOF},
		{name: "other", raw: rawErr, want: rawErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := &scriptedRawConn{reads: []rawRead{{data: []byte("abcd"), err: tc.raw}}}
			engine := &scriptedEngine{
				reads:     []engineResult{{err: shim.ErrWantRead}, {err: shim.ErrWantRead}, {n: 1}, {err: shim.ErrWantRead}},
				bioWrites: []engineResult{{n: 2}, {n: 2}},
			}
			c := readyScriptedConn(raw, engine)
			n, err := c.Read(make([]byte, 1))
			if err != nil || n != 1 || !bytes.Equal(engine.in, []byte("abcd")) {
				t.Fatalf("Read = (%d, %v), input=%q", n, err, engine.in)
			}
			_, err = c.Read(make([]byte, 1))
			if !errors.Is(err, tc.want) {
				t.Fatalf("terminal error = %v, want %v", err, tc.want)
			}
			if _, err = c.Write([]byte("x")); !errors.Is(err, tc.want) {
				t.Fatalf("sticky terminal error = %v, want %v", err, tc.want)
			}
		})
	}

	raw := &scriptedRawConn{reads: []rawRead{{data: []byte("x")}}}
	c := readyScriptedConn(raw, &scriptedEngine{
		reads:     []engineResult{{err: shim.ErrWantRead}},
		bioWrites: []engineResult{{n: 0}},
	})
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("zero BIO progress error = %v", err)
	}

	timeoutErr := refreshableTimeoutError{}
	raw = &scriptedRawConn{reads: []rawRead{
		{data: []byte("a"), err: timeoutErr},
		{data: []byte("b")},
	}}
	engine := &scriptedEngine{
		reads: []engineResult{
			{err: shim.ErrWantRead}, {n: 1},
			{err: shim.ErrWantRead},
			{err: shim.ErrWantRead}, {n: 1},
		},
	}
	c = readyScriptedConn(raw, engine)
	if n, err := c.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatalf("Read before deferred timeout = (%d, %v)", n, err)
	}
	if n, err := c.Read(make([]byte, 1)); n != 0 || !errors.As(err, &timeoutErr) {
		t.Fatalf("deferred timeout = (%d, %v)", n, err)
	}
	if n, err := c.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatalf("Read after deferred timeout = (%d, %v)", n, err)
	}
	if len(raw.reads) != 0 || !bytes.Equal(engine.in, []byte("ab")) {
		t.Fatalf("remaining raw reads=%d, input=%q", len(raw.reads), engine.in)
	}
}

func TestOutputFlushIsLosslessAndAccountsPlaintext(t *testing.T) {
	raw := &scriptedRawConn{writes: []engineResult{{n: 2}, {n: 4}}}
	c := newConnWithEngine(raw, &scriptedEngine{handshakes: []engineResult{{out: []byte("abcdef")}}})
	if err := c.HandshakeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw.written, []byte("abcdef")) {
		t.Fatalf("handshake wire output = %q", raw.written)
	}

	raw = &scriptedRawConn{}
	c = newConnWithEngine(raw, &scriptedEngine{handshakes: []engineResult{
		{err: shim.ErrWantWrite, out: []byte("flight")}, {},
	}})
	if err := c.HandshakeContext(context.Background()); err != nil || !bytes.Equal(raw.written, []byte("flight")) {
		t.Fatalf("WANT_WRITE handshake = %v, output=%q", err, raw.written)
	}

	wireErr := errors.New("wire failed")
	raw = &scriptedRawConn{writes: []engineResult{{n: 2, err: wireErr}}}
	c = newConnWithEngine(raw, &scriptedEngine{handshakes: []engineResult{{out: []byte("flight")}}})
	if err := c.HandshakeContext(context.Background()); !errors.Is(err, wireErr) || !bytes.Equal(c.pendingOut, []byte("ight")) {
		t.Fatalf("short-error handshake = %v, tail=%q", err, c.pendingOut)
	}

	raw = &scriptedRawConn{writes: []engineResult{{n: 2, err: wireErr}}}
	c = readyScriptedConn(raw, &scriptedEngine{writes: []engineResult{{n: 3, out: []byte("cipher")}}})
	n, err := c.Write([]byte("app"))
	if n != 3 || !errors.Is(err, wireErr) || !bytes.Equal(c.pendingOut, []byte("pher")) {
		t.Fatalf("Write = (%d, %v), tail=%q", n, err, c.pendingOut)
	}
	if _, err := c.Write([]byte("x")); !errors.Is(err, wireErr) {
		t.Fatalf("sticky write error = %v", err)
	}

	raw = &scriptedRawConn{writes: []engineResult{{n: 0}}}
	c = readyScriptedConn(raw, &scriptedEngine{reads: []engineResult{{n: 2, out: []byte("alert")}}})
	n, err = c.Read(make([]byte, 2))
	if n != 2 || !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("Read with control flush = (%d, %v)", n, err)
	}

	raw = &scriptedRawConn{writes: []engineResult{{n: 2}}}
	c = readyScriptedConn(raw, &scriptedEngine{reads: []engineResult{{n: 1, out: []byte("alert")}}})
	n, err = c.Read(make([]byte, 1))
	if n != 1 || err != nil || !bytes.Equal(raw.written, []byte("alert")) {
		t.Fatalf("Read with short control flush = (%d, %v), output=%q", n, err, raw.written)
	}

	raw = &scriptedRawConn{}
	c = readyScriptedConn(raw, &scriptedEngine{reads: []engineResult{
		{err: shim.ErrWantWrite, out: []byte("write-progress")}, {n: 1},
	}})
	if n, err := c.Read(make([]byte, 1)); n != 1 || err != nil ||
		!bytes.Equal(raw.written, []byte("write-progress")) {
		t.Fatalf("Read progressing pending Write = (%d, %v), output=%q", n, err, raw.written)
	}

	raw = &scriptedRawConn{}
	c = readyScriptedConn(raw, &scriptedEngine{writes: []engineResult{
		{err: shim.ErrWantWrite, out: []byte("flight")},
		{n: 1, out: []byte("record")},
	}})
	n, err = c.Write([]byte("x"))
	if n != 1 || err != nil || !bytes.Equal(raw.written, []byte("flightrecord")) {
		t.Fatalf("WANT_WRITE transition = (%d, %v), output=%q", n, err, raw.written)
	}

	c = readyScriptedConn(&scriptedRawConn{}, &scriptedEngine{
		writes: []engineResult{{err: shim.ErrWantWrite}},
	})
	if _, err := c.Write([]byte("x")); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("zero-progress WANT_WRITE error = %v", err)
	}

	raw = &scriptedRawConn{reads: []rawRead{{data: []byte("peer-control")}}}
	engine := &scriptedEngine{writes: []engineResult{{err: shim.ErrWantRead}, {n: 1}}}
	c = readyScriptedConn(raw, engine)
	if n, err := c.Write([]byte("x")); n != 1 || err != nil ||
		!bytes.Equal(engine.in, []byte("peer-control")) {
		t.Fatalf("WANT_READ Write = (%d, %v), input=%q", n, err, engine.in)
	}

	timeoutErr := refreshableTimeoutError{}
	raw = &scriptedRawConn{reads: []rawRead{{err: timeoutErr}}}
	c = readyScriptedConn(raw, &scriptedEngine{writes: []engineResult{
		{err: shim.ErrWantRead}, {n: 1},
	}})
	if n, err := c.Write([]byte("x")); n != 0 || !errors.As(err, &timeoutErr) {
		t.Fatalf("WANT_READ Write timeout = (%d, %v)", n, err)
	}
	if n, err := c.Write([]byte("x")); n != 1 || err != nil {
		t.Fatalf("Write after timeout = (%d, %v)", n, err)
	}

	c = readyScriptedConn(&scriptedRawConn{writes: []engineResult{{n: 0}}}, &scriptedEngine{
		writes: []engineResult{{n: 1, out: []byte("record")}},
	})
	if n, err := c.Write([]byte("x")); n != 1 || !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("zero raw-write progress = (%d, %v)", n, err)
	}
}

func TestReadControlOutputPrecedesConcurrentWriteOutput(t *testing.T) {
	raw := &scriptedRawConn{}
	engine := &blockingReadEngine{
		scriptedEngine: scriptedEngine{
			writes: []engineResult{{n: 1, out: []byte("application")}},
		},
		readStarted: make(chan struct{}),
		releaseRead: make(chan struct{}),
	}
	c := readyScriptedConn(raw, engine)

	readDone := make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 1))
		readDone <- err
	}()
	select {
	case <-engine.readStarted:
	case <-time.After(time.Second):
		t.Fatal("Read did not enter the TLS engine")
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := c.Write([]byte("x"))
		writeDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for c.outMu.TryLock() {
		c.outMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("Write did not acquire the output lock")
		}
		time.Sleep(time.Millisecond)
	}
	close(engine.releaseRead)
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("Read error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not finish")
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("Write error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Write did not finish")
	}
	if !bytes.Equal(raw.written, []byte("controlapplication")) {
		t.Fatalf("wire output = %q", raw.written)
	}
}

func TestHandshakeStateAndCancellationCleanup(t *testing.T) {
	raw := &scriptedRawConn{}
	c := newConnWithEngine(raw, &scriptedEngine{handshakes: []engineResult{{}}})
	if err := c.HandshakeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(raw.deadlines) != 0 {
		t.Fatalf("background handshake changed deadline: %v", raw.deadlines)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.HandshakeContext(ctx); err != nil {
		t.Fatalf("completed handshake with canceled context: %v", err)
	}

	raw = &scriptedRawConn{}
	c = newConnWithEngine(raw, &scriptedEngine{handshakes: []engineResult{{err: shim.ErrWantRead}}})
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	err := c.HandshakeContext(ctx)
	if !errors.Is(err, context.Canceled) || len(raw.deadlines) == 0 || !raw.deadlines[len(raw.deadlines)-1].IsZero() {
		t.Fatalf("canceled handshake = %v, deadlines=%v", err, raw.deadlines)
	}
	if err := c.HandshakeContext(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("failed handshake was retried: %v", err)
	}
}

type deadlineBarrierConn struct {
	*scriptedRawConn
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *deadlineBarrierConn) SetDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		c.once.Do(func() {
			close(c.started)
			<-c.release
		})
	}
	return c.scriptedRawConn.SetDeadline(deadline)
}

type handshakeBarrierEngine struct {
	*scriptedEngine
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *handshakeBarrierEngine) Handshake() error {
	e.once.Do(func() { close(e.started) })
	<-e.release
	return nil
}

func TestHandshakeCancellationSynchronizesDeadlineCallback(t *testing.T) {
	raw := &deadlineBarrierConn{
		scriptedRawConn: &scriptedRawConn{},
		started:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	engine := &handshakeBarrierEngine{
		scriptedEngine: &scriptedEngine{},
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	c := newConnWithEngine(raw, engine)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- c.HandshakeContext(ctx) }()

	<-engine.started
	cancel()
	<-raw.started
	close(engine.release)
	select {
	case err := <-result:
		t.Fatalf("handshake returned before deadline callback completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(raw.release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("HandshakeContext error = %v", err)
	}
	if len(raw.deadlines) != 2 || raw.deadlines[0].IsZero() || !raw.deadlines[1].IsZero() {
		t.Fatalf("deadline sequence = %v", raw.deadlines)
	}
}

func TestClosedStateAndConstructorValidation(t *testing.T) {
	if !Supported() {
		t.Fatal("Supported returned false in a CGO build")
	}
	c := newConnWithEngine(&scriptedRawConn{}, &scriptedEngine{})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed Read error = %v", err)
	}
	if _, err := c.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed Write error = %v", err)
	}

	c = readyScriptedConn(&scriptedRawConn{}, &scriptedEngine{})
	c.state = stateShuttingDown
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("shutting-down Read error = %v", err)
	}
	if _, err := c.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("shutting-down Write error = %v", err)
	}

	cert, key := generateTestCert(t)
	if _, err := Client(nil, &Config{CertPEM: cert, KeyPEM: key}); err == nil {
		t.Fatal("Client accepted nil connection")
	}
	if _, err := NewListener(nil, &Config{CertPEM: cert, KeyPEM: key}); err == nil {
		t.Fatal("NewListener accepted nil listener")
	}
	ln := &trackingListener{}
	if _, err := NewListener(ln, &Config{CertPEM: []byte("bad"), KeyPEM: []byte("bad")}); err == nil || ln.closed {
		t.Fatalf("NewListener malformed config = %v, closed=%v", err, ln.closed)
	}
	ln = &trackingListener{}
	if _, err := NewListener(ln, &Config{
		CertPEM: cert, KeyPEM: key, CipherList: "not-a-real-cipher",
	}); err == nil || ln.closed {
		t.Fatalf("NewListener invalid cipher config = %v, closed=%v", err, ln.closed)
	}
	ln = &trackingListener{}
	listener, err := NewListener(ln, &Config{CertPEM: cert, KeyPEM: key})
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed listener Accept error = %v", err)
	}
}

type trackingListener struct{ closed bool }

func (l *trackingListener) Accept() (net.Conn, error) { return nil, io.EOF }
func (l *trackingListener) Close() error              { l.closed = true; return nil }
func (l *trackingListener) Addr() net.Addr            { return testAddr("listener") }

type closeBarrierConn struct {
	closeOnce sync.Once
	closed    chan struct{}
}

func (c *closeBarrierConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *closeBarrierConn) Write(p []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
		return len(p), nil
	}
}

func (c *closeBarrierConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *closeBarrierConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *closeBarrierConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *closeBarrierConn) SetDeadline(time.Time) error      { return nil }
func (c *closeBarrierConn) SetReadDeadline(time.Time) error  { return nil }
func (c *closeBarrierConn) SetWriteDeadline(time.Time) error { return nil }

type shutdownBarrierEngine struct {
	*scriptedEngine
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *shutdownBarrierEngine) Shutdown() error {
	e.once.Do(func() { close(e.started) })
	<-e.release
	return shim.ErrWantRead
}

func TestShutdownContextConcurrentClose(t *testing.T) {
	raw := &closeBarrierConn{closed: make(chan struct{})}
	engine := &shutdownBarrierEngine{
		scriptedEngine: &scriptedEngine{},
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	c := readyScriptedConn(raw, engine)
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- c.ShutdownContext(context.Background()) }()
	<-engine.started

	closeDone := make(chan error, 1)
	go func() { closeDone <- c.Close() }()
	<-raw.closed
	close(engine.release)

	if err := <-shutdownDone; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ShutdownContext error = %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if !engine.freed || c.ssl != nil {
		t.Fatalf("TLS engine not freed: freed=%v, ssl=%v", engine.freed, c.ssl)
	}
}

func TestShutdownContextSendsCloseNotify(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		client, server := newTestConnPair(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- client.(GracefulConn).ShutdownContext(ctx) }()
		if n, err := server.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("peer Read after graceful shutdown = (%d, %v)", n, err)
		}
		peerDone := make(chan error, 1)
		go func() { peerDone <- server.(GracefulConn).ShutdownContext(ctx) }()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := <-peerDone; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("abrupt", func(t *testing.T) {
		client, server := newTestConnPair(t)
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		if n, err := server.Read(make([]byte, 1)); n != 0 || err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("peer Read after abrupt close = (%d, %v)", n, err)
		}
	})
}

func TestShutdownContextWaitsForPeerAlert(t *testing.T) {
	client, _ := newTestConnPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := client.(GracefulConn).ShutdownContext(ctx)
	var timeout net.Error
	if !errors.Is(err, context.DeadlineExceeded) &&
		(!errors.As(err, &timeout) || !timeout.Timeout()) {
		t.Fatalf("ShutdownContext without peer alert = %v", err)
	}
}
