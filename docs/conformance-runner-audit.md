# Runner fidelity audit for the 3.4.1 corpus

This reconciles #1503 with the runner present at the start of #2015. The audited
release base is `02f4f17c7d1c1b5676b112ddfa669655bcff4415`; the corresponding
main snapshot is `100cbc36e7fc90db915ccdb53d225f87857b23e7`. Historical issue
descriptions are not evidence that every reported weakness still exists.

## Safeguards already present

The v3 resolver already shared the configured corpus between normal and fuzz
entrypoints, validated manifests and fixture identities, propagated discovery
errors, and rejected missing required data. Strict decoding, amendment/operation
validation, dependency read/parse/cycle errors, exact symbolic/numeric TER,
applied/queued/fee checks, metadata/state hashes, rejected state nonmutation,
and account sequence/flags/owner-count assertions were already implemented.
The required entrypoints did not universally skip missing data. Existing
contract tests for these behaviors remain in the package.

## Remaining v3 assumptions and their release disposition

| Existing behavior | Required v4 path |
| --- | --- |
| `setupEnv` constructs a synthetic genesis and uses fixture setup operations. | Load the complete recorded parent header, state, transaction leaves, rules and fees; verify its roots before submission. |
| `execTx` disables signature checking and remaps AMM addresses through reflection. | Parse canonical signed bytes, verify that serialization preserves them, and submit with signature verification enabled. No address or amount rewriting. |
| Six named lookup tables supply TxQ configuration, direct apply, injected transactions, fee votes, close delays and load changes. | Record the complete supported TxQ configuration and close inputs explicitly. No testcase-name or step-index overrides are consulted. |
| Scope boundaries and AMM identities use heuristics. | Every case is independent; prerequisite effects are already in its captured parent. No implicit reset or identity inference. |
| Dependency replay omits the owning fixture's post-state comparison. | External dependencies and step programs are outside the v4 schema and fail decoding. No fallback or partial replay. |
| Legacy trust setup parses decimal strings with `float64`. | Preserve trust amounts in canonical transaction/SLE bytes, without decimal conversion by the harness. |
| Held and queued transactions have legacy replay machinery. | Observe the first submission exactly and build from the independently recorded consensus transaction set; never resubmit to repair an expected result. |
| Legacy out-of-scope policy and retired-amendment skips can omit fixtures. | Validate all fixture files before exclusions, require an explicit reason, reject wholly excluded/zero-executed profiles, and reconcile every denominator. |

The new contract rejects unknown fields, amendments, unsupported boundaries and
configuration, missing/null values, and duplicate JSON keys. It compares exact
open-ledger submission results and complete post-submit state, then canonical
closed state/transaction/metadata bytes and the entire header and ledger hash.
This includes sequence, ticket, owner-directory and account-flag effects without
special tolerances. A rejected submission must preserve both open-ledger maps.

The required test and fuzz entrypoints use only v4. Legacy code remains available
to its diagnostic contract tests; reorganizing or deleting that package surface
does not establish additional release parity. The v4 corpus does not claim to
exercise RPC/parser rejection boundaries, historical queue/load transitions or
every transaction family. Those limitations are explicit in its manifest and
execution report; they cannot be reported as executed coverage. The whole-release
sign-off remains #2017.
