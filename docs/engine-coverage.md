# Engine coverage inventory

`scripts/engine-coverage/inventory.json` is a checked-in source and registry
inventory for issue #2016. It is pinned to `XRPLF/xrpld-private` tag `3.4.1`,
commit `d147fccf54a500fce586522f28d6044c37fd8d29`, and Go base
`02f4f17c7d1c1b5676b112ddfa669655bcff4415`. The snapshot records source SHA256
values so a registry change cannot be hidden by an unchanged count.

Regenerate or check the snapshot from the Go repository root:

```sh
python3 scripts/engine-coverage/generate.py \
  --oracle ../rippled-worktrees/v3.4.1-oracle
python3 scripts/engine-coverage/generate.py \
  --oracle ../rippled-worktrees/v3.4.1-oracle --check
```

The generator reads the oracle transaction macro, common-field declarations,
and amendment macro, then reconciles them with the Go transaction constants,
runtime registration files, templates, method receivers, and amendment
registry. It fails on missing or duplicate transaction rows and on common-field
differences. It does not run a transaction engine or claim behavioral parity.

The current snapshot contains:

| Registry | Rows |
| --- | ---: |
| Oracle active transaction macro | 82 |
| Go runtime transaction registry | 82 |
| Go historical enum rows absent from the oracle macro | 4 |
| Oracle amendment macro | 108 |
| Go amendment registry | 112 |
| Go-only historical amendment rows | 4 |
| Common transaction fields | 20 |

All 82 transaction rows include oracle and Go unique fields, field-template
comparison, amendment requirements, source paths, method locations, and engine
stage notes. The common fields match in order and style. The four Go-only
amendments are `InvariantsV1_1`, `NonFungibleTokensV1`, `fixNFTokenDirV1`, and
`fixNFTokenNegOffer`; the snapshot has no oracle-only amendment rows.

The `engine` object records the dispatch seams. Normal transactions enter at
`internal/tx/engine/apply.go`, pass the preflight and preclaim seams in
`internal/tx/engine/preflight.go` and `preclaim.go`, apply through
`do_apply.go`, and run outer invariants through `runInvariants`. Pseudo
transactions use `ApplyPseudo` and the gates in `pseudo_gates.go`; the snapshot
records that this path does not run the normal invariant seam. Batch inner
transactions have separate `preflightInner`, `preclaimInner`,
`ApplyInnerTransaction`, and `CheckInnerInvariants` entries. Registry and
template symbols are recorded alongside these paths so a drift review can find
the relevant dispatch point directly.

Each transaction row has explicit `oracle_registered`, `go_registered`,
`go_supported`, `pseudo`, `conformance_excluded`,
`executed_in_checked_in_corpus`, and `oracle_comparison_executed` status. The
base snapshot sets execution and oracle-comparison status to `false` for every
row. A source reference under `test_references` identifies exact Go test
functions containing a type reference; it is evidence that a test exists, not
evidence that the test ran or compared against C++.

`internal/testing/conformance` still requires an external
`GOXRPL_FIXTURES_DIR` corpus and is pinned to the older v3.4.0 runner contract
at this base. The checked-in tree has no fixture manifest, so the inventory
does not mark any row executed. The legacy out-of-scope list excludes
`app/Batch`, `app/Delegate`, and `app/Vault`; those rows remain registered and
are marked with an exclusion reason rather than being removed. The proposed
45-case v4 corpus from PR #2022 is unmerged and is not counted here.

The focused drift test is:

```sh
GOCACHE=/private/tmp/issue-2016-go-cache \
  go test ./internal/testing/conformance \
  -run '^TestEngineCoverageInventory$' -count=1
```

`TestEngineCoverageInventory` checks the pinned oracle identity and counts,
reconciles every transaction code/name and `Appliable` registration with
`all.RegisterAll`/`tx.SupportedTypes`, compares runtime templates and common
fields, and reconciles all amendment names and support states with
`amendment.AllFeatures`. It deliberately does not turn named Go tests into
execution claims or substitute a source inventory for an executed C++
differential run.
