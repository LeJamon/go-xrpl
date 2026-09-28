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
start. It then replays and verifies the transition, including the network and
trusted-validation context. A verified success archives the journal as
`replay-fault.json.<fault-id>.resolved` and clears the active fault. If the
node crashes or a detailed-fault write fails after the intent is recorded, the
intent remains durable and blocks validator duties on restart until an
explicitly verified recovery completes. The journal is an exact transition
record; it is not a generic whole-run dirty marker.

`replay_recover` admits a service-owned recovery worker and returns an accepted
response while the worker is in flight. `recovery.in_flight` and
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
