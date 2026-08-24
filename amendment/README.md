# Amendment

Package `amendment` models XRPL amendment capability, per-ledger activation
rules, and a node's live amendment policy and observed state.

It does not decode ledger entries, persist operator preferences, or tally
validator votes. Those integrations live in the ledger, storage, RPC, and
consensus packages.

## Mental model

The package has three separate layers:

| Layer | Type | Meaning |
| --- | --- | --- |
| Registry | `Feature` | Process-wide catalog of known amendments, build capability, and default vote behavior |
| Ledger rules | `Rules` | Read-only activation snapshot used while processing one ledger |
| Node state | `Table` | Concurrent mutable state for local vote preferences, validated-ledger observations, majority warnings, and amendment blocking |

These states are independent. A known amendment may be unsupported by this
binary, a supported amendment may not be enabled on a ledger, and an enabled
amendment is not necessarily one the local validator voted for.

## Feature states

- **Known** means the amendment is present in the registry and can be found with
  `FeatureByName` or `FeatureByID`.
- **Supported** means this binary can execute the amendment's behavior. It does
  not mean the amendment is active on a particular ledger.
- **Enabled** means the amendment is active in a particular `Rules` snapshot.
- **Default yes** and **default no** are local validator vote defaults. Operators
  may override these for supported, non-obsolete amendments.
- **Obsolete** is a third vote state. Obsolete amendments remain known and may
  remain supported, but the node never proposes them.
- **Retired** amendments are supported and obsolete, and their pre-amendment
  behavior has been removed. Their IDs form part of the permanent rules
  baseline even when they are absent from the ledger's Amendments object.

The generated [amendment catalog](../docs/amendments.md) lists the registry's
current capability, vote behavior, and lifecycle metadata.

## Registry

Amendment IDs are SHA-512Half hashes of their exact, case-sensitive names.
`FeatureID` performs only that hash; it does not register or validate a name.

```go
feature := amendment.FeatureByName("XRPFees")
if feature == nil {
	return errors.New("unknown amendment")
}

fmt.Printf("%X supported=%t\n", feature.ID, feature.IsSupported())
```

`FeatureByName`, `FeatureByID`, `AllFeatures`, `SupportedFeatures`, and
`DefaultYesFeatures` return copies. Callers cannot mutate the process-wide
registry through their results. The list functions are ordered by amendment
ID.

The registry is fixed during package initialization; there is no runtime
registration API. `ConfidentialTransfer` is a special capability case: it is
reported as supported only when the optional native MPT cryptography backend is
available in the current build.

## Rules snapshots

`Rules` is the transaction-processing view of amendment activation. Production
code obtains it from a ledger's Amendments object and adds the IDs returned by
`PermanentlyEnabledIDs`. `NewRules` deliberately constructs the exact explicit
set passed by the caller; it does not add permanent IDs automatically.

```go
rules := amendment.NewRules([][32]byte{amendment.FeatureXRPFees})
if rules.Enabled(amendment.FeatureXRPFees) {
	// Apply XRPFees behavior for this ledger snapshot.
}
```

After construction, a `Rules` value is read-only and may be shared by
concurrent readers. `EnabledIDs` returns the explicitly stored IDs in ID order.
It does not necessarily enumerate every ID for which `Enabled` returns true:
`NonFungibleTokensV1_1` logically subsumes `NonFungibleTokensV1`,
`fixNFTokenNegOffer`, and `fixNFTokenDirV1` for historical replay compatibility.

The presets and `RulesBuilder` are primarily useful in tests:

```go
rules := amendment.NewRulesBuilder().
	FromPreset(amendment.PresetGenesis).
	Enable(amendment.FeatureAMM).
	Disable(amendment.FeatureXRPFees).
	Build()
```

`FromPreset` merges into the builder's current set. `EnableByName` and
`DisableByName` ignore unknown names, so code handling user input should first
validate it with `FeatureByName`. `AllSupportedRules` activates every capability
for testing; it is not a substitute for rules loaded from a real ledger.

## Live node state

`Table` owns four kinds of live state:

- amendments observed as enabled on validated ledgers;
- local operator vetoes and explicit upvotes;
- the earliest projected activation time for an unsupported amendment holding
  majority;
- sticky unsupported-enabled and amendment-blocked state.

`NewTable` uses the protocol default majority duration of
`DefaultMajorityTime` (14 days). `NewTableWithMajorityTime` is used for networks
that configure another duration. The application setting is
`amendment_majority_time`, expressed as a Go duration such as `"15m"` or
`"336h"`; values below 15 minutes are rejected.

`NeedValidatedLedger` tells the caller when a validated ledger has crossed a
256-ledger amendment window. `DoValidatedLedger` then folds the enabled and
majority sets into the table. Enabled state is monotonic: amendments are added,
not removed. Only entries whose value is `true` are treated as enabled.

When an unsupported amendment holds majority, the table projects its earliest
activation by adding the configured majority duration to the XRPL-epoch close
time. Enabling an unsupported amendment records that fact immediately;
amendment blocking becomes active when the state is folded from a validated
ledger. Both flags remain set because a node cannot safely resume validation
without software that supports the active amendment.

`Desired` returns the ID-sorted, supported, non-obsolete amendments selected by
the local default and operator preferences. It is used when constructing a
fresh genesis ledger. The consensus package performs the actual validator vote
tally and stores its latest counts in `LastVote` for RPC inspection.

## Concurrency and ownership

- The registry is immutable after package initialization, and lookup results
  are copies.
- `Rules` is immutable through its public API and safe for concurrent reads.
- `RulesBuilder` is mutable, supports its zero value, and should have one owner.
- `Table` supports its zero value and serializes access internally. Its slice
  results, `LastVote`, and `Clone` are independent copies.

Avoid copying a `Table` value after first use because it contains a mutex. Share
its pointer or use `Clone` when an independent snapshot is required.

## Verification

From the module root:

```sh
just test-pkg ./amendment/...
go test -race ./amendment/...
go vet ./amendment/...
```

The tests cover registry IDs and metadata, rule construction and historical
subsumption, deterministic enumeration, vote preferences, validated-ledger
windowing, majority projections, defensive copies, and amendment blocking.
