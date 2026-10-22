import contextlib
import importlib.util
import json
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


SPEC = importlib.util.spec_from_file_location("slow_acceptance", Path(__file__).with_name("profile-slow-acceptance.py"))
slow = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(slow)


class PProfHandler(BaseHTTPRequestHandler):
    requests = []
    fail_posts = False

    def do_GET(self):
        self.requests.append(self.path)
        if self.fail_posts and (self.path.startswith("/debug/pprof/trace") or
                                self.path.startswith("/debug/pprof/goroutine")):
            self.send_error(500)
            return
        if self.path.startswith("/debug/pprof/profile"):
            time.sleep(0.05)
            body = b"cpu-profile"
        elif self.path.startswith("/debug/pprof/trace"):
            body = b"trace"
        elif self.path.startswith("/debug/pprof/goroutine"):
            body = b"goroutine dump"
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


@contextlib.contextmanager
def fake_server(fail_posts=False):
    PProfHandler.requests = []
    PProfHandler.fail_posts = fail_posts
    server = ThreadingHTTPServer(("127.0.0.1", 0), PProfHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}"
    finally:
        server.shutdown()
        server.server_close()
        thread.join(2)


def wait_for(predicate, timeout=2):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(0.02)
    return predicate()


class ProfileRecorderTests(unittest.TestCase):
    def test_acceptance_event_parses_text_and_json(self):
        text = ('time=2026-10-22T12:00:00Z level=INFO '
                'msg="Consensus acceptance timings" seq=7 total=2.12s '
                'parse=523ms transaction_apply=1.21s next_open=376ms')
        event = slow.acceptance_event(text)
        self.assertAlmostEqual(event["total_seconds"], 2.12)
        self.assertEqual(event["seq"], "7")
        self.assertEqual(event["acceptance_log_time"], "2026-10-22T12:00:00Z")
        encoded = json.dumps({"msg": "Consensus acceptance timings", "seq": 8, "total": 2120120231})
        self.assertAlmostEqual(slow.acceptance_event(encoded)["total_seconds"], 2.120120231)
        self.assertAlmostEqual(slow.duration("1m2.3s"), 62.3)
        self.assertIsNone(slow.acceptance_event('msg="other" total=10s'))

    def test_gate_threshold_and_cooldown(self):
        gate = slow.TriggerGate(2, 60)
        self.assertFalse(gate.allow(1.9, 0))
        self.assertTrue(gate.allow(2, 0))
        self.assertFalse(gate.allow(3, 59.9))
        self.assertTrue(gate.allow(3, 60))

    def test_ring_is_bounded_and_attaches_active_window(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            store = slow.Store(root / "incidents", 3, 100_000, lambda _: None)
            ring = slow.CaptureRing(root / "ring", 5, 60, 1_200, store)
            for index in range(12):
                window = ring.begin(index * 5.0)
                window.path.write_bytes(b"x" * ring.window_quota)
                ring.publish(window, index * 5.0 + 5)
            self.assertEqual(len(ring.windows), 12)
            self.assertLessEqual(ring.bytes, 1_200)
            event = store.new({"total_seconds": 2.0})
            active = ring.begin(60.0)
            self.assertEqual(active.limit, ring.window_quota)
            ring.snapshot(event, 61.0)
            self.assertTrue(any(artifact["name"].startswith("pre-") for artifact in event["artifacts"]))
            active.path.write_bytes(b"future")
            ring.publish(active, 65.0)
            self.assertTrue(any(artifact["name"].startswith("overlap-") for artifact in event["artifacts"]))
            future = ring.begin(70.0)
            future.path.write_bytes(b"not part of the earlier event")
            ring.publish(future, 75.0)
            ring.snapshot(event, 61.0)
            self.assertFalse(any(artifact["name"] == "pre-cpu-000014.pb"
                                 for artifact in event["artifacts"]))

    def test_ring_active_window_rejects_response_over_remaining_budget(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            store = slow.Store(root / "incidents", 1, 10_000, lambda _: None)
            ring = slow.CaptureRing(root / "ring", 5, 60, 100, store)
            retained = ring.begin(0)
            retained.path.write_bytes(b"x" * 100)
            ring.publish(retained, 5)
            active = ring.begin(5)
            self.assertEqual(active.limit, ring.window_quota)
            self.assertEqual(ring.windows, [])
            active.path.write_bytes(b"x" * 101)
            ring.publish(active, 10)
            self.assertFalse(active.path.exists())
            self.assertLessEqual(ring.bytes, ring.max_bytes)

    def test_ring_overlap_survives_post_capture_reservation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            store = slow.Store(root / "incidents", 1, 10_000, lambda _: None, overlap_bytes=100)
            ring = slow.CaptureRing(root / "ring", 5, 60, 100, store)
            event = store.new({"total_seconds": 2.0})
            active = ring.begin(0)
            ring.snapshot(event, 0)
            reserved = store.reserve()
            active.path.write_bytes(b"x" * 5)
            ring.publish(active, 5)
            with store.lock:
                store.reserved -= reserved
            overlap = next(artifact for artifact in event["artifacts"]
                           if artifact["name"].startswith("overlap-"))
            self.assertEqual(overlap["status"], "complete")
            self.assertEqual(overlap["capture_started_at"], active.capture_started_at)
            self.assertEqual(overlap["capture_finished_at"], active.capture_finished_at)

    def test_ring_recovers_after_failed_profile_with_small_leftover(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            store = slow.Store(root / "incidents", 1, 10_000, lambda _: None)
            ring = slow.CaptureRing(root / "ring", 5, 60, 100, store)
            for index in (0, 5):
                path = ring.root / f"cpu-retained-{index}.pb"
                path.write_bytes(b"x" * 48)
                ring.windows.append(slow.Window(index, path, index + 5))
                ring.bytes += 48
            leftover = ring.max_bytes - ring.bytes
            failed = ring.begin(10)
            self.assertGreater(failed.limit, leftover)
            failed.path.write_bytes(b"x" * 5)
            failed.path.unlink()
            ring.cancel(failed)
            recovered = ring.begin(15)
            recovered.path.write_bytes(b"x" * 5)
            ring.publish(recovered, 20)
            self.assertTrue(any(window is recovered for window in ring.windows))

    def test_manifest_budget_keeps_skipped_capture_status(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            store = slow.Store(root / "incidents", 1, 2_048, lambda _: None, overlap_bytes=1_400)
            event = store.new({"total_seconds": 2.0})
            self.assertIsNotNone(event)
            reserved = store.reserve()
            store.capture(event, "http://127.0.0.1:1/unused", "trace.pb", 1)
            with store.lock:
                store.reserved -= reserved
            self.assertTrue(store.manifest(event))
            manifest = json.loads((event["dir"] / "manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(manifest["artifacts"][0]["status"], "skipped")
            self.assertEqual(manifest["artifacts"][0]["error"], "incident byte budget exhausted")

    def test_small_incident_budget_still_admits_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            quota = slow.ring_window_quota(64 * 1024, 5, 60)
            overlap = min(4_096 // 2, quota * 3)
            store = slow.Store(root / "incidents", 3, 4_096, lambda _: None, overlap_bytes=overlap)
            self.assertIsNotNone(store.new({"total_seconds": 2.0}))

    def test_fake_pprof_capture_and_loopback_validation(self):
        with fake_server() as url:
            self.assertEqual(slow.loopback_url(url), url)
            with self.assertRaises(ValueError):
                slow.loopback_url("http://example.invalid:6060")
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                store = slow.Store(root / "incidents", 1, 10_000, lambda _: None)
                event = store.new({"total_seconds": 2.0})
                store.capture(event, url + "/debug/pprof/trace?seconds=1", "trace.pb", 2)
                store.capture(event, url + "/debug/pprof/goroutine?debug=1", "goroutine.txt", 2)
                self.assertEqual({"trace.pb", "goroutine.txt"},
                                 {artifact["name"] for artifact in event["artifacts"]})
                self.assertTrue(all(artifact["status"] == "complete" for artifact in event["artifacts"]))
                self.assertIn("/debug/pprof/trace?seconds=1", PProfHandler.requests)

    def _run_subprocess_capture(self, fail_posts):
        with fake_server(fail_posts=fail_posts) as url, tempfile.TemporaryDirectory() as directory:
            root, log = Path(directory), Path(directory) / "node.log"
            log.write_text("", encoding="utf-8")
            output = root / "profiles"
            command = [sys.executable, str(Path(__file__).with_name("profile-slow-acceptance.py")),
                       "--log", str(log), "--output", str(output), "--pprof-url", url,
                       "--threshold", "1s", "--cooldown", "0s", "--window", "1", "--ring", "2",
                       "--post", "1", "--max-bytes", "100000", "--ring-bytes", "10000"]
            process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            line = ('time=2026-10-22T12:00:00Z level=INFO msg="Consensus acceptance timings" '
                    'seq=7 total=2.12s parse=523ms transaction_apply=1.21s next_open=376ms\n')
            try:
                incident = None
                for _ in range(20):
                    with log.open("a", encoding="utf-8") as stream:
                        stream.write(line)
                    if wait_for(lambda: list(output.glob("slow-acceptance-*/incidents/incident-*")), 0.2):
                        incident = list(output.glob("slow-acceptance-*/incidents/incident-*"))[0]
                        break
                self.assertIsNotNone(incident)
                manifest_path = incident / "manifest.json"

                def finished():
                    try:
                        return json.loads(manifest_path.read_text(encoding="utf-8")).get("status") in {"complete", "partial", "failed"}
                    except (FileNotFoundError, json.JSONDecodeError):
                        return False

                self.assertTrue(wait_for(finished, 5))
                manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
                present = {artifact["name"]: (incident / artifact["name"]).exists()
                           for artifact in manifest["artifacts"] if artifact["kind"] == "post"}
                return manifest, present
            finally:
                process.terminate()
                _, stderr = process.communicate(timeout=10)
                self.assertEqual(process.returncode, 0, stderr)

    def test_subprocess_trigger_records_log_time_and_failed_posts(self):
        manifest, _ = self._run_subprocess_capture(fail_posts=True)
        self.assertEqual(manifest["acceptance_log_time"], "2026-10-22T12:00:00Z")
        self.assertEqual(manifest["status"], "failed")
        self.assertEqual({artifact["status"] for artifact in manifest["artifacts"] if artifact["kind"] == "post"},
                         {"failed"})
        self.assertTrue(all("started_at" in artifact and "finished_at" in artifact
                            for artifact in manifest["artifacts"]))

    def test_subprocess_trigger_records_successful_posts(self):
        manifest, present = self._run_subprocess_capture(fail_posts=False)
        self.assertEqual(manifest["status"], "complete")
        self.assertEqual(present, {"post-trace.pb.gz": True, "post-goroutine.txt": True})

    def test_follow_handles_append_rotation_and_truncation(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "node.log"
            path.write_text("old\n", encoding="utf-8")
            stop, lines, seen = threading.Event(), [], threading.Event()

            def record(line):
                if line not in lines:
                    lines.append(line)
                seen.set()

            thread = threading.Thread(target=slow.follow, args=(path, stop, record, lambda _: None), daemon=True)
            thread.start()
            for _ in range(20):
                with path.open("a", encoding="utf-8") as output:
                    output.write("append\n")
                if seen.wait(0.1):
                    break
            self.assertTrue(seen.is_set())
            rotated = path.with_suffix(".1")
            path.replace(rotated)
            path.write_text("rotated\n", encoding="utf-8")
            self.assertTrue(wait_for(lambda: "rotated" in lines))
            path.write_text("x\n", encoding="utf-8")
            self.assertTrue(wait_for(lambda: "x" in lines))
            stop.set()
            thread.join(2)
            self.assertEqual(lines, ["append", "rotated", "x"])

    def test_follow_discards_oversized_unterminated_line(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "node.log"
            path.write_text("", encoding="utf-8")
            stop, ready, lines = threading.Event(), threading.Event(), []
            thread = threading.Thread(target=slow.follow,
                                      args=(path, stop, lines.append, lambda _: None, ready), daemon=True)
            thread.start()
            self.assertTrue(ready.wait(2))
            with path.open("a", encoding="utf-8") as output:
                output.write("x" * (slow.MAX_LOG_LINE + 100))
                output.write("discarded\n")
                output.write("kept\n")
            self.assertTrue(wait_for(lambda: "kept" in lines))
            stop.set()
            thread.join(2)
            self.assertEqual(lines, ["kept"])


if __name__ == "__main__":
    unittest.main()
