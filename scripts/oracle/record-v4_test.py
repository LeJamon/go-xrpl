import hashlib
import importlib.util
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("record_v4", Path(__file__).with_name("record-v4.py"))
recorder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recorder)


class RecorderProvenanceTest(unittest.TestCase):
    def test_build_must_not_alias_production_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            production = Path(directory) / "production"
            production.mkdir()
            binary = production / "xrpld"
            binary.write_bytes(b"verified production")
            alias = Path(directory) / "alias"
            alias.symlink_to(production, target_is_directory=True)
            for build in [production, alias]:
                with self.subTest(build=build), patch.object(recorder, "run") as run:
                    with self.assertRaisesRegex(SystemExit, "must not alias"):
                        recorder.compile_recorder(
                            old_build=production, build=build,
                            recorder_source=Path(directory) / "recorder.cpp",
                        )
                    run.assert_not_called()
                    self.assertEqual(binary.read_bytes(), b"verified production")

    def test_reused_objects_must_reproduce_pinned_binary(self):
        for object_bytes in [b"pinned production", b"stale production"]:
            with self.subTest(object_bytes=object_bytes), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                old = root / "old"
                build = root / "build"
                old.mkdir()
                build.mkdir()
                original = old / "xrpld"
                original.write_bytes(b"pinned production")
                os.link(original, build / "xrpld")
                (build / "production.o").write_bytes(object_bytes)
                old_object = "CMakeFiles/xrpld.dir/src/test/app/Batch_test.cpp.o"
                batch = root / "src/test/app/Batch_test.cpp"
                (old / "compile_commands.json").write_text(json.dumps([{
                    "file": str(batch),
                    "output": str(old / old_object),
                    "command": f"c++ -c {batch} -o {old_object}",
                }]))
                source = root / "strict_recorder.cpp"
                source.write_text("recorder")
                calls = []

                def run(command, *, cwd):
                    calls.append(list(command))
                    if "-c" in command:
                        return
                    data = (cwd / "production.o").read_bytes()
                    if any("StrictOracleRecorder_test.cpp.o" in arg for arg in command):
                        data += b" with recorder"
                    (cwd / "xrpld").write_bytes(data)

                link = f"c++ {old_object} production.o -o xrpld"
                with patch.object(recorder, "run", side_effect=run), \
                        patch.object(recorder.subprocess, "check_output", return_value=link), \
                        patch.object(recorder, "OLD_BINARY_SHA256", hashlib.sha256(original.read_bytes()).hexdigest()):
                    if object_bytes == b"pinned production":
                        binary, _, _ = recorder.compile_recorder(old_build=old, build=build, recorder_source=source)
                        self.assertEqual(binary.read_bytes(), b"pinned production with recorder")
                        self.assertEqual(len(calls), 3)
                    else:
                        with self.assertRaisesRegex(SystemExit, "do not reproduce"):
                            recorder.compile_recorder(old_build=old, build=build, recorder_source=source)
                        self.assertEqual(len(calls), 1)
                self.assertEqual(original.read_bytes(), b"pinned production")


if __name__ == "__main__":
    unittest.main()
