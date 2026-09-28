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


def parse(line):
    return {key: quoted or plain for key, quoted, plain in FIELDS.findall(line)}


def audit(lines, now, minutes=10):
    cutoff = now - dt.timedelta(minutes=minutes)
    mature = now - dt.timedelta(seconds=30)
    trusted, emitted = {}, {}
    recent = []
    progress = None
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
        if stamp < cutoff:
            continue
        message = fields.get("msg", "")
        if message == "stored SHAMap verification progress":
            progress = {key: fields[key] for key in ("root", "nodes_checked", "elapsed", "branches_complete") if key in fields}
            progress["time"] = stamp.isoformat()
        if fields.get("level") in ("WARN", "ERROR"):
            recent.append(line.rstrip())
        try:
            sequence = int(fields.get("seq", "0"))
        except ValueError:
            continue
        if sequence and message in ("Ledger fully validated", "trusted validation quorum observed") and stamp <= mature:
            trusted[sequence] = fields.get("hash", "").lower()
        if sequence and message == "validation emitted":
            emitted[sequence] = (fields.get("hash", "").lower(), fields.get("full") == "true")
    counts = dict(agreed=0, missed=0, partial=0, divergent=0, unverified=0)
    first = min(trusted) if trusted else None
    last = max(trusted) if trusted else None
    if trusted:
        for sequence in range(first, last + 1):
            validation = emitted.get(sequence)
            if validation is None:
                counts["missed"] += 1
            elif not trusted.get(sequence):
                counts["unverified"] += 1
            elif validation[0] != trusted[sequence]:
                counts["divergent"] += 1
            elif not validation[1]:
                counts["partial"] += 1
            else:
                counts["agreed"] += 1
    total = sum(counts.values())
    return dict(counts, ledger_first=first, ledger_last=last, total=total,
                agreement_percent=round(100 * counts["agreed"] / total, 4) if total else None,
                last_log_time=last_timestamp.isoformat() if last_timestamp else None,
                verification=progress, recent_warnings=recent[-10:])


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
    state = report.get("container", {}).get("state", {})
    info = report.get("server", {})
    if not state.get("Running"):
        report["reasons"].append("container is not running")
    elif info.get("server_state") in ("full", "proposing", "validating"):
        report["status"] = "HEALTHY"
        if info.get("validated_ledger", {}).get("age", 9999) > 30:
            report["reasons"].append("validated ledger is older than 30 seconds")
        if not report["total"]:
            report["reasons"].append("no mature trusted-ledger evidence in log window")
        for category in ("missed", "partial", "divergent", "unverified"):
            if report.get(category):
                report["reasons"].append(f'{category} validations: {report[category]}')
    else:
        progress = report.get("verification")
        old = previous.get("verification")
        fresh = progress and (now - dt.datetime.fromisoformat(progress["time"])).total_seconds() < 90
        advancing = fresh and (not old or old.get("root") != progress.get("root") or
                               int(progress.get("nodes_checked", 0)) > int(old.get("nodes_checked", 0)))
        if advancing:
            report["status"] = "VERIFYING"
            report["note"] = "Startup verification is advancing; RPC/validation are not yet ready."
        else:
            report["reasons"].append("not validating and no fresh advancing startup verification")
    old_container = previous.get("container", {})
    if old_container and old_container.get("state", {}).get("StartedAt") != state.get("StartedAt"):
        report["reasons"].append("container restarted or was replaced since previous check")
    if report["reasons"]:
        report["status"] = "ALERT"
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
                      ("checked_at", "status", "agreement_percent", "ledger_first", "ledger_last", "reasons", "verification")}))
    return 1 if report["status"] == "ALERT" else 0


if __name__ == "__main__":
    raise SystemExit(main())
