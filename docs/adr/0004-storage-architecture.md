# 0004 — Content-addressed node store + relational indexes

## Status

Accepted.

## Context

The node must store two very different kinds of data: the ledger state itself
(SHAMap nodes and serialized objects, addressed by hash and verified by hash) and
the answers to history queries ("which transactions touched this account?") that
the state tree cannot answer efficiently. rippled splits these into its NodeStore
and a set of relational (SQLite) databases.

## Decision

Mirror that split. `storage/nodestore` is a content-addressed store: serialized
ledger objects keyed by their SHA-512Half hash, with the key computed by the
SHAMap/ledger layers and the payload treated as opaque (never re-hashed by the
store). `storage/relationaldb` holds the secondary indexes — ledgers,
transactions, the account-transaction index, validations, amendment votes. SQLite
is the default backend (pure-Go, no external service, with ledger headers and
indexes committed together in `transaction.db`); PostgreSQL is available for shared
deployments. A low-level `storage/kvstore` abstraction (memory + Pebble) backs the
node store.

Ledger execution uses root descriptors and bounded change overlays rather than
retained mutable SHAMaps. Persistent nodes also maintain a Pebble flat-state
materialization for the selected durable ledger. Exact-root SHAMap reads serve
history and forks when that materialization does not match. Canonical NodeStore
nodes remain authoritative; synchronized nodes, an advancement marker and a
publication outbox make flat-state and index recovery explicit. There is one
execution engine in persistent, standalone, replay and test modes. See the
[state-memory specification](../state-memory-spec.md) for ownership and limits.

## Consequences

- Content addressing gives integrity for free: a node's key *is* its hash.
- History RPCs are served from purpose-built tables instead of walking state.
- Operators choose a relational backend without touching the rest of the system.
- See [../architecture.md](../architecture.md#storage-layering) for the layering
  and [../operating.md](../operating.md) for configuration.
