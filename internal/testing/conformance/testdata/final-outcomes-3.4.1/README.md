# Final transaction outcome regressions

These five signed snapshots were recorded from rippled **3.4.1**, commit
`d147fccf54a500fce586522f28d6044c37fd8d29`, through a network-isolated standalone
server's JSON-RPC interface. Setup used ordinary transactions; no ledger objects
were injected or edited. The executable reported that exact commit and came from
the official `rippled_3.4.1-1_amd64.deb` package:

- Package SHA-256: `cae8ce3b9bc9451b19975c890714ba789d2004987cdfff9cbd55522c612c6f26`
- Executable SHA-256: `fb8430dfdbee9d8016818dab22881e48a125598beebf9b5b8c90ca7fdba1faaa`

`TestFinalOutcomeRegressionSnapshots` pins each file's digest and runs the existing
strict snapshot executor. Each case preserves the complete parent ledger, rules,
fees, signed input/order, open result/state, and closed transaction/state bytes.
The executor compares open admission, fee effects, closed metadata and roots, and
authenticated replay with signature checks enabled. Close times are supplied from
the recorded oracle context; these cases do not test clock or consensus scheduling.

| Case | Boundary exercised |
|---|---|
| AMMCreate directory boundary | Two IOUs from one issuer fill and overflow an owner-directory page in transaction Amount/Amount2 order. |
| AMMDeposit freeze before authorization | With AMMClawback enabled and fixCleanup3_3_0 disabled, a frozen first asset takes precedence over a missing second-asset line. |
| AMMWithdraw canonical asset precedence | Full withdrawal without explicit amounts checks the AMM's canonical asset order. |
| PermissionedDomainSet first ticket | With fixCleanup3_1_3 disabled, the domain uses raw Sequence=0; ticket 4 is consumed. |
| PermissionedDomainSet second ticket | Starts independently from the first oracle closed ledger. A second create collides, returns tefEXCEPTION, preserves ticket/fee/state, and is absent from the next closed ledger. |

The market RPC captures were normalized into the existing v4 snapshot schema;
transaction, metadata, ledger-header and SLE bytes were retained unchanged. These
regressions supplement the independently pinned 118-case `rippled-3.4.1-v4` corpus.
