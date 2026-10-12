#!/usr/bin/env python3
"""Read-only health audit. A timer records evidence; it never blindly restarts a node."""
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.request

UTC = dt.timezone.utc
FIELDS = re.compile(r'(\w+)=(?:"([^"\n]*)"|(\S+))')
FULL_STATES = ("full", "proposing", "validating")


def parse(line):
    return {key: quoted or plain for key, quoted, plain in FIELDS.findall(line)}


def audit(lines, now, minutes=10):
    mature = now - dt.timedelta(seconds=30)
    # Inspect a full interval ending at the maturity watermark. Subtracting
    # the grace only from the end would leave 30 seconds uncovered between
    # audits that run exactly `minutes` apart.
    cutoff = mature - dt.timedelta(minutes=minutes)
    trusted, emitted = {}, {}
    recent = []
    progress = None
    replay = None
    last_timestamp = None
    for line in lines:
        fields = parse(line)
        try:
            stamp = dt.datetime.fromisoformat(fields.get("time", "").replace("Z", "+00:00"))
        except ValueError:
            continue
        if stamp.tzinfo is None:
            continue
        last_timestamp = max(last_timestamp, stamp) if last_timestamp else stamp
        message = fields.get("msg", "")
        try:
            sequence = int(fields.get("seq", "0"))
        except ValueError:
            sequence = 0
        # A full validation often precedes the matching quorum confirmation.
        # Select the denominator by quorum time, but retain emissions just
        # outside the window too, otherwise its first ledger can look missed.
        if sequence and message == "validation emitted":
            emitted[sequence] = (fields.get("hash", "").lower(), fields.get("full") == "true")
        if stamp < cutoff:
            continue
        if message in ("stored SHAMap verification progress", "replay admission verification progress"):
            progress = {key: fields[key] for key in ("root", "nodes_checked", "elapsed", "branches_complete") if key in fields}
            progress["time"] = stamp.isoformat()
        if sequence > 0 and message == "applied standard transaction replay pipeline entry":
            # The earlier 'replay delta applied' message precedes admission:
            # only the completed pipeline entry proves committed progress.
            replay = {key: fields[key] for key in ("hash", "generation") if key in fields}
            replay.update(seq=sequence, time=stamp.isoformat())
        if fields.get("level") in ("WARN", "ERROR"):
            recent.append(line.rstrip())
        if sequence and message in ("Ledger fully validated", "trusted validation quorum observed") and stamp <= mature:
            trusted[sequence] = fields.get("hash", "").lower()
    counts = dict(agreed=0, missed=0, partial=0, divergent=0, unverified=0)
    details = {key + "_sequences": [] for key in ("missed", "partial", "divergent", "unverified")}
    first = min(trusted) if trusted else None
    last = max(trusted) if trusted else None
    if trusted:
        for sequence in range(first, last + 1):
            validation = emitted.get(sequence)
            if validation is None:
                counts["missed"] += 1
                details["missed_sequences"].append(sequence)
            elif not trusted.get(sequence):
                counts["unverified"] += 1
                details["unverified_sequences"].append(sequence)
            elif validation[0] != trusted[sequence]:
                counts["divergent"] += 1
                details["divergent_sequences"].append(sequence)
            elif not validation[1]:
                counts["partial"] += 1
                details["partial_sequences"].append(sequence)
            else:
                counts["agreed"] += 1
    total = sum(counts.values())
    return dict(counts, **details, ledger_first=first, ledger_last=last, total=total,
                agreement_percent=round(100 * counts["agreed"] / total, 4) if total else None,
                window_start=cutoff.isoformat(), window_end=mature.isoformat(), maturity_grace_seconds=30,
                last_log_time=last_timestamp.isoformat() if last_timestamp else None,
                verification=progress, replay=replay, recent_warnings=recent[-10:])


def recent_lines(path, limit=64 * 1024 * 1024):
    with path.open("rb") as stream:
        size = stream.seek(0, os.SEEK_END)
        stream.seek(max(0, size - limit))
        if size > limit:
            stream.readline()  # Omit an incomplete first line.
        return stream.read().decode("utf-8", errors="replace").splitlines()


def container_state(container):
    template = '{"state":{{json .State}},"restarts":{{.RestartCount}},"image":{{json .Config.Image}}}'
    output = subprocess.check_output(["docker", "inspect", "--format", template, container],
                                     timeout=10, stderr=subprocess.STDOUT)
    return json.loads(output)


def server_info(url):
    request = urllib.request.Request(url, json.dumps({"method": "server_info", "params": [{"counters": True}]}).encode(),
                                     {"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=10) as response:
        result = json.load(response)
    info = result.get("result", {}).get("info")
    if not isinstance(info, dict):
        raise ValueError("server_info response has no info object")
    return info


def assess_health(report, previous, now):
    """Classify captured evidence without making RPC calls or changing the node."""
    report["status"] = "ALERT"
    report.setdefault("reasons", [])
    state = report.get("container", {}).get("state", {})
    info = report.get("server", {})
    old_container = previous.get("container", {})
    started = state.get("StartedAt")
    same_run = bool(started) and started == old_container.get("state", {}).get("StartedAt")
    reached_full = info.get("server_state") in FULL_STATES
    for name in FULL_STATES:
        accounting = info.get("state_accounting", {}).get(name, {})
        for field in ("transitions", "duration_us"):
            try:
                reached_full |= int(accounting.get(field, 0)) > 0
            except (TypeError, ValueError):
                pass
    if same_run:
        reached_full |= (bool(previous.get("has_reached_full_state")) or
                         previous.get("server", {}).get("server_state") in FULL_STATES)
    # A background re-certification walk can keep advancing after consensus
    # stops. Once this process has reached full state, such progress must not
    # downgrade the loss of normal operation to benign startup verification.
    # Carry this evidence across audits, but never across process restarts.
    report["has_reached_full_state"] = bool(reached_full)
    if not state.get("Running"):
        report["reasons"].append("container is not running")
    elif info.get("server_state") in FULL_STATES:
        report["status"] = "HEALTHY"
        if info.get("validated_ledger", {}).get("age", 9999) > 30:
            report["reasons"].append("validated ledger is older than 30 seconds")
        if not report["total"]:
            report["reasons"].append("no mature trusted-ledger evidence in log window")
        for category in ("missed", "partial", "divergent", "unverified"):
            if report.get(category):
                report["reasons"].append(f'{category} validations: {report[category]}')
    elif reached_full:
        report["reasons"].append("node left full state after previously reaching normal validation")
    else:
        replay = report.get("replay")
        old_replay = previous.get("replay") if same_run else None
        replay_advancing = False
        if replay and info.get("server_state") in ("connected", "syncing", "tracking"):
            age = (now - dt.datetime.fromisoformat(replay["time"])).total_seconds()
            replay_advancing = (0 <= age < 90 and
                                (not old_replay or int(replay["seq"]) > int(old_replay["seq"])))
        progress = report.get("verification")
        old = previous.get("verification")
        fresh = progress and (now - dt.datetime.fromisoformat(progress["time"])).total_seconds() < 90
        advancing = fresh and (not old or old.get("root") != progress.get("root") or
                               int(progress.get("nodes_checked", 0)) > int(old.get("nodes_checked", 0)))
        if replay_advancing:
            report["status"] = "REPLAYING"
            report["note"] = "Startup replay is committing newer ledgers; live validation has not resumed."
        elif advancing:
            report["status"] = "VERIFYING"
            report["note"] = "Startup verification is advancing; RPC/validation are not yet ready."
        else:
            report["reasons"].append("not validating and no fresh advancing startup verification or replay")
    if info.get("replay_fault", {}).get("blocked"):
        report["reasons"].append("validation is blocked by a replay fault")
    if old_container and old_container.get("state", {}).get("StartedAt") != state.get("StartedAt"):
        report["reasons"].append("container restarted or was replaced since previous check")
    if report["reasons"]:
        report["status"] = "ALERT"
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--log", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--minutes", type=int, default=10)
    parser.add_argument("--container", default="goxrpl-testnet-validator")
    parser.add_argument("--rpc-url", default="http://127.0.0.1:5005/")
    args = parser.parse_args()
    if args.minutes <= 0:
        parser.error("minutes must be positive")
    now = dt.datetime.now(UTC)
    args.output.mkdir(parents=True, exist_ok=True)
    previous = {}
    latest = args.output / "latest.json"
    if latest.exists():
        try:
            previous = json.loads(latest.read_text())
        except (ValueError, OSError):
            pass
    try:
        report = audit(recent_lines(args.log), now, args.minutes)
    except OSError as error:
        report = {"log_error": str(error), "verification": None, "total": 0}
    report.update(checked_at=now.isoformat(), window_minutes=args.minutes, status="ALERT", reasons=[])
    try:
        report["container"] = container_state(args.container)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        report["reasons"].append("container inspection failed: " + str(error))
    try:
        report["server"] = server_info(args.rpc_url)
    except (OSError, ValueError) as error:
        report["rpc_error"] = str(error)
    assess_health(report, previous, now)
    for resource in ("cpu", "io", "memory"):
        try:
            report[resource + "_pressure"] = Path("/proc/pressure", resource).read_text().strip()
        except OSError:
            pass
    payload = json.dumps(report, indent=2) + "\n"
    stamp = now.strftime("%Y%m%dT%H%M%SZ")
    (args.output / (stamp + ".json")).write_text(payload)
    temporary = args.output / "latest.json.partial"
    temporary.write_text(payload)
    temporary.replace(latest)
    print(json.dumps({key: report.get(key) for key in
                      ("checked_at", "status", "agreement_percent", "ledger_first", "ledger_last", "reasons", "verification", "replay")}))
    return 1 if report["status"] == "ALERT" else 0


if __name__ == "__main__":
    raise SystemExit(main())
