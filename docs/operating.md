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
# or
go build -o ./tmp/main ./cmd/xrpld
```

A `CGO_ENABLED=0 go build ./cmd/xrpld` also works: the resulting binary cannot
connect to or accept peers (`peertls` returns `ErrSessionSigUnsupported`) and uses
the slower pure-Go verify, but RPC, WebSocket, transactions, codec, and every
other subsystem work unchanged. Useful for contributors without a CGO toolchain.

## Running

```bash
just run                   # go run ./cmd/xrpld
# or run the built binary
./tmp/main
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
| gRPC (optional) | `127.0.0.1:50051` | Clio integration (uncomment `[port_grpc]`) |

A port gets **admin** role when its `admin` field lists the client's IP (CIDR
supported); a port with no `admin` field is public and all clients get the
**guest** role.

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
| `fetch_depth` | `"full"` | Back-fill depth: integer, `"full"`, or `"none"` (values < 10 clamp to 10). |
| `network_id` | `"main"` | `"main"`, `"testnet"`, `"devnet"`, or an integer. |
| `ledger_replay` | `0` | `0` = disabled, `1` = enabled. |

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
`send_queue_limit` sizes the per-connection WebSocket send buffer (default 100).
List IPs in `admin` to grant those clients admin role.

### `[node_db]` — content-addressed state store

| Key | Example | Meaning |
|-----|---------|---------|
| `type` | `"NuDB"` | Backend engine (`NuDB` or `RocksDB`). |
| `path` | `/var/lib/xrpld/db/nudb` | Node-store directory. |
| `online_delete` | `512` | Keep this many recent ledgers online (`0` disables online delete). |
| `advisory_delete` | `0` | `1` = only delete on an explicit trigger. |
| `cache_size` | `16384` | In-memory node-cache entries. |
| `cache_age` | `5` | Node-cache age in minutes. |
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

### `[transaction_queue]`

Governs fee escalation and queueing (EXPERIMENTAL). Every key is optional;
omit one to use rippled's `TxQ::Setup` default, or set it explicitly
(including `0`). Keys: `ledgers_in_queue`, `minimum_queue_size`,
`retry_sequence_percent`, `minimum_escalation_multiplier`,
`minimum_txn_in_ledger`, `minimum_txn_in_ledger_standalone`, `target_txn_in_ledger`,
`maximum_txn_in_ledger` (`0` = no maximum), `normal_consensus_increase_percent`,
`slow_consensus_decrease_percent`, `maximum_txn_per_account`,
`minimum_last_ledger_buffer`.

### Optional sections

- **`[validation_archive]`** — persist pruned validations to a `validations` table
  for forensic queries. `enabled` (default false), `retention_ledgers`
  (`0` = forever), `batch_size`, `flush_interval_ms`, `delete_batch`,
  `in_memory_ledgers`. Backed by SQLite (shares `ledger.db`); under sustained
  write overload the archive drops rather than blocking consensus.
- **`[amendments]`** — operator amendment-vote preferences. `upvote` votes *for*
  an amendment (rippled's `[amendments]` stanza); `veto` refuses to vote for it
  (rippled's `[veto_amendments]`). Names match the amendment registry; an amendment
  must not appear in both lists.

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
