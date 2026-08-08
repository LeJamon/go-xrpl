//go:build cgo

package shim

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"math/big"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	tlsVersion10 = 0x0301
	tlsVersion11 = 0x0302
	tlsVersion12 = 0x0303
	tlsVersion13 = 0x0304
)

var (
	testCertOnce sync.Once
	testCertPEM  []byte
	testKeyPEM   []byte
	testCertErr  error
)

func testCertificate(t testing.TB) ([]byte, []byte) {
	t.Helper()
	testCertOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			testCertErr = err
			return
		}
		now := time.Now()
		template := &x509.Certificate{
			SerialNumber:          big.NewInt(1),
			Subject:               pkix.Name{CommonName: "peertls shim test"},
			NotBefore:             now.Add(-time.Minute),
			NotAfter:              now.Add(time.Hour),
			KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			BasicConstraintsValid: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			testCertErr = err
			return
		}
		testCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		testKeyPEM = pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		})
	})
	if testCertErr != nil {
		t.Fatal(testCertErr)
	}
	return append([]byte(nil), testCertPEM...), append([]byte(nil), testKeyPEM...)
}

func TestErrorMappingAndCheckedLength(t *testing.T) {
	for _, test := range []struct {
		code int
		want error
	}{
		{errCodeWantRead, ErrWantRead},
		{errCodeWantWrite, ErrWantWrite},
		{errCodeSyscall, ErrSyscall},
		{errCodeSSL, ErrSSL},
		{errCodeZeroReturn, ErrZeroRet},
		{errCodeWriteRetry, ErrWriteRetry},
		{-12345, ErrOther},
	} {
		err := codeError(test.code, "operation detail")
		if !errors.Is(err, test.want) {
			t.Fatalf("codeError(%d) = %v, want %v", test.code, err, test.want)
		}
		if test.want != ErrWantRead && test.want != ErrWantWrite &&
			test.want != ErrZeroRet && !strings.Contains(err.Error(), "operation detail") {
			t.Fatalf("codeError(%d) lost detail: %v", test.code, err)
		}
	}

	if _, err := checkedOpenSSLLength(-1); !errors.Is(err, ErrLength) {
		t.Fatalf("negative length error = %v", err)
	}
	if _, err := checkedOpenSSLLength(math.MaxInt32); err != nil {
		t.Fatalf("MaxInt32 rejected: %v", err)
	}
	if strconv.IntSize > 32 {
		if _, err := checkedOpenSSLLength(math.MaxInt32 + 1); !errors.Is(err, ErrLength) {
			t.Fatalf("oversized length error = %v", err)
		}
	}
}

func TestContextPolicyCipherValidationAndLifetime(t *testing.T) {
	ctx, err := NewCtx(true)
	if err != nil {
		t.Fatal(err)
	}
	minVersion, maxVersion, err := ctx.protocolBounds()
	if err != nil {
		t.Fatal(err)
	}
	if minVersion != tlsVersion12 || maxVersion != 0 {
		t.Fatalf("protocol bounds = (%#x, %#x), want (%#x, 0)",
			minVersion, maxVersion, tlsVersion12)
	}
	options, required, err := ctx.options()
	if err != nil {
		t.Fatal(err)
	}
	if required == 0 || options&required != required {
		t.Fatalf("TLS options = %#x, required %#x", options, required)
	}
	ctx.Free()
	ctx.Free()
	cert, key := testCertificate(t)
	if err := ctx.UseCertPEM(cert, key); !errors.Is(err, ErrClosed) {
		t.Fatalf("UseCertPEM after Free = %v", err)
	}
	if _, err := ctx.NewSSL(); !errors.Is(err, ErrClosed) {
		t.Fatalf("NewSSL after Free = %v", err)
	}

	for _, cipherList := range []string{"", "not-a-real-cipher"} {
		if _, err := NewCtxWithCipherList(false, cipherList); !errors.Is(err, ErrContext) {
			t.Fatalf("cipher list %q error = %v", cipherList, err)
		}
	}
}

func TestBIOPartialFillAndEmptyDrain(t *testing.T) {
	ctx, err := NewCtx(false)
	if err != nil {
		t.Fatal(err)
	}
	ssl, err := ctx.NewSSL()
	ctx.Free()
	if err != nil {
		t.Fatal(err)
	}
	defer ssl.Free()
	if n, err := ssl.BIORead(make([]byte, 1)); n != 0 || err != nil {
		t.Fatalf("empty BIORead = (%d, %v)", n, err)
	}
	payload := make([]byte, 64*1024)
	n, err := ssl.BIOWrite(payload)
	if err != nil || n <= 0 || n >= len(payload) {
		t.Fatalf("partial BIOWrite = (%d, %v), input=%d", n, err, len(payload))
	}
	if next, err := ssl.BIOWrite(payload[n:]); next != 0 || err != nil {
		t.Fatalf("full BIOWrite retry = (%d, %v)", next, err)
	}
}

func TestPEMFailureDoesNotContaminateLaterOperations(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx, err := NewCtx(false)
	if err != nil {
		t.Fatal(err)
	}
	err = ctx.UseCertPEM([]byte("not a certificate"), []byte("not a key"))
	if !errors.Is(err, ErrSSL) || !strings.Contains(err.Error(), "PEM") {
		t.Fatalf("malformed PEM error = %v", err)
	}
	ctx.Free()
	_, err = ctx.NewSSL()
	if !errors.Is(err, ErrClosed) || strings.Contains(err.Error(), "PEM") {
		t.Fatalf("operation-local error after PEM failure = %v", err)
	}

	cert, _ := testCertificate(t)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	otherKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(otherKey),
	})
	mismatch, err := NewCtx(false)
	if err != nil {
		t.Fatal(err)
	}
	defer mismatch.Free()
	if err := mismatch.UseCertPEM(cert, otherKeyPEM); !errors.Is(err, ErrSSL) {
		t.Fatalf("mismatched certificate and key error = %v", err)
	}

	fresh, err := NewCtx(false)
	if err != nil {
		t.Fatalf("fresh context inherited stale error: %v", err)
	}
	defer fresh.Free()
	ssl, err := fresh.NewSSL()
	if err != nil {
		t.Fatalf("fresh SSL inherited stale error: %v", err)
	}
	ssl.Free()
}

type testPair struct {
	client *SSL
	server *SSL
}

func newTestPair(t testing.TB, cipherList string, minVersion, maxVersion int) testPair {
	t.Helper()
	newContext := func(server bool) *Ctx {
		var ctx *Ctx
		var err error
		if cipherList == "" {
			ctx, err = NewCtx(server)
		} else {
			ctx, err = NewCtxWithCipherList(server, cipherList)
		}
		if err != nil {
			t.Fatal(err)
		}
		cert, key := testCertificate(t)
		if err := ctx.UseCertPEM(cert, key); err != nil {
			ctx.Free()
			t.Fatal(err)
		}
		if minVersion != 0 || maxVersion != 0 {
			if err := ctx.setProtocolBoundsForTest(minVersion, maxVersion); err != nil {
				ctx.Free()
				t.Fatal(err)
			}
		}
		return ctx
	}
	clientCtx := newContext(false)
	serverCtx := newContext(true)
	client, err := clientCtx.NewSSL()
	if err != nil {
		t.Fatal(err)
	}
	server, err := serverCtx.NewSSL()
	clientCtx.Free()
	serverCtx.Free()
	if err != nil {
		client.Free()
		t.Fatal(err)
	}
	t.Cleanup(client.Free)
	t.Cleanup(server.Free)
	return testPair{client: client, server: server}
}

func transferTLS(from, to *SSL) (int, error) {
	buf := make([]byte, 4096)
	total := 0
	for {
		n, err := from.BIORead(buf)
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
		total += n
		for offset := 0; offset < n; {
			written, err := to.BIOWrite(buf[offset:n])
			if err != nil {
				return total, err
			}
			if written == 0 {
				return total, errors.New("BIO transfer made no progress")
			}
			offset += written
		}
	}
}

func driveHandshake(pair testPair) error {
	clientDone := false
	serverDone := false
	for range 1000 {
		if !clientDone {
			err := pair.client.Handshake()
			switch {
			case err == nil:
				clientDone = true
			case errors.Is(err, ErrWantRead), errors.Is(err, ErrWantWrite):
			default:
				return fmt.Errorf("client handshake: %w", err)
			}
		}
		if !serverDone {
			err := pair.server.Handshake()
			switch {
			case err == nil:
				serverDone = true
			case errors.Is(err, ErrWantRead), errors.Is(err, ErrWantWrite):
			default:
				return fmt.Errorf("server handshake: %w", err)
			}
		}
		if _, err := transferTLS(pair.client, pair.server); err != nil {
			return err
		}
		if _, err := transferTLS(pair.server, pair.client); err != nil {
			return err
		}
		if clientDone && serverDone {
			return nil
		}
	}
	return errors.New("TLS handshake did not converge")
}

func TestTLSVersionsFinishedAndDHEPolicy(t *testing.T) {
	for _, version := range []int{tlsVersion12, tlsVersion13} {
		t.Run(fmt.Sprintf("TLS_%x", version), func(t *testing.T) {
			pair := newTestPair(t, "", version, version)
			if got := pair.client.GetFinished(make([]byte, 64)); got != 0 {
				t.Fatalf("Finished before handshake = %d", got)
			}
			if err := driveHandshake(pair); err != nil {
				t.Fatal(err)
			}
			if pair.client.version() != version || pair.server.version() != version {
				t.Fatalf("negotiated versions = (%#x, %#x), want %#x",
					pair.client.version(), pair.server.version(), version)
			}
			assertFinishedPair(t, pair.client, pair.server)
		})
	}

	pair := newTestPair(t, "DHE-RSA-AES128-GCM-SHA256", tlsVersion12, tlsVersion12)
	if err := driveHandshake(pair); err != nil {
		t.Fatalf("DHE handshake: %v", err)
	}
}

func assertFinishedPair(t testing.TB, client, server *SSL) {
	t.Helper()
	clientFinished := make([]byte, 1024)
	serverPeerFinished := make([]byte, 1024)
	clientLen := client.GetFinished(clientFinished)
	serverPeerLen := server.GetPeerFinished(serverPeerFinished)
	if clientLen < 12 || clientLen != serverPeerLen ||
		!bytes.Equal(clientFinished[:clientLen], serverPeerFinished[:serverPeerLen]) {
		t.Fatalf("client Finished mismatch: local=%d peer=%d", clientLen, serverPeerLen)
	}

	serverFinished := make([]byte, 1024)
	clientPeerFinished := make([]byte, 1024)
	serverLen := server.GetFinished(serverFinished)
	clientPeerLen := client.GetPeerFinished(clientPeerFinished)
	if serverLen < 12 || serverLen != clientPeerLen ||
		!bytes.Equal(serverFinished[:serverLen], clientPeerFinished[:clientPeerLen]) {
		t.Fatalf("server Finished mismatch: local=%d peer=%d", serverLen, clientPeerLen)
	}

	small := make([]byte, 1)
	if got := client.GetFinished(small); got != clientLen || small[0] != clientFinished[0] {
		t.Fatalf("undersized Finished = (%d, %x), want (%d, %x)",
			got, small[0], clientLen, clientFinished[0])
	}
	if got := client.GetFinished(nil); got != clientLen {
		t.Fatalf("zero-capacity Finished length = %d, want %d", got, clientLen)
	}
}

func TestTLS10And11AreRejected(t *testing.T) {
	for _, version := range []int{tlsVersion10, tlsVersion11} {
		t.Run(fmt.Sprintf("TLS_%x", version), func(t *testing.T) {
			pair := newTestPair(t, "", version, version)
			serverCtx, err := NewCtx(true)
			if err != nil {
				t.Fatal(err)
			}
			cert, key := testCertificate(t)
			if err := serverCtx.UseCertPEM(cert, key); err != nil {
				t.Fatal(err)
			}
			server, err := serverCtx.NewSSL()
			serverCtx.Free()
			if err != nil {
				t.Fatal(err)
			}
			pair.server.Free()
			pair.server = server
			t.Cleanup(server.Free)
			if err := driveHandshake(pair); err == nil {
				t.Fatal("handshake unexpectedly accepted obsolete TLS version")
			}
		})
	}
}

func TestRetryableWriteUsesStableCOwnedMemory(t *testing.T) {
	pair := newTestPair(t, "", tlsVersion12, tlsVersion12)
	if err := driveHandshake(pair); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("0123456789abcdef"), 4096)

	if n, err := pair.client.Write(payload); n != 0 || !errors.Is(err, ErrWantWrite) {
		t.Fatalf("initial large Write = (%d, %v)", n, err)
	}
	if got := pair.client.pendingWriteSize(); got != len(payload) {
		t.Fatalf("pending write size = %d, want %d", got, len(payload))
	}
	runtime.GC()
	if n, err := pair.client.Write(append([]byte(nil), payload...)); n != 0 || !errors.Is(err, ErrWantWrite) {
		t.Fatalf("same-data moving retry = (%d, %v)", n, err)
	}

	received := make([]byte, 0, len(payload))
	writeDone := false
	for range 1000 {
		if _, err := transferTLS(pair.client, pair.server); err != nil {
			t.Fatal(err)
		}
		for {
			buf := make([]byte, 8192)
			n, err := pair.server.Read(buf)
			if n > 0 {
				received = append(received, buf[:n]...)
			}
			if errors.Is(err, ErrWantRead) {
				break
			}
			if errors.Is(err, ErrWantWrite) {
				if _, transferErr := transferTLS(pair.server, pair.client); transferErr != nil {
					t.Fatal(transferErr)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if !writeDone {
			n, err := pair.client.Write(append([]byte(nil), payload...))
			switch {
			case err == nil:
				if n != len(payload) {
					t.Fatalf("completed Write = %d, want %d", n, len(payload))
				}
				writeDone = true
			case errors.Is(err, ErrWantWrite), errors.Is(err, ErrWantRead):
			default:
				t.Fatal(err)
			}
		}
		if writeDone && len(received) == len(payload) {
			break
		}
	}
	if !writeDone || !bytes.Equal(received, payload) {
		t.Fatalf("large write completion=%v received=%d want=%d", writeDone, len(received), len(payload))
	}
	if got := pair.client.pendingWriteSize(); got != 0 {
		t.Fatalf("pending write retained after success: %d", got)
	}
}

func TestReadProgressReleasesCompletedPendingWrite(t *testing.T) {
	pair := newTestPair(t, "", tlsVersion12, tlsVersion12)
	if err := driveHandshake(pair); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("read-side-progress"), 4096)
	if _, err := pair.client.Write(payload); !errors.Is(err, ErrWantWrite) {
		t.Fatalf("initial Write error = %v", err)
	}

	received := make([]byte, 0, len(payload))
	drainServer := func() {
		t.Helper()
		for {
			buf := make([]byte, 8192)
			n, err := pair.server.Read(buf)
			if n > 0 {
				received = append(received, buf[:n]...)
			}
			if errors.Is(err, ErrWantRead) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	completed := false
	for range 1000 {
		if _, err := transferTLS(pair.client, pair.server); err != nil {
			t.Fatal(err)
		}
		drainServer()
		_, err := pair.client.Read(make([]byte, 1))
		if pair.client.pendingWriteSize() == 0 {
			if !errors.Is(err, ErrWantRead) {
				t.Fatalf("Read completing pending Write error = %v", err)
			}
			completed = true
			break
		}
		if !errors.Is(err, ErrWantWrite) {
			t.Fatalf("Read progressing pending Write error = %v", err)
		}
	}
	if !completed {
		t.Fatal("Read did not complete the pending Write")
	}

	retry := append([]byte(nil), payload...)
	if n, err := pair.client.Write(retry); err != nil || n != len(payload) {
		t.Fatalf("completed Write acknowledgment = (%d, %v), want (%d, nil)", n, err, len(payload))
	}

	for range 1000 {
		if _, err := transferTLS(pair.client, pair.server); err != nil {
			t.Fatal(err)
		}
		drainServer()
		if len(received) == len(payload) {
			break
		}
	}
	if !bytes.Equal(received, payload) {
		t.Fatalf("received %d bytes after read-side completion, want %d", len(received), len(payload))
	}
}

func TestPendingWriteReleaseOnMismatchAndFree(t *testing.T) {
	for _, test := range []struct {
		name string
		end  func(*testing.T, *SSL, []byte)
	}{
		{
			name: "mismatch",
			end: func(t *testing.T, ssl *SSL, payload []byte) {
				changed := append([]byte(nil), payload...)
				changed[0] ^= 0xff
				if _, err := ssl.Write(changed); !errors.Is(err, ErrWriteRetry) {
					t.Fatalf("mismatched retry error = %v", err)
				}
				if got := ssl.pendingWriteSize(); got != 0 {
					t.Fatalf("pending write after mismatch = %d", got)
				}
				if _, err := ssl.BIORead(make([]byte, 1)); !errors.Is(err, ErrOther) {
					t.Fatalf("BIORead after mismatch error = %v", err)
				}
				if _, err := ssl.BIOWrite([]byte("x")); !errors.Is(err, ErrOther) {
					t.Fatalf("BIOWrite after mismatch error = %v", err)
				}
			},
		},
		{
			name: "free",
			end: func(t *testing.T, ssl *SSL, _ []byte) {
				ssl.Free()
				ssl.Free()
				if got := ssl.pendingWriteSize(); got != 0 {
					t.Fatalf("pending write after Free = %d", got)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pair := newTestPair(t, "", tlsVersion12, tlsVersion12)
			if err := driveHandshake(pair); err != nil {
				t.Fatal(err)
			}
			payload := bytes.Repeat([]byte("x"), 64*1024)
			if _, err := pair.client.Write(payload); !errors.Is(err, ErrWantWrite) {
				t.Fatalf("initial Write error = %v", err)
			}
			test.end(t, pair.client, payload)
		})
	}
}

func TestCleanShutdownStateMachine(t *testing.T) {
	pair := newTestPair(t, "", tlsVersion13, tlsVersion13)
	if err := driveHandshake(pair); err != nil {
		t.Fatal(err)
	}
	if err := pair.client.Shutdown(); !errors.Is(err, ErrWantRead) {
		t.Fatalf("client first Shutdown = %v", err)
	}
	if _, err := transferTLS(pair.client, pair.server); err != nil {
		t.Fatal(err)
	}
	if _, err := pair.server.Read(make([]byte, 1)); !errors.Is(err, ErrZeroRet) {
		t.Fatalf("server read close_notify = %v", err)
	}
	serverErr := pair.server.Shutdown()
	if serverErr != nil && !errors.Is(serverErr, ErrWantRead) {
		t.Fatalf("server Shutdown = %v", serverErr)
	}
	if _, err := transferTLS(pair.server, pair.client); err != nil {
		t.Fatal(err)
	}
	if err := pair.client.Shutdown(); err != nil {
		t.Fatalf("client final Shutdown = %v", err)
	}
}

func TestNilAndFreedHandles(t *testing.T) {
	var ctx *Ctx
	ctx.Free()
	if _, err := ctx.NewSSL(); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil context NewSSL = %v", err)
	}
	var ssl *SSL
	ssl.Free()
	if err := ssl.Handshake(); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil SSL Handshake = %v", err)
	}
	if _, err := ssl.Read([]byte{0}); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil SSL Read = %v", err)
	}
	if _, err := ssl.Write([]byte{0}); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil SSL Write = %v", err)
	}
	if _, err := ssl.Read(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil SSL zero-length Read = %v", err)
	}
	if _, err := ssl.Write(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil SSL zero-length Write = %v", err)
	}
	if _, err := ssl.BIORead(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil SSL zero-length BIORead = %v", err)
	}
	if _, err := ssl.BIOWrite(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil SSL zero-length BIOWrite = %v", err)
	}

	ctx, err := NewCtx(false)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Free()
	freed, err := ctx.NewSSL()
	if err != nil {
		t.Fatal(err)
	}
	freed.Free()
	if _, err := freed.Read(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("freed SSL zero-length Read = %v", err)
	}
	if _, err := freed.BIOWrite(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("freed SSL zero-length BIOWrite = %v", err)
	}
}
