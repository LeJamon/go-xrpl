//go:build cgo

package peertls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
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
