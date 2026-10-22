#!/usr/bin/env python3
"""Keep bounded pprof windows around slow consensus acceptance log events."""

import argparse
import datetime as dt
import json
import os
import re
import shutil
import signal
import sys
import tempfile
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

FIELDS = re.compile(r'(\w+)=(?:"((?:\\.|[^"])*)"|(\S+))')
DURATION = re.compile(r'(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)')
ACCEPTANCE_MESSAGE = "Consensus acceptance timings"
MAX_LOG_LINE = 1 << 20
MANIFEST_HEADROOM = 16 * 1024
UTC = dt.timezone.utc


def ring_window_quota(max_bytes, window_seconds, ring_seconds):
    retained = max(1, (ring_seconds + window_seconds - 1) // window_seconds)
    return max(1, max_bytes // (retained + 1))


class RefuseRedirects(HTTPRedirectHandler):
    def redirect_request(self, *_):
        raise ValueError("pprof redirect refused")


OPEN = build_opener(RefuseRedirects)


def duration(value):
    if isinstance(value, (int, float)):
        return float(value) * 1e-9
    text, position, result = str(value), 0, 0.0
    for match in DURATION.finditer(text):
        if match.start() != position:
            raise ValueError(f"invalid duration: {value!r}")
        result += float(match.group(1)) * {"ns": 1e-9, "us": 1e-6, "µs": 1e-6,
                                           "ms": 1e-3, "s": 1, "m": 60, "h": 3600}[match.group(2)]
        position = match.end()
    if position != len(text) or not position:
        raise ValueError(f"invalid duration: {value!r}")
    return result


def parse_fields(line):
    text = line.strip()
    if text.startswith("{"):
        value = json.loads(text)
        return value if isinstance(value, dict) else {}
    return {key: quoted or plain for key, quoted, plain in FIELDS.findall(text)}


def acceptance_event(line):
    try:
        fields = parse_fields(line)
        if fields.get("msg", fields.get("message")) != ACCEPTANCE_MESSAGE:
            return None
        total = duration(fields["total"])
    except (KeyError, TypeError, ValueError, json.JSONDecodeError):
        return None
    event = {key: fields[key] for key in ("seq", "parse", "transaction_apply", "next_open") if key in fields}
    if fields.get("time", fields.get("timestamp")) is not None:
        event["acceptance_log_time"] = fields.get("time", fields.get("timestamp"))
    event["total_seconds"] = total
    return event


def loopback_url(value):
    parsed = urlsplit(value)
    try:
        port = parsed.port
    except ValueError as error:
        raise ValueError("pprof URL has an invalid port") from error
    if (parsed.scheme != "http" or parsed.username or parsed.password or
            parsed.hostname not in ("127.0.0.1", "localhost", "::1") or
            parsed.path not in ("", "/") or parsed.query or parsed.fragment or port is None):
        raise ValueError("pprof URL must be loopback HTTP without credentials or a path")
    return value.rstrip("/")


def fetch(url, destination, timeout, limit):
    partial = destination.with_name(destination.name + ".partial")
    try:
        with OPEN.open(Request(url), timeout=timeout) as response, partial.open("wb") as output:
            size = 0
            while chunk := response.read(64 * 1024):
                size += len(chunk)
                if size > limit:
                    raise ValueError("pprof response exceeds the byte budget")
                output.write(chunk)
        os.replace(partial, destination)
        return size
    except Exception as error:
        partial.unlink(missing_ok=True)
        destination.unlink(missing_ok=True)
        return error


class Store:
    def __init__(self, root, max_count, max_bytes, log, overlap_bytes=0):
        self.root, self.max_count, self.max_bytes, self.log = root, max_count, max_bytes, log
        self.lock, self.count, self.bytes, self.reserved = threading.Lock(), 0, 0, 0
        self.manifest_reserve = min(MANIFEST_HEADROOM,
                                    max(256, max_bytes // (4 * max(1, max_count))))
        self.metadata_reserved = 0
        self.overlap_reserved = min(max_bytes, max(0, overlap_bytes))
        self.root.mkdir(parents=True, exist_ok=False)

    def new(self, metadata):
        with self.lock:
            if self.count >= self.max_count:
                self.log("incident limit reached; trigger ignored")
                return None
            directory = self.root / f"incident-{self.count + 1:03d}"
            directory.mkdir()
            self.count += 1
        event = {"dir": directory, "meta": dict(metadata), "artifacts": [],
                 "sources": set(), "lock": threading.Lock()}
        with self.lock:
            data = self._manifest_data(event)
            if (self.bytes + len(data) + self.reserved + self.overlap_reserved +
                    self.metadata_reserved + self.manifest_reserve > self.max_bytes):
                shutil.rmtree(directory)
                self.log("incident byte budget exhausted; trigger ignored")
                return None
            directory.joinpath("manifest.json").write_bytes(data)
            self.bytes += len(data)
            self.metadata_reserved += self.manifest_reserve
        return event

    @staticmethod
    def _manifest_data(event):
        return json.dumps({**event["meta"], "artifacts": event["artifacts"]},
                          sort_keys=True, indent=2).encode()

    def manifest(self, event):
        data = self._manifest_data(event)
        path = event["dir"] / "manifest.json"
        with self.lock:
            old = path.stat().st_size if path.exists() else 0
            metadata_reserved = max(0, self.metadata_reserved - max(0, len(data) - old))
            if (self.bytes - old + len(data) + self.reserved + self.overlap_reserved +
                    metadata_reserved > self.max_bytes):
                return False
            partial = path.with_name(path.name + ".partial")
            partial.write_bytes(data)
            os.replace(partial, path)
            self.bytes += len(data) - old
            self.metadata_reserved = metadata_reserved
            return True

    def reserve(self):
        with self.lock:
            available = (self.max_bytes - self.bytes - self.reserved -
                         self.metadata_reserved - self.overlap_reserved)
            self.reserved += max(0, available)
            return max(0, available)

    def _record_artifact(self, event, artifact, target, size, overlap=False):
        event["artifacts"].append(artifact)
        if self.manifest(event):
            return artifact["status"]
        self.log(f"incident manifest budget exhausted: {artifact['name']}")
        if artifact["status"] == "complete":
            with self.lock:
                target.unlink(missing_ok=True)
                self.bytes -= size
                if overlap:
                    self.overlap_reserved += size
            artifact.update(status="skipped", bytes=0,
                            error="incident manifest budget exhausted")
        else:
            artifact["error"] = "incident manifest budget exhausted"
        self.manifest(event)
        return artifact["status"]

    def source(self, event, source, name, overlap=False,
               capture_started_at=None, capture_finished_at=None):
        started = dt.datetime.now(UTC).isoformat()
        status, size, error = "failed", 0, None
        with event["lock"]:
            key = str(source)
            if key in event["sources"]:
                return "duplicate"
            event["sources"].add(key)
            target = event["dir"] / name
            try:
                size = source.stat().st_size
                with self.lock:
                    budget = self.bytes + size + self.reserved + self.metadata_reserved
                    if not overlap:
                        budget += self.overlap_reserved
                    if budget > self.max_bytes:
                        if self.reserved:
                            event["sources"].remove(key)
                            return "deferred"
                        status, error = "skipped", "incident byte budget exhausted"
                    else:
                        partial = target.with_name(target.name + ".partial")
                        shutil.copyfile(source, partial)
                        os.replace(partial, target)
                        self.bytes += size
                        if overlap:
                            self.overlap_reserved = max(0, self.overlap_reserved - size)
                        status = "complete"
                if status != "complete":
                    size = 0
            except (FileNotFoundError, OSError):
                target.with_name(target.name + ".partial").unlink(missing_ok=True)
                error = "source window is unavailable"
            artifact = {"name": name, "kind": "ring", "status": status,
                        "bytes": size, "started_at": started,
                        "finished_at": dt.datetime.now(UTC).isoformat()}
            if capture_started_at:
                artifact["capture_started_at"] = capture_started_at
            if capture_finished_at:
                artifact["capture_finished_at"] = capture_finished_at
            if error:
                artifact["error"] = error
            return self._record_artifact(event, artifact, target, size, overlap)

    def capture(self, event, url, name, timeout):
        target, started = event["dir"] / name, dt.datetime.now(UTC).isoformat()
        limit = self.reserve()
        result = fetch(url, target, timeout, limit) if limit else ValueError("incident byte budget exhausted")
        finished = dt.datetime.now(UTC).isoformat()
        status, size, error = ("failed" if limit else "skipped"), 0, None
        with self.lock:
            self.reserved -= limit
            if isinstance(result, int) and self.bytes + result <= self.max_bytes:
                self.bytes += result
                status, size = "complete", result
            else:
                target.unlink(missing_ok=True)
                error = ("incident byte budget exhausted" if isinstance(result, int) else str(result))
        with event["lock"]:
            artifact = {"name": name, "kind": "post", "status": status,
                        "bytes": size, "started_at": started, "finished_at": finished}
            if error:
                artifact["error"] = error
            self._record_artifact(event, artifact, target, size)


@dataclass
class Window:
    started: float
    path: Path
    ended: float = 0
    limit: int = 0
    capture_started_at: str = ""
    capture_finished_at: str = ""


class CaptureRing:
    def __init__(self, root, window_seconds, ring_seconds, max_bytes, store):
        self.root, self.window, self.ring, self.max_bytes = root, window_seconds, ring_seconds, max_bytes
        self.retained_windows = max(1, (ring_seconds + window_seconds - 1) // window_seconds)
        self.window_quota = ring_window_quota(max_bytes, window_seconds, ring_seconds)
        self.store = store
        self.lock, self.active, self.windows, self.pending, self.serial, self.bytes = threading.Lock(), None, [], [], 0, 0
        self.root.mkdir()

    def begin(self, started):
        with self.lock:
            self.prune(started)
            while self.windows and self.bytes + self.window_quota > self.max_bytes:
                self._drop_oldest()
            self.serial += 1
            limit = min(self.window_quota, max(0, self.max_bytes - self.bytes))
            self.active = Window(started, self.root / f"cpu-{self.serial:06d}.pb", limit=limit,
                                 capture_started_at=dt.datetime.now(UTC).isoformat())
            return self.active

    def _drop_oldest(self):
        old = self.windows.pop(0)
        try:
            self.bytes -= old.path.stat().st_size
        except FileNotFoundError:
            pass
        old.path.unlink(missing_ok=True)

    def cancel(self, window):
        with self.lock:
            if self.active is window:
                self.active = None
            self.prune(time.monotonic())

    def publish(self, window, ended):
        with self.lock:
            if self.active is not window:
                return
            self.active = None
            try:
                size = window.path.stat().st_size
            except FileNotFoundError:
                return
            if size > window.limit or self.bytes + size > self.max_bytes:
                window.path.unlink(missing_ok=True)
                return
            window.ended = ended
            window.capture_finished_at = dt.datetime.now(UTC).isoformat()
            self.windows.append(window)
            self.bytes += size
            for event, trigger in self.pending[:]:
                if window.started <= trigger <= ended:
                    status = self.store.source(
                        event, window.path, f"overlap-{window.path.name}", overlap=True,
                        capture_started_at=window.capture_started_at,
                        capture_finished_at=window.capture_finished_at)
                    if status != "deferred":
                        self.pending.remove((event, trigger))
            self.prune(ended)

    def snapshot(self, event, trigger):
        with self.lock:
            for window in self.windows:
                if window.started <= trigger and window.ended > trigger - self.ring:
                    self.store.source(
                        event, window.path, f"pre-{window.path.name}",
                        capture_started_at=window.capture_started_at,
                        capture_finished_at=window.capture_finished_at)
            if self.active and self.active.started <= trigger:
                self.pending.append((event, trigger))

    def retry_pending(self):
        with self.lock:
            for event, trigger in self.pending[:]:
                for window in self.windows:
                    if window.started <= trigger <= window.ended:
                        status = self.store.source(
                            event, window.path, f"overlap-{window.path.name}", overlap=True,
                            capture_started_at=window.capture_started_at,
                            capture_finished_at=window.capture_finished_at)
                        if status != "deferred":
                            self.pending.remove((event, trigger))
                        break

    def prune(self, now):
        keep = now - self.ring
        while self.windows and (self.windows[0].ended <= keep or
                                 len(self.windows) > self.retained_windows or
                                self.bytes > self.max_bytes):
            old = self.windows.pop(0)
            try:
                self.bytes -= old.path.stat().st_size
            except FileNotFoundError:
                pass
            old.path.unlink(missing_ok=True)
        self.pending[:] = [(event, trigger) for event, trigger in self.pending
                           if trigger >= keep]


class TriggerGate:
    def __init__(self, threshold, cooldown):
        self.threshold, self.cooldown, self.last = threshold, cooldown, -float("inf")

    def allow(self, total, now):
        if total < self.threshold or now - self.last < self.cooldown:
            return False
        self.last = now
        return True


def follow(path, stop, callback, log, ready=None):
    handle, identity, carry, discarding = None, None, "", False
    while not stop.is_set():
        try:
            info = path.stat()
            rotated = identity != (info.st_dev, info.st_ino) or (handle and info.st_size < handle.tell())
            if handle is None or rotated:
                if handle:
                    handle.close()
                handle = path.open(encoding="utf-8", errors="replace")
                if identity is None:
                    handle.seek(0, os.SEEK_END)
                identity, carry, discarding = (info.st_dev, info.st_ino), "", False
                if ready:
                    ready.set()
            line = handle.readline(MAX_LOG_LINE + 1)
            if line:
                if discarding:
                    if line.endswith("\n"):
                        discarding = False
                    continue
                carry += line
                if len(carry) > MAX_LOG_LINE:
                    log("discarding oversized log line")
                    carry = ""
                    discarding = not line.endswith("\n")
                    continue
                if line.endswith("\n"):
                    try:
                        callback(carry.rstrip("\n"))
                    except Exception as error:
                        log(f"log event handler failed: {error}")
                    carry = ""
            else:
                stop.wait(0.2)
        except FileNotFoundError:
            if handle:
                handle.close()
            handle, identity, carry, discarding = None, None, "", False
            stop.wait(1)
        except (OSError, UnicodeError) as error:
            log(f"log follower retrying: {error}")
            stop.wait(1)
    if handle:
        handle.close()


def run(args):
    base = args.output.resolve()
    base.mkdir(parents=True, exist_ok=True)
    root = Path(tempfile.mkdtemp(prefix="slow-acceptance-", dir=base))
    log_lock = threading.Lock()

    def log(message):
        line = f"{dt.datetime.now(UTC).isoformat()} {message}"
        with log_lock:
            print(line, file=sys.stderr, flush=True)

    overlap_quota = ring_window_quota(args.ring_bytes, args.window, args.ring)
    overlap_budget = min(args.max_bytes // 2, overlap_quota * args.max_incidents)
    store = Store(root / "incidents", args.max_incidents, args.max_bytes, log,
                  overlap_bytes=overlap_budget)
    ring = CaptureRing(root / "ring", args.window, args.ring, args.ring_bytes, store)
    gate, stop, posts = TriggerGate(args.threshold, args.cooldown), threading.Event(), []

    def trigger(line):
        event = acceptance_event(line)
        now = time.monotonic()
        if not event or not gate.allow(event["total_seconds"], now):
            return
        event.update(trigger_observed_at=dt.datetime.now(UTC).isoformat(),
                     threshold_seconds=args.threshold, status="capturing")
        incident = store.new(event)
        if incident is None:
            return
        ring.snapshot(incident, now)

        def post():
            store.capture(incident, f"{args.pprof}/debug/pprof/trace?seconds={args.post}",
                          "post-trace.pb.gz", args.post + 10)
            ring.retry_pending()
            store.capture(incident, f"{args.pprof}/debug/pprof/goroutine?debug=1",
                          "post-goroutine.txt", 10)
            ring.retry_pending()
            with incident["lock"]:
                captures = [artifact for artifact in incident["artifacts"] if artifact["kind"] == "post"]
                complete = sum(artifact["status"] == "complete" for artifact in captures)
                incident["meta"]["status"] = ("complete" if complete == len(captures) else
                                                "partial" if complete else "failed")
                store.manifest(incident)

        thread = threading.Thread(target=post, name="slow-acceptance-post", daemon=True)
        posts.append(thread)
        thread.start()
        log(f"capturing {incident['dir'].name} total={event['total_seconds']:.3f}s")

    def profiles():
        while not stop.is_set():
            window = ring.begin(time.monotonic())
            result = fetch(f"{args.pprof}/debug/pprof/profile?seconds={args.window}", window.path,
                           args.window + 10, window.limit)
            if isinstance(result, int):
                ring.publish(window, time.monotonic())
            else:
                ring.cancel(window)
                log(f"CPU profile unavailable: {result}")
                stop.wait(args.window)

    profile_thread = threading.Thread(target=profiles, name="slow-acceptance-cpu", daemon=True)
    log_thread = threading.Thread(target=follow, args=(args.log, stop, trigger, log), daemon=True)
    try:
        profile_thread.start()
        log_thread.start()
        while not stop.wait(0.5):
            pass
    finally:
        stop.set()
        profile_thread.join(args.window + 15)
        log_thread.join(2)
        for thread in posts:
            thread.join(args.post + 25)
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--log", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path, help="parent for a new recorder directory")
    parser.add_argument("--pprof-url", default="http://127.0.0.1:6060", type=loopback_url)
    parser.add_argument("--threshold", default="1s", type=duration)
    parser.add_argument("--cooldown", default="60s", type=duration)
    parser.add_argument("--window", default=5, type=int)
    parser.add_argument("--ring", default=60, type=int)
    parser.add_argument("--post", default=5, type=int)
    parser.add_argument("--max-incidents", default=3, type=int)
    parser.add_argument("--max-bytes", default=256 * 1024 * 1024, type=int)
    parser.add_argument("--ring-bytes", default=64 * 1024 * 1024, type=int)
    args = parser.parse_args()
    if not (1 <= args.window <= 60 and args.ring >= args.window and 1 <= args.post <= 60):
        parser.error("window/post must be 1..60 seconds and ring must cover one window")
    if args.max_incidents < 1 or args.max_bytes < 1 or args.ring_bytes < 1:
        parser.error("capture limits must be positive")
    args.pprof = args.pprof_url
    try:
        return run(args)
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    stop_signal = lambda *_: (_ for _ in ()).throw(KeyboardInterrupt)
    signal.signal(signal.SIGINT, stop_signal)
    signal.signal(signal.SIGTERM, stop_signal)
    main()
