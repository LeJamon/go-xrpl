#!/usr/bin/env python3
"""Build and run the pinned v4 C++ recorder without changing the oracle tree."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shlex
import shutil
import subprocess
from pathlib import Path


ORACLE_COMMIT = "d147fccf54a500fce586522f28d6044c37fd8d29"
ORACLE_TAG = "3.4.1"
ORACLE_REPOSITORY = "XRPLF/xrpld-private"
OLD_BINARY_SHA256 = "f05b910157c5e3a416ee723332ce2fbd83cf7e3c02d4a61af09060871e3d6113"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def run(command: list[str], *, cwd: Path | None = None, log: Path | None = None) -> None:
    print("+", shlex.join(command))
    if log is None:
        subprocess.run(command, cwd=cwd, check=True)
        return
    log.parent.mkdir(parents=True, exist_ok=True)
    with log.open("w", encoding="utf-8") as stream:
        subprocess.run(command, cwd=cwd, check=True, stdout=stream, stderr=subprocess.STDOUT)


def check_oracle(oracle: Path) -> dict[str, str]:
    commit = subprocess.check_output(
        ["git", "-C", str(oracle), "rev-parse", "HEAD"], text=True
    ).strip()
    describe = subprocess.check_output(
        ["git", "-C", str(oracle), "describe", "--tags", "--always", "--dirty"],
        text=True,
    ).strip()
    status = subprocess.check_output(
        ["git", "-C", str(oracle), "status", "--porcelain"], text=True
    )
    if commit != ORACLE_COMMIT or describe != ORACLE_TAG or status:
        raise SystemExit(
            f"oracle must be clean {ORACLE_TAG} at {ORACLE_COMMIT}; "
            f"got commit={commit!r}, describe={describe!r}, status={status!r}"
        )
    return {"repository": ORACLE_REPOSITORY, "tag": ORACLE_TAG, "commit": commit}


def copy_build(old_build: Path, build: Path) -> None:
    if build.exists():
        return
    build.parent.mkdir(parents=True, exist_ok=True)
    # Hardlink the verified production objects into a private build directory. No
    # existing object is rebuilt or opened for writing by the recorder linker.
    run(
        [
            "rsync",
            "-a",
            "--link-dest=" + str(old_build) + "/",
            str(old_build) + "/",
            str(build) + "/",
        ]
    )


def compile_recorder(
    *, old_build: Path, build: Path, recorder_source: Path
) -> tuple[Path, str, str]:
    compile_commands = json.loads((old_build / "compile_commands.json").read_text())
    selected = next(
        entry
        for entry in compile_commands
        if entry["file"].endswith("/src/test/app/Batch_test.cpp")
        and "/CMakeFiles/xrpld.dir/" in entry["output"]
    )
    strict_relative = Path("CMakeFiles/xrpld.dir/src/test/app/StrictOracleRecorder_test.cpp.o")
    strict_object = build / strict_relative
    strict_object.parent.mkdir(parents=True, exist_ok=True)
    old_object_suffix = "CMakeFiles/xrpld.dir/src/test/app/Batch_test.cpp.o"
    old_dep_suffix = old_object_suffix + ".d"
    args = shlex.split(selected["command"])
    for index, argument in enumerate(args):
        if argument == selected["file"]:
            args[index] = str(recorder_source)
        elif argument.endswith(old_dep_suffix):
            args[index] = str(strict_object.with_suffix(".d")) if argument.startswith("/") else str(strict_relative.with_suffix(".d"))
        elif argument.endswith(old_object_suffix):
            args[index] = str(strict_object) if argument.startswith("/") else str(strict_relative)
    # The command in compile_commands uses an absolute output path in this build.
    if str(recorder_source) not in args or (
        str(strict_relative) not in args and str(strict_object) not in args
    ):
        raise RuntimeError("could not specialize the pinned test compile command")
    compile_text = shlex.join(args)
    compile_hash = hashlib.sha256(compile_text.encode()).hexdigest()
    run(args, cwd=build)

    commands = subprocess.check_output(
        ["ninja", "-C", str(old_build), "-t", "commands", "xrpld"], text=True
    ).splitlines()
    link_line = next(
        line
        for line in reversed(commands)
        if "CMakeFiles/xrpld.dir/" in line and " -o xrpld" in line
    )
    if link_line.startswith(": && "):
        link_line = link_line[4:]
    if link_line.endswith(" && :"):
        link_line = link_line[:-5]
    link_args = shlex.split(link_line)
    try:
        output_index = link_args.index("-o")
    except ValueError as error:
        raise RuntimeError("pinned xrpld link command has no output argument") from error
    link_args.insert(output_index, str(strict_relative))
    link_text = shlex.join(link_args)
    link_hash = hashlib.sha256(link_text.encode()).hexdigest()
    output = build / "xrpld"
    if output.exists():
        output.unlink()
    run(link_args, cwd=build)
    return output, compile_hash, link_hash


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--oracle",
        type=Path,
        default=Path(__file__).resolve().parents[4] / "rippled-worktrees/v3.4.1-oracle",
    )
    parser.add_argument(
        "--old-build", type=Path, default=Path("/private/tmp/issue-2011-oracle-build")
    )
    parser.add_argument(
        "--build-root", type=Path, default=Path("/private/tmp/issue-2015-oracle-build")
    )
    parser.add_argument(
        "--fixture-dir",
        type=Path,
        default=Path(__file__).resolve().parents[2]
        / "internal/testing/conformance/testdata/rippled-3.4.1-v4",
    )
    parser.add_argument("--record", action="store_true")
    args = parser.parse_args()

    oracle = args.oracle.resolve()
    old_build = args.old_build.resolve()
    build_root = args.build_root.resolve()
    build = build_root / "build"
    recorder_source = Path(__file__).with_name("strict_recorder.cpp").resolve()
    config = Path(__file__).with_name("strict-corpus-config.json").resolve()
    if not oracle.is_dir() or not old_build.is_dir() or not recorder_source.is_file():
        raise SystemExit("oracle, verified old build, and recorder source are required")
    oracle_identity = check_oracle(oracle)
    production_binary = old_build / "build/xrpld"
    if sha256(production_binary) != OLD_BINARY_SHA256:
        raise SystemExit("verified 3.4.1 production binary hash changed")
    version_output = subprocess.check_output([str(production_binary), "--version"], text=True)
    version = version_output.splitlines()[0].strip()
    if version != "xrpld version 3.4.1":
        raise SystemExit(f"unexpected production binary version: {version_output.strip()}")

    copy_build(old_build / "build", build)
    strict_binary, compile_hash, link_hash = compile_recorder(
        old_build=old_build / "build", build=build, recorder_source=recorder_source
    )
    strict_hash = sha256(strict_binary)
    identity = {
        "oracle": oracle_identity,
        "production_source": str(oracle),
        "verified_production_binary": str(production_binary),
        "verified_production_binary_sha256": OLD_BINARY_SHA256,
        "verified_production_binary_version": version,
        "instrumented_source_root": str(old_build / "source"),
        "instrumented_batch_test_sha256": sha256(old_build / "source/src/test/app/Batch_test.cpp"),
        "recorder_source": str(recorder_source),
        "recorder_source_sha256": sha256(recorder_source),
        "config": str(config),
        "config_sha256": sha256(config),
        "compile_command_sha256": compile_hash,
        "link_command_sha256": link_hash,
        "strict_binary": str(strict_binary),
        "strict_binary_sha256": strict_hash,
        "build_method": "hardlinked exact-commit production build objects; newly compiled recorder object; relinked xrpld",
    }
    build_root.mkdir(parents=True, exist_ok=True)
    (build_root / "build-identity.json").write_text(json.dumps(identity, indent=2) + "\n")
    print(json.dumps(identity, indent=2))

    if not args.record:
        return
    args.fixture_dir.mkdir(parents=True, exist_ok=True)
    for path in args.fixture_dir.glob("*.json"):
        path.unlink()
    record_log = build_root / "record-v4.log"
    env = os.environ.copy()
    env["GOXRPL_V4_FIXTURE_DIR"] = str(args.fixture_dir)
    print("+", shlex.join([str(strict_binary), "--unittest=StrictOracleRecorder", "--unittest-jobs", "1"]))
    with record_log.open("w", encoding="utf-8") as stream:
        subprocess.run(
            [str(strict_binary), "--unittest=StrictOracleRecorder", "--unittest-jobs", "1"],
            env=env,
            check=True,
            stdout=stream,
            stderr=subprocess.STDOUT,
        )
    print(f"recorded {len(list(args.fixture_dir.glob('*.json')))} fixtures")


if __name__ == "__main__":
    main()
