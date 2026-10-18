# Testnet AMM withdrawal regression

`withdraw_testnet21633429.json` captures public Testnet data for issue #2088
from `https://s.altnet.rippletest.net:51234/`. It contains all 11 signed
transactions, their canonical metadata in `TransactionIndex` order, and 60
serialized parent ledger entries needed by this execution fixture.

- Parent ledger: 21633428, hash `64DF260421809F4EBDE0B12BEA766D3A2997D77A9D5AAD9DE002DCDACE7A7E7F`.
- Target ledger: 21633429, network transaction root `CD0D945ECCC1158094F0DDADD5C9555F0DCB82048D4CB34962CF6331C27721F1`.
- Incident transaction at index 8: `413B76F8ECC10B3FE446AB321739798A64E3ABB7A7B3567153F23AC1015F8F13`.

Capture uses `ledger` with `transactions=true, expand=true` to obtain the
ordered transaction identities, `tx` with `binary=true` for each transaction
and metadata blob, and `ledger_entry` with `binary=true` and the exact parent
ledger hash for each included SLE. The captured entries include the metadata's
modified/deleted objects, transaction accounts, owner directories, relevant
asset/LP trust lines, AMM, Oracle, Amendments and FeeSettings. Earlier
transactions do not change the incident AMM or its account/trust-line state.

Run from the repository root:

```sh
just test-pkg './internal/testing/amm -run TestAMMWithdrawTestnet21633429 -count=1'
```

The test verifies signatures, transaction hashes, every metadata blob, the
computed transaction root and charged fees. For index 8 it also checks
`tecINTERNAL`, the exact 151-byte metadata, and that captured entries other
than the fee-paying account remain unchanged. Without the withdrawal guard,
index 8 returns `tecAMM_BALANCE`.

This is a sparse state fixture. It does not authenticate the complete parent
state or reproduce the target account root or ledger hash. Those checks need
the operator's complete parent state or an authenticated node store. Live
validation recovery also requires deploying the fix and using authenticated
recovery; this test does not change or bypass a replay-fault journal.
