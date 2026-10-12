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

    def test_emission_before_window_can_agree_with_quorum_inside_window(self):
        lines = [line("validation emitted", 100, timestamp="2026-09-18T11:49:29.900Z"),
                 line("Ledger fully validated", 100, timestamp="2026-09-18T11:49:30.100Z")]
        report = health.audit(lines, NOW)
        self.assertEqual((report["agreed"], report["missed"], report["total"]), (1, 0, 1))
        self.assertEqual(report["missed_sequences"], [])

    def test_boundary_does_not_hide_partial_or_divergent_emissions(self):
        for full, digest, category in ((False, "aabb", "partial"), (True, "ccdd", "divergent")):
            with self.subTest(category=category):
                lines = [line("validation emitted", 100, digest=digest, full=full,
                              timestamp="2026-09-18T11:49:29.900Z"),
                         line("Ledger fully validated", 100, timestamp="2026-09-18T11:49:30.100Z")]
                report = health.audit(lines, NOW)
                self.assertEqual(report[category], 1)
                self.assertEqual(report[category + "_sequences"], [100])
                self.assertEqual(report["agreed"], 0)

    def test_old_quorum_is_not_included_by_old_emission(self):
        lines = [line("validation emitted", 100, timestamp="2026-09-18T11:49:29Z"),
                 line("Ledger fully validated", 100, timestamp="2026-09-18T11:49:29Z")]
        self.assertEqual(health.audit(lines, NOW)["total"], 0)

    def test_maturity_grace_does_not_leave_a_gap_between_periodic_audits(self):
        # This ledger is too recent for the first check. It must be covered
        # by the next check, including when no validation was emitted.
        lines = [line("Ledger fully validated", 100, timestamp="2026-09-18T11:59:45Z")]
        first = health.audit(lines, NOW)
        second = health.audit(lines, NOW + dt.timedelta(minutes=10))
        self.assertEqual(first["total"], 0)
        self.assertEqual(second["total"], 1)
        self.assertEqual(second["missed_sequences"], [100])
        self.assertEqual(first["window_end"], second["window_start"])

    def test_mature_window_is_a_full_requested_interval(self):
        report = health.audit([], NOW, minutes=10)
        start = dt.datetime.fromisoformat(report["window_start"])
        end = dt.datetime.fromisoformat(report["window_end"])
        self.assertEqual(end - start, dt.timedelta(minutes=10))
        self.assertEqual(NOW - end, dt.timedelta(seconds=30))

    def test_replay_verification_progress_is_observable(self):
        lines = ['time=2026-09-18T11:59:55Z level=INFO msg="replay admission verification progress" '
                 'root=abcdef nodes_checked=12345 elapsed=10s branches_complete=4']
        report = health.audit(lines, NOW)
        self.assertEqual(report["verification"]["nodes_checked"], "12345")

    def test_committed_replay_progress_is_observable(self):
        report = health.audit([
            line("applied standard transaction replay pipeline entry", 100) + " generation=2",
            line("applied standard transaction replay pipeline entry", 101) + " generation=2",
            line("replay delta applied", 102),
        ], NOW)
        self.assertEqual(report["replay"]["seq"], 101)
        self.assertEqual(report["replay"]["generation"], "2")

    def test_uncommitted_replay_delta_is_not_progress(self):
        report = health.audit([line("replay delta applied", 100)], NOW)
        self.assertIsNone(report["replay"])


class PhaseTests(unittest.TestCase):
    def evidence(self, state="connected", started="run-1"):
        return {
            "container": {"state": {"Running": True, "StartedAt": started}},
            "server": {"server_state": state, "validated_ledger": {"age": 1}},
            "verification": {"root": "abcd", "nodes_checked": "1000",
                             "time": (NOW - dt.timedelta(seconds=10)).isoformat()},
            "total": 1, "agreed": 1,
        }

    def test_fresh_startup_verification_remains_allowed(self):
        report = health.assess_health(self.evidence(), {}, NOW)
        self.assertEqual(report["status"], "VERIFYING")

    def test_background_walk_cannot_hide_loss_of_full_state(self):
        report = self.evidence()
        report["server"]["state_accounting"] = {
            "full": {"transitions": "1", "duration_us": "1000000"}}
        report = health.assess_health(report, {}, NOW)
        self.assertEqual(report["status"], "ALERT")
        self.assertTrue(report["has_reached_full_state"])

    def test_zero_or_unavailable_accounting_is_still_a_fresh_startup(self):
        report = self.evidence()
        report["server"]["state_accounting"] = {
            "full": {"transitions": "0", "duration_us": "0"},
            "proposing": {"transitions": None, "duration_us": "unknown"}}
        report = health.assess_health(report, {}, NOW)
        self.assertEqual(report["status"], "VERIFYING")
        self.assertFalse(report["has_reached_full_state"])

    def test_previous_full_audit_is_evidence_even_without_accounting(self):
        previous = self.evidence("proposing")
        previous["verification"]["nodes_checked"] = "900"
        report = health.assess_health(self.evidence(), previous, NOW)
        self.assertEqual(report["status"], "ALERT")

    def test_loss_of_rpc_does_not_hide_previous_validation(self):
        previous = self.evidence("proposing")
        previous["verification"]["nodes_checked"] = "900"
        report = self.evidence()
        report.pop("server")
        self.assertEqual(health.assess_health(report, previous, NOW)["status"], "ALERT")

    def test_full_state_evidence_survives_multiple_unhealthy_audits(self):
        previous = self.evidence()
        previous["has_reached_full_state"] = True
        previous["verification"]["nodes_checked"] = "900"
        report = health.assess_health(self.evidence(), previous, NOW)
        self.assertEqual(report["status"], "ALERT")
        self.assertTrue(report["has_reached_full_state"])

    def test_restart_clears_previous_run_full_state_evidence(self):
        previous = self.evidence("proposing", "old-run")
        previous["has_reached_full_state"] = True
        report = health.assess_health(self.evidence(started="new-run"), previous, NOW)
        self.assertEqual(report["status"], "ALERT")  # Replacement itself is reported.
        self.assertFalse(report["has_reached_full_state"])
        newer = self.evidence(started="new-run")
        newer["verification"]["nodes_checked"] = "1100"
        self.assertEqual(health.assess_health(newer, report, NOW)["status"], "VERIFYING")

    def test_unknown_process_identity_does_not_inherit_full_state(self):
        previous = self.evidence("proposing", None)
        previous["verification"]["nodes_checked"] = "900"
        report = health.assess_health(self.evidence(started=None), previous, NOW)
        self.assertEqual(report["status"], "VERIFYING")

    def test_healthy_full_state_remains_healthy(self):
        report = health.assess_health(self.evidence("proposing"), {}, NOW)
        self.assertEqual(report["status"], "HEALTHY")
        self.assertTrue(report["has_reached_full_state"])

    def test_replay_fault_is_not_hidden_by_progress(self):
        report = self.evidence()
        report["server"]["replay_fault"] = {"blocked": True}
        self.assertEqual(health.assess_health(report, {}, NOW)["status"], "ALERT")

    def test_replay_fault_overrides_a_full_rpc_state_and_old_agreements(self):
        report = self.evidence("proposing")
        report["server"]["replay_fault"] = {"blocked": True}
        self.assertEqual(health.assess_health(report, {}, NOW)["status"], "ALERT")

    def replay_evidence(self, sequence=101, seconds_ago=10):
        report = self.evidence()
        report["verification"] = None
        report["replay"] = {"seq": sequence, "generation": "1",
                            "time": (NOW - dt.timedelta(seconds=seconds_ago)).isoformat()}
        return report

    def test_startup_replay_is_allowed_when_committed_sequence_advances(self):
        report = health.assess_health(self.replay_evidence(), self.replay_evidence(100), NOW)
        self.assertEqual(report["status"], "REPLAYING")
        self.assertFalse(report["has_reached_full_state"])

    def test_first_fresh_committed_replay_is_allowed(self):
        self.assertEqual(health.assess_health(self.replay_evidence(), {}, NOW)["status"], "REPLAYING")

    def test_stale_or_future_replay_does_not_hide_stall(self):
        for age in (90, 600, -10):
            with self.subTest(age=age):
                report = health.assess_health(self.replay_evidence(seconds_ago=age), {}, NOW)
                self.assertEqual(report["status"], "ALERT")

    def test_same_or_regressing_replay_sequence_is_not_progress(self):
        for seq in (100, 99):
            with self.subTest(sequence=seq):
                report = health.assess_health(self.replay_evidence(seq), self.replay_evidence(100), NOW)
                self.assertEqual(report["status"], "ALERT")

    def test_replay_cannot_hide_loss_of_previous_full_state(self):
        previous = self.evidence("full")
        report = health.assess_health(self.replay_evidence(), previous, NOW)
        self.assertEqual(report["status"], "ALERT")
        self.assertTrue(report["has_reached_full_state"])

    def test_replay_fault_and_stopped_process_still_alert(self):
        report = self.replay_evidence()
        report["server"]["replay_fault"] = {"blocked": True}
        self.assertEqual(health.assess_health(report, {}, NOW)["status"], "ALERT")
        report = self.replay_evidence()
        report["container"]["state"]["Running"] = False
        self.assertEqual(health.assess_health(report, {}, NOW)["status"], "ALERT")

    def test_missing_rpc_does_not_claim_replaying(self):
        report = self.replay_evidence()
        report.pop("server")
        self.assertEqual(health.assess_health(report, {}, NOW)["status"], "ALERT")


if __name__ == "__main__":
    unittest.main()
