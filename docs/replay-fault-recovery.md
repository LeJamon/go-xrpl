# Replay fault recovery

goXRPL records a transaction execution failure that could make a validator's
replay disagree with the network in the node's local state directory (the
directory resolved by `Config.LocalStateDir`). The journal is stored at:

```
<local-state-dir>/replay-fault.json
```

The node loads this file before consensus starts. While an unresolved fault is
present, the node remains available for peer networking, ledger acquisition,
RPC, and diagnostics, but it does not propose or sign validations. A restart
does not clear the gate. Non-validator nodes report the same diagnostic shape;
`follower_mode` is currently `false` because follower-only recovery is not an
automatic policy.

Use the admin `server_info` or `server_state` method to inspect
`replay_fault`. The response includes the fault ID, class, affected ledger
sequence and hashes, recovery progress, and any persistence error. The
serialized evidence and parent ledger snapshot are intentionally omitted from
the RPC response.

After correcting the underlying storage or execution problem, call the
admin-only `replay_recover` method with the exact fault ID from the status:

```json
{
  "method": "replay_recover",
  "params": {
    "fault_id": "<fault-id>",
    "strategy": "exact_transition"
  }
}
```

`exact_transition` is the default when `strategy` is omitted. It verifies the
saved historical transition; downloading a newer ledger does not satisfy this
strategy.

The service captures available signed quorum evidence and header ancestry, then
persists and fsyncs the exact transition intent before executing replay. If the
intent cannot be written, execution does not start. A successful replay clears
the intent. If the node crashes or a detailed-fault write fails after the intent is recorded, the
intent remains durable and blocks validator duties on restart until an
explicitly verified recovery completes. The journal is an exact transition
record. While replay is pending, validator duties remain governed by the
last verified frontier; a failed replay latches the duty gate.

`replay_recover` admits a service-owned recovery worker and returns an accepted
response after admission. The worker verifies the saved transition, including
the network and trusted-validation context. Successful recovery archives the
journal as `replay-fault.json.<fault-id>.resolved` and clears the active fault.
`recovery.in_flight` and
`recovery.last_error` report explicit revalidation progress. Canceling or
timing out the RPC request after admission does not cancel the worker; stopping
the service cancels and drains it. A failed or canceled worker leaves the fault
active. `transition_verification` describes the current closed ledger as
`locally_replayed`, `acquired_state`, or `unknown`; a matching downloaded state
root alone does not verify its transition. A fault without trusted-validation
evidence cannot be cleared unless the running node can authenticate that target
through its trusted-validation rules. Repeating the request is safe after the
underlying problem is fixed.

Do not delete `replay-fault.json` or its `.parent-<fault-id>` evidence file to
resume the node. Deleting either file removes the recovery evidence and does
not provide proof that the transition is safe. If the journal is malformed or
cannot be read, keep the validator stopped and repair the data directory or
restore the journal from a trusted backup before restarting.

Missing or corrupt parent state can request full-state acquisition of that exact
parent, authenticated by the target's committed parent hash. The journal limits
repair requests to three across restarts. Missing or corrupt target transactions
can request acquisition of the committed target instead. Acquired ledgers stay quarantined
until its completeness and roots are verified and the failed transition is
successfully replayed. Exact-transition acquisition never clears a fault or advances past the
failed transition. `recovery.acquisition_attempts` and
`recovery.acquisition_error` expose this work. Evidence capture can initially
make recovery report that another recovery operation is running; retry after
capture completes.

Ordinary acquisition of a parent that has not yet been downloaded is not an
execution disagreement. Execution disagreement requires authenticated target
inputs, a complete parent matching its state commitment, and reproduction of
the same structured failure on a fresh replay. Other execution failures remain
unclassified and keep validator duties blocked.

## Restarting with incomplete state and a legacy fault

When strict startup verification discovers missing or corrupt state belonging
to the saved fault's parent, the journal retains that new diagnosis alongside
the original replay evidence. The fault ID, original failure message,
transactions, and authentication evidence are preserved. A storage diagnosis
does not authenticate the failed transition or supersede an execution
disagreement.

Ordinary pivot acquisition is suspended while the replay fault is unresolved.
Only an explicitly admitted `verified_pivot` recovery may establish a new baseline.
Repeated peer or quorum notifications do not create and cancel new pivot
sessions. Inspect `replay_fault.recovery.blocked_reason` and
`replay_fault.recovery.operator_action` in the admin `server_info` response, then call `replay_recover` with the reported fault ID. Recovery
checks authentication before requesting bounded acquisition of the exact
missing state; inspect `recovery.last_error` and `recovery.acquisition_error`
after the worker finishes. For the default `exact_transition` strategy, retry
explicitly after the requested acquisition completes. Successful verification
of the saved transition is required to release that recovery gate.

For `exact_transition`, if the old target has aged out of trusted-validation
history, a quorum for a newer ledger is insufficient. The node must also have a verified ancestry chain
connecting the saved target to that trusted ledger. Restoring the missing
parent nodes alone does not supply this authentication. Do not edit
`authenticated`, remove the journal, or treat acquisition of a newer pivot as
proof that the old replay failure is resolved.

## Explicit verified-pivot recovery

After fixing the engine or storage problem, an operator can explicitly admit a
new recovery baseline when the historical transition or its usable parent
cannot be authenticated and recovered:

```json
{
  "method": "replay_recover",
  "params": {
    "fault_id": "<fault-id>",
    "strategy": "verified_pivot"
  }
}
```

This strategy has different trust semantics. A newer ledger authenticated by
this node's trusted-validator quorum can establish a new baseline. It does not
prove that the original failed transition was correct. The original evidence
must remain available as unresolved history, with a separate
`superseded_by_verified_pivot` outcome. Exact successful replay is recorded as
`resolved_by_replay`. Pivot recovery archives the original fault as
`replay-fault.json.<fault-id>.superseded`, preserving its original evidence.

The node remains non-signing while it selects and acquires the pivot, verifies
complete state, executes a linked authenticated successor locally, and catches
up to the live head. Downloading state alone never permits proposals or
validations. The successor execution checks that transition, not every possible
engine behavior. Ordinary consensus eligibility and durable signing-sequence
protections still apply before validation resumes.

Recovery is bound to the fault ID and a persisted generation. A stale request
or result cannot activate another generation. Missing data and verification
budgets are availability problems: recovery retains the selected pivot and
verified durable progress and retries with backoff while the service runs.
Restart resumes the same nonterminal route, even if exact-transition evidence
has since become available. Persisted proofs are checked against the current
network and validator trust context. Temporary quorum unavailability waits; a
changed trust context or invalid proof requires operator attention.
A reproduced execution mismatch stops the attempt
and requires a new explicit admission after diagnosis and repair; recovery
must not silently keep choosing newer pivots.

Keep the data directory, replay journal, and parent evidence. Do not edit the
`authenticated` field or remove the journal. Recovery reuses local nodes only
after verifying their hashes and reachability from the authenticated root;
old completeness flags are insufficient. An explorer response or an arbitrary
RPC ledger is not trusted-validator evidence.

The recovery status reports the strategy, phase, binary revision, evidence
authentication, selected pivot, first verified successor, current verified
frontier and replay lag. `reused_node_reads` counts durable node reads,
`fetched_nodes` counts useful received nodes, and `state_missing_nodes_discovered`
counts missing state-node discoveries; these are activity counters, not a percentage
or a count of unique state entries. `validator_duties_blocked` describes the
replay gate separately from network and sync activity. Clearing this gate does
not override the node's other consensus eligibility checks.
