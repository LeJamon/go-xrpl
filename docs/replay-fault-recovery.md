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
    "fault_id": "<fault-id>"
  }
}
```

The service first persists and fsyncs the exact transition intent before it
executes the replay. If the intent cannot be written, execution does not
start. A successful replay clears the intent. If the
node crashes or a detailed-fault write fails after the intent is recorded, the
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
successfully replayed. Acquisition never clears a fault or advances past the
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
Repeated peer or quorum notifications do not create and cancel new pivot
sessions. Inspect `replay_fault.recovery.blocked_reason` and
`replay_fault.recovery.operator_action` in the admin `server_info` response, then call `replay_recover` with the reported fault ID. Recovery
checks authentication before requesting bounded acquisition of the exact
missing state; inspect `recovery.last_error` and `recovery.acquisition_error`
after the worker finishes. Retry explicitly after the requested acquisition
completes. Successful verification of the saved transition is still required
to release validator duties.

If the old target has aged out of trusted-validation history, a quorum for a
newer ledger is insufficient. The node must also have a verified ancestry chain
connecting the saved target to that trusted ledger. Restoring the missing
parent nodes alone does not supply this authentication. Do not edit
`authenticated`, remove the journal, or treat acquisition of a newer pivot as
proof that the old replay failure is resolved.

If historical authentication cannot be established, keep this validator
blocked and preserve its data directory and evidence for diagnosis. To restore
network visibility meanwhile, start a separate non-validating node with a new
data directory, the same network and trusted-validator configuration, and
neither `validation_seed` nor `validator_token`. Let that node acquire and
verify network state normally. This is an observer recovery workflow; it does
not authorize resuming the faulted validator or clear its journal. There is no
administrative override that certifies an unauthenticated historical transition.
