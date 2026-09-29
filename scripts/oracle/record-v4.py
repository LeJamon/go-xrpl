#!/usr/bin/env python3
"""Build and run the pinned v4 C++ recorder without changing the oracle tree."""

from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
import shlex
import shutil
import subprocess
import tarfile
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


def sha256_text(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def run(
    command: list[str],
    *,
    cwd: Path | None = None,
    log: Path | None = None,
    env: dict[str, str] | None = None,
) -> None:
    print("+", shlex.join(command))
    if log is None:
        subprocess.run(command, cwd=cwd, check=True, env=env)
        return
    log.parent.mkdir(parents=True, exist_ok=True)
    with log.open("w", encoding="utf-8") as stream:
        subprocess.run(
            command,
            cwd=cwd,
            check=True,
            env=env,
            stdout=stream,
            stderr=subprocess.STDOUT,
        )


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


def verify_config(config: Path) -> dict:
    values = json.loads(config.read_text())
    expected_txq = {
        "ledgers_in_queue": 20,
        "queue_size_min": 2000,
        "retry_sequence_percent": 25,
        "minimum_escalation_multiplier": 128000,
        "minimum_txn_in_ledger": 32,
        "minimum_txn_in_ledger_standalone": 1000,
        "target_txn_in_ledger": 256,
        "maximum_txn_in_ledger": 0,
        "maximum_txn_in_ledger_set": False,
        "normal_consensus_increase_percent": 20,
        "slow_consensus_decrease_percent": 50,
        "maximum_txn_per_account": 10,
        "minimum_last_ledger_buffer": 2,
        "standalone": True,
    }
    expected = {
        "fixture_version": "v4",
        "oracle_repository": ORACLE_REPOSITORY,
        "oracle_tag": ORACLE_TAG,
        "oracle_commit": ORACLE_COMMIT,
        "network_id": 0,
        "apply_flags": 0,
        "skip_signature_verification": False,
        "txq_config": expected_txq,
        "fees": {
            "reference_fee_drops": 10,
            "account_reserve_drops": 200_000_000,
            "owner_reserve_increment_drops": 50_000_000,
        },
        "close_policy": {
            "setup": "fund named accounts, then close before recording parent",
            "request_seconds_after_now": 5,
            "capture": "effective agreed close time computed from the pre-close header and resolution",
        },
    }
    for key, expected_value in expected.items():
        if values.get(key) != expected_value:
            raise SystemExit(
                f"{config} does not match recorder setting {key!r}: "
                f"got {values.get(key)!r}, want {expected_value!r}"
            )
    expected_families = {
        "Payment",
        "AccountSet",
        "TrustSet",
        "TicketCreate",
        "Batch",
        "VaultCreate",
        "LoanBrokerSet",
        "NFTokenAcceptOffer",
    }
    if set(values.get("families", [])) != expected_families:
        raise SystemExit("strict-corpus-config.json families do not match recorder families")
    expected_overrides = {
        "network_id": {
            "AccountSet/network-id-missing": 1025,
            "AccountSet/network-id-wrong": 1025,
        },
        "queue_history": {
            "AccountSet/queued-low-fee": {
                "txq_config": {
                    "ledgers_in_queue": 2,
                    "queue_size_min": 2,
                    "minimum_txn_in_ledger_standalone": 2,
                    "normal_consensus_increase_percent": 0,
                },
                "internal_config": {
                    "min_ledgers_to_compute_size_limit": 3,
                    "max_ledger_counts_to_store": 100,
                },
                "pre_submit_count": 3,
                "pre_submit_order": "alice->bob, bob->alice, alice->bob",
                "primary": {
                    "engine_result": "terQUEUED",
                    "applied": False,
                    "queued": True,
                },
            }
        },
        "persistent_cleanup": {
            "NFTokenAcceptOffer/expired-sell-offer-cleanup": {
                "profile": "c1-l1-b1-f1",
                "engine_result": "tecEXPIRED",
                "applied": True,
                "queued": False,
                "expected_deleted_object": "NFTokenOffer",
            }
        },
        "seeded_payments": {
            "seed": 2016,
            "profile": "c1-l1-b1-f1",
            "samples": [
                "Payment/seed2016-payment-0-valid-base-fee",
                "Payment/seed2016-payment-1-insufficient-balance",
                "Payment/seed2016-payment-2-valid-fee-edge",
                "Payment/seed2016-payment-3-future-sequence",
            ],
            "coverage": "deterministic signed XRP payments with base/above-base fee, insufficient balance, and future sequence",
        },
    }
    if values.get("scenario_overrides") != expected_overrides:
        raise SystemExit(
            "strict-corpus-config.json scenario overrides do not match recorder scenarios"
        )
    return values


def verify_instrumented_source(oracle: Path, instrumented: Path) -> str:
    """Prove the optimized production object tree came from this oracle commit."""
    if not instrumented.is_dir():
        raise SystemExit(f"instrumented source is missing: {instrumented}")
    oracle_files = {
        path.relative_to(oracle)
        for path in oracle.rglob("*")
        if path.is_file() and ".git" not in path.parts
    }
    instrumented_files = {
        path.relative_to(instrumented)
        for path in instrumented.rglob("*")
        if path.is_file() and ".git" not in path.parts
    }
    if oracle_files != instrumented_files:
        raise SystemExit("instrumented source file set differs from exact oracle source")
    allowed = {Path("src/test/app/Batch_test.cpp")}
    changed = set()
    for relative in oracle_files:
        if sha256(oracle / relative) != sha256(instrumented / relative):
            changed.add(relative)
    if changed != allowed:
        raise SystemExit(
            "instrumented source differs from exact oracle outside the allowed prior "
            f"recorder patch: {sorted(str(path) for path in changed)}"
        )
    return sha256(instrumented / "src/test/app/Batch_test.cpp")


def clean_source(oracle: Path, destination: Path, recorder_source: Path) -> None:
    marker = destination / ".issue-2015-recorder-source"
    if destination.exists():
        if not marker.is_file() or marker.read_text(encoding="utf-8").strip() != (
            f"oracle_commit={ORACLE_COMMIT}\nowner=issue-2015-recorder"
        ):
            raise SystemExit(
                f"refusing to remove preexisting unowned clean source: {destination}"
            )
        shutil.rmtree(destination)
    destination.mkdir(parents=True)
    archive = subprocess.check_output(["git", "-C", str(oracle), "archive", ORACLE_COMMIT])
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as stream:
        stream.extractall(destination)
    target = destination / "src/test/app/StrictOracleRecorder_test.cpp"
    target.write_bytes(recorder_source.read_bytes())
    marker.write_text(
        f"oracle_commit={ORACLE_COMMIT}\nowner=issue-2015-recorder\n",
        encoding="utf-8",
    )


def clean_build(
    *, oracle: Path, build_root: Path, recorder_source: Path
) -> tuple[Path, str | None, str | None, dict[str, object]]:
    """Build xrpld from an archived exact commit and the recorder source."""
    clean_root = build_root / "clean"
    source = clean_root / "source"
    install = clean_root / "install"
    build = clean_root / "build"
    clean_source(oracle, source, recorder_source)
    conan_home = clean_root / "conan-home"
    conan_home.mkdir(parents=True, exist_ok=True)
    conan_env = os.environ.copy()
    conan_env["CONAN_HOME"] = str(conan_home)
    run(
        ["conan", "profile", "detect", "--force"],
        log=clean_root / "conan-profile-detect.log",
        env=conan_env,
    )
    conan = [
        "conan",
        "install",
        str(source),
        "--output-folder",
        str(install),
        "--build=missing",
        "--lockfile=" + str(oracle / "conan.lock"),
        "--profile:host=default",
        "--profile:build=default",
        "--settings:host=build_type=Release",
        "--settings:build=build_type=Release",
        "--options:host=xrpl/*:xrpld=True",
        "--options:host=xrpl/*:tests=True",
        "--conf:host=tools.build:jobs=4",
        "--conf:build=tools.build:jobs=4",
        "--conf:all=tools.cmake.cmaketoolchain:user_presets=",
        "--core-conf=core:non_interactive=True",
    ]
    run(conan, log=clean_root / "conan-install.log", env=conan_env)
    cmake = [
        "cmake",
        "-S",
        str(source),
        "-B",
        str(build),
        "-G",
        "Ninja",
        "-DCMAKE_TOOLCHAIN_FILE=" + str(install / "build/generators/conan_toolchain.cmake"),
        "-DCMAKE_BUILD_TYPE=Release",
        "-Dxrpld=ON",
        "-Dtests=ON",
    ]
    run(cmake, log=clean_root / "cmake-configure.log")
    build_command = [
        "cmake",
        "--build",
        str(build),
        "--parallel",
        "4",
        "--target",
        "xrpld",
    ]
    run(build_command, log=clean_root / "cmake-build.log")
    binary = build / "xrpld"
    return (
        binary,
        None,
        None,
        {
            "source": str(source),
            "install": str(install),
            "build": str(build),
            "conan_lock_sha256": sha256(oracle / "conan.lock"),
            "conan_profile_detect_command_sha256": sha256_text(
                "conan profile detect --force"
            ),
            "configure_command": cmake,
            "configure_command_sha256": sha256_text(shlex.join(cmake)),
            "build_command": build_command,
            "build_command_sha256": sha256_text(shlex.join(build_command)),
        },
    )


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
    parser.add_argument(
        "--clean-build",
        action="store_true",
        help="archive the exact oracle commit and perform a clean Conan/CMake/Ninja build",
    )
    parser.add_argument("--record", action="store_true")
    args = parser.parse_args()

    oracle = args.oracle.resolve()
    old_build = args.old_build.resolve()
    build_root = args.build_root.resolve()
    build = build_root / "build"
    recorder_source = Path(__file__).with_name("strict_recorder.cpp").resolve()
    config = Path(__file__).with_name("strict-corpus-config.json").resolve()
    if not oracle.is_dir() or not recorder_source.is_file():
        raise SystemExit("exact oracle checkout and recorder source are required")
    oracle_identity = check_oracle(oracle)
    config_values = verify_config(config)
    if args.clean_build:
        strict_binary, compile_hash, link_hash, clean_identity = clean_build(
            oracle=oracle, build_root=build_root, recorder_source=recorder_source
        )
        production_binary = strict_binary
        instrumented_batch_hash = None
        build_method = "git archive exact oracle commit; Conan lockfile; clean CMake/Ninja xrpld target"
    else:
        if not old_build.is_dir():
            raise SystemExit("verified old build is required unless --clean-build is used")
        production_binary = old_build / "build/xrpld"
        if sha256(production_binary) != OLD_BINARY_SHA256:
            raise SystemExit("verified 3.4.1 production binary hash changed")
        instrumented_batch_hash = verify_instrumented_source(
            oracle, old_build / "source"
        )
        version_output = subprocess.check_output([str(production_binary), "--version"], text=True)
        version = version_output.splitlines()[0].strip()
        if version != "xrpld version 3.4.1":
            raise SystemExit(f"unexpected production binary version: {version_output.strip()}")
        copy_build(old_build / "build", build)
        strict_binary, compile_hash, link_hash = compile_recorder(
            old_build=old_build / "build", build=build, recorder_source=recorder_source
        )
        clean_identity = {}
        build_method = "hardlinked exact-commit production build objects; newly compiled recorder object; relinked xrpld"
        version_output = subprocess.check_output([str(strict_binary), "--version"], text=True)
        version = version_output.splitlines()[0].strip()
    if args.clean_build:
        version_output = subprocess.check_output([str(strict_binary), "--version"], text=True)
        version = version_output.splitlines()[0].strip()
    if version != "xrpld version 3.4.1":
        raise SystemExit(f"unexpected recorder binary version: {version_output.strip()}")
    strict_hash = sha256(strict_binary)
    identity = {
        "oracle": oracle_identity,
        "production_source": str(oracle),
        "verified_production_binary": str(production_binary),
        "verified_production_binary_sha256": sha256(production_binary),
        "verified_production_binary_version": version,
        "instrumented_source_root": str(old_build / "source") if not args.clean_build else None,
        "instrumented_batch_test_sha256": instrumented_batch_hash,
        "recorder_source": str(recorder_source),
        "recorder_source_sha256": sha256(recorder_source),
        "config": str(config),
        "config_sha256": sha256(config),
        "config_values_verified": config_values,
        "compile_command_sha256": compile_hash,
        "link_command_sha256": link_hash,
        "strict_binary": str(strict_binary),
        "strict_binary_sha256": strict_hash,
        "build_method": build_method,
        "clean_build": clean_identity,
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
