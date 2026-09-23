import datetime as dt
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("health", Path(__file__).with_name("testnet-health.py"))
health = importlib.util.module_from_spec(spec)
spec.loader.exec_module(health)
NOW = dt.datetime(2026, 9, 18, 12, 0, tzinfo=dt.timezone.utc)


def line(message, sequence, digest="aabb", full=True, timestamp="2026-09-18T11:59:00Z"):
    return f'time={timestamp} level=INFO msg="{message}" seq={sequence} hash={digest} full={str(full).lower()}'


class HealthTests(unittest.TestCase):
    def test_missing_log_events_are_not_excluded_from_denominator(self):
        lines = [line("Ledger fully validated", n) for n in (100, 102)]
        lines += [line("validation emitted", n) for n in (100, 102)]
        report = health.audit(lines, NOW)
        self.assertEqual((report["agreed"], report["missed"], report["total"]), (2, 1, 3))

    def test_partial_and_divergent(self):
        lines = [line("trusted validation quorum observed", n) for n in (1, 2)]
        lines += [line("validation emitted", 1, full=False), line("validation emitted", 2, digest="ccdd")]
        report = health.audit(lines, NOW)
        self.assertEqual((report["partial"], report["divergent"]), (1, 1))

    def test_pending_latest_ledger_gets_grace(self):
        report = health.audit([line("Ledger fully validated", 1, timestamp="2026-09-18T11:59:55Z")], NOW)
        self.assertEqual(report["total"], 0)
        self.assertIsNone(report["agreement_percent"])

    def test_emitted_without_network_hash_is_unverified(self):
        lines = [line("Ledger fully validated", n) for n in (1, 3)]
        lines += [line("validation emitted", n) for n in (1, 2, 3)]
        self.assertEqual(health.audit(lines, NOW)["unverified"], 1)

    def test_stale_log_does_not_claim_healthy(self):
        report = health.audit([line("Ledger fully validated", 1, timestamp="2026-09-18T10:00:00Z")], NOW)
        self.assertEqual(report["total"], 0)


if __name__ == "__main__":
    unittest.main()
