# Consensus regression smoke

A per-PR check that boots a small mixed network — 2 rippled validators +
1 goxrpl validator, all three in the shared UNL — and asserts that every
validated ledger hash matches across all three nodes, both with an empty
ledger and after a small burst of payments.

## Why

A regression gate for goxrpl ↔ rippled interop. Two phases:

1. **Empty-ledger consensus** — all three nodes must validate the same
   genesis-onwards ledger sequence and agree on `ledger_hash` and
   `account_hash`. Catches handshake, peering, and consensus-message wire-
   format regressions.
2. **Tx-execution determinism** — after a small burst of Payments, every
   submission must be accepted and later validate with `tesSUCCESS`; all
   three nodes must still agree on every ledger that contains one of them.
   Catches transaction-engine and metadata regressions.

Quorum = ceil(0.8 * 3) = 3, so all three nodes must agree on every
validated ledger — any divergence wedges the network and times out the
smoke.

> Known caveat: with a larger 5-validator UNL the network currently wedges
> at seq=6/7 because of a tx-execution divergence ([go-xrpl#418](https://github.com/LeJamon/go-xrpl/issues/418)).
> This smoke uses a 3-validator UNL where that fork doesn't reproduce, so
> it functions as a passing baseline today. When tightening to 5 validators
> becomes worthwhile, flip `validators.{txt,toml}` to include extra rippled
> nodes and add the corresponding rippled services to `docker-compose.yml`.

## Running locally

```
# Build a goxrpl image from your branch first:
docker build --build-arg "VERSION=$(git rev-parse HEAD)" -t goxrpl:latest .

# Then run the smoke:
bash scripts/consensus-smoke/smoke.sh
```

The script tears the topology down on exit. Set `KEEP_RUNNING=1` to leave
containers up after a failure for inspection.

## Knobs

| env var          | default                            | meaning                                       |
|------------------|------------------------------------|-----------------------------------------------|
| `GOXRPL_IMAGE`   | `goxrpl:latest`                    | image for goxrpl-0                            |
| `RIPPLED_IMAGE`  | `xrpllabsofficial/xrpld:3.3.0`    | image for rippled-0,1                         |
| `MIN_SEQ_EMPTY`  | `15`                               | seq target for phase 1 (empty ledger)         |
| `PAYMENT_COUNT`  | `5`                                | number of payments to submit in phase 2       |
| `BOOT_TIMEOUT`   | `180`                              | seconds to wait for phase 1 to validate       |
| `TX_TIMEOUT`     | `180`                              | seconds to wait for a payment to validate     |
| `KEEP_RUNNING`   | `0`                                | `1` = keep containers up after exit (debug)   |

`RIPPLED_IMAGE` overrides must expose `/usr/bin/xrpld`. The compose file mounts
each rippled config and validator file at both the official image's `/config`
paths and the pinned final image's `/etc/xrpld` paths, so either entrypoint
contract reads the same topology configuration.

## Files

- `docker-compose.yml` — 3-service topology with a shared bridge network.
- `configs/rippled-{0,1}.cfg` — rippled configs (quorum=3, UNL contains all 3 validator pubkeys).
- `configs/goxrpl-0.toml` — goxrpl config (full validator, same UNL).
- `configs/validators.txt` — UNL in rippled's INI format.
- `configs/validators.toml` — same UNL in goxrpl's TOML format.
- `xrpld-healthcheck.sh` — dependency-free HTTP readiness probe used by both
  official and checksum-pinned rippled images.
- `smoke.sh` — driver: brings up the topology, waits for validation,
  asserts cross-node hash equality, submits payments, verifies every payment,
  and asserts every transaction-containing ledger before tearing down.

Configs were rendered statically from the `xrpl-confluence` topology
templates (`xrpl-confluence/src/topology.star`). The validator seeds are
public test keys — there is no secret material in this directory.
