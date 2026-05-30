# goXRPL documentation

Start here. This directory holds the prose guides; the per-package API reference
lives on [pkg.go.dev](https://pkg.go.dev/github.com/LeJamon/goXRPLd), and the
project overview is in the top-level [README](../README.md).

## Guides

| Document | Audience / purpose |
|----------|--------------------|
| [architecture.md](architecture.md) | Anyone — how the node is structured, the transaction pipeline, ledger lifecycle, consensus, and storage layering. |
| [operating.md](operating.md) | **Node operators** — building (incl. CGO requirements), running, and the full `xrpld.toml` configuration reference. |
| [conformance.md](conformance.md) | **Contributors** — how rippled-parity is verified and how to run the conformance suite. |
| [../CONTRIBUTING.md](../CONTRIBUTING.md) | **Contributors** — the implement-against-rippled workflow, test layout, and build/test/lint commands. |

For the API of any public package, prefer godoc on
[pkg.go.dev](https://pkg.go.dev/github.com/LeJamon/goXRPLd) — it is generated from
the source and never goes stale.

## Generated reference

These files are produced from the code by `just docs-gen` (see the
[justfile](../justfile)) and carry a "do not edit" header — change the source or
the generator, not the Markdown. `just docs-check` fails CI if they drift.

| Document | Generated from |
|----------|----------------|
| [rpc-methods.md](rpc-methods.md) | The RPC method registry |
| [supported-transactions.md](supported-transactions.md) | The `tx.Register` transaction registry |
| [amendments.md](amendments.md) | The amendment registry |
| [conformance-status.md](conformance-status.md) | The conformance suite summary |

## Archive

[archive/](archive/) holds historical snapshots (gap analyses, status reports,
point-in-time porting notes) kept for provenance only. They are **not maintained**
and will not match the current code — see [archive/README.md](archive/README.md).
