# TLS 1.2 Session-Signature (Issue #269) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Go's reflection-based TLS Finished extraction with a CGO+OpenSSL shim so that peer-to-peer handshakes compute identical 32-byte `sharedValue`s on both sides and full session-signature verification works against rippled, removing the `VerifyHandshakeHeadersNoSig` compromise.

**Architecture:** New `internal/peermanagement/peertls/` package exposes a `PeerConn` interface (`net.Conn` + `HandshakeContext` + `SharedValue()`). Backed by a ~250-line C shim around OpenSSL using `BIO_pair` memory BIOs (Go pumps bytes between TCP and the BIO so deadlines/cancel stay in Go). `//go:build cgo` separates the OpenSSL impl from a `//go:build !cgo` stub that returns `ErrSessionSigUnsupported`. `peer.go` and `overlay.go` swap from `*tls.Conn` to `peertls.PeerConn`. Spec at `tasks/pr-handshake-session-sig.md`.

**Tech Stack:** Go 1.24, CGO, OpenSSL ≥1.1.1 (shipped on alpine + Ubuntu + macOS Homebrew). `pkg-config` for header discovery. `libssl_static` + musl for the production Docker build.

---

## Task 1: Set up worktree and branch

**Files:**
- Create worktree: `goXRPL/.worktrees/handshake-session-sig-269/`

- [ ] **Step 1: Create the worktree**

```bash
cd /Users/thomashussenet/Documents/project_goXRPL/goXRPL
git worktree add .worktrees/handshake-session-sig-269 -b feature/handshake-session-sig-269
```

- [ ] **Step 2: Verify the worktree is clean**

```bash
cd .worktrees/handshake-session-sig-269
git status
```

Expected output: `On branch feature/handshake-session-sig-269` and `nothing to commit, working tree clean`.

- [ ] **Step 3: Confirm baseline tests pass before any change**

```bash
go test -count=1 ./internal/peermanagement/...
```

Expected: PASS. If any pre-existing failure shows up, note it and confirm it's unrelated before proceeding.

> **All subsequent file paths in this plan are relative to `goXRPL/.worktrees/handshake-session-sig-269/`.**

---

## Task 2: Create the `peertls` package skeleton (interface, errors, types)

**Files:**
- Create: `internal/peermanagement/peertls/peertls.go`

- [ ] **Step 1: Write the package file**

```go
// Package peertls provides the TLS 1.2 transport for XRPL peer-to-peer
// connections. Unlike crypto/tls, it exposes the post-handshake Finished
// bytes via SharedValue() so the rippled session-signature verification
// can succeed both client- and server-side. The OpenSSL-backed
// implementation lives in tls_openssl.go (build tag: cgo); a stub
// returning ErrSessionSigUnsupported lives in tls_stub.go (build tag:
// !cgo).
package peertls

import (
	"context"
	"errors"
	"net"
)

// PeerConn is the TLS connection abstraction used by the peer subsystem.
// All methods of net.Conn are passed through to the underlying TLS
// stream. HandshakeContext drives the handshake explicitly.
// SharedValue returns the 32-byte sha512Half(c1 ^ c2) of the local and
// peer Finished messages, matching rippled's makeSharedValue.
type PeerConn interface {
	net.Conn
	HandshakeContext(ctx context.Context) error
	SharedValue() ([]byte, error)
}

// Config is the per-connection TLS configuration. It deliberately omits
// CA pools and verification toggles: rippled peers do not trust certs,
// they trust the public key advertised in the Public-Key header. The
// shim sets SSL_VERIFY_NONE.
type Config struct {
	// CertPEM is the PEM-encoded x509 certificate (concatenated chain
	// allowed). Required.
	CertPEM []byte
	// KeyPEM is the PEM-encoded private key. Required.
	KeyPEM []byte
}

// ErrSessionSigUnsupported is returned by every constructor in non-CGO
// builds. The peer subsystem cannot operate without it; RPC, WebSocket,
// tx, and other subsystems are unaffected.
var ErrSessionSigUnsupported = errors.New(
	"peertls: session-signature TLS requires CGO + OpenSSL; rebuild with CGO_ENABLED=1")

// ErrHandshakeIncomplete is returned by SharedValue if called before a
// successful HandshakeContext.
var ErrHandshakeIncomplete = errors.New("peertls: handshake not complete")
```

- [ ] **Step 2: Run go vet**

```bash
go vet ./internal/peermanagement/peertls/
```

Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add internal/peermanagement/peertls/peertls.go
git commit -m "feat(peertls): add package skeleton with PeerConn interface"
```

---

## Task 3: Create the `!cgo` stub

**Files:**
- Create: `internal/peermanagement/peertls/tls_stub.go`

- [ ] **Step 1: Write the stub**

```go
//go:build !cgo

package peertls

import (
	"context"
	"net"
	"time"
)

// Client returns ErrSessionSigUnsupported. Non-CGO builds cannot perform
// the rippled session-signature handshake.
func Client(_ net.Conn, _ *Config) (PeerConn, error) {
	return nil, ErrSessionSigUnsupported
}

// NewListener wraps inner but every Accept returns ErrSessionSigUnsupported.
func NewListener(inner net.Listener, _ *Config) net.Listener {
	return &stubListener{inner: inner}
}

type stubListener struct{ inner net.Listener }

func (s *stubListener) Accept() (net.Conn, error) { return nil, ErrSessionSigUnsupported }
func (s *stubListener) Close() error              { return s.inner.Close() }
func (s *stubListener) Addr() net.Addr            { return s.inner.Addr() }

// stubConn is unused at runtime in a non-CGO build (constructors fail
// first), but kept to ensure the type system has a PeerConn implementation
// the rest of the codebase can reference under !cgo.
type stubConn struct{}

var _ PeerConn = (*stubConn)(nil)

func (s *stubConn) Read([]byte) (int, error)              { return 0, ErrSessionSigUnsupported }
func (s *stubConn) Write([]byte) (int, error)             { return 0, ErrSessionSigUnsupported }
func (s *stubConn) Close() error                          { return ErrSessionSigUnsupported }
func (s *stubConn) LocalAddr() net.Addr                   { return nil }
func (s *stubConn) RemoteAddr() net.Addr                  { return nil }
func (s *stubConn) SetDeadline(time.Time) error           { return ErrSessionSigUnsupported }
func (s *stubConn) SetReadDeadline(time.Time) error       { return ErrSessionSigUnsupported }
func (s *stubConn) SetWriteDeadline(time.Time) error      { return ErrSessionSigUnsupported }
func (s *stubConn) HandshakeContext(context.Context) error { return ErrSessionSigUnsupported }
func (s *stubConn) SharedValue() ([]byte, error)          { return nil, ErrSessionSigUnsupported }
```

- [ ] **Step 2: Confirm it compiles under !cgo**

```bash
CGO_ENABLED=0 go build ./internal/peermanagement/peertls/
```

Expected: success (no output).

- [ ] **Step 3: Commit**

```bash
git add internal/peermanagement/peertls/tls_stub.go
git commit -m "feat(peertls): add !cgo stub returning ErrSessionSigUnsupported"
```

---

## Task 4: Create the CGO shim header and skeleton

**Files:**
- Create: `internal/peermanagement/peertls/shim/shim.h`
- Create: `internal/peermanagement/peertls/shim/shim.c`

- [ ] **Step 1: Write `shim.h`**

```c
/* shim.h - thin OpenSSL shim for XRPL peer TLS.
 *
 * Connection model: the SSL object is bound to a memory BIO_pair (no fd
 * is passed to OpenSSL). Go pumps bytes between the network BIO and the
 * underlying net.Conn; OpenSSL only ever sees memory buffers. This
 * keeps deadlines, context cancellation, and goroutine teardown on the
 * Go side.
 *
 * Error convention: shim functions returning int return:
 *   >  0   bytes processed (read/write/bio_read/bio_write)
 *   == 0   clean shutdown / EOF
 *   <  0   negative SSL_get_error code (one of PEERTLS_ERR_*)
 */

#ifndef PEERTLS_SHIM_H
#define PEERTLS_SHIM_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct peertls_ctx peertls_ctx;
typedef struct peertls_ssl peertls_ssl;

/* Error codes (negative). Matches SSL_ERROR_* but stable across OpenSSL
 * versions and non-overlapping with positive byte counts. */
#define PEERTLS_ERR_WANT_READ  -1
#define PEERTLS_ERR_WANT_WRITE -2
#define PEERTLS_ERR_SYSCALL    -3
#define PEERTLS_ERR_SSL        -4
#define PEERTLS_ERR_ZERO_RET   -5
#define PEERTLS_ERR_OTHER      -99

/* Context lifecycle. is_server=1 for a server-role context (uses
 * TLS_server_method), 0 for client. The context owns the cert/key. */
peertls_ctx* peertls_ctx_new(int is_server);
void         peertls_ctx_free(peertls_ctx* ctx);

/* Load an X509 certificate + private key from PEM buffers. Returns 0 on
 * success, negative error code on failure. */
int peertls_ctx_use_cert_pem(peertls_ctx* ctx,
                              const char* cert, int cert_len,
                              const char* key,  int key_len);

/* SSL lifecycle. peertls_new returns a new SSL bound to a fresh
 * BIO_pair. Once created, all I/O goes through peertls_{read,write} and
 * peertls_bio_{read,write}. */
peertls_ssl* peertls_new(peertls_ctx* ctx);
void         peertls_free(peertls_ssl* s);

/* Drive the handshake. Returns 0 on success, PEERTLS_ERR_WANT_READ or
 * PEERTLS_ERR_WANT_WRITE if more network I/O is needed, or another
 * negative error. The Go pump loops on this function. */
int peertls_handshake(peertls_ssl* s);

/* Encrypted application I/O. Same return convention as handshake. */
int peertls_read (peertls_ssl* s, void* buf, int len);
int peertls_write(peertls_ssl* s, const void* buf, int len);

/* Drain/fill the network BIO. peertls_bio_read pulls outgoing TLS
 * record bytes that OpenSSL has produced; peertls_bio_write feeds
 * incoming TLS record bytes from the wire into OpenSSL. Returns the
 * number of bytes processed; 0 means no bytes available / no space. */
int peertls_bio_read (peertls_ssl* s, void* buf, int len);
int peertls_bio_write(peertls_ssl* s, const void* buf, int len);

/* TLS 1.2 Finished bytes. Returns the number of bytes copied (typically
 * 12 for TLS 1.2), or 0 if the handshake hasn't completed. */
int peertls_get_finished     (peertls_ssl* s, void* buf, int len);
int peertls_get_peer_finished(peertls_ssl* s, void* buf, int len);

/* Send TLS close_notify. Idempotent. */
int peertls_shutdown(peertls_ssl* s);

/* Returns a static pointer to the most recent SSL error string in this
 * thread. Only valid until the next OpenSSL call on this thread. */
const char* peertls_last_error(void);

#ifdef __cplusplus
}
#endif

#endif /* PEERTLS_SHIM_H */
```

- [ ] **Step 2: Write `shim.c`**

```c
/* shim.c - implementation of shim.h. */

#include "shim.h"

#include <openssl/bio.h>
#include <openssl/err.h>
#include <openssl/pem.h>
#include <openssl/ssl.h>
#include <stdlib.h>
#include <string.h>

struct peertls_ctx {
    SSL_CTX* ctx;
    int is_server;
};

struct peertls_ssl {
    SSL* ssl;
    BIO* internal_bio;  /* bound to ssl */
    BIO* network_bio;   /* the side Go pumps */
};

static int map_ssl_error(SSL* ssl, int rc) {
    int err = SSL_get_error(ssl, rc);
    switch (err) {
        case SSL_ERROR_NONE:        return 0;
        case SSL_ERROR_ZERO_RETURN: return PEERTLS_ERR_ZERO_RET;
        case SSL_ERROR_WANT_READ:   return PEERTLS_ERR_WANT_READ;
        case SSL_ERROR_WANT_WRITE:  return PEERTLS_ERR_WANT_WRITE;
        case SSL_ERROR_SYSCALL:     return PEERTLS_ERR_SYSCALL;
        case SSL_ERROR_SSL:         return PEERTLS_ERR_SSL;
        default:                    return PEERTLS_ERR_OTHER;
    }
}

peertls_ctx* peertls_ctx_new(int is_server) {
    const SSL_METHOD* m = is_server ? TLS_server_method() : TLS_client_method();
    SSL_CTX* ctx = SSL_CTX_new(m);
    if (!ctx) return NULL;

    /* Force TLS 1.2 only — matches rippled. */
    SSL_CTX_set_min_proto_version(ctx, TLS1_2_VERSION);
    SSL_CTX_set_max_proto_version(ctx, TLS1_2_VERSION);

    /* Rippled peers don't validate certs (Public-Key header is the trust
     * anchor). Match the existing InsecureSkipVerify behavior. */
    SSL_CTX_set_verify(ctx, SSL_VERIFY_NONE, NULL);

    peertls_ctx* out = calloc(1, sizeof(*out));
    if (!out) {
        SSL_CTX_free(ctx);
        return NULL;
    }
    out->ctx = ctx;
    out->is_server = is_server;
    return out;
}

void peertls_ctx_free(peertls_ctx* ctx) {
    if (!ctx) return;
    if (ctx->ctx) SSL_CTX_free(ctx->ctx);
    free(ctx);
}

int peertls_ctx_use_cert_pem(peertls_ctx* ctx,
                              const char* cert, int cert_len,
                              const char* key,  int key_len) {
    if (!ctx || !ctx->ctx) return PEERTLS_ERR_OTHER;

    BIO* cb = BIO_new_mem_buf(cert, cert_len);
    if (!cb) return PEERTLS_ERR_OTHER;
    X509* x = PEM_read_bio_X509(cb, NULL, NULL, NULL);
    BIO_free(cb);
    if (!x) return PEERTLS_ERR_SSL;

    if (SSL_CTX_use_certificate(ctx->ctx, x) != 1) {
        X509_free(x);
        return PEERTLS_ERR_SSL;
    }
    X509_free(x);

    BIO* kb = BIO_new_mem_buf(key, key_len);
    if (!kb) return PEERTLS_ERR_OTHER;
    EVP_PKEY* pk = PEM_read_bio_PrivateKey(kb, NULL, NULL, NULL);
    BIO_free(kb);
    if (!pk) return PEERTLS_ERR_SSL;

    if (SSL_CTX_use_PrivateKey(ctx->ctx, pk) != 1) {
        EVP_PKEY_free(pk);
        return PEERTLS_ERR_SSL;
    }
    EVP_PKEY_free(pk);

    if (SSL_CTX_check_private_key(ctx->ctx) != 1) {
        return PEERTLS_ERR_SSL;
    }
    return 0;
}

peertls_ssl* peertls_new(peertls_ctx* ctx) {
    if (!ctx || !ctx->ctx) return NULL;

    SSL* ssl = SSL_new(ctx->ctx);
    if (!ssl) return NULL;

    BIO* internal = NULL;
    BIO* network  = NULL;
    /* 0 size == default 17 KiB, large enough for any TLS record. */
    if (BIO_new_bio_pair(&internal, 0, &network, 0) != 1) {
        SSL_free(ssl);
        return NULL;
    }

    SSL_set_bio(ssl, internal, internal);

    if (ctx->is_server) {
        SSL_set_accept_state(ssl);
    } else {
        SSL_set_connect_state(ssl);
    }

    peertls_ssl* out = calloc(1, sizeof(*out));
    if (!out) {
        SSL_free(ssl);
        BIO_free(network);
        return NULL;
    }
    out->ssl = ssl;
    out->internal_bio = internal; /* freed by SSL_free */
    out->network_bio = network;
    return out;
}

void peertls_free(peertls_ssl* s) {
    if (!s) return;
    if (s->ssl) SSL_free(s->ssl);
    if (s->network_bio) BIO_free(s->network_bio);
    free(s);
}

int peertls_handshake(peertls_ssl* s) {
    if (!s || !s->ssl) return PEERTLS_ERR_OTHER;
    int rc = SSL_do_handshake(s->ssl);
    if (rc == 1) return 0;
    return map_ssl_error(s->ssl, rc);
}

int peertls_read(peertls_ssl* s, void* buf, int len) {
    if (!s || !s->ssl) return PEERTLS_ERR_OTHER;
    int rc = SSL_read(s->ssl, buf, len);
    if (rc > 0) return rc;
    return map_ssl_error(s->ssl, rc);
}

int peertls_write(peertls_ssl* s, const void* buf, int len) {
    if (!s || !s->ssl) return PEERTLS_ERR_OTHER;
    int rc = SSL_write(s->ssl, buf, len);
    if (rc > 0) return rc;
    return map_ssl_error(s->ssl, rc);
}

int peertls_bio_read(peertls_ssl* s, void* buf, int len) {
    if (!s || !s->network_bio) return PEERTLS_ERR_OTHER;
    int pending = BIO_ctrl_pending(s->network_bio);
    if (pending == 0) return 0;
    int rc = BIO_read(s->network_bio, buf, len);
    if (rc <= 0) return 0;
    return rc;
}

int peertls_bio_write(peertls_ssl* s, const void* buf, int len) {
    if (!s || !s->network_bio) return PEERTLS_ERR_OTHER;
    int rc = BIO_write(s->network_bio, buf, len);
    if (rc <= 0) return 0;
    return rc;
}

int peertls_get_finished(peertls_ssl* s, void* buf, int len) {
    if (!s || !s->ssl) return 0;
    return (int)SSL_get_finished(s->ssl, buf, (size_t)len);
}

int peertls_get_peer_finished(peertls_ssl* s, void* buf, int len) {
    if (!s || !s->ssl) return 0;
    return (int)SSL_get_peer_finished(s->ssl, buf, (size_t)len);
}

int peertls_shutdown(peertls_ssl* s) {
    if (!s || !s->ssl) return PEERTLS_ERR_OTHER;
    int rc = SSL_shutdown(s->ssl);
    if (rc >= 0) return 0;
    return map_ssl_error(s->ssl, rc);
}

const char* peertls_last_error(void) {
    static __thread char buf[256];
    unsigned long e = ERR_peek_last_error();
    if (e == 0) return "";
    ERR_error_string_n(e, buf, sizeof(buf));
    return buf;
}
```

- [ ] **Step 2: Verify the C compiles standalone with pkg-config**

```bash
gcc -c $(pkg-config --cflags libssl libcrypto) \
    internal/peermanagement/peertls/shim/shim.c \
    -o /tmp/shim.o
```

Expected: success (file `/tmp/shim.o` produced). If `pkg-config` fails on macOS, run `export PKG_CONFIG_PATH="$(brew --prefix openssl@3)/lib/pkgconfig"` first.

Cleanup: `rm /tmp/shim.o`.

- [ ] **Step 3: Commit**

```bash
git add internal/peermanagement/peertls/shim/shim.h internal/peermanagement/peertls/shim/shim.c
git commit -m "feat(peertls): add OpenSSL shim (BIO_pair, get_finished, get_peer_finished)"
```

---

## Task 5: Add the CGO Go bindings

**Files:**
- Create: `internal/peermanagement/peertls/shim/shim.go`

- [ ] **Step 1: Write `shim.go`**

```go
//go:build cgo

// Package shim provides Go bindings for the OpenSSL TLS shim used by
// peertls. It is intentionally low-level: callers in peertls own the
// goroutine pump and the BIO drain/fill cadence. Callers must call
// runtime.LockOSThread before any series of operations that may inspect
// peertls_last_error, since OpenSSL's per-thread error queue is per OS
// thread.
package shim

// #cgo pkg-config: libssl libcrypto
// #include <stdlib.h>
// #include "shim.h"
import "C"

import (
	"errors"
	"unsafe"
)

// Error codes mirrored from shim.h.
const (
	ErrCodeWantRead  = -1
	ErrCodeWantWrite = -2
	ErrCodeSyscall   = -3
	ErrCodeSSL       = -4
	ErrCodeZeroRet   = -5
	ErrCodeOther     = -99
)

var (
	ErrWantRead  = errors.New("peertls/shim: SSL_ERROR_WANT_READ")
	ErrWantWrite = errors.New("peertls/shim: SSL_ERROR_WANT_WRITE")
	ErrSyscall   = errors.New("peertls/shim: SSL_ERROR_SYSCALL")
	ErrSSL       = errors.New("peertls/shim: SSL_ERROR_SSL")
	ErrZeroRet   = errors.New("peertls/shim: SSL_ERROR_ZERO_RETURN")
	ErrOther     = errors.New("peertls/shim: unknown SSL error")
)

// CodeToErr maps a negative shim return code to an error.
func CodeToErr(code int) error {
	switch code {
	case ErrCodeWantRead:
		return ErrWantRead
	case ErrCodeWantWrite:
		return ErrWantWrite
	case ErrCodeSyscall:
		return ErrSyscall
	case ErrCodeSSL:
		return ErrSSL
	case ErrCodeZeroRet:
		return ErrZeroRet
	default:
		return ErrOther
	}
}

// Ctx wraps peertls_ctx*.
type Ctx struct{ p *C.peertls_ctx }

// SSL wraps peertls_ssl*.
type SSL struct{ p *C.peertls_ssl }

// NewCtx creates a new SSL context. isServer selects role.
func NewCtx(isServer bool) (*Ctx, error) {
	flag := C.int(0)
	if isServer {
		flag = 1
	}
	p := C.peertls_ctx_new(flag)
	if p == nil {
		return nil, errors.New("peertls/shim: peertls_ctx_new failed")
	}
	return &Ctx{p: p}, nil
}

// Free releases the context.
func (c *Ctx) Free() {
	if c == nil || c.p == nil {
		return
	}
	C.peertls_ctx_free(c.p)
	c.p = nil
}

// UseCertPEM loads cert + key into the context.
func (c *Ctx) UseCertPEM(cert, key []byte) error {
	if len(cert) == 0 || len(key) == 0 {
		return errors.New("peertls/shim: cert and key must be non-empty")
	}
	rc := C.peertls_ctx_use_cert_pem(
		c.p,
		(*C.char)(unsafe.Pointer(&cert[0])), C.int(len(cert)),
		(*C.char)(unsafe.Pointer(&key[0])), C.int(len(key)),
	)
	if rc != 0 {
		return CodeToErr(int(rc))
	}
	return nil
}

// NewSSL creates an SSL bound to a fresh BIO_pair under the context.
func (c *Ctx) NewSSL() (*SSL, error) {
	p := C.peertls_new(c.p)
	if p == nil {
		return nil, errors.New("peertls/shim: peertls_new failed")
	}
	return &SSL{p: p}, nil
}

// Free releases the SSL and its BIO pair.
func (s *SSL) Free() {
	if s == nil || s.p == nil {
		return
	}
	C.peertls_free(s.p)
	s.p = nil
}

// Handshake drives one step of the TLS handshake. Returns nil on
// completion, ErrWantRead/ErrWantWrite if the pump must do more I/O, or
// another error.
func (s *SSL) Handshake() error {
	rc := C.peertls_handshake(s.p)
	if rc == 0 {
		return nil
	}
	return CodeToErr(int(rc))
}

// Read decrypts up to len(buf) bytes. Returns (n, nil) on success or
// (0, err) when the SSL state machine wants more bytes (caller pumps).
func (s *SSL) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	rc := C.peertls_read(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	if rc > 0 {
		return int(rc), nil
	}
	if rc == 0 {
		return 0, ErrZeroRet
	}
	return 0, CodeToErr(int(rc))
}

// Write encrypts len(buf) bytes. Same return convention as Read.
func (s *SSL) Write(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	rc := C.peertls_write(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	if rc > 0 {
		return int(rc), nil
	}
	return 0, CodeToErr(int(rc))
}

// BIORead drains pending TLS record bytes from the network BIO.
// Returns (n, nil) where n may be 0 if the BIO is empty.
func (s *SSL) BIORead(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	rc := C.peertls_bio_read(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	if rc < 0 {
		return 0, CodeToErr(int(rc))
	}
	return int(rc), nil
}

// BIOWrite feeds raw TLS record bytes into OpenSSL.
func (s *SSL) BIOWrite(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	rc := C.peertls_bio_write(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	if rc < 0 {
		return 0, CodeToErr(int(rc))
	}
	return int(rc), nil
}

// GetFinished copies up to len(buf) bytes of the local-side Finished
// message into buf and returns the number of bytes copied.
func (s *SSL) GetFinished(buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	n := C.peertls_get_finished(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	return int(n)
}

// GetPeerFinished copies up to len(buf) bytes of the peer-side Finished
// message into buf and returns the number of bytes copied.
func (s *SSL) GetPeerFinished(buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	n := C.peertls_get_peer_finished(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	return int(n)
}

// Shutdown sends close_notify.
func (s *SSL) Shutdown() error {
	rc := C.peertls_shutdown(s.p)
	if rc == 0 {
		return nil
	}
	return CodeToErr(int(rc))
}

// LastError returns the latest OpenSSL error string from this thread,
// or "" if none.
func LastError() string {
	c := C.peertls_last_error()
	if c == nil {
		return ""
	}
	return C.GoString(c)
}
```

- [ ] **Step 2: Confirm the package builds with CGO**

```bash
go build ./internal/peermanagement/peertls/shim/
```

Expected: success.

- [ ] **Step 3: Commit**

```bash
git add internal/peermanagement/peertls/shim/shim.go
git commit -m "feat(peertls): add CGO Go bindings for the OpenSSL shim"
```

---

## Task 6: Implement the OpenSSL-backed `PeerConn`

**Files:**
- Create: `internal/peermanagement/peertls/tls_openssl.go`

- [ ] **Step 1: Write `tls_openssl.go`**

```go
//go:build cgo

package peertls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/peertls/shim"
)

const (
	// TLS 1.2 verify_data is exactly 12 bytes for the default PRF; we
	// allocate a comfortable upper bound (RFC 5246 ¶7.4.9 lets cipher
	// suites override but none in our pinned TLS_*WITH_*_GCM* set do).
	finishedBufSize = 64
	// Pump buffer for moving TLS records between BIO and net.Conn.
	pumpBufSize = 16 * 1024
)

// Client wraps an existing net.Conn (typically a *net.TCPConn) as the
// client side of a peertls connection. Caller must subsequently invoke
// HandshakeContext.
func Client(inner net.Conn, cfg *Config) (PeerConn, error) {
	return newConn(inner, cfg, false)
}

// NewListener returns a net.Listener whose Accept produces server-side
// PeerConns.
func NewListener(inner net.Listener, cfg *Config) net.Listener {
	return &listener{inner: inner, cfg: cfg}
}

type listener struct {
	inner net.Listener
	cfg   *Config
}

func (l *listener) Accept() (net.Conn, error) {
	c, err := l.inner.Accept()
	if err != nil {
		return nil, err
	}
	pc, err := newConn(c, l.cfg, true)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return pc, nil
}

func (l *listener) Close() error   { return l.inner.Close() }
func (l *listener) Addr() net.Addr { return l.inner.Addr() }

type conn struct {
	inner net.Conn

	mu        sync.Mutex // guards ssl + handshakeDone
	ctx       *shim.Ctx
	ssl       *shim.SSL
	handshake bool

	closeOnce sync.Once
	closeErr  error
}

func newConn(inner net.Conn, cfg *Config, isServer bool) (*conn, error) {
	if cfg == nil || len(cfg.CertPEM) == 0 || len(cfg.KeyPEM) == 0 {
		return nil, errors.New("peertls: Config requires CertPEM and KeyPEM")
	}
	ctx, err := shim.NewCtx(isServer)
	if err != nil {
		return nil, err
	}
	if err := ctx.UseCertPEM(cfg.CertPEM, cfg.KeyPEM); err != nil {
		ctx.Free()
		return nil, fmt.Errorf("peertls: load cert: %w", err)
	}
	s, err := ctx.NewSSL()
	if err != nil {
		ctx.Free()
		return nil, err
	}
	return &conn{inner: inner, ctx: ctx, ssl: s}, nil
}

func (c *conn) HandshakeContext(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handshake {
		return nil
	}
	if err := c.pumpUntil(ctx, c.ssl.Handshake); err != nil {
		return fmt.Errorf("peertls: handshake: %w", err)
	}
	c.handshake = true
	return nil
}

// pumpUntil runs op repeatedly, moving bytes between the BIO and the
// underlying net.Conn whenever op returns ErrWantRead/ErrWantWrite.
// Honors ctx cancellation by setting a short read deadline on the
// underlying conn.
func (c *conn) pumpUntil(ctx context.Context, op func() error) error {
	for {
		err := op()
		if err == nil {
			// Even on success, drain any final bytes (e.g. last
			// Finished record on the server side) to the wire.
			if drainErr := c.drain(ctx); drainErr != nil {
				return drainErr
			}
			return nil
		}
		switch {
		case errors.Is(err, shim.ErrWantWrite):
			if drainErr := c.drain(ctx); drainErr != nil {
				return drainErr
			}
		case errors.Is(err, shim.ErrWantRead):
			// First flush anything pending we owe the peer (TLS
			// record we just produced), then read.
			if drainErr := c.drain(ctx); drainErr != nil {
				return drainErr
			}
			if fillErr := c.fill(ctx); fillErr != nil {
				return fillErr
			}
		default:
			return err
		}
	}
}

func (c *conn) drain(ctx context.Context) error {
	buf := make([]byte, pumpBufSize)
	for {
		n, err := c.ssl.BIORead(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := c.applyContextDeadline(ctx); err != nil {
			return err
		}
		if _, werr := c.inner.Write(buf[:n]); werr != nil {
			return werr
		}
	}
}

func (c *conn) fill(ctx context.Context) error {
	buf := make([]byte, pumpBufSize)
	if err := c.applyContextDeadline(ctx); err != nil {
		return err
	}
	n, err := c.inner.Read(buf)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	if n == 0 {
		return nil
	}
	if _, werr := c.ssl.BIOWrite(buf[:n]); werr != nil {
		return werr
	}
	return nil
}

func (c *conn) applyContextDeadline(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.inner.SetDeadline(dl)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (c *conn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.handshake {
		return 0, ErrHandshakeIncomplete
	}
	for {
		n, err := c.ssl.Read(b)
		if err == nil {
			return n, nil
		}
		if errors.Is(err, shim.ErrWantRead) {
			if ferr := c.fill(nil); ferr != nil {
				return 0, ferr
			}
			continue
		}
		if errors.Is(err, shim.ErrWantWrite) {
			if derr := c.drain(nil); derr != nil {
				return 0, derr
			}
			continue
		}
		if errors.Is(err, shim.ErrZeroRet) {
			return 0, io.EOF
		}
		return 0, err
	}
}

func (c *conn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.handshake {
		return 0, ErrHandshakeIncomplete
	}
	written := 0
	for written < len(b) {
		n, err := c.ssl.Write(b[written:])
		if err == nil {
			written += n
			// Flush the encrypted record(s) to the wire.
			if derr := c.drain(nil); derr != nil {
				return written, derr
			}
			continue
		}
		if errors.Is(err, shim.ErrWantWrite) {
			if derr := c.drain(nil); derr != nil {
				return written, derr
			}
			continue
		}
		if errors.Is(err, shim.ErrWantRead) {
			if ferr := c.fill(nil); ferr != nil {
				return written, ferr
			}
			continue
		}
		return written, err
	}
	return written, nil
}

func (c *conn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		// Best-effort close_notify; ignore errors.
		if c.handshake {
			_ = c.ssl.Shutdown()
			_ = c.drain(nil)
		}
		if c.ssl != nil {
			c.ssl.Free()
			c.ssl = nil
		}
		if c.ctx != nil {
			c.ctx.Free()
			c.ctx = nil
		}
		c.mu.Unlock()
		c.closeErr = c.inner.Close()
	})
	return c.closeErr
}

func (c *conn) LocalAddr() net.Addr  { return c.inner.LocalAddr() }
func (c *conn) RemoteAddr() net.Addr { return c.inner.RemoteAddr() }

func (c *conn) SetDeadline(t time.Time) error      { return c.inner.SetDeadline(t) }
func (c *conn) SetReadDeadline(t time.Time) error  { return c.inner.SetReadDeadline(t) }
func (c *conn) SetWriteDeadline(t time.Time) error { return c.inner.SetWriteDeadline(t) }

// SharedValue computes the rippled-compatible 32-byte shared value:
// sha512Half(sha512(local_finished) XOR sha512(peer_finished)).
func (c *conn) SharedValue() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.handshake {
		return nil, ErrHandshakeIncomplete
	}
	local := make([]byte, finishedBufSize)
	peer := make([]byte, finishedBufSize)

	ln := c.ssl.GetFinished(local)
	if ln < 12 {
		return nil, fmt.Errorf("peertls: local Finished too short (%d bytes)", ln)
	}
	pn := c.ssl.GetPeerFinished(peer)
	if pn < 12 {
		return nil, fmt.Errorf("peertls: peer Finished too short (%d bytes)", pn)
	}
	return computeSharedValue(local[:ln], peer[:pn])
}
```

- [ ] **Step 2: Add the shared-value computation file**

Create `internal/peermanagement/peertls/shared_value.go` with:

```go
package peertls

import (
	"crypto/sha512"
	"errors"
)

// computeSharedValue mirrors rippled's makeSharedValue:
//
//	sha512Half(sha512(local) XOR sha512(peer))
//
// where sha512Half is the first 32 bytes of SHA-512.
func computeSharedValue(local, peer []byte) ([]byte, error) {
	if len(local) < 12 || len(peer) < 12 {
		return nil, errors.New("peertls: Finished message shorter than 12 bytes")
	}

	h1 := sha512.Sum512(local)
	h2 := sha512.Sum512(peer)

	var xor [64]byte
	allZero := true
	for i := 0; i < 64; i++ {
		xor[i] = h1[i] ^ h2[i]
		if xor[i] != 0 {
			allZero = false
		}
	}
	if allZero {
		return nil, errors.New("peertls: identical local and peer Finished")
	}

	final := sha512.Sum512(xor[:])
	out := make([]byte, 32)
	copy(out, final[:32])
	return out, nil
}
```

- [ ] **Step 3: Build the package**

```bash
go build ./internal/peermanagement/peertls/
```

Expected: success.

- [ ] **Step 4: Commit**

```bash
git add internal/peermanagement/peertls/tls_openssl.go internal/peermanagement/peertls/shared_value.go
git commit -m "feat(peertls): implement OpenSSL-backed PeerConn with BIO pump"
```

---

## Task 7: Round-trip unit test for `peertls`

**Files:**
- Create: `internal/peermanagement/peertls/tls_openssl_test.go`

- [ ] **Step 1: Write the round-trip test**

```go
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
// connection through a full handshake over an in-memory pipe and asserts
// that both sides compute identical SharedValue bytes.
func TestHandshake_SessionSigRoundTrip(t *testing.T) {
	clientCert, clientKey := generateTestCert(t)
	serverCert, serverKey := generateTestCert(t)

	clientPipe, serverPipe := net.Pipe()
	defer clientPipe.Close()
	defer serverPipe.Close()

	clientConn, err := Client(clientPipe, &Config{CertPEM: clientCert, KeyPEM: clientKey})
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	defer clientConn.Close()

	listener := newSinglePipeListener(serverPipe)
	wrapped := NewListener(listener, &Config{CertPEM: serverCert, KeyPEM: serverKey})

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

// singlePipeListener serves exactly one inbound conn, then blocks.
type singlePipeListener struct {
	conn net.Conn
	ch   chan struct{}
}

func newSinglePipeListener(c net.Conn) *singlePipeListener {
	l := &singlePipeListener{conn: c, ch: make(chan struct{}, 1)}
	l.ch <- struct{}{}
	return l
}

func (l *singlePipeListener) Accept() (net.Conn, error) {
	<-l.ch
	return l.conn, nil
}
func (l *singlePipeListener) Close() error   { return l.conn.Close() }
func (l *singlePipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }
```

- [ ] **Step 2: Run the test**

```bash
go test -count=1 -run TestHandshake_SessionSigRoundTrip ./internal/peermanagement/peertls/
```

Expected: PASS. If FAIL with deadlock or `client HandshakeContext: net.Pipe doesn't support deadlines`, replace `net.Pipe()` with the in-process TCP loopback pattern (a `net.Listen("tcp", "127.0.0.1:0")` + `net.Dial`); update the test accordingly. Stage 3 below shows the swap if needed.

- [ ] **Step 3: (Conditional) Swap to TCP loopback if pipe fails**

Replace the `net.Pipe()` setup in the test with:

```go
ln, err := net.Listen("tcp", "127.0.0.1:0")
if err != nil {
	t.Fatalf("Listen: %v", err)
}
defer ln.Close()

dialer := &net.Dialer{Timeout: 2 * time.Second}
clientPipe, err := dialer.Dial("tcp", ln.Addr().String())
if err != nil {
	t.Fatalf("Dial: %v", err)
}
defer clientPipe.Close()

serverPipe, err := ln.Accept()
if err != nil {
	t.Fatalf("Accept: %v", err)
}
defer serverPipe.Close()
```

Drop the `singlePipeListener` helper and instead reuse `ln` directly: `wrapped := NewListener(ln, &Config{...})`. Re-run; expect PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/peermanagement/peertls/tls_openssl_test.go
git commit -m "test(peertls): add session-sig round-trip test"
```

---

## Task 8: Add `Identity.TLSCertificatePEM`

**Files:**
- Modify: `internal/peermanagement/identity.go:225-262`

- [ ] **Step 1: Replace `TLSCertificate` with a PEM-first implementation**

In `identity.go`, replace the existing `TLSCertificate` (lines 225–262) with:

```go
// TLSCertificatePEM generates a self-signed TLS certificate for this
// identity and returns the PEM-encoded cert and private key. Uses a
// fresh P256 key for TLS (separate from the secp256k1 node identity)
// since neither Go's TLS stack nor OpenSSL's default config supports
// secp256k1 server certs. Cert lifetime is 365 days.
func (i *Identity) TLSCertificatePEM() (certPEM, keyPEM []byte, err error) {
	tlsKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: i.EncodedPublicKey(),
		},
		NotBefore:             now,
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &tlsKey.PublicKey, tlsKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(tlsKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// TLSCertificate is the stdlib-form of TLSCertificatePEM, kept for any
// remaining stdlib crypto/tls call sites (RPC, WS).
func (i *Identity) TLSCertificate() tls.Certificate {
	certPEM, keyPEM, err := i.TLSCertificatePEM()
	if err != nil {
		return tls.Certificate{}
	}
	cert, _ := tls.X509KeyPair(certPEM, keyPEM)
	return cert
}
```

- [ ] **Step 2: Build to confirm callers still compile**

```bash
go build ./...
```

Expected: success (no errors). The existing `TLSCertificate` callers in `overlay.go` and `peer.go` are still compatible.

- [ ] **Step 3: Commit**

```bash
git add internal/peermanagement/identity.go
git commit -m "feat(identity): expose TLSCertificatePEM for OpenSSL handover"
```

---

## Task 9: Switch `PeerConfig` and `peer.go::Connect` to `peertls`

**Files:**
- Modify: `internal/peermanagement/peer.go:100-116, 240-283, 293-368`

- [ ] **Step 1: Replace the import + `PeerConfig` struct (lines 1-30 and 100-116)**

Add to imports (and remove `crypto/tls`):
```go
import (
    // ... existing imports
    "github.com/LeJamon/goXRPLd/internal/peermanagement/peertls"
)
```

Replace the `PeerConfig` block:

```go
// PeerConfig holds peer connection configuration.
type PeerConfig struct {
	SendBufferSize int
	PeerTLSConfig  *peertls.Config
}

// DefaultPeerConfig returns the default peer configuration.
// Callers must populate PeerTLSConfig with cert + key from the local
// Identity before invoking Connect.
func DefaultPeerConfig() PeerConfig {
	return PeerConfig{
		SendBufferSize: DefaultSendBufferSize,
	}
}
```

- [ ] **Step 2: Replace `Connect` (lines 240-283)**

```go
// Connect establishes connection to the peer (outbound).
func (p *Peer) Connect(ctx context.Context, cfg PeerConfig) error {
	p.mu.Lock()
	if p.state != PeerStateDisconnected {
		p.mu.Unlock()
		return ErrAlreadyConnected
	}
	p.state = PeerStateConnecting
	p.mu.Unlock()

	if cfg.PeerTLSConfig == nil {
		p.setState(PeerStateDisconnected)
		return errors.New("peer.Connect: PeerTLSConfig required")
	}

	addr := p.endpoint.String()

	dialer := &net.Dialer{Timeout: DefaultConnectTimeout}
	tcpConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		p.setState(PeerStateDisconnected)
		return NewEndpointError(p.endpoint, "connect", err)
	}

	tlsConn, err := peertls.Client(tcpConn, cfg.PeerTLSConfig)
	if err != nil {
		tcpConn.Close()
		p.setState(PeerStateDisconnected)
		return NewHandshakeError(p.endpoint, "tls_setup", err)
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		tlsConn.Close()
		p.setState(PeerStateDisconnected)
		return NewHandshakeError(p.endpoint, "tls", err)
	}

	p.mu.Lock()
	p.conn = tlsConn
	p.mu.Unlock()

	if err := p.performHandshake(ctx, tlsConn); err != nil {
		tlsConn.Close()
		p.setState(PeerStateDisconnected)
		return err
	}

	p.setState(PeerStateConnected)
	return nil
}
```

Add `"errors"` to imports if not already present.

- [ ] **Step 3: Replace `performHandshake` signature + body (lines 293-368)**

Find the function and update:

```go
// performHandshake performs the XRPL HTTP upgrade handshake.
func (p *Peer) performHandshake(ctx context.Context, tlsConn peertls.PeerConn) error {
	sharedValue, err := tlsConn.SharedValue()
	if err != nil {
		return NewHandshakeError(p.endpoint, "shared_value", err)
	}

	req, err := BuildHandshakeRequest(p.identity, sharedValue, p.handshakeCfg)
	if err != nil {
		return NewHandshakeError(p.endpoint, "build_request", err)
	}

	if peerIP := tcpRemoteIP(tlsConn); peerIP != nil {
		addAddressHeaders(req.Header, p.handshakeCfg, peerIP)
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(DefaultHandshakeTimeout)
	}
	tlsConn.SetDeadline(deadline)
	defer tlsConn.SetDeadline(time.Time{})

	if err := WriteRawHandshakeRequest(tlsConn, req); err != nil {
		return NewHandshakeError(p.endpoint, "send_request", err)
	}

	p.mu.Lock()
	p.bufReader = bufio.NewReader(tlsConn)
	p.mu.Unlock()

	resp, err := http.ReadResponse(p.bufReader, req)
	if err != nil {
		return NewHandshakeError(p.endpoint, "read_response", err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		body := make([]byte, 1024)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		return NewHandshakeError(p.endpoint, "verify",
			fmt.Errorf("%w: got status %d, headers: %v, body: %s",
				ErrInvalidHandshake, resp.StatusCode, resp.Header, string(body[:n])))
	}
	resp.Body.Close()

	// Full session-signature verification — the whole point of #269.
	peerPubKey, err := VerifyPeerHandshake(
		resp.Header,
		sharedValue,
		p.identity.EncodedPublicKey(),
		p.handshakeCfg,
	)
	if err != nil {
		return NewHandshakeError(p.endpoint, "verify", err)
	}
	p.mu.Lock()
	p.remotePubKey = peerPubKey
	p.mu.Unlock()

	caps := NewPeerCapabilities()
	caps.Features = ParseProtocolCtlFeatures(resp.Header)
	p.mu.Lock()
	p.capabilities = caps
	p.mu.Unlock()

	extras, err := ParseHandshakeExtras(
		resp.Header,
		p.handshakeCfg.PublicIP,
		tcpRemoteIP(tlsConn),
	)
	if err != nil {
		return NewHandshakeError(p.endpoint, "verify_extras", err)
	}
	p.applyHandshakeExtras(extras)

	return nil
}
```

- [ ] **Step 4: Build**

```bash
go build ./internal/peermanagement/...
```

Expected: success. If `tls.Conn` is referenced anywhere else in `peer.go` outside the changes above (e.g., a getter), update it to use `net.Conn` or `peertls.PeerConn`.

- [ ] **Step 5: Run package tests**

```bash
go test -count=1 ./internal/peermanagement/peertls/
```

Expected: PASS (round-trip test still works).

- [ ] **Step 6: Commit**

```bash
git add internal/peermanagement/peer.go
git commit -m "feat(peer): use peertls for outbound and restore full session-sig verify"
```

---

## Task 10: Switch `overlay.go` listener + inbound handshake

**Files:**
- Modify: `internal/peermanagement/overlay.go:510-526, 549-612, 614-700, 1295-1330`

- [ ] **Step 1: Update imports**

In `overlay.go`, ensure `peertls` is imported and remove `crypto/tls` if it has no remaining uses (it likely does for the second `tls.Config` at line 1312-1316 — that one is now also `peertls`). Final import block additions:

```go
"github.com/LeJamon/goXRPLd/internal/peermanagement/peertls"
```

- [ ] **Step 2: Replace `startListener` (lines 510-526)**

```go
func (o *Overlay) startListener() error {
	tcpListener, err := net.Listen("tcp", o.cfg.ListenAddr)
	if err != nil {
		return err
	}

	certPEM, keyPEM, err := o.identity.TLSCertificatePEM()
	if err != nil {
		tcpListener.Close()
		return fmt.Errorf("overlay: build TLS cert: %w", err)
	}

	o.listener = peertls.NewListener(tcpListener, &peertls.Config{
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
	})
	return nil
}
```

- [ ] **Step 3: Update `handleInbound` (around line 549-580)**

The existing type assertion `tlsConn, ok := conn.(*tls.Conn)` becomes:

```go
tlsConn, ok := conn.(peertls.PeerConn)
if !ok {
	slog.Error("Inbound connection is not peertls", "t", "Overlay", "remote", remoteAddr)
	conn.Close()
	return
}
```

Replace the call to `o.performInboundHandshake(ctx, peer, tlsConn)` — the signature change is in the next step.

- [ ] **Step 4: Replace `performInboundHandshake` (lines 614-700)**

```go
func (o *Overlay) performInboundHandshake(ctx context.Context, peer *Peer, tlsConn peertls.PeerConn) error {
	handshakeCtx, cancel := context.WithTimeout(ctx, o.cfg.HandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return NewHandshakeError(peer.Endpoint(), "tls", err)
	}

	sharedValue, err := tlsConn.SharedValue()
	if err != nil {
		return NewHandshakeError(peer.Endpoint(), "shared_value", err)
	}

	deadline := time.Now().Add(o.cfg.HandshakeTimeout)
	tlsConn.SetDeadline(deadline)
	defer tlsConn.SetDeadline(time.Time{})

	bufReader := bufio.NewReader(tlsConn)
	req, err := http.ReadRequest(bufReader)
	if err != nil {
		return NewHandshakeError(peer.Endpoint(), "read_request", err)
	}
	req.Body.Close()

	// Full session-signature verification — the whole point of #269.
	peerPubKey, verifyErr := VerifyPeerHandshake(
		req.Header,
		sharedValue,
		o.identity.EncodedPublicKey(),
		o.handshakeConfigFor(),
	)
	if verifyErr != nil {
		if !errors.Is(verifyErr, ErrSelfConnection) && !errors.Is(verifyErr, ErrNetworkMismatch) {
			o.IncPeerBadData(peer.ID(), "handshake-verify")
		}
		return NewHandshakeError(peer.Endpoint(), "verify", verifyErr)
	}
	peer.mu.Lock()
	peer.remotePubKey = peerPubKey
	peer.mu.Unlock()

	hsCfg := o.handshakeConfigFor()

	peerRemote := tcpRemoteIP(tlsConn)
	extras, extraErr := ParseHandshakeExtras(
		req.Header,
		o.cfg.PublicIP,
		peerRemote,
	)
	if extraErr != nil {
		o.IncPeerBadData(peer.ID(), "handshake-malformed-extras")
		return NewHandshakeError(peer.Endpoint(), "verify_extras", extraErr)
	}
	peer.applyHandshakeExtras(extras)

	caps := NewPeerCapabilities()
	caps.Features = ParseProtocolCtlFeatures(req.Header)

	peer.mu.Lock()
	peer.bufReader = bufReader
	peer.capabilities = caps
	peer.mu.Unlock()

	resp := BuildHandshakeResponse(o.identity, sharedValue, hsCfg)
	addAddressHeaders(resp.Header, hsCfg, peerRemote)
	if err := resp.Write(tlsConn); err != nil {
		return NewHandshakeError(peer.Endpoint(), "send_response", err)
	}

	return nil
}
```

- [ ] **Step 5: Replace the outbound config builder (lines ~1295-1330)**

Find the block that builds `cfg := PeerConfig{... TLSConfig: &tls.Config{...}}` and replace with:

```go
certPEM, keyPEM, err := o.identity.TLSCertificatePEM()
if err != nil {
	return fmt.Errorf("overlay: build TLS cert: %w", err)
}
cfg := PeerConfig{
	SendBufferSize: DefaultSendBufferSize,
	PeerTLSConfig: &peertls.Config{
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
	},
}
```

- [ ] **Step 6: Build**

```bash
go build ./...
```

Expected: success. If any other call site references `PeerConfig.TLSConfig`, update it.

- [ ] **Step 7: Run all peer-management tests**

```bash
go test -count=1 ./internal/peermanagement/... 2>&1 | tail -40
```

Expected: PASS (or only the deletions in Task 11 are still pending — see next task).

- [ ] **Step 8: Commit**

```bash
git add internal/peermanagement/overlay.go
git commit -m "feat(overlay): use peertls and restore full session-sig verify on inbound"
```

---

## Task 11: Delete `MakeSharedValue(*tls.Conn)` and `VerifyHandshakeHeadersNoSig`

**Files:**
- Modify: `internal/peermanagement/handshake.go`
- Modify: `internal/peermanagement/handshake_test.go`

- [ ] **Step 1: Delete `MakeSharedValue(*tls.Conn)` and the `MakeSharedValueFromFinished` helper**

`MakeSharedValueFromFinished` is replaced by `peertls.computeSharedValue` (in `peertls/shared_value.go`). Delete both:

In `internal/peermanagement/handshake.go`, remove:
- The `crypto/tls` and `reflect` imports (if unused after the deletions).
- The `MakeSharedValue(conn *tls.Conn) ([]byte, error)` function (lines 95–130).
- The `MakeSharedValueFromFinished` function (lines 132–165).

- [ ] **Step 2: Delete `VerifyHandshakeHeadersNoSig`**

Remove lines 711–778 (`VerifyHandshakeHeadersNoSig` function and its doc comment).

- [ ] **Step 3: Delete tests covering the removed functions**

In `internal/peermanagement/handshake_test.go`:
- Delete `TestMakeSharedValueFromFinished` and its sub-cases.
- Delete `TestMakeSharedValueFromFinished_TooShort`.
- Delete `TestMakeSharedValueFromFinished_Identical`.
- Delete `TestVerifyHandshakeHeadersNoSig` and its sub-cases.
- Delete any other test function whose body references `VerifyHandshakeHeadersNoSig` or `MakeSharedValueFromFinished` (use grep).

```bash
grep -n "MakeSharedValueFromFinished\|VerifyHandshakeHeadersNoSig\|MakeSharedValue\b" internal/peermanagement/handshake_test.go
```

If only the deleted-test bodies match, you've cleaned them all.

- [ ] **Step 4: Build + test**

```bash
go build ./... && go test -count=1 ./internal/peermanagement/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/peermanagement/handshake.go internal/peermanagement/handshake_test.go
git commit -m "refactor(handshake): drop reflection MakeSharedValue and NoSig fallback"
```

---

## Task 12: Update Dockerfile for static-linked CGO build

**Files:**
- Modify: `Dockerfile`

- [ ] **Step 1: Replace the Dockerfile**

```dockerfile
# Stage 1: Build
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git gcc musl-dev pkgconf openssl-dev openssl-libs-static

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 go build \
    -trimpath \
    -ldflags="-s -w -linkmode external -extldflags '-static'" \
    -o /usr/local/bin/goxrpl ./cmd/xrpld

# Stage 2: Runtime
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /usr/local/bin/goxrpl /usr/local/bin/goxrpl

# 5005  = RPC admin
# 5555  = RPC public
# 6005  = WebSocket public
# 6006  = WebSocket admin
# 51235 = peer protocol
EXPOSE 5005 5555 6005 6006 51235

ENTRYPOINT ["goxrpl"]
CMD ["server", "--conf", "/etc/goxrpl/xrpld.toml"]
```

- [ ] **Step 2: Build the image locally**

```bash
docker build -t goxrpl-test:269 .
```

Expected: build succeeds and final image is < 35 MB. Verify size:

```bash
docker images goxrpl-test:269
```

- [ ] **Step 3: Smoke-test the binary**

```bash
docker run --rm goxrpl-test:269 --help
```

Expected: prints CLI help (exit code 0 or 2, depending on Cobra behavior — both fine, you just want no `dynamic linker` errors).

- [ ] **Step 4: Verify the binary is statically linked**

```bash
docker run --rm --entrypoint=/bin/true goxrpl-test:269 || \
docker run --rm --entrypoint=goxrpl goxrpl-test:269 --version 2>&1 | head -1
```

(The runtime image has no shell. The fact that it executes at all on `distroless/static` proves static linking — there's no dynamic loader to find `libssl.so`.)

- [ ] **Step 5: Commit**

```bash
git add Dockerfile
git commit -m "build: switch to CGO+OpenSSL static link on alpine, keep distroless/static runtime"
```

---

## Task 13: Update CI to install OpenSSL dev headers

**Files:**
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Add the install step to each job**

In `.github/workflows/ci.yml`, add an "Install OpenSSL dev headers" step **before** every `actions/setup-go` invocation in `lint`, `build`, and `test` jobs. The full file becomes:

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  lint:
    name: Lint
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - name: Install OpenSSL dev headers
        run: sudo apt-get update && sudo apt-get install -y libssl-dev pkg-config
      - uses: actions/setup-go@v6
        with:
          go-version: "1.24"
          cache: true
      - uses: golangci/golangci-lint-action@v8
        with:
          version: v2.11.3

  build:
    name: Build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - name: Install OpenSSL dev headers
        run: sudo apt-get update && sudo apt-get install -y libssl-dev pkg-config
      - uses: actions/setup-go@v6
        with:
          go-version: "1.24"
          cache: true
      - run: go build -v ./cmd/xrpld

  test:
    name: Test (${{ matrix.group }})
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        include:
          - group: integration
            packages: ./internal/testing/...
          - group: tx
            packages: ./internal/tx/...
          - group: core
            packages: ./internal/ledger/... ./internal/txq/... ./internal/rpc/... ./internal/consensus/... ./internal/peermanagement/...
          - group: libs
            packages: ./codec/... ./crypto/... ./shamap/... ./storage/... ./keylet/... ./ledger/... ./amendment/... ./drops/... ./protocol/... ./config/...
    steps:
      - uses: actions/checkout@v5
      - name: Install OpenSSL dev headers
        run: sudo apt-get update && sudo apt-get install -y libssl-dev pkg-config
      - uses: actions/setup-go@v6
        with:
          go-version: "1.24"
          cache: true
      - run: go test -race -count=1 -timeout 15m ${{ matrix.packages }}
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: install libssl-dev + pkg-config for CGO+OpenSSL builds"
```

---

## Task 14: Add a "Building" section to README

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Locate the right insertion point**

```bash
grep -n "^## " README.md | head -20
```

Insert the new "## Building" section after the existing "Overview" / project intro and before "Architecture" (or wherever the existing top-level structure puts development setup — adapt to actual file).

- [ ] **Step 2: Add the section**

```markdown
## Building

`goxrpl` uses CGO to call OpenSSL for the peer-to-peer TLS handshake — required to compute the
session-signature shared value matching rippled's `SSL_get_finished` / `SSL_get_peer_finished`
flow. You need OpenSSL development headers installed on the build host.

### macOS

```bash
brew install openssl@3 pkg-config
export PKG_CONFIG_PATH="$(brew --prefix openssl@3)/lib/pkgconfig"
go build ./cmd/xrpld
```

### Ubuntu / Debian

```bash
sudo apt install -y libssl-dev pkg-config
go build ./cmd/xrpld
```

### Alpine (or static-linked Linux build)

```bash
apk add --no-cache gcc musl-dev pkgconf openssl-dev openssl-libs-static
CGO_ENABLED=1 go build -ldflags="-linkmode external -extldflags '-static'" ./cmd/xrpld
```

### CGO-disabled builds

`CGO_ENABLED=0 go build ./cmd/xrpld` is supported. The resulting binary cannot
connect to or accept peers (peertls returns `ErrSessionSigUnsupported`), but RPC,
WebSocket, tx, codec, and all other subsystems work unchanged. Useful for contributors
without an OpenSSL toolchain.

### Running interop tests

A docker-based interop test against a real rippled instance lives at
`internal/peermanagement/peertls/tls_interop_test.go`. It is gated by a build tag and
an env var so CI never runs it:

```bash
PEERTLS_DOCKER_INTEROP=1 go test -tags 'docker' \
    ./internal/peermanagement/peertls/ \
    -run TestHandshake_Interop_RippledDocker
```
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: add Building section covering OpenSSL prerequisites"
```

---

## Task 15: Add the docker interop test (optional but in scope)

**Files:**
- Create: `internal/peermanagement/peertls/tls_interop_test.go`

- [ ] **Step 1: Write the gated docker test**

```go
//go:build cgo && docker

package peertls

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestHandshake_Interop_RippledDocker connects to a rippled instance running
// in a docker container, runs the XRPL HTTP-Upgrade handshake, and asserts
// 101 Switching Protocols. Skipped unless PEERTLS_DOCKER_INTEROP=1.
func TestHandshake_Interop_RippledDocker(t *testing.T) {
	if os.Getenv("PEERTLS_DOCKER_INTEROP") == "" {
		t.Skip("PEERTLS_DOCKER_INTEROP not set")
	}

	image := os.Getenv("RIPPLED_IMAGE")
	if image == "" {
		image = "xrpllabsofficial/xrpld:latest"
	}

	cidBytes, err := exec.Command("docker", "run", "-d",
		"-p", "0:51235",
		"--name", "peertls-interop-269",
		image,
	).Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	cid := strings.TrimSpace(string(cidBytes))
	defer exec.Command("docker", "rm", "-f", cid).Run()

	// Discover the host port docker bound for 51235.
	portBytes, err := exec.Command("docker", "port", cid, "51235").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	host, port := parseDockerPort(t, string(portBytes))

	// Wait for rippled to start listening (≤30s).
	addr := net.JoinHostPort(host, port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(time.Second)
	}

	cert, key := generateTestCert(t)

	tcp, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial rippled: %v", err)
	}
	defer tcp.Close()

	pc, err := Client(tcp, &Config{CertPEM: cert, KeyPEM: key})
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	defer pc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pc.HandshakeContext(ctx); err != nil {
		t.Fatalf("HandshakeContext: %v", err)
	}

	if _, err := pc.SharedValue(); err != nil {
		t.Fatalf("SharedValue: %v", err)
	}

	// Send a minimal Upgrade request and read the response status line —
	// we don't need to parse headers fully, just confirm 101.
	req := "GET / HTTP/1.1\r\n" +
		"Upgrade: XRPL/2.2\r\n" +
		"Connection: Upgrade\r\n" +
		"Connect-As: Peer\r\n" +
		"\r\n"
	if _, err := pc.Write([]byte(req)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	br := bufio.NewReader(pc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("got status %d, want 101", resp.StatusCode)
	}
}

func parseDockerPort(t *testing.T, raw string) (host, port string) {
	t.Helper()
	// docker port output: "51235/tcp -> 0.0.0.0:32812\n"
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		parts := strings.Split(line, "->")
		if len(parts) != 2 {
			continue
		}
		hp := strings.TrimSpace(parts[1])
		h, p, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		if h == "0.0.0.0" || h == "" {
			h = "127.0.0.1"
		}
		return h, p
	}
	t.Fatalf("could not parse docker port output: %q", raw)
	return "", ""
}
```

- [ ] **Step 2: Smoke-build the test (it won't run without the build tag + env)**

```bash
go vet -tags 'docker' ./internal/peermanagement/peertls/
```

Expected: no errors.

- [ ] **Step 3: (Optional, if docker is available locally) Run the interop test**

```bash
PEERTLS_DOCKER_INTEROP=1 go test -tags 'docker' -count=1 -v \
    -run TestHandshake_Interop_RippledDocker \
    ./internal/peermanagement/peertls/
```

Expected: PASS, exits cleanly, `peertls-interop-269` container is removed.

If you don't have docker locally or the test fails on environmental issues (rippled image pull, port binding), document the failure mode in a follow-up commit and continue — the unit round-trip test in Task 7 is the primary acceptance signal.

- [ ] **Step 4: Commit**

```bash
git add internal/peermanagement/peertls/tls_interop_test.go
git commit -m "test(peertls): add docker-gated rippled interop test"
```

---

## Task 16: Final verification

**Files:** none (verification only)

- [ ] **Step 1: Full build under CGO**

```bash
go build ./...
```

Expected: success.

- [ ] **Step 2: Full build under !cgo**

```bash
CGO_ENABLED=0 go build ./...
```

Expected: success (peer subsystem compiles via the stub).

- [ ] **Step 3: Full test suite**

```bash
go test -count=1 -timeout 15m ./...
```

Expected: PASS — only the pre-existing failures listed in `MEMORY.md` (`TestFlow_TransferRate`, `TestFlow_BookStep/*`, `TestDeliverMin_MultipleOffers/Providers`, `TestDepositPreauth_*`) may remain. No new failures.

- [ ] **Step 4: Confirm dead code is gone**

```bash
grep -rn "VerifyHandshakeHeadersNoSig\|MakeSharedValueFromFinished" \
    internal/peermanagement/ 2>/dev/null
```

Expected: no matches.

```bash
grep -rn "MakeSharedValue(.*tls\.Conn" internal/peermanagement/ 2>/dev/null
```

Expected: no matches.

- [ ] **Step 5: Lint**

```bash
golangci-lint run ./internal/peermanagement/peertls/...
```

Expected: clean.

- [ ] **Step 6: Docker build + smoke**

```bash
docker build -t goxrpl-269-final .
docker run --rm goxrpl-269-final --version 2>&1 | head -3
```

Expected: image builds, binary runs.

- [ ] **Step 7: Open PR**

Push the branch and open a PR titled "TLS 1.2 session-signature via OpenSSL CGO shim (closes #269)" with a body that links to `tasks/pr-handshake-session-sig.md` and lists the bullet-point commits. Use:

```bash
git push -u origin feature/handshake-session-sig-269
gh pr create --title "TLS 1.2 session-signature via OpenSSL CGO shim (closes #269)" \
    --body "$(cat <<'EOF'
## Summary
- Adds `internal/peermanagement/peertls/` — OpenSSL-backed TLS connection with `SharedValue()` exposing the rippled session-signature material.
- Switches inbound and outbound peer paths off stdlib `crypto/tls` and onto `peertls`.
- Drops `VerifyHandshakeHeadersNoSig` and the reflection-based `MakeSharedValue`; restores full `VerifyPeerHandshake` (signature verification) on both paths.
- Static-linked CGO build on alpine; runtime stays on `distroless/static`.
- `!cgo` builds compile and run; peer subsystem returns `ErrSessionSigUnsupported`.

Spec: tasks/pr-handshake-session-sig.md
Plan: tasks/plan-handshake-session-sig-269.md

Closes #269.

## Test plan
- [x] `go test ./internal/peermanagement/peertls/` (round-trip)
- [x] `go test ./internal/peermanagement/...` (no regressions)
- [x] `CGO_ENABLED=0 go build ./...`
- [x] `docker build .` (static-linked image)
- [ ] `PEERTLS_DOCKER_INTEROP=1 go test -tags docker ./internal/peermanagement/peertls/` (local-only)
EOF
)"
```

---

## Files

**New**
- `internal/peermanagement/peertls/peertls.go`
- `internal/peermanagement/peertls/tls_openssl.go`
- `internal/peermanagement/peertls/tls_stub.go`
- `internal/peermanagement/peertls/shared_value.go`
- `internal/peermanagement/peertls/tls_openssl_test.go`
- `internal/peermanagement/peertls/tls_interop_test.go`
- `internal/peermanagement/peertls/shim/shim.h`
- `internal/peermanagement/peertls/shim/shim.c`
- `internal/peermanagement/peertls/shim/shim.go`

**Modified**
- `internal/peermanagement/identity.go`
- `internal/peermanagement/handshake.go`
- `internal/peermanagement/handshake_test.go`
- `internal/peermanagement/peer.go`
- `internal/peermanagement/overlay.go`
- `Dockerfile`
- `.github/workflows/ci.yml`
- `README.md`

---

## Self-review notes

- All spec sections from `tasks/pr-handshake-session-sig.md` are covered: shim API (Task 4), Go bindings (Task 5), PeerConn impl (Task 6), build tags (Tasks 3+6), TLSCertificatePEM (Task 8), peer.go swap (Task 9), overlay.go swap (Task 10), deletions (Task 11), Dockerfile/CI/README (Tasks 12-14), tests (Tasks 7+15), final verification (Task 16).
- No placeholders. Every code block is the actual content to write.
- Type consistency: `peertls.PeerConn`, `peertls.Config`, `peertls.Client`, `peertls.NewListener`, `Identity.TLSCertificatePEM`, `PeerConfig.PeerTLSConfig` are used identically across all tasks that reference them.
- One conditional path in Task 7 (net.Pipe vs TCP loopback) — flagged as a fallback, with the exact replacement code provided so the engineer doesn't need to improvise.
