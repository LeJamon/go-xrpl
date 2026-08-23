# Operating a node

This guide covers building, running, and configuring a go-xrpl node. For how the
node is put together internally, see [architecture.md](architecture.md); for the
library API, see [pkg.go.dev](https://pkg.go.dev/github.com/LeJamon/go-xrpl).

## Build requirements

go-xrpl uses CGO for two subsystems:

- **OpenSSL** — the peer-to-peer TLS handshake (`peertls`), computing the
  session-signature shared value that matches rippled's `SSL_get_finished` flow.
- **libsecp256k1** — ECDSA signature verification on the hot path. Falls back to a
  pure-Go implementation (~6× slower per verify) under `CGO_ENABLED=0`.

Install the development headers before building:

```bash
# macOS
brew install openssl@3 secp256k1 pkg-config
export PKG_CONFIG_PATH="$(brew --prefix openssl@3)/lib/pkgconfig:$(brew --prefix secp256k1)/lib/pkgconfig"

# Debian / Ubuntu
sudo apt install -y libssl-dev libsecp256k1-dev pkg-config
```

Then build:

```bash
just build                 # → ../tmp/main (CGO + OpenSSL)
```

The recipe embeds `git describe --tags --always --dirty` in the binary. Set
`VERSION` explicitly to override it for a packaged build.

A `CGO_ENABLED=0 go build ./cmd/xrpld` also works: the resulting binary cannot
connect to or accept peers (`peertls` returns `ErrSessionSigUnsupported`) and uses
the slower pure-Go verify, but RPC, WebSocket, transactions, codec, and every
other subsystem work unchanged. Useful for contributors without a CGO toolchain.

## Running

```bash
just run                   # go run ./cmd/xrpld
# or run the built binary
../tmp/main
# or hot-reload during development (needs `air`)
just dev
```

The node reads its configuration from `xrpld.toml`. Generate a starter file with:

```bash
xrpld generate-config
```

A fully-commented example lives at
[`config/examples/xrpld.toml`](../config/examples/xrpld.toml). Every field there
is **required** unless marked optional — the server refuses to start if a required
field is missing.

### Startup ledger selection

The `server` command accepts one startup mode:

| Flag | Behavior |
|------|----------|
| `--start` | Start from a fresh ledger without consulting local history or the network. |
| `--load` | Load the newest complete ledger available in local storage. |
| `--ledger ID` | Load a locally stored ledger selected by `ID`. |
| `--ledgerfile PATH` | Load a full, expanded rippled ledger JSON file. |
| `--replay --ledger ID` | Load the selected ledger and its parent, then deterministically replay the selected close. |
| `--net` | Ignore local startup history and acquire the initial ledger from peers. |

The modes are mutually exclusive, except that `--replay` requires an explicitly
specified `--ledger`. A ledger identifier may be a 64-character hexadecimal hash,
the case-insensitive word `latest`, or an unsigned 32-bit decimal ledger sequence.
An explicitly empty `--ledger=` also means the latest local ledger.
`--ledgerfile` requires a non-empty path; its input is the full expanded ledger
JSON produced by rippled, including the expanded account state.

Fresh startup seeds the temporary chain with the configured genesis amendments
and does not request a network ledger. Network startup uses an amendment-empty,
unvalidated temporary ledger until a peer ledger is acquired. A successfully
loaded ledger file is stored in the configured node and relational databases, so
later `--load` or `--ledger` startups can select it without the source file.

An explicit local load, file load, or replay failure normally stops startup. Set
`[node_db].fast_load = true` only when falling back is acceptable: a failed
explicit load then starts from genesis in standalone mode or acquires an initial
ledger from the network in networked mode. With no startup flag on a networked
node, `fast_load` first attempts the latest local ledger and otherwise acquires
one from peers.

Fast-load integrity verification uses `[node_db].fast_load_workers` concurrent
workers. Leave it at `0` to select an automatic value based on available CPUs,
or set an explicit value from `1` through `64`. More workers can improve
verification throughput until storage bandwidth is saturated.

Startup replay is separate from the top-level `ledger_replay` setting.
`ledger_replay` advertises and controls peer replay capability; it does not select
a startup mode. `--replay` starts from the selected ledger's stored parent and
stages the selected transactions and close metadata for deterministic application
on the next ledger close.

Common recovery commands:

```bash
# Resume from the newest complete local ledger.
xrpld server --conf /etc/xrpld/xrpld.toml --load

# Recover from a known local sequence (a 64-character hash also works).
xrpld server --conf /etc/xrpld/xrpld.toml --ledger 32570

# Import an expanded ledger snapshot.
xrpld server --conf /etc/xrpld/xrpld.toml --ledgerfile /var/lib/xrpld/recovery-ledger.json

# Reproduce a stored close from its parent, or bypass suspect local history.
xrpld server --conf /etc/xrpld/xrpld.toml --replay --ledger 32570
xrpld server --conf /etc/xrpld/xrpld.toml --net
```

### Time synchronization

Accurate host time is a hard requirement. The peer handshake rejects a connection
when the two nodes' clocks differ by more than 20 seconds, and go-xrpl does not
include an SNTP client. Install and enable a host time service such as `chrony` or
the operating system's NTP daemon, then monitor its synchronization state and
offset. Do not rely on a VM or container clock without a synchronized host.

### Endpoints

The server exposes the protocols configured in the `[server]` `ports` list. With
the example configuration:

| Endpoint | Default (example) | Purpose |
|----------|-------------------|---------|
| JSON-RPC (admin) | `http://127.0.0.1:5005` | Admin-role JSON-RPC, localhost only |
| JSON-RPC (public) | `http://0.0.0.0:5555` | Guest-role JSON-RPC |
| WebSocket (admin) | `ws://127.0.0.1:6006` | Admin-role WS + subscriptions |
| WebSocket (public) | `ws://0.0.0.0:6005` | Guest-role WS + subscriptions |
| Peer protocol | `0.0.0.0:51235` | XRPL peer overlay |
| Health check | `/health` on an HTTP port | Liveness probe |
| gRPC (optional) | `127.0.0.1:50051` | Clio integration (uncomment `[port_grpc]` and add it to `[server].ports`) |

A port gets **admin** role when its `admin` field lists the client's IP (CIDR
supported). When neither `admin` nor `secure_gateway` is configured, direct
loopback requests also receive admin role; other clients receive **guest** role.
Always set `secure_gateway` on a same-host public proxy backend so loopback does
not trigger that admin fallback.

The JSON-RPC handler accepts POST requests only. `GET /health` remains available
as the liveness endpoint, but it is still subject to the configured per-port
Origin and HTTP Basic Auth policy. Arbitrary GET query parameters are never
dispatched as RPC commands.

`server_info` and `server_state` accept `counters` and report shared HTTP/WebSocket
RPC timings, current RPC activity, and the node-store counters available from the
configured backend. The required `job_queue` and current `jobs` containers are
empty because go-xrpl has no central rippled-style JobQueue. For the same reason,
the admin-only `load` field is omitted rather than populated with unrelated
goroutine or client-admission statistics.

### TLS and reverse proxies

The HTTP and WebSocket listeners do not terminate TLS. For public RPC, run a TLS
reverse proxy on the same host and bind the guest RPC ports to loopback rather
than a public interface. The configuration rejects `https` and `wss` protocols
until native TLS termination is available. Set `secure_gateway` on each proxied
guest port to the proxy's exact source IP; for a same-host proxy this is normally
`127.0.0.1` or `::1`:

```toml
[port_rpc_public]
port = 5555
ip = "127.0.0.1"
protocol = "http"
secure_gateway = ["127.0.0.1"]

[port_rpc_admin_local]
port = 5005
ip = "127.0.0.1"
protocol = "http"
admin = ["127.0.0.1"]
```

Expose only the proxy's TLS listener publicly. Configure the proxy to replace,
not append to, client-supplied `Forwarded`, `X-Forwarded-For`, and `X-Real-IP`
headers, and strip any client-supplied `X-User` header before setting trusted
forwarding headers itself. Never use a broad `secure_gateway` network when a
single proxy address is sufficient.

Keep admin HTTP and WebSocket ports separate, loopback-only, and absent from the
public proxy configuration. The peer port is different: its TLS handshake feeds
the XRPL session signature, so peers must connect directly or through a
transparent TCP path that preserves TLS end to end. Do not terminate peer TLS in
an HTTP reverse proxy or load balancer.

### Standalone vs networked

The peer-discovery `ips` list and `[port_peer]` connect the node to the XRPL
overlay. For a single-node / local setup, leave `ips` empty (an empty list is
valid) so the node does not dial out. Validator operation additionally requires
`validation_seed` or `validator_token` (see [Validation](#validation)).

## Configuration reference

Fields are grouped as they appear in `xrpld.toml`. TOML requires all top-level
keys to precede any `[section]` header.

### Top-level — peer protocol

| Key | Example | Meaning |
|-----|---------|---------|
| `compression` | `false` | Enable peer link compression. |
| `peer_private` | `0` | `0` = normal, `1` = private (do not advertise peers). |
| `peers_max` | `21` | Maximum peer connections. |
| `max_transactions` | `250` | Job-queue maximum (100–1000). |
| `ips` | list | Peer-discovery seeds (`"host port"`); empty list is valid. Optional. |
| `ips_fixed` | list | Always-connect fixed peers. Optional. |

### Top-level — Ripple protocol

| Key | Example | Meaning |
|-----|---------|---------|
| `relay_proposals` | `"trusted"` | `all`, `trusted`, or `drop_untrusted`. |
| `relay_validations` | `"all"` | `all`, `trusted`, or `drop_untrusted`. |
| `ledger_history` | `256` | Ledgers to retain: integer, `"full"`, or `"none"`. |
| `ledger_cache_size` | `256` | Recent in-memory ledger/transaction-index window and persisted-ledger lookup cache; integer from 1 to 384. |
| `fetch_depth` | `"full"` | Back-fill depth: integer, `"full"`, or `"none"` (values < 10 clamp to 10). |
| `network_id` | `"main"` | `"main"`, `"testnet"`, `"devnet"`, or an integer. |
| `ledger_replay` | `0` | `0` = disabled, `1` = advertise peer replay capability. Unrelated to startup `--replay`. |

### Top-level — client, storage, diagnostics

| Key | Example | Meaning |
|-----|---------|---------|
| `database_path` | `/var/lib/xrpld/db` | Base directory for SQLite databases. |
| `debug_logfile` | `/var/log/xrpld/debug.log` | Debug log path. |
| `node_size` | `"medium"` | Resource sizing: `tiny`, `small`, `medium`, `large`, `huge`. |
| `beta_rpc_api` | `0` | Expose the beta API version. |
| `validators_file` | — | Path to `validators.toml`/`.txt`. Optional. |
| `genesis_file` | — | Custom genesis; omit for built-in defaults. Optional. |

### `[server]` and `[port_*]`

`[server].ports` lists the named port sections to open. Each named
`[port_<name>]` requires `port`, `ip`, and `protocol` (`http`, `ws`, `peer`, or
`grpc`); optional `limit` caps concurrent connections (`0` = unlimited) and
`send_queue_limit` sizes the per-connection WebSocket send buffer. `0` uses the
rippled-compatible default of 100; explicit values must be in the range 1–65535.
List IPs in `admin` to grant those clients admin role. `user` and `password`
enable HTTP Basic Auth and must be configured together. `allowed_origins` is an
exact list of HTTP(S) origins shared by HTTP CORS and WebSocket upgrades; an
empty list rejects browser-originated requests while clients without an Origin
header (including the CLI) remain usable. An admin-capable port that enables a
browser origin must also configure Basic Auth.

### `[node_db]` — content-addressed state store

| Key | Example | Meaning |
|-----|---------|---------|
| `type` | `"pebble"` | Backend engine. |
| `path` | `/var/lib/xrpld/db/pebble` | Node-store directory. |
| `online_delete` | `512` | Keep this many recent ledgers online (`0` disables online delete). |
| `advisory_delete` | `0` | `1` = only delete on an explicit trigger. |
| `cache_mb` | `2048` | Pebble block-cache capacity in MiB (`0` = 256 MiB). The writable and archive generations share this total. |
| `open_files` | `1000` | Pebble open-file soft limit (`0` = 500). Single stores require at least 74; rotating stores require an even value of at least 148 and split it between generations. A rotation briefly opens a third generation and may use another half-limit. |
| `cache_size` | `16384` | Decoded node-object cache entries (`0` = the `node_size` profile). This is independent of `cache_mb`. |
| `cache_age` | `5` | Decoded node-object cache age in minutes (`0` = the `node_size` profile). |
| `fast_load` | `false` | On networked startup, try the newest local ledger by default; also permit explicit load/replay failures to fall back to genesis or network acquisition. |
| `fast_load_workers` | `0` | Concurrent workers for fast-load integrity verification. `0` selects an automatic value based on available CPUs; explicit values may range from `1` to `64`. |
| `earliest_seq` | `32570` | Lowest ledger sequence to retain. |
| `delete_batch` / `back_off_milliseconds` / `age_threshold_seconds` / `recovery_wait_seconds` | `100`/`100`/`60`/`5` | Online-delete pacing (batch size, inter-batch pause, minimum age, catch-up wait). |

### `[sqlite]` — relational index databases

| Key | Example | Meaning |
|-----|---------|---------|
| `journal_mode` | `"wal"` | `delete`, `truncate`, `persist`, `memory`, `wal`, `off`. |
| `synchronous` | `"normal"` | `off`, `normal`, `full`, `extra`. |
| `temp_store` | `"file"` | `default`, `file`, `memory`. |
| `page_size` | `4096` | Power of two, 512–65536. |
| `journal_size_limit` | `1582080` | WAL/journal size cap in bytes. |

### `[overlay]`

| Key | Example | Meaning |
|-----|---------|---------|
| `max_unknown_time` | `600` | Seconds a peer may stay in the "unknown" sanity state (300–1800). |
| `max_diverged_time` | `300` | Seconds a peer may stay "diverged" before being dropped (60–900). |
| `max_untrusted_count` | `300` | Maximum cached manifests for unlisted validator masters (50–1000). |
| `max_trusted_count` | `300` | Trusted-manifest allowance used when sizing manifest messages (50–1000). |

Both manifest-count keys are optional and default independently to `300`; an
explicit value must be between `50` and `1000`, inclusive.

### `[transaction_queue]`

Governs fee escalation and queueing (EXPERIMENTAL). Every key is optional;
omit one to use rippled's `TxQ::Setup` default, or set it explicitly
(including `0`, where allowed). Keys: `ledgers_in_queue`, `minimum_queue_size`,
`retry_sequence_percent`, `minimum_escalation_multiplier`,
`minimum_txn_in_ledger`, `minimum_txn_in_ledger_standalone`, `target_txn_in_ledger`,
`maximum_txn_in_ledger` (omit for no maximum), `normal_consensus_increase_percent`,
`slow_consensus_decrease_percent`, `maximum_txn_per_account`,
`minimum_last_ledger_buffer`.

### Optional sections

- **`[validation_archive]`** — persist pruned validations to a `validations` table
  for forensic queries, including partial validations. This go-xrpl extension is
  inspired by rippled's historical validation database. `enabled` (default false), `retention_ledgers`
  (`0` = forever), `batch_size`, `flush_interval_ms`, `delete_batch`,
  `in_memory_ledgers`. Backed by SQLite (shares `ledger.db`); under sustained
  write overload the archive counts drops and rate-limits warnings rather than
  blocking consensus. Independent maintenance drains expired rows in bounded
  batches even while archive writes are idle.
- **`[amendments]`** — operator amendment-vote preferences. `upvote` votes *for*
  an amendment (rippled's `[amendments]` stanza); `veto` refuses to vote for it
  (rippled's `[veto_amendments]`). Names match the amendment registry; an amendment
  must not appear in both lists.

Before deploying to a public network, manually compare that network's amendment
state with this build's registry:

```bash
# Defaults to the XRPL Testnet JSON-RPC endpoint.
go run ./scripts/amendment-watch

# Check another rippled-compatible endpoint and require its expected network.
go run ./scripts/amendment-watch -rpc-url https://example.net:51234/ -network-id 1 -timeout 15s
```

The command exits nonzero when the remote node reports an amendment absent from
the local registry, even if it is disabled, or when a locally unsupported
amendment is enabled or holds majority. A known, disabled unsupported amendment
and an amendment present only in the local registry are safe. The check also
requires the same endpoint to report the expected network ID and a validated
ledger no more than 60 seconds old; HTTPS redirects are not allowed to downgrade
to HTTP.

### Validation

A validator node sets `validation_seed` (a seed) or, preferably, a
`validator_token` (rotatable token from `validator-keys`). Both are optional and
omitted on non-validating nodes. The trusted validator list is supplied via
`validators_file` or the network defaults selected by `network_id`.

## Storage backends

go-xrpl keeps content-addressed state separate from queryable indexes (see
[architecture.md](architecture.md#storage-layering)):

- **Node store** (`[node_db]`) holds serialized ledger objects keyed by hash.
- **Relational databases** (`[sqlite]`, under `database_path`) hold the
  transaction/account/ledger/validation indexes that answer history RPCs. SQLite
  is the default and needs no external service; a PostgreSQL backend
  ([`storage/relationaldb/postgres`](../storage/relationaldb/postgres)) is
  available for shared deployments.

## See also

- [architecture.md](architecture.md) — how the node is structured.
- [conformance.md](conformance.md) — verifying behavior against rippled.
- [`config/examples/xrpld.toml`](../config/examples/xrpld.toml) — the annotated reference config.
