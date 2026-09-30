# Private rippled 3.4.1 signed snapshot corpus

These files were emitted by the executed C++ `app/StrictOracleRecorder` linked
against `XRPLF/xrpld-private` tag `3.4.1`, commit
`d147fccf54a500fce586522f28d6044c37fd8d29`. `manifest.json` authenticates every
fixture, the four recorder sources, the recorder source commit, the clean
recorder binary, the archived build identity, the configuration and all 16
amendment profiles. The recorder sources and build identity are archived at
`scripts/oracle/recorded/cfaf47a857b04b97ca806133432c9ec1a3f1db39/`.

The final recorder source commit is
`cfaf47a857b04b97ca806133432c9ec1a3f1db39`. It produced **118 fixtures** and
**4,262 assertions with zero failures**. The fixtures contain 137 signed
submissions in total: 118 primary submissions, 11 main pre-submissions and 8
history pre-submissions. The independent close inputs contain 108 transaction
blobs across the main and history ledgers.

| Submitted transaction family | Cases |
| --- | ---: |
| AccountSet | 29 |
| Batch | 48 |
| EscrowCancel | 1 |
| EscrowToken | 2 |
| LoanBrokerSet | 1 |
| NFTokenAcceptOffer | 1 |
| NFTokenAuth | 4 |
| OfferCreate | 1 |
| Payment | 18 |
| TicketCreate | 5 |
| TrustSet | 4 |
| VaultCreate | 4 |

Every profile in the 4-bit amendment matrix executes. There are no recorder
skips or exclusions. The common-engine rows cover flags and network IDs,
first-error ordering, single and multisign authorization, master and regular
keys, delegates and sponsors, fees and reserves, sequence and tickets, expiry,
prior-transaction checks, Batch wrappers and cleanup behavior.

## Recorded transient and queue boundaries

The six transient rows submit against an authenticated pre-transaction open
ledger without closing it first. Four rows exercise `NFTokenAuth` and two
exercise `EscrowToken`:

- `NFTokenAuth/Unauthorized_buyer_tries_to_create_buy_offer`
- `NFTokenAuth/Seller_tries_to_accept_buy_offer_from_unauth_buyer`
- `NFTokenAuth/Unauthorized_buyer_tries_to_accept_sell_offer`
- `NFTokenAuth/Authorized_broker_tries_to_bridge_offers_from_unauthorized_buyer.`
- `EscrowToken/MPT_Cancel_Preclaim`
- `EscrowToken/MPT_Finish_Preclaim`

Each row records authenticated `rawInsert` and `rawErase` deltas, applies them
to the open ledger before submission, and records an independent close input
and closed result. Go validates the old bytes for erasures and rejects missing
or unexpected mutations; it never creates this state from expected outcomes.

Queue rows use the same production lifecycle. The default-threshold row records
the network values directly. The candidate row records five fee-ordered
candidates, real eviction/order behavior and a final queue of four. The
multi-ledger row records three closes with pre-submit counts `[0, 8, 0]` and
post-close queue counts `[0, 1, 0]`; its first close is an authenticated empty
warmup. Go and service replay keep one open ledger, process each authenticated
close, and accept the resulting queue through the production API.

The cleanup rows assert that expired Offer and NFTokenOffer objects exist in the
parent and are absent after submission and close. The Escrow invariant row
observes fee-only `tecINVARIANT_FAILED` recovery. The AccountSet invariant row
uses an authenticated malformed AccountRoot parent and observes persistent
`tefINVARIANT_FAILED`; its fatal diagnostic is intentional and the recorder
still passes.

Seed `2016` produces four bounded signed XRP Payment samples: base-fee success,
insufficient balance, the fee edge and future sequence. The Go soak permutes
and replays recorded cases; it does not generate fresh oracle inputs at
runtime. AMM test-fidelity gap #1498 remains outside this corpus.

## Executed build and provenance

The recorder was built with the clean exact-commit workflow on macOS arm64:

```sh
python3 scripts/oracle/record-v4.py --clean-build --record \
  --fixture-dir /private/tmp/issue-2017-transient-final-cfaf47a85
```

The clean build archived the exact oracle commit, used its Conan lockfile and
registered the pinned public package remote
`https://conan.xrplf.org/repository/conan/`. The original oracle checkout was
not modified. The exact 62,369-byte build identity is archived at
`scripts/oracle/recorded/cfaf47a857b04b97ca806133432c9ec1a3f1db39/build-identity.json`.
It records the source, binary and configuration hashes, compiler and linker
commands, clean-build commands, Conan lock hash and pinned remote. The loader
authenticates the archive bytes and checks their oracle, recorder, configuration
and binary identities before loading fixtures.

| Identity | Value |
| --- | --- |
| Recorder source commit | `cfaf47a857b04b97ca806133432c9ec1a3f1db39` |
| Recorder binary SHA-256 | `6b5faf1e7711342e823c24a7a39968d9a0f6dafe1aafbcd3140592e56efd6339` |
| Build identity archive SHA-256 | `d116bed6c3649544e94237b49cd81a699bb7fabc5cdc874b743c79cac8b40653` |
| Recorder C++ source SHA-256 | `bed9c7a8181885208b10a74c080dc429595dba2409b82d9088b4e5fff096d19c` |
| Recorder configuration SHA-256 | `511d9308c2d03b3e84316146c4c1f14bd54c6959d87f196fcd008463652d665d` |
| Compile command SHA-256 | `180378f2af31fec6a3a29c023de40f949be5558c46b20a2b8c15a5e1052c4f95` |
| Link command SHA-256 | `e768ef906039f04d73ede0754c954dc275bd0a5254a7226c73a47887a27d5ca5` |
| Conan lock SHA-256 | `9d5e382cce56445d65694ed3add13b0bccea58ff9d4e2aad65ac3c9c54089bc5` |
| Corpus manifest SHA-256 | `55ef9157dfcc40bf3abebe8ddda8aed6a333ccc7038177e7fabbe7f1c0f73b63` |

The recorder emitted 118 fixtures and reported:

```text
xrpl.app.StrictOracleRecorder had 0 failures.
1 suite, 1 case, 4262 tests total, 0 failures
```

## Regeneration and replay

Run from the goXRPL checkout after committing the four recorder sources:

```sh
python3 scripts/oracle/record-v4.py --clean-build --record \
  --oracle /path/to/clean/private-3.4.1 \
  --build-root /tmp/private-3.4.1-recorder \
  --fixture-dir /tmp/private-3.4.1-recordings
```

Then regenerate `manifest.json` with
`scripts/recorded-corpus-manifest.py`, supplying the actual recorder commit,
binary, build identity, configuration, four `--recorder-source` paths and all
coverage limits. Manifest generation checks source bytes against that commit,
archives the sources and never rewrites fixture inputs or observations.

Replay needs no private checkout or C++ build:

```sh
GOXRPL_FIXTURES_DIR=internal/testing/conformance/testdata/rippled-3.4.1-v4 \
  GOXRPL_CONFORMANCE_REQUIRED=1 \
  GOXRPL_CONFORMANCE_REPORT=/tmp/conformance-report.json \
  GOXRPL_ENGINE_EVIDENCE_REPORT=/tmp/engine-report.json \
  GOXRPL_ENGINE_SOAK_SECONDS=10 \
  go test -race -count=1 -v ./internal/testing/conformance

go test -race -count=1 -v ./internal/ledger/service \
  -run '^TestServiceSnapshotExecutionFromDurableParent$'
```

The six coverage limits in `manifest.json` state the bounded common-engine,
queue, invariant, transient, generator and protocol-scope boundaries. Native
cryptography capability, live peer/consensus behavior, protocol-facing RPC
parity and whole-release parity require evidence outside this corpus.
