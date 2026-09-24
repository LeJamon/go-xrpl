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
serialized evidence and parent-state snapshot are intentionally omitted from
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

The service replays and verifies the persisted transition, including the
network and trusted-validation context. It removes the journal only after that
verification succeeds. A failed or canceled request leaves the fault active;
the `transition_verification` status identifies whether verification is
unknown, pending, or running and reports the last failure. A fault without
trusted-validation evidence cannot be cleared by this request. Repeating the
request is safe after the underlying problem is fixed.

Do not delete `replay-fault.json` or its `.parent-<fault-id>` evidence file to
resume the node. Deleting either file removes the recovery evidence and does
not provide proof that the transition is safe. If the journal is malformed or
cannot be read, keep the validator stopped and repair the data directory or
restore the journal from a trusted backup before restarting.
