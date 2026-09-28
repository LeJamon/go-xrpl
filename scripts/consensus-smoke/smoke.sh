#!/usr/bin/env bash
# Consensus regression smoke for goxrpl.
#
# Boots 2 rippled validators + 1 goxrpl tracker via docker-compose, waits for
# the network to validate a ledger, and asserts that ledger_hash /
# account_hash / transaction_hash agree across all three nodes — both with
# an empty ledger and after a small burst of payments.
#
# Pass criteria:
#   - empty phase: all three nodes report the same validated ledger_hash and
#     account_hash for the same seq >= MIN_SEQ_EMPTY (default 15)
#   - tx phase: PAYMENT_COUNT genesis-funded payments are accepted, all
#     eventually validate with tesSUCCESS, and all three nodes still agree on
#     every ledger that contains one of those payments
#
# Run locally:
#   bash scripts/consensus-smoke/smoke.sh
#
# Knobs (env vars):
#   GOXRPL_IMAGE        image to use for goxrpl-0 (default: goxrpl:latest)
#   RIPPLED_IMAGE       image to use for rippled-0,1 (default: xrpllabsofficial/xrpld:3.3.0)
#   MIN_SEQ_EMPTY       minimum validated seq for the empty-ledger phase (default 15)
#   PAYMENT_COUNT       payments to submit (default 5)
#   BOOT_TIMEOUT        seconds to wait for the network to validate seq>=MIN_SEQ_EMPTY (default 180)
#   TX_TIMEOUT          seconds to wait for every submitted payment to validate (default 180)
#   KEEP_RUNNING        if "1", do not tear down on exit (for debugging)

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.yml"

GOXRPL_IMAGE="${GOXRPL_IMAGE:-goxrpl:latest}"
RIPPLED_IMAGE="${RIPPLED_IMAGE:-xrpllabsofficial/xrpld:3.3.0}"
MIN_SEQ_EMPTY="${MIN_SEQ_EMPTY:-15}"
PAYMENT_COUNT="${PAYMENT_COUNT:-5}"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-180}"
TX_TIMEOUT="${TX_TIMEOUT:-180}"
KEEP_RUNNING="${KEEP_RUNNING:-0}"

# Genesis credentials (same private-net master across xrpl-confluence + here).
GENESIS_ADDRESS="rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
GENESIS_SECRET="snoPBrXtMeMyMHUVTgbuqAfg1SUTb"

# Destination addresses for the tx phase. Deterministic test wallets — only
# their addresses matter; the smoke never needs the secrets because we send
# from genesis.
DEST_ADDRS=(
    "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
    "rJrxi4Wxev4bnAGVNP9YCdKPdAoKfAmcsi"
)

# --- helpers ------------------------------------------------------------

log() { printf "[smoke] %s\n" "$*" >&2; }

require() {
    for cmd in "$@"; do
        if ! command -v "$cmd" >/dev/null 2>&1; then
            log "missing required command: $cmd"
            exit 2
        fi
    done
}

compose() {
    GOXRPL_IMAGE="$GOXRPL_IMAGE" RIPPLED_IMAGE="$RIPPLED_IMAGE" \
        docker compose -f "$COMPOSE_FILE" "$@"
}

teardown() {
    local code=$?
    if [[ "$KEEP_RUNNING" != "1" ]]; then
        log "tearing down (exit=$code)"
        compose down -v --remove-orphans >/dev/null 2>&1 || true
    else
        log "KEEP_RUNNING=1 — leaving containers up for inspection (exit=$code)"
        compose ps
    fi
    exit "$code"
}

# Host-mapped RPC URL for a given service. Reads the published port from
# `docker compose port` so the smoke works regardless of which host port
# Docker picks.
rpc_url() {
    local svc="$1"
    local raw
    raw="$(compose port "$svc" 5005 2>/dev/null | tail -1)" || return 1
    # raw is "0.0.0.0:NNNN" — normalize to localhost.
    raw="${raw##*:}"
    if [[ -z "$raw" ]]; then return 1; fi
    printf "http://127.0.0.1:%s" "$raw"
}

rpc_call() {
    local url="$1"
    local payload="$2"
    curl -sS --max-time 5 -H 'Content-Type: application/json' \
         --data "$payload" "$url"
}

# Validated ledger info for one node. Echoes "seq hash account_hash tx_hash"
# (space-separated) or empty on failure.
validated_info() {
    local url="$1"
    local resp
    resp="$(rpc_call "$url" '{"method":"ledger","params":[{"ledger_index":"validated","transactions":false,"expand":false}]}' 2>/dev/null)" || return 1
    [[ -n "$resp" ]] || return 1
    echo "$resp" | jq -r '.result.ledger | "\(.ledger_index) \(.ledger_hash) \(.account_hash) \(.transaction_hash)"' 2>/dev/null
}

# Wait until every node reports validated_seq >= target. Polls every 2s.
wait_validated_at_least() {
    local target="$1"
    local timeout="$2"
    local deadline=$(($(date +%s) + timeout))
    while (( $(date +%s) < deadline )); do
        local all_ok=1
        local summary=""
        for svc in rippled-0 rippled-1 goxrpl-0; do
            local url info seq
            url="$(rpc_url "$svc")" || { all_ok=0; break; }
            info="$(validated_info "$url" 2>/dev/null)" || { all_ok=0; summary="$svc not responding"; break; }
            seq="$(awk '{print $1}' <<<"$info")"
            summary+=" $svc=$seq"
            if [[ -z "$seq" || "$seq" == "null" || "$seq" -lt "$target" ]]; then
                all_ok=0
            fi
        done
        if (( all_ok )); then
            log "all nodes reached validated seq >= $target —$summary"
            return 0
        fi
        log "waiting for seq >= $target —$summary"
        sleep 2
    done
    log "timeout waiting for validated seq >= $target after ${timeout}s"
    return 1
}

SERVICES=(rippled-0 rippled-1 goxrpl-0)

# Each node's ledger snapshot is reported as "seq ledger_hash account_hash
# transaction_hash". ledger_hash is the canonical agreement signal: it's a
# hash *of* account_hash + transaction_hash + parent + close info, so if
# ledger_hash matches across nodes the underlying tree contents agree by
# definition — even if a node's RPC happens to report transaction_hash as
# all zeros (a known goxrpl RPC quirk that's NOT a real consensus divergence).
#
# So we assert strict equality on ledger_hash, and log the other hashes
# for diagnostic context. account_hash equality is also checked because
# it's reported reliably by both implementations.

# Compare ledger_hash + account_hash across all three nodes at the lowest
# common validated seq.
#
# Uses parallel arrays rather than declare -A so the script runs on macOS's
# default bash 3.2 in addition to Linux bash 4/5.
assert_hashes_agree() {
    local label="$1"
    local min_seq=""
    local i seq url info
    for i in "${!SERVICES[@]}"; do
        url="$(rpc_url "${SERVICES[i]}")"
        info="$(validated_info "$url")"
        if [[ -z "$info" ]]; then
            log "[$label] ${SERVICES[i]}: failed to read validated ledger"
            return 1
        fi
        seq="$(awk '{print $1}' <<<"$info")"
        if [[ -z "$min_seq" || "$seq" -lt "$min_seq" ]]; then
            min_seq="$seq"
        fi
    done
    compare_ledger_at_seq "$label" "$min_seq"
}

# Re-fetch each node's view of the given seq and assert ledger_hash +
# account_hash agree. Logs all four fields (seq / ledger_hash / account_hash
# / transaction_hash) for diagnostic context.
compare_ledger_at_seq() {
    local label="$1"
    local seq="$2"
    local common_info=()
    local i url resp row
    for i in "${!SERVICES[@]}"; do
        url="$(rpc_url "${SERVICES[i]}")"
        resp="$(rpc_call "$url" "{\"method\":\"ledger\",\"params\":[{\"ledger_index\":$seq,\"transactions\":false,\"expand\":false}]}")"
        row="$(echo "$resp" | jq -r '.result.ledger | "\(.ledger_index) \(.ledger_hash) \(.account_hash) \(.transaction_hash)"')"
        common_info[i]="$row"
    done

    log "[$label] seq=$seq ledger snapshot:"
    for i in "${!SERVICES[@]}"; do
        log "    ${SERVICES[i]}: ${common_info[i]}"
    done

    local ref_ledger ref_account
    ref_ledger="$(awk '{print $2}' <<<"${common_info[0]}")"
    ref_account="$(awk '{print $3}' <<<"${common_info[0]}")"
    local fail=0
    for i in "${!SERVICES[@]}"; do
        local lh ah
        lh="$(awk '{print $2}' <<<"${common_info[i]}")"
        ah="$(awk '{print $3}' <<<"${common_info[i]}")"
        if [[ "$lh" != "$ref_ledger" ]]; then
            log "[$label] DIVERGENCE: ${SERVICES[i]} ledger_hash $lh != $ref_ledger"
            fail=1
        fi
        if [[ "$ah" != "$ref_account" ]]; then
            log "[$label] DIVERGENCE: ${SERVICES[i]} account_hash $ah != $ref_account"
            fail=1
        fi
    done

    if (( fail )); then
        return 1
    fi
    log "[$label] all three nodes agree on seq=$seq (ledger_hash + account_hash)"
    return 0
}

submit_payment() {
    local url="$1" dest="$2" amount_drops="$3"
    rpc_call "$url" "$(cat <<EOF
{"method":"submit","params":[{
    "secret":"$GENESIS_SECRET",
    "fee_mult_max":10000,
    "tx_json":{
        "TransactionType":"Payment",
        "Account":"$GENESIS_ADDRESS",
        "Destination":"$dest",
        "Amount":"$amount_drops"
    }
}]}
EOF
)"
}

SUBMITTED_TX_HASHES=()

run_tx_burst() {
    local url
    url="$(rpc_url rippled-0)"
    local ok=0 fail=0
    SUBMITTED_TX_HASHES=()
    for ((i=0; i<PAYMENT_COUNT; i++)); do
        local dest="${DEST_ADDRS[i % ${#DEST_ADDRS[@]}]}"
        local resp engine txh
        # 20 XRP = 20,000,000 drops. Comfortably above the 10 XRP account
        # reserve on the test network so each Payment funds a new account.
        resp="$(submit_payment "$url" "$dest" "$((20000000 + i))")"
        engine="$(echo "$resp" | jq -r '.result.engine_result // "rpc_error"')"
        if [[ "$engine" == "tesSUCCESS" || "$engine" == "terQUEUED" ]]; then
            txh="$(echo "$resp" | jq -r '.result.tx_json.hash // empty')"
            if [[ -n "$txh" && "$txh" != "null" ]]; then
                ok=$((ok + 1))
                SUBMITTED_TX_HASHES+=("$txh")
            else
                fail=$((fail + 1))
                log "submit $((i+1))/$PAYMENT_COUNT: $engine response did not include a transaction hash"
            fi
        else
            fail=$((fail + 1))
            log "submit $((i+1))/$PAYMENT_COUNT: $engine"
        fi
        # Small pause so the genesis account's sequence number is observed
        # by rippled before the next sign_and_submit. Without this we lose
        # ~50% of submissions to sequence races on the same account.
        sleep 1
    done
    log "tx burst: $ok accepted, $fail rejected"
    if (( ok != PAYMENT_COUNT )); then
        log "only $ok/$PAYMENT_COUNT payments were accepted — aborting tx phase"
        return 1
    fi
}

# Ledger sequence for each submitted hash, populated by
# wait_for_all_txs_in_ledger after every payment is validated successfully.
TX_LEDGER_SEQS=()

# Polls rippled-0's tx RPC until every submitted payment is validated with a
# successful transaction result. A queued submission is accepted in the burst,
# but it must still settle as tesSUCCESS before the smoke can pass.
wait_for_all_txs_in_ledger() {
    local timeout="$1"
    local deadline=$(($(date +%s) + timeout))
    local url h resp validated result seq pending i
    url="$(rpc_url rippled-0)"
    while (( $(date +%s) < deadline )); do
        pending=0
        TX_LEDGER_SEQS=()
        for i in "${!SUBMITTED_TX_HASHES[@]}"; do
            h="${SUBMITTED_TX_HASHES[i]}"
            resp="$(rpc_call "$url" "{\"method\":\"tx\",\"params\":[{\"transaction\":\"$h\"}]}")"
            validated="$(echo "$resp" | jq -r '.result.validated // false')"
            result="$(echo "$resp" | jq -r '.result.meta.TransactionResult // .result.engine_result // empty')"
            seq="$(echo "$resp" | jq -r '.result.ledger_index // empty')"
            if [[ "$validated" == "true" && "$result" != "tesSUCCESS" ]]; then
                log "payment $((i+1))/$PAYMENT_COUNT validated with unexpected result ${result:-unknown}"
                return 1
            fi
            if [[ "$validated" != "true" || -z "$seq" || "$seq" == "null" || "$result" != "tesSUCCESS" ]]; then
                pending=$((pending + 1))
            else
                TX_LEDGER_SEQS[i]="$seq"
            fi
        done
        if (( pending == 0 )); then
            log "all $PAYMENT_COUNT submitted payments validated with tesSUCCESS"
            return 0
        fi
        log "waiting for $pending/$PAYMENT_COUNT submitted payments to validate"
        sleep 2
    done
    log "timeout waiting for all $PAYMENT_COUNT submitted payments to validate after ${timeout}s"
    return 1
}

# Wrapper around compare_ledger_at_seq that additionally asserts at least
# one rippled node reports a non-zero transaction_hash for this seq — i.e.,
# we're checking a ledger that *contains transactions*, not an empty close
# right after the tx-containing one.
assert_hashes_at_seq() {
    local label="$1"
    local seq="$2"
    # Pre-flight: confirm rippled-0 sees a tx in this ledger before we
    # assert. (goxrpl's transaction_hash RPC reporting is currently buggy
    # — see #419 — so we read this off rippled.)
    local resp tx_hash url
    url="$(rpc_url rippled-0)"
    resp="$(rpc_call "$url" "{\"method\":\"ledger\",\"params\":[{\"ledger_index\":$seq,\"transactions\":false,\"expand\":false}]}")"
    tx_hash="$(echo "$resp" | jq -r '.result.ledger.transaction_hash // ""')"
    if [[ "$tx_hash" == "0000000000000000000000000000000000000000000000000000000000000000" || -z "$tx_hash" ]]; then
        log "[$label] expected seq=$seq to contain transactions but rippled-0 reports transaction_hash=0"
        return 1
    fi
    compare_ledger_at_seq "$label" "$seq"
}

# --- main ---------------------------------------------------------------

require docker jq curl awk
if ! [[ "$PAYMENT_COUNT" =~ ^[1-9][0-9]*$ ]]; then
    log "PAYMENT_COUNT must be a positive integer, got '$PAYMENT_COUNT'"
    exit 2
fi
trap teardown EXIT INT TERM

log "smoke config: goxrpl=$GOXRPL_IMAGE rippled=$RIPPLED_IMAGE empty_seq=$MIN_SEQ_EMPTY payments=$PAYMENT_COUNT"

log "bringing up topology"
compose down -v --remove-orphans >/dev/null 2>&1 || true
if ! compose up -d --quiet-pull; then
    log "docker compose up failed"
    exit 1
fi

log "phase 1 (empty): wait for all nodes to validate seq >= $MIN_SEQ_EMPTY"
if ! wait_validated_at_least "$MIN_SEQ_EMPTY" "$BOOT_TIMEOUT"; then
    log "phase 1 failed — dumping container logs"
    compose logs --no-color --tail=80
    exit 1
fi
if ! assert_hashes_agree "phase 1 / empty"; then
    log "phase 1 hash divergence — dumping container logs"
    compose logs --no-color --tail=80
    exit 1
fi

log "phase 2 (tx): submit $PAYMENT_COUNT payments, wait for every payment to validate"
if ! run_tx_burst; then
    compose logs --no-color --tail=80
    exit 1
fi
if ! wait_for_all_txs_in_ledger "$TX_TIMEOUT"; then
    log "phase 2: not every submitted payment reached a validated ledger in ${TX_TIMEOUT}s"
    compose logs --no-color --tail=80
    exit 1
fi

# Compare every distinct transaction-containing ledger. This keeps the
# consensus claim aligned with the acceptance claim above when payments span
# more than one close.
TX_ASSERTED_SEQS=()
for TX_LEDGER_SEQ in "${TX_LEDGER_SEQS[@]}"; do
    TX_ALREADY_ASSERTED=0
    for TX_ASSERTED_SEQ in "${TX_ASSERTED_SEQS[@]}"; do
        if [[ "$TX_ASSERTED_SEQ" == "$TX_LEDGER_SEQ" ]]; then
            TX_ALREADY_ASSERTED=1
            break
        fi
    done
    if (( TX_ALREADY_ASSERTED )); then
        continue
    fi
    TX_ASSERTED_SEQS+=("$TX_LEDGER_SEQ")
    if ! wait_validated_at_least "$TX_LEDGER_SEQ" "$TX_TIMEOUT"; then
        log "phase 2: a node didn't catch up to seq=$TX_LEDGER_SEQ — dumping logs"
        compose logs --no-color --tail=80
        exit 1
    fi
    if ! assert_hashes_at_seq "phase 2 / tx seq=$TX_LEDGER_SEQ" "$TX_LEDGER_SEQ"; then
        log "phase 2 hash divergence at seq=$TX_LEDGER_SEQ — dumping container logs"
        compose logs --no-color --tail=80
        exit 1
    fi
done

log "consensus smoke PASSED"
exit 0
