import hashlib
import importlib.util
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


spec = importlib.util.spec_from_file_location("engine_coverage", Path(__file__).with_name("generate.py"))
coverage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(coverage)


class CoverageProvenanceTest(unittest.TestCase):
    def test_checked_in_inventory_matches_sources_and_corpus(self):
        repo = Path(__file__).resolve().parents[2]
        inventory = json.loads((repo / "scripts/engine-coverage/inventory.json").read_text())
        for name, checksum in inventory["source_sha256"].items():
            with self.subTest(source=name):
                self.assertEqual(hashlib.sha256((repo / name).read_bytes()).hexdigest(), checksum)
        engine = coverage.engine_inventory(repo)
        coverage.validate_engine_inventory(repo, engine)
        self.assertEqual(inventory["engine"], engine)
        self.assertEqual(inventory["common_fields"]["stage_mapping"], coverage.common_field_stage_mapping())
        self.assertEqual(inventory["evidence"]["checked_in_v4_corpus"], coverage.parse_v4_corpus(repo))

    def test_fixture_bytes_must_match_manifest(self):
        repo = Path(__file__).resolve().parents[2]
        source = repo / coverage.V4_MANIFEST
        manifest = json.loads(source.read_text())
        name, pin = next(iter(manifest["fixtures"].items()))
        original = (source.parent / name).read_bytes()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / coverage.V4_MANIFEST
            target.parent.mkdir(parents=True)
            archive = manifest["recorder_source_archive"]
            shutil.copytree(repo / archive, root / archive)
            manifest["fixtures"] = {name: pin}
            manifest["fixture_count"] = 1
            target.write_text(json.dumps(manifest))
            fixture = target.parent / name
            fixture.write_bytes(original)
            self.assertEqual(coverage.parse_v4_corpus(root)["fixture_count"], 1)
            fixture.write_bytes(original + b"\n")
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                coverage.parse_v4_corpus(root)
            manifest["fixtures"][name]["sha256"] = hashlib.sha256(fixture.read_bytes()).hexdigest()
            target.write_text(json.dumps(manifest))
            self.assertEqual(coverage.parse_v4_corpus(root)["fixture_count"], 1)
            live_script = root / "scripts/oracle/record-v4.py"
            live_script.write_text("changed tooling after the recording")
            self.assertEqual(coverage.parse_v4_corpus(root)["fixture_count"], 1)
            recorded_script = root / archive / "scripts/oracle/record-v4.py"
            recorded_script.write_bytes(recorded_script.read_bytes() + b"\n")
            with self.assertRaisesRegex(ValueError, "recorder source checksum mismatch"):
                coverage.parse_v4_corpus(root)

    def test_oracle_must_be_clean_and_at_the_tag(self):
        with tempfile.TemporaryDirectory() as directory:
            oracle = Path(directory)

            def git(*args):
                return subprocess.check_output(["git", "-C", str(oracle), *args], text=True).strip()

            git("init", "--quiet")
            source = oracle / "source.cpp"
            source.write_text("original")
            git("add", "source.cpp")
            git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
            git("-c", "tag.gpgsign=false", "tag", coverage.ORACLE_TAG)
            self.assertEqual(coverage.oracle_head(oracle), git("rev-parse", "HEAD"))
            source.write_text("modified")
            with self.assertRaisesRegex(SystemExit, "must be clean"):
                coverage.oracle_head(oracle)
            source.write_text("original")
            extra = oracle / "extra.cpp"
            extra.write_text("untracked")
            with self.assertRaisesRegex(SystemExit, "must be clean"):
                coverage.oracle_head(oracle)
            extra.unlink()
            git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "next")
            with self.assertRaisesRegex(SystemExit, "must match tag"):
                coverage.oracle_head(oracle)


if __name__ == "__main__":
    unittest.main()
