# PR Handshake Headers — Six remaining headers (Issue #270)

**Branch:** `feature/handshake-headers-270`
**Issue:** [#270](https://github.com/LeJamon/go-xrpl/issues/270) — deferred from PR #264 round-7 (`tasks/pr264-round7-fixes.md`, Out-of-scope row C4)
**Goal:** Emit, parse, store, and consume six rippled handshake headers that goXRPL currently does not support, bringing parity with `rippled/src/xrpld/overlay/detail/Handshake.cpp` `buildHandshake` + `verifyHandshake`.

---

## Scope

| Header | Direction | Format | Source of truth |
|--------|-----------|--------|-----------------|
| `Instance-Cookie` | both | decimal `uint64` (1..MAX-1, generated once per process) | `Handshake.cpp:208` |
| `Server-Domain` | both | RFC-1035-style TOML-form domain string | `Handshake.cpp:210-211, 235-239` |
| `Closed-Ledger` | both | lower-case hex of `uint256` (32 bytes → 64 chars) | `Handshake.cpp:219-223`, `PeerImp::run()` parser |
| `Previous-Ledger` | both | lower-case hex of `uint256` (32 bytes → 64 chars) | `Handshake.cpp:222`, `PeerImp::run()` parser |
| `Remote-IP` | outbound only writes; both verify | dotted-quad / colon-IPv6, only emitted when peer's IP is public | `Handshake.cpp:213-214, 340-359` |
| `Local-IP` | outbound only writes; both verify | dotted-quad / colon-IPv6, only emitted when our public IP is configured | `Handshake.cpp:216-217, 325-338` |

Constants for `Closed-Ledger` and `Previous-Ledger` already exist at `internal/peermanagement/handshake.go:35-36` but are never read or written. The other four header names do not yet exist as constants.

**In scope (this PR):**
- Add header constants for the four missing names.
- Generate per-process `Instance-Cookie` once at `Overlay` construction (matches rippled `1 + rand_int(prng, MAX-1)` at Application.cpp).
- Plumb `ServerDomain string` and `PublicIP net.IP` through `Config` and `HandshakeConfig`.
- Plumb `ClosedLedger`/`PreviousLedger` 32-byte hashes from a `LedgerHintProvider` callback so `internal/peermanagement` does not have to import `internal/ledger`.
- Emit all six on outbound (`BuildHandshakeRequest`) and on inbound (`BuildHandshakeResponse`).
- Parse all six on inbound and outbound; store on `Peer`. **Strict rippled parity**: Instance-Cookie is stored only, never compared — `verifyHandshake` does not inspect it; self-connection detection stays pubkey-only at `Handshake.cpp:322`.
- IP consistency checks: faithfully reproduce `verifyHandshake` lines 325-359.
- Expose `Closed-Ledger` hints as a peer-selection primitive: `Overlay.PeersWithClosedLedger` plus `LedgerSyncHandler.PreferredPeersForLedger` filter peers by their last-known closed-ledger hash (seeded from the handshake hint, refreshed by `mtSTATUS_CHANGE`). This is a coarse filter only — it is NOT a port of rippled's catchup peer selection, which goes through `PeerImp::hasLedger(hash, seq)` and inspects `[minLedger_, maxLedger_]` plus the `recentLedgers_` ring (state goXRPL does not yet track per peer).
- Surface peer info through the RPC `peers` handler: `types.PeerSource` interface plus `Server.SetPeerSource`; `Overlay.PeersJSON()` produces the per-peer entries with `server_domain`, `closed_ledger`, `previous_ledger`, `remote_ip`, `local_ip`. CLI wires the overlay in when running in consensus mode.
- Four acceptance tests as named in the issue, plus targeted tests for the catchup picker and the RPC handler.

**Out of scope:**
- TOML domain validation per RFC-1035: rippled uses `isProperlyFormedTomlDomain`. We add a Go equivalent but keep the rule narrow (length ≤ 253, label-by-label `[A-Za-z0-9-]{1,63}`, no leading/trailing hyphen, optional trailing dot stripped). Stricter TOML semantics are a follow-up if/when operators report rejections.

---

## Files

### Modified

| File | Change |
|------|--------|
| `internal/peermanagement/handshake.go` | Header constants; `HandshakeConfig.{ServerDomain, PublicIP, InstanceCookie, LedgerHintProvider}`; emit/parse helpers; `VerifyHandshakeHeadersNoSig` extended with IP consistency checks; new `parseLedgerHashHeader`. |
| `internal/peermanagement/peer.go` | `Peer` gains `instanceCookie uint64`, `serverDomain string`, `closedLedger [32]byte`, `previousLedger [32]byte`, `remoteIPSelfReport string`, `localIPSelfReport string`, plus accessors. Populated from headers in the inbound and outbound code paths that call `VerifyHandshakeHeadersNoSig`. |
| `internal/peermanagement/overlay.go` | At Overlay construction: generate `instanceCookie` once via `crypto/rand` (range `[1, math.MaxUint64-1]` to match rippled's `1 + rand_int(..., MAX-1)`); store on Overlay; thread into `HandshakeConfig`; expose `Overlay.InstanceCookie()` for tests. Self-connection check at the peer-attach site additionally compares cookies. |
| `internal/peermanagement/handshake_test.go` | Four new acceptance tests + helper `buildAllHeadersRequest`. |
| `internal/rpc/types/services.go` (or wherever `PeerInfo` lives) | Extend `PeerInfo` with the five new optional fields. |
| `internal/peermanagement/overlay.go` (`Peers()` method) | Populate the new `PeerInfo` fields from `Peer` accessors. |
| `internal/rpc/handlers/peers.go` | Pass the new fields through into the RPC response. JSON keys: `server_domain`, `closed_ledger`, `previous_ledger`, `remote_ip`, `local_ip` (matching rippled's `peers` method casing). |

### Reference (read-only)

- `rippled/src/xrpld/overlay/detail/Handshake.cpp:178-362` — `buildHandshake` + `verifyHandshake`.
- `rippled/src/xrpld/overlay/detail/PeerImp.cpp` (lambda `parseLedgerHash` inside `PeerImp::run()`) — hex-or-base64 ledger hash parser, used as guidance for `parseLedgerHashHeader`.
- `rippled/src/xrpld/app/main/Application.cpp` — `instanceCookie_(1 + rand_int(crypto_prng(), max-1))`.

---

## Phase 1 — Plumbing: constants, config, instance cookie

### Task 1.1 — Add the missing header-name constants

Append to the constant block at `handshake.go:27-39`:

```go
HeaderInstanceCookie = "Instance-Cookie"
HeaderServerDomain   = "Server-Domain"
HeaderRemoteIP       = "Remote-IP"
HeaderLocalIP        = "Local-IP"
```

Existing `HeaderClosedLedger` and `HeaderPreviousLedger` constants stay where they are.

### Task 1.2 — Extend `HandshakeConfig`

```go
type HandshakeConfig struct {
    // ... existing fields ...

    // ServerDomain is an operator-provided domain string advertised on
    // every handshake. Empty disables the header (matches rippled's
    // `if (!app.config().SERVER_DOMAIN.empty())`).
    ServerDomain string

    // PublicIP is our observed public address, if known. An unspecified
    // value (zero-length / nil) disables the Local-IP header on outbound
    // and disables the Remote-IP consistency check on inbound — same as
    // rippled's `public_ip.is_unspecified()` short-circuit.
    PublicIP net.IP

    // InstanceCookie is the per-process nonce. Set once at Overlay
    // construction; propagated by-value into every HandshakeConfig copy.
    InstanceCookie uint64

    // LedgerHintProvider returns the most recent closed-ledger hash
    // and its parent. Returning ok=false suppresses both Closed-Ledger
    // and Previous-Ledger headers on outbound (rippled wraps both
    // `insert` calls under a single `if` on getClosedLedger()).
    LedgerHintProvider func() (closed [32]byte, parent [32]byte, ok bool)
}
```

`DefaultHandshakeConfig()` leaves the new fields zero / nil; production wiring lives in `overlay.go`.

### Task 1.3 — Generate the instance cookie at Overlay construction

In the Overlay constructor:

```go
var b [8]byte
if _, err := rand.Read(b[:]); err != nil {
    return nil, fmt.Errorf("instance cookie: %w", err)
}
cookie := binary.BigEndian.Uint64(b[:])
// Match rippled's `1 + rand_int(prng, MAX-1)`: avoid 0 and MAX.
if cookie == 0 {
    cookie = 1
} else if cookie == math.MaxUint64 {
    cookie = math.MaxUint64 - 1
}
o.instanceCookie = cookie
```

Expose via `Overlay.InstanceCookie() uint64`. Thread into the `HandshakeConfig` value used for both client and server paths.

---

## Phase 2 — Emit on outbound and inbound

### Task 2.1 — `addHandshakeHeaders` writes the new headers

Extend the existing helper at `handshake.go:222-248`:

```go
if cfg.InstanceCookie != 0 {
    h.Set(HeaderInstanceCookie, strconv.FormatUint(cfg.InstanceCookie, 10))
}
if cfg.ServerDomain != "" {
    // Reject malformed domains at config time, not on every handshake.
    h.Set(HeaderServerDomain, cfg.ServerDomain)
}
if cfg.LedgerHintProvider != nil {
    if closed, parent, ok := cfg.LedgerHintProvider(); ok {
        h.Set(HeaderClosedLedger, hex.EncodeToString(closed[:]))
        h.Set(HeaderPreviousLedger, hex.EncodeToString(parent[:]))
    }
}
// Remote-IP / Local-IP added at the call sites that know the peer's
// remote address and whether it's public — we only have that
// information once a TCP conn is in hand, not at config time.
```

`Remote-IP` and `Local-IP` need the per-connection peer address, so they are written by a separate helper invoked from the conn-attached code path (after the TCP connection is open) rather than from `addHandshakeHeaders`. Helper signature:

```go
func addAddressHeaders(h http.Header, cfg HandshakeConfig, remote net.IP) {
    if remote != nil && isPublicIP(remote) {
        h.Set(HeaderRemoteIP, remote.String())
    }
    if cfg.PublicIP != nil && !cfg.PublicIP.IsUnspecified() {
        h.Set(HeaderLocalIP, cfg.PublicIP.String())
    }
}
```

`isPublicIP` returns `false` for loopback, link-local, multicast, and RFC-1918 ranges (mirrors `beast::IP::is_public` for both v4 and v6).

### Task 2.2 — Wire `addAddressHeaders` into the request and response builders

`BuildHandshakeRequest` and `BuildHandshakeResponse` gain a `remote net.IP` argument; existing call sites (overlay client/server attach) pass `conn.RemoteAddr().(*net.TCPAddr).IP`. `WriteRawHandshakeRequest` whitelist gets the four new header names appended to its writeHeader sequence so the raw HTTP frame includes them.

---

## Phase 3 — Parse on inbound, store on `Peer`

### Task 3.1 — Extend `Peer` struct and accessors

Add to `peer.go`:

```go
instanceCookie       uint64
serverDomain         string
closedLedger         [32]byte
previousLedger       [32]byte
hasLedgerHints       bool
remoteIPSelfReport   string
localIPSelfReport    string
```

Public accessors: `InstanceCookie()`, `ServerDomain()`, `ClosedLedger() ([32]byte, bool)`, `PreviousLedger() [32]byte`, `RemoteIPSelfReport()`, `LocalIPSelfReport()`. The boolean on `ClosedLedger()` is `hasLedgerHints` so callers don't need to compare against the zero hash.

### Task 3.2 — `parseLedgerHashHeader`

```go
// parseLedgerHashHeader accepts a 64-char lower-case hex string
// (rippled's strHex output). Rippled's PeerImp::run also accepts a
// 32-byte base64 fallback; we mirror that so heterogeneous peers can
// interop.
func parseLedgerHashHeader(s string) ([32]byte, error) {
    var out [32]byte
    if len(s) == hex.EncodedLen(32) {
        if _, err := hex.Decode(out[:], []byte(s)); err == nil {
            return out, nil
        }
    }
    if dec, err := base64.StdEncoding.DecodeString(s); err == nil && len(dec) == 32 {
        copy(out[:], dec)
        return out, nil
    }
    return out, fmt.Errorf("unrecognised ledger hash %q", s)
}
```

A malformed hash on a present header is logged but not fatal — rippled's parser returns `std::nullopt` and `PeerImp::run()` simply does not store the hint. We follow the same lenient stance.

### Task 3.3 — `VerifyHandshakeHeadersNoSig` parses and returns the new fields

Refactor the function to return a richer struct so callers can populate `Peer` in one step:

```go
type VerifiedHandshake struct {
    PeerPubKey     *PublicKeyToken
    InstanceCookie uint64
    ServerDomain   string
    ClosedLedger   [32]byte
    PreviousLedger [32]byte
    HasLedgerHints bool
    RemoteIPSelf   string
    LocalIPSelf    string
}
```

Existing single-value callers wrap the new function. Inside the function, *after* the existing pubkey + network-id + network-time checks:

1. **Instance-Cookie self-connection:** parse `Instance-Cookie` (uint64). If it parses and equals `cfg.InstanceCookie`, return `ErrSelfConnection`. (This is the issue's "second signal".)
2. **Server-Domain:** if present, validate via `isWellFormedDomain` (helper detailed in Task 3.4). On failure, return `ErrInvalidHandshake` wrapping the parse error.
3. **Closed-Ledger / Previous-Ledger:** parse via `parseLedgerHashHeader`. Lenient on parse failure; strict on "Previous-Ledger present without Closed-Ledger" only when both are required (rippled tolerates either order, so we do too).
4. **Local-IP consistency check** — rippled `Handshake.cpp:325-338`:
   - If header parse fails → fatal `ErrInvalidHandshake`.
   - If the connection's remote IP (`peerRemote`, passed in by caller) is public AND `peerRemote != localIP` → fatal `ErrInvalidHandshake` (`"Incorrect Local-IP"`).
   - Loopback / private peer → skip the comparison.
5. **Remote-IP consistency check** — rippled `Handshake.cpp:340-359`:
   - If header parse fails → fatal.
   - If `peerRemote` is public AND `cfg.PublicIP` is configured AND header value differs from `cfg.PublicIP` → fatal (`"Incorrect Remote-IP"`).

The function gains two new arguments: `peerRemote net.IP` (the TCP remote address) and `cfg HandshakeConfig` (replaces the standalone `localPubKey, localNetworkID` pair). Existing callers thread the values they already have.

### Task 3.4 — `isWellFormedDomain`

```go
// isWellFormedDomain accepts the conservative subset of
// rippled's isProperlyFormedTomlDomain: total length ≤ 253, each label
// 1..63 chars of [A-Za-z0-9-], no leading/trailing hyphen, optional
// trailing dot tolerated.
```

Pure string validation; no DNS lookup.

---

## Phase 4 — Acceptance tests

All four go in `internal/peermanagement/handshake_test.go`. Helpers reuse the existing `NewIdentity` / `BuildHandshakeRequest` patterns at `handshake_test.go:76-173`.

### `TestHandshake_InstanceCookie_DetectsSelfConnection`

Build a request with the same `Instance-Cookie` value as the local config. Verify rejects with `ErrSelfConnection` even when public keys are different. (We use a freshly-generated remote `Identity` so the pubkey path doesn't trigger first.)

### `TestHandshake_ClosedLedgerHint_ReadableAfterHandshake`

Build a request whose `LedgerHintProvider` returns a non-zero `(closed, parent)` pair. Drive it through `VerifyHandshakeHeadersNoSig`. Assert `result.ClosedLedger == closed`, `result.PreviousLedger == parent`, `result.HasLedgerHints == true`.

### `TestHandshake_RemoteIPSelfReported_MatchesTcpConn`

Build a request with `cfg.PublicIP = 198.51.100.1` (TEST-NET-2 public). Pass `peerRemote = 198.51.100.1` to verify → success, returned `RemoteIPSelf == "198.51.100.1"`. Then mutate the header to `203.0.113.5` and re-verify → fatal `ErrInvalidHandshake`. Then re-test with `peerRemote = 127.0.0.1` (loopback) → check is skipped, success regardless of header value.

### `TestHandshake_AllHeaders_RoundTrip`

Build a request with all six headers populated. Verify it through the inbound code path. Assert each field on the returned `VerifiedHandshake` matches what was sent. Then build a response from the same config and re-verify on the symmetric path. Assert no regression on existing X-Protocol-Ctl / Network-ID / Network-Time / Public-Key / Session-Signature handling — these fields on `VerifiedHandshake` should also match.

---

## Phase 5 — RPC `peers` surface

### Task 5.1 — Extend `PeerInfo`

Add (using rippled's `peers` method JSON key casing):

```go
type PeerInfo struct {
    // ... existing fields ...
    ServerDomain   string `json:"server_domain,omitempty"`
    ClosedLedger   string `json:"closed_ledger,omitempty"`
    PreviousLedger string `json:"previous_ledger,omitempty"`
    RemoteIP       string `json:"remote_ip,omitempty"`
    LocalIP        string `json:"local_ip,omitempty"`
}
```

`omitempty` so peers that never sent the header don't bloat the response.

### Task 5.2 — Populate from `Overlay.Peers()`

Map each `Peer`'s accessors into the new `PeerInfo` fields. Hashes formatted as upper-case hex (rippled `peers` convention for ledger hashes; differs from the lower-case wire format).

### Task 5.3 — Pass through `internal/rpc/handlers/peers.go`

Existing handler is currently a stub; this PR's contribution is to ensure that *if* it ever returns real peers, the new fields are wired in. No behavioural change in the stub path.

---

## Verification

```sh
cd goXRPL
go build ./...
go test ./internal/peermanagement/...
go test ./internal/rpc/...
```

All four named acceptance tests pass; no pre-existing test regresses; `go vet ./...` clean.

---

## Risk notes

1. **`VerifyHandshakeHeadersNoSig` signature change** — every caller must update. Audit: `rg 'VerifyHandshakeHeadersNoSig|VerifyPeerHandshake' internal/peermanagement` enumerates them. The refactor replaces `(localPubKey, localNetworkID)` with `(cfg, peerRemote)`; keep a short overload that constructs a minimal `HandshakeConfig` for tests that don't care about IP semantics.
2. **`isPublicIP` parity with `beast::IP::is_public`** — Go's `net.IP.IsLoopback`/`IsPrivate`/`IsLinkLocalUnicast` cover the same ranges; one-liner combination is sufficient. Pin with a table-driven test.
3. **Cookie collision is the second self-connection signal** — diverges from rippled's literal logic (rippled checks pubkey only). Issue #270 explicitly requests this. Documented inline at the check site.
4. **`omitempty` on `closed_ledger`** — empty hash is `0000…0000` which is technically a valid header on the wire; using a `bool hasLedgerHints` on `Peer` and only formatting the hex when true keeps the RPC response truthful (no synthetic all-zeros).
