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
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
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
	clientWire, serverWire := net.Pipe()

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
	defer ln.Close()

	wrapped := NewListener(ln, &Config{CertPEM: serverCert, KeyPEM: serverKey})

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
	defer ln.Close()

	wrapped := NewListener(ln, &Config{CertPEM: serverCert, KeyPEM: serverKey})

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
		errCh <- err
	}()
	go func() {
		n, err := serverConn.Write(serverPayload)
		if err == nil && n != len(serverPayload) {
			err = io.ErrShortWrite
		}
		errCh <- err
	}()
	go func() {
		got := make([]byte, len(serverPayload))
		_, err := io.ReadFull(clientConn, got)
		if err == nil && !bytes.Equal(got, serverPayload) {
			err = errors.New("client received corrupted payload")
		}
		errCh <- err
	}()
	go func() {
		got := make([]byte, len(clientPayload))
		_, err := io.ReadFull(serverConn, got)
		if err == nil && !bytes.Equal(got, clientPayload) {
			err = errors.New("server received corrupted payload")
		}
		errCh <- err
	}()

	for range 4 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
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
