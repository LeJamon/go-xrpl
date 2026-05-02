# Issue #179 — Inbound rejected when rippled uses `peer_private=1`

## Status

Investigation only. No production code change in this branch.

## Symptom recap

`rippled` with `peer_private=1` returns HTTP 503 to inbound TCP from
goXRPL even though goXRPL is listed in rippled's `ips_fixed`. Workaround
in xrpl-confluence: `peer_private=0`.

The HTTP 503 originates inside **rippled** when its `PeerFinder`
`fixed()` IP matching fails — see `rippled/src/xrpld/peerfinder/detail/Logic.h:1020`
and `Counts.h:70`. Root cause is rippled's lazy DNS resolution of
`ips_fixed`: hostnames are resolved on demand for outbound, so the
resolved IP is not in the `fixed_` map when goXRPL's inbound arrives
first. This is a **rippled bug**, not a goXRPL bug.

## What goXRPL controls

There are two goXRPL-side angles worth tracking:

### 1. Outbound to fixed peers is opportunistic, not guaranteed

`internal/peermanagement/discovery.go`

- `NewDiscovery` (424-444) reads `cfg.FixedPeers` into a `fixedPeers`
  map but only uses it to seed `peers` via `AddPeer` in
  `Start` (456-458). After that, fixed peers are indistinguishable
  from any other discovered peer.
- `SelectPeersToConnect` (541-568) random-shuffles all candidates and
  returns the first `count`. Fixed peers get **no priority**.

`internal/peermanagement/overlay.go`

- `autoconnect` (1379-1405) is gated by `NeedsMorePeers()` — i.e.,
  `len(connected) < MaxOutbound`. Once goXRPL has *any* outbound
  connections (e.g. to other goXRPL nodes or non-fixed peers), it
  stops trying to connect to its fixed peers entirely.
- There is no dedicated retry loop for fixed peers and no exponential
  backoff scoped to a particular fixed-peer endpoint.

Compare: rippled's `PeerFinder::Logic::makeOutgoingConnections`
attempts every fixed peer until connected, on a 1s timer, with its own
slot budget that does **not** count against the general outbound cap.

**Consequence in the kurtosis scenario**: when rippled's outbound to
its own `ips_fixed` (= goXRPL) eventually retries and succeeds (via
TLS on goXRPL's listener), goXRPL accepts the inbound just fine
(see #2 below). But until then, goXRPL is not *helping* by
aggressively retrying outbound to rippled — and goXRPL's first
outbound attempt landing inside rippled's DNS race is exactly what
triggers the 503. A dedicated fixed-peer retry would give the cluster
many more chances to settle on a working direction.

### 2. Inbound accept path is healthy

`internal/peermanagement/overlay.go`

- `acceptLoop` (670-688) → `handleInbound` (690-753) →
  `canAcceptInbound` (2094-2106) only checks the inbound count vs
  `MaxInbound`. No IP allowlist, no `peer_private`-equivalent
  rejection. So when rippled does eventually reach goXRPL outbound,
  the connection is accepted.
- Note: `MarkConnected` is keyed by the dial address string. An inbound
  from rippled is keyed by `RemoteAddr()` (IP:ephemeral port), so the
  inbound never matches the fixed-peer entry that nominally listed
  rippled by `host:51235`. This means goXRPL would happily *also*
  attempt outbound to that same rippled instance even though it is
  already connected inbound. Not a correctness bug, but a duplicate-
  effort smell.

## Recommendation

Smallest goXRPL-side change that materially helps:

**Add a fixed-peer maintenance loop** that, on a periodic tick (e.g.,
1–5s), iterates `cfg.FixedPeers`, and for each entry that has no
matching connected peer (matched by **resolved IP**, not by the
literal config string), kicks an outbound attempt with bounded
retry/backoff. Slot budget should be in addition to `MaxOutbound`,
mirroring rippled's split between fixed and general outbound budgets.

Out of scope here because:

- Behavioural change to peer-management slot accounting; warrants
  design discussion (does fixed-peer outbound count against
  `MaxOutbound`? What if a fixed peer is also reached inbound?).
- The rippled-side workaround (`peer_private=0`) is already in
  the test harness, so this is a hardening item rather than a
  ship-blocker.
- May intersect with #190 (consensus stuck in "full") — better to land
  the consensus fix first before reasoning about peer-set churn.

## Files referenced

- `internal/peermanagement/config.go:42,193`
- `internal/peermanagement/discovery.go:413,429-444,456-458,541-568`
- `internal/peermanagement/overlay.go:670-753,1378-1405,2094-2106`
- rippled `src/xrpld/peerfinder/detail/Logic.h:1020`
- rippled `src/xrpld/peerfinder/detail/Counts.h:70`
- rippled `src/xrpld/peerfinder/detail/PeerfinderConfig.cpp:89`
