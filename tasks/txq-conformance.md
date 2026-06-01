# TxQ Conformance Session

## Status
- Started: 22 failing TxQ conformance tests
- Previous: 18 failing (4 fixed: straightfoward_positive_case, replace_last_tx, replace_middle_tx, last_ledger_sequence)
- Current: 16 failing (6 fixed total: +queue_sequence, +queue_ticket)
- 22 passing (up from 16)

## Fixes Applied

### 1. maxSize initialization (txq/txq.go)
Changed `maxSize` from `uint32` to `*uint32`. Starts as nil (no limit) matching rippled's `std::optional<size_t> maxSize_` which starts as `nullopt`. Queue has no capacity limit until first `processClosedLedger()` call.

### 2. tryClearAccountQueue (txq/apply.go)
Implemented rippled's `tryClearAccountQueueUpThruTx` (TxQ.cpp:518-614). When a high-fee transaction is submitted for an account with queued transactions, it checks whether the total fee paid across all queued + new transactions can cover the escalated series fee. If so, it applies all queued transactions and the new one atomically. This fixes the `terQUEUED` vs `tesSUCCESS` pattern for queue-clearing transactions.

### 3. Time-leap close support (testing/env_submission.go, conformance/runner.go)
Added `CloseWithTimeLeap()` method that calls `ProcessClosedLedger(ctx, true)` instead of `false`. Time-leap closes reset TxQ fee metrics (`txnsExpected`) back toward the minimum, matching rippled's `env.close(env.now() + 5s, 10000ms)`. Added `txqTimeLeapLookup` map to identify which fixture steps need time-leap closes.

### 4. Actual fee level tracking (testing/env_submission.go, testing/env.go)
Added `closingFeeLevels` to track actual fee levels of applied transactions instead of assuming all transactions pay BaseLevel (256). This correctly computes the escalation multiplier (median fee) in `ProcessClosedLedger`, making fee escalation match rippled when high-fee transactions are in the ledger.

### 5. initFee config support (conformance/runner.go, testing/env.go)
Added `txqInitFeeLookup` map and `SetBaseFee`/`SetReserves` methods. Fixtures that use rippled's `initFee()` pattern (255 close steps followed by fee vote) need post-initFee reserves applied. The runner detects the initFee pattern and applies the correct reserves (e.g., base=10, reserve=200, increment=50 drops).

### 6. Relevant transaction count for blocker detection (txq/apply.go, txq/candidate.go)
Rippled computes `acctTxCount` using `lower_bound(acctSeqProx)` to skip stale transactions in the account queue (seqProxy < account's current sequence). go-xrpl was counting ALL transactions. Added `RelevantCount()` and `FirstRelevant()` methods to `AccountQueue` and updated all blocker detection, sequence validation, and in-flight balance checks to use only relevant transactions.

### 7. Front-of-queue sequence validation (txq/apply.go)
When a sequence-based transaction goes at the FRONT of the account queue (before all existing entries), rippled returns `terPRE_SEQ` for future sequences. When it goes AFTER existing entries and doesn't match `nextQueuableSeq`, it returns `telCAN_NOT_QUEUE`. go-xrpl was returning `telCAN_NOT_QUEUE` in both cases. Added `goesAtFront` detection using `GetPrevTx()` to match rippled's behavior. This fixed queue_ticket.

### 8. localTxs retry mechanism (testing/env_submission.go)
Rippled's `localTxs` mechanism retries ALL locally-submitted transactions at the next ledger close, regardless of result code (including tel codes like `telCAN_NOT_QUEUE_FULL`). go-xrpl was only holding `terQUEUED` and retryable (`ter`) results. Extended the held transaction mechanism to also hold `tel` results, and added `retryAllHeldViaTxQ()` which is called after queue drain during Close() to resubmit held transactions through the TxQ. This fixed queue_sequence and contributed to fixing queue_ticket.

## Remaining Failing Tests (16)

### TxQMetaInfo (9 fail)
1. Sequence_in_queue_and_open_ledger — direct open ledger modify (bypasses TxQ)
2. Ticket_in_queue_and_open_ledger — similar direct modify pattern
3. Zero_reference_fee — zero baseFee edge case
4. clear_queue_failure_(load) — depends on replace_middle_tx chain
5. expiration_replacement — expiration-based gap handling
6. full_queue_gap_handling — gap filling logic
7. scaling — metric computation during scaling
8. Queue_full_drop_penalty — drop penalty during queue management
9. Re-execute_preflight — preflight re-execution during queue processing

### TxQPosNegFlows (7 fail, was 9)
1. zero_transaction_fee — fee=0 blocker can't be drained (feeLevel=0 < baseLevel)
2. unexpected_balance_change — in-flight balance/preclaim differences
3. multi_tx_per_account — balance/blocker edge cases (many steps)
4. blockers_sequence — blocker staying in queue after drain, localTxs gaps
5. blockers_ticket — blocker detection with tickets
6. In-flight_balance_checks — telCAN_NOT_QUEUE_BALANCE vs terINSUF_FEE_B (2 errors)
7. ~~queue_sequence~~ — FIXED (localTxs retry)
8. ~~queue_ticket~~ — FIXED (front-of-queue sequence validation)

## Remaining Root Causes

### Direct open ledger modify (2 tests: Sequence_in_queue_and_open_ledger, Ticket_in_queue_and_open_ledger)
Some rippled tests use `env.app().openLedger().modify()` to apply transactions directly to the open ledger, bypassing the TxQ entirely. The fixture recorder captures these as normal tx steps with tesSUCCESS. The go-xrpl runner routes them through TxQ, causing terQUEUED instead of tesSUCCESS.

### Blocker detection differences (PARTIALLY FIXED)
The blocker check now uses RelevantCount (seqProxy >= acctSeqProx), matching rippled's lower_bound behavior. Remaining blocker issues (blockers_sequence, blockers_ticket, zero_transaction_fee) are caused by fee=0 blocker transactions that can't be drained from the queue during accept() because feeLevel=0 < baseLevel=256. In rippled, these appear to be drained through the localTxs replay mechanism or through the open-ledger transaction re-application path in OpenLedger::accept().

### Fee=0 blocker drain (3 tests: zero_transaction_fee, blockers_sequence, blockers_ticket)
Transactions with fee=0 have feeLevel=0 < baseLevel(256). During TxQ::accept() drain, these can't meet the fee requirement. In rippled, the open-ledger re-application path (OpenLedger.cpp:96-112) re-applies the previous open ledger's transactions to the new open ledger, which accounts for these fee=0 transactions being in the view. go-xrpl doesn't replicate this step.

### In-flight balance check ordering (2 tests: In-flight_balance_checks, unexpected_balance_change)
In rippled, the in-flight balance check (telCAN_NOT_QUEUE_BALANCE) is followed by a preclaim check on a modified test view (with deducted balance). When the modified balance is too low for the fee, preclaim returns terINSUF_FEE_B. go-xrpl's balance check catches the same condition earlier with telCAN_NOT_QUEUE_BALANCE, before preclaim can run.

### Preclaim in TxQ multiTxn path (not implemented)
Rippled creates a `MultiTxn` object with a modified ApplyView that adjusts the account balance and sequence for preclaim. This allows preclaim to run with simulated post-queue state. go-xrpl skips this preclaim step entirely — it only checks the simple balance/reserve threshold.

### localTxs retry limitations
The held-txn retry mechanism now includes tel results and retries at close time. However, some edge cases remain where rippled's localTxs retry differs from go-xrpl's implementation. The primary gap is that rippled's OpenLedger::accept() re-applies the current open view's transactions to the new view (step b), which go-xrpl doesn't replicate for TxQ-routed submissions.

## Key Files

### go-xrpl TxQ
- `internal/txq/txq.go` — main TxQ implementation (maxSize now *uint32)
- `internal/txq/apply.go` — Apply logic + tryClearAccountQueue
- `internal/txq/fee.go` — fee escalation
- `internal/txq/config.go` — configuration
- `internal/txq/process.go` — ProcessClosedLedger + SetMaxSize
- `internal/testing/env_submission.go` — TxQ integration, fee level tracking, time-leap close
- `internal/testing/env.go` — closingFeeLevels field, SetBaseFee, SetReserves

### rippled TxQ (reference)
- `rippled/src/xrpld/app/misc/detail/TxQ.cpp` — main implementation
- `rippled/src/xrpld/app/misc/TxQ.h` — header
- `rippled/src/test/app/TxQ_test.cpp` — tests (shows initFee, time-leap usage)

### Conformance runner
- `internal/testing/conformance/runner.go` — txqMinTxnLookup, txqTimeLeapLookup, txqInitFeeLookup, execClose with time-leap

## Time-Leap Close Mapping
Derived from `rippled/src/test/app/TxQ_test.cpp`:
- queue sequence: step 27
- last ledger sequence: steps 2, 5, 8
- zero transaction fee: steps 2, 4
- scaling: steps 150, 151, 152, 153, 203
- multi tx per account, In-flight balance checks, unexpected balance change, Zero reference fee: step 257 (initFee)
