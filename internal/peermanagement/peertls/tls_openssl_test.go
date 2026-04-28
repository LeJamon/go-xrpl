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
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

func generateTestCert(t *testing.T) (certPEM, keyPEM []byte) {
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
		for i := 0; i < rounds; i++ {
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
