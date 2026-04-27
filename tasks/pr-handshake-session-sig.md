# PR Handshake Session-Signature — TLS 1.2 Finished via OpenSSL CGO shim (Issue #269)

**Branch:** `feature/handshake-session-sig-269`
**Issue:** [#269](https://github.com/LeJamon/go-xrpl/issues/269) — deferred from PR #264 round-7 (`tasks/pr264-round7-fixes.md`, Out-of-scope C2)
**Goal:** Compute the rippled-compatible 32-byte `sharedValue` from real TLS 1.2 Finished bytes so that peer handshakes pass full session-signature verification (`Handshake.cpp:makeSharedValue` + `verifyHandshake` sign/verify), and remove the `VerifyHandshakeHeadersNoSig` compromise.

---

## Why this exists

Go's `crypto/tls` does not expose TLS 1.2 Finished bytes:

- `c.clientFinished` is populated on both sides but unexported, only reachable via reflection (today's `MakeSharedValue` in `internal/peermanagement/handshake.go:99`).
- **`c.serverFinished` stays all-zeros after a full server-side handshake** because Go's `crypto/tls/handshake_server.go:119` calls `sendFinished(nil)` and never copies the verify bytes back. Only the client sees both fields populated.
- Result: client and server compute *different* `sharedValue`s for the same connection, so the rippled-style "sign over `sharedValue`, peer verifies with public key from `Public-Key` header" check cannot succeed both ways.

The current code masks this by skipping signature verification entirely (`VerifyHandshakeHeadersNoSig`), enforcing only header-level trust. Every connection to a rippled peer is unauthenticated beyond the public-key-in-headers, which a MITM with a different secp256k1 keypair could fabricate trivially.

The proper fix is to use a TLS implementation that exposes Finished bytes symmetrically: OpenSSL via `SSL_get_finished` / `SSL_get_peer_finished`. We choose **Option B (CGO shim)** from the issue — Option A (new protocol layer) requires rippled-side coordination, Option C (forked stdlib `crypto/tls`) is unmaintainable across Go versions.

---

## Scope

**In scope:**
- New package `internal/peermanagement/peertls/` providing a `PeerConn` abstraction backed by a minimal CGO+OpenSSL shim.
- Switch outbound (`peer.go`) and inbound (`overlay.go`) peer paths to `peertls`.
- Restore full `VerifyPeerHandshake` (signature verification) on both inbound and outbound.
- Delete `MakeSharedValue(*tls.Conn)` (reflection version) and `VerifyHandshakeHeadersNoSig`.
- Add `Identity.TLSCertificatePEM()` returning `(certPEM, keyPEM []byte)` for handover to OpenSSL.
- Build-tag fallback: `//go:build !cgo` stub returns `ErrSessionSigUnsupported` so non-CGO builds still compile (and tx/RPC/codec code stays portable for contributors without OpenSSL toolchains).
- Update `Dockerfile` for static CGO build on alpine + `openssl-libs-static`. Runtime image stays `distroless/static`.
- Update `.github/workflows/ci.yml` to install `libssl-dev` + `pkg-config` on the Ubuntu runners.
- Add a "Building" section to `README.md` covering OpenSSL prerequisites per OS.
- `TestHandshake_SessionSigRoundTrip`: in-process two-sided handshake, identical `sharedValue`, mutual signature verification.
- `TestHandshake_Interop_RippledDocker` (build-tag-gated): connect to a real rippled docker container, expect `101 Switching Protocols`.

**Out of scope:**
- TLS 1.3 support. Rippled forces TLS 1.2 (`Handshake.cpp` cipher list pre-dates 1.3); we mirror that with `MinVersion=MaxVersion=TLS12` today and keep that constraint.
- Migration of RPC HTTPS / WebSocket TLS to OpenSSL. Those paths don't need Finished bytes — they keep stdlib `crypto/tls`.
- Feature-flag rollout. The codebase is pre-mainnet ("not live in prod"); we ship the change atomically.

---

## Architecture

### Package layout (new)

```
internal/peermanagement/peertls/
├── peertls.go            // PeerConn interface, public Dial / Listen / Server / Client
├── tls_openssl.go        //go:build cgo  — OpenSSL implementation
├── tls_stub.go           //go:build !cgo — ErrSessionSigUnsupported on every method
├── tls_openssl_test.go   //go:build cgo  — round-trip + error-path unit tests
├── tls_interop_test.go   //go:build cgo && docker — rippled docker interop
└── shim/
    ├── shim.h            // C API declarations
    ├── shim.c            // C implementation (~250 LOC)
    └── shim.go           // CGO bindings (#cgo pkg-config: libssl libcrypto)
```

### `PeerConn` interface

```go
package peertls

type PeerConn interface {
    net.Conn                                 // Read, Write, Close, LocalAddr, RemoteAddr, deadlines
    HandshakeContext(ctx context.Context) error
    SharedValue() ([]byte, error)            // 32-byte sha512Half(c1 ^ c2) — only valid post-handshake
}

// Outbound. tcp is an already-connected *net.TCPConn (or any net.Conn).
func Client(tcp net.Conn, cfg *Config) (PeerConn, error)

// Inbound listener wrapper.
func NewListener(inner net.Listener, cfg *Config) net.Listener  // .Accept() returns PeerConn

type Config struct {
    CertPEM []byte    // PEM-encoded x509 certificate (from Identity.TLSCertificatePEM)
    KeyPEM  []byte    // PEM-encoded private key
    // No CA pool: rippled peers don't trust certs (InsecureSkipVerify: true today).
    // The shim sets SSL_VERIFY_NONE.
}
```

`PeerConn` is a drop-in replacement for `*tls.Conn` in `peer.go` / `overlay.go`. The single new method `SharedValue()` replaces `MakeSharedValue(tlsConn)`.

### CGO shim API surface

```c
// shim.h
typedef struct peertls_ctx peertls_ctx;
typedef struct peertls_ssl peertls_ssl;

peertls_ctx* peertls_ctx_new(int is_server);
void         peertls_ctx_free(peertls_ctx*);
int          peertls_ctx_use_cert_pem(peertls_ctx*, const char* cert, int cert_len,
                                                    const char* key,  int key_len);

peertls_ssl* peertls_new(peertls_ctx*);
void         peertls_free(peertls_ssl*);

// I/O is via memory BIO_pair: Go pumps bytes between the BIO and the underlying
// net.Conn so deadlines / context cancellation stay on the Go side.
int peertls_handshake(peertls_ssl*);                                   // SSL_do_handshake
int peertls_read     (peertls_ssl*, void* buf, int len);               // SSL_read
int peertls_write    (peertls_ssl*, const void* buf, int len);         // SSL_write
int peertls_shutdown (peertls_ssl*);                                   // SSL_shutdown

// Network BIO drain / fill — Go pumps these into/out of the TCP socket.
int peertls_bio_read  (peertls_ssl*, void* buf, int len);  // pull TLS records OUT
int peertls_bio_write (peertls_ssl*, const void* buf, int len); // push TLS records IN

// The whole reason for this PR.
int peertls_get_finished      (peertls_ssl*, void* buf, int len);  // SSL_get_finished
int peertls_get_peer_finished (peertls_ssl*, void* buf, int len);  // SSL_get_peer_finished

const char* peertls_err_string(int code);                          // ERR_error_string for SSL errors
```

All ~14 functions; ~250 LOC of C including error mapping. No callbacks across the FFI boundary, no Go function pointers passed to C — keeps the boundary trivial to audit.

### BIO pump strategy (Go side)

OpenSSL is configured with `BIO_new_bio_pair` (memory BIOs, no fd). The Go side runs:

```
underlying net.Conn  ←→  Go pump  ←→  shim.peertls_bio_{read,write}  ←→  SSL state machine
```

`PeerConn.Read` / `Write` / `HandshakeContext` loop:
1. Call the SSL operation (`SSL_read` / `SSL_write` / `SSL_do_handshake`).
2. If it returns `WANT_READ` / `WANT_WRITE`:
   - Drain pending TLS bytes from the BIO out-buffer → write to `net.Conn`.
   - Read from `net.Conn` → push into BIO in-buffer.
3. Repeat until success or fatal error.

Result: deadlines, ctx cancellation, and goroutine teardown all live in Go. The shim never touches the socket directly — it only sees memory buffers.

### Build tags

```go
// tls_openssl.go
//go:build cgo

// tls_stub.go
//go:build !cgo
```

The `!cgo` stub provides the same `peertls.PeerConn` interface and the same `Client` / `NewListener` constructors, but every constructor returns `ErrSessionSigUnsupported`. Non-CGO builds compile cleanly, the binary runs, and only attempting outbound peer connection or accepting an inbound peer surfaces the missing capability. RPC, WebSocket, tx engine, codec, etc. are unaffected.

---

## Code changes outside `peertls/`

### `internal/peermanagement/identity.go`
Add:
```go
// TLSCertificatePEM returns the same self-signed P256 cert as TLSCertificate
// but in PEM form, suitable for handover to OpenSSL via SSL_CTX_use_certificate_PEM.
func (i *Identity) TLSCertificatePEM() (certPEM, keyPEM []byte, err error)
```
Refactor `TLSCertificate()` to call `TLSCertificatePEM()` and `tls.X509KeyPair` the result. No behavior change for existing callers.

### `internal/peermanagement/peer.go`
- `PeerConfig.TLSConfig *tls.Config` (line 103) becomes `PeerConfig.PeerTLSConfig *peertls.Config`. The default constructor at line 110 returns a `peertls.Config` populated with `CertPEM`/`KeyPEM` from the identity.
- Field `conn net.Conn` already untyped — no change needed.
- `Connect`: `tls.Client(tcpConn, tlsConfig)` → `peertls.Client(tcpConn, peertlsCfg)`. The `tlsConn` local is now `peertls.PeerConn` (still satisfies `net.Conn`).
- `performHandshake`: signature changes from `*tls.Conn` to `peertls.PeerConn`. `MakeSharedValue(tlsConn)` → `tlsConn.SharedValue()`. Restore call to full `VerifyPeerHandshake` on the response (instead of the truncated header-only path that exists in the outbound flow today).
- Drop the R5.2 NOTE comment block (lines 342–346).

### `internal/peermanagement/overlay.go`
- Inbound listener: `tls.NewListener(tcpListener, tlsConfig)` at `overlay.go:524` → `peertls.NewListener(tcpListener, peertlsCfg)`.
- `performInboundHandshake`: replace `MakeSharedValue` + `VerifyHandshakeHeadersNoSig` with `tlsConn.SharedValue()` + `VerifyPeerHandshake`. Drop the R5.2 + R6.1 + R6.2 comment block (lines 642–650).
- Outbound peer config builder at `overlay.go:1310-1318`: replace the `tls.Config` literal (passed to `peer.Connect` via `PeerConfig.TLSConfig`) with a `peertls.Config` so the outbound `peer.go` path receives the right thing. `PeerConfig.TLSConfig *tls.Config` field becomes `PeerConfig.PeerTLSConfig *peertls.Config`.

### `internal/peermanagement/handshake.go`
Delete:
- `MakeSharedValue(conn *tls.Conn) ([]byte, error)` — the reflection version (lines 95–130).
- `VerifyHandshakeHeadersNoSig(...)` (lines 711–778).

Keep:
- `MakeSharedValueFromFinished(localFinished, peerFinished []byte) ([]byte, error)` — still used internally by `peertls` after extracting Finished bytes via the shim. (Move into `peertls/` if it's the only remaining caller; leave in place if anything else needs it.)

### `internal/peermanagement/handshake_test.go`
Delete tests that exclusively cover `VerifyHandshakeHeadersNoSig` (`TestVerifyHandshakeHeadersNoSig` and the self-connection case using it). The full-signature path is now covered by the new `peertls` round-trip test plus the existing `TestVerifyPeerHandshake_*` family.

---

## Build / CI / docs

### `Dockerfile`

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

# Stage 2: Runtime — stays static, since the binary statically links libssl/libcrypto.
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /usr/local/bin/goxrpl /usr/local/bin/goxrpl
EXPOSE 5005 5555 6005 6006 51235
ENTRYPOINT ["goxrpl"]
CMD ["server", "--conf", "/etc/goxrpl/xrpld.toml"]
```

Three substantive lines change: `apk add` list grows, `CGO_ENABLED=0` → `=1`, and `-ldflags` adds `-linkmode external -extldflags '-static'`. Runtime image is unchanged. Final image stays under 30 MB.

### `.github/workflows/ci.yml`

Add to each job (lint, build, test) before the Go steps:
```yaml
- name: Install OpenSSL dev headers
  run: sudo apt-get update && sudo apt-get install -y libssl-dev pkg-config
```

The `core` matrix group already covers `internal/peermanagement/...`, so the new round-trip test runs in the existing test job.

### `README.md` — new "Building" section

```markdown
## Building

`goxrpl` uses CGO to call OpenSSL for the peer-to-peer TLS handshake (required to compute the
session-signature shared value matching rippled). You need OpenSSL development headers installed
on the build host.

### macOS
brew install openssl@3 pkg-config
export PKG_CONFIG_PATH="$(brew --prefix openssl@3)/lib/pkgconfig"
go build ./cmd/xrpld

### Ubuntu / Debian
sudo apt install -y libssl-dev pkg-config
go build ./cmd/xrpld

### Alpine (or static Linux build)
apk add --no-cache gcc musl-dev pkgconf openssl-dev openssl-libs-static
CGO_ENABLED=1 go build -ldflags="-linkmode external -extldflags '-static'" ./cmd/xrpld

### CGO-disabled builds
go build -tags '!cgo' ./cmd/xrpld is supported. The resulting binary cannot connect to or
accept peers (peertls returns ErrSessionSigUnsupported), but RPC, WebSocket, tx, codec, and
all other subsystems work unchanged. Useful for contributors who don't have an OpenSSL toolchain.
```

---

## Tests

### `TestHandshake_SessionSigRoundTrip` (new, build-tag `cgo`)

In `internal/peermanagement/peertls/tls_openssl_test.go`:

1. Create two `Identity` instances `idA`, `idB`.
2. Build a `net.Pipe()` for the underlying transport.
3. `clientConn := peertls.Client(pipeA, cfgFromIdentity(idA))`.
4. Run `serverConn := peertls.NewListener(...).Accept()` for `pipeB` with `cfgFromIdentity(idB)`.
5. Drive both `HandshakeContext` calls in goroutines until done.
6. Assert `clientConn.SharedValue() == serverConn.SharedValue()` (32 bytes, byte-for-byte equal).
7. Build a handshake request from `idA` signing the shared value; verify it on the server side via `VerifyPeerHandshake` using `idB`'s view of the shared value. Assert success.
8. Cross-check the negative case: tamper one byte of the signature → `ErrInvalidSignature`.

### `TestHandshake_Interop_RippledDocker` (new, build-tag `cgo && docker`)

In `internal/peermanagement/peertls/tls_interop_test.go`:

1. Skip if `os.Getenv("PEERTLS_DOCKER_INTEROP") == ""`.
2. Start `xrpllabsofficial/xrpld:latest` (or a pinned tag) on a random port via `testcontainers-go` or shell-out to `docker run`.
3. `peertls.Client` to it. Drive the XRPL handshake (`BuildHandshakeRequest` + `WriteRawHandshakeRequest`).
4. Assert `101 Switching Protocols` and that `VerifyPeerHandshake` accepts the response (real signature verification, not no-sig).
5. Send and read one `mtPING` to prove the post-handshake stream is intact.

Not run in CI by default. Documented in `README.md` under a "Running interop tests" subsection.

### Tests removed
- `TestVerifyHandshakeHeadersNoSig` (and its 8 sub-cases) in `internal/peermanagement/handshake_test.go`.
- Any test that explicitly asserts the truncated/no-sig path is taken.

---

## Acceptance

- `go test ./internal/peermanagement/peertls/... -run TestHandshake_SessionSigRoundTrip` — passes.
- `PEERTLS_DOCKER_INTEROP=1 go test -tags 'docker' ./internal/peermanagement/peertls/... -run TestHandshake_Interop_RippledDocker` — passes locally.
- `go test ./internal/peermanagement/...` — all pre-existing tests still pass; deleted `NoSig` tests are gone.
- `go build ./goXRPL/cmd/xrpld` — works on macOS (with brew openssl), Ubuntu (with libssl-dev).
- `CGO_ENABLED=0 go build ./goXRPL/cmd/xrpld` — works (uses the stub).
- `docker build .` — produces a working static-linked image on `distroless/static`.
- `MakeSharedValue(*tls.Conn)` and `VerifyHandshakeHeadersNoSig` are removed.
- `golangci-lint run` — clean on the new package.

---

## Files

**New**
- `goXRPL/internal/peermanagement/peertls/peertls.go`
- `goXRPL/internal/peermanagement/peertls/tls_openssl.go` (`//go:build cgo`)
- `goXRPL/internal/peermanagement/peertls/tls_stub.go` (`//go:build !cgo`)
- `goXRPL/internal/peermanagement/peertls/tls_openssl_test.go` (`//go:build cgo`)
- `goXRPL/internal/peermanagement/peertls/tls_interop_test.go` (`//go:build cgo && docker`)
- `goXRPL/internal/peermanagement/peertls/shim/shim.h`
- `goXRPL/internal/peermanagement/peertls/shim/shim.c`
- `goXRPL/internal/peermanagement/peertls/shim/shim.go`

**Modified**
- `goXRPL/internal/peermanagement/handshake.go` — delete `MakeSharedValue(*tls.Conn)`, delete `VerifyHandshakeHeadersNoSig`, update doc comment on `MakeSharedValueFromFinished`.
- `goXRPL/internal/peermanagement/handshake_test.go` — delete `NoSig` tests.
- `goXRPL/internal/peermanagement/peer.go` — swap `*tls.Conn` for `peertls.PeerConn`, restore full `VerifyPeerHandshake`, drop R5.2 comment.
- `goXRPL/internal/peermanagement/overlay.go` — same swap on inbound, restore full `VerifyPeerHandshake`, drop R5.2/R6.1/R6.2 comment.
- `goXRPL/internal/peermanagement/identity.go` — add `TLSCertificatePEM()`, refactor `TLSCertificate()` to use it.
- `goXRPL/Dockerfile` — alpine + `openssl-dev openssl-libs-static`, `CGO_ENABLED=1`, static `-extldflags`.
- `goXRPL/.github/workflows/ci.yml` — `apt install libssl-dev pkg-config` step.
- `goXRPL/README.md` — new "Building" section.

---

## Risks and rough edges

1. **Static link on alpine**: `openssl-libs-static` is in alpine's main repo and stable. If a future alpine release drops it (unlikely — used by curl, wget), we fall back to dynamic link + `distroless/base-debian12`.
2. **OpenSSL ABI drift**: `SSL_get_finished` has been stable since OpenSSL 1.0.2. Both 1.1.x and 3.x are fine. We don't pin a version; we accept whatever the build host ships.
3. **macOS dev ergonomics**: every macOS contributor needs the `PKG_CONFIG_PATH` export. README documents it. Acceptable cost.
4. **Interop test flakiness**: docker-based tests are notoriously flaky. We gate behind an env var so CI never fails on a docker hiccup. Local-only verification.
5. **Memory safety in the shim**: every C allocation is freed on the Go side via `defer C.peertls_free(...)`. The shim doesn't hold Go pointers across calls. Buffers passed to `peertls_read`/`peertls_write` are short-lived `[]byte` whose backing array is pinned by Go for the duration of the call. No `cgo.Handle` needed.
6. **Concurrent access to `*peertls_ssl`**: OpenSSL `SSL` objects are not goroutine-safe for concurrent reads+writes without locking. We add a `sync.Mutex` per `peerConnImpl` covering all SSL operations. Since `peer.go` already serializes reads and writes through dedicated goroutines, this is a safety net rather than a hot path.

---

## Implementation order

1. Land the shim package alone (`peertls/`) with passing unit tests, no call-site changes. Fail CI early if the build infra (apk packages, pkg-config) is wrong before touching the rest of the codebase.
2. Add `Identity.TLSCertificatePEM()`.
3. Switch `peer.go` (outbound).
4. Switch `overlay.go` (inbound).
5. Delete `MakeSharedValue(*tls.Conn)`, `VerifyHandshakeHeadersNoSig`, and dead tests.
6. Update Dockerfile, CI, README.
7. Add the docker interop test (last, since it's optional).

Each step keeps the tree green; commit at each boundary.
