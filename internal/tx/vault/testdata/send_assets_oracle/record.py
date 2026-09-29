#!/usr/bin/env python3
"""Link the recorder against an existing pinned Ninja xrpld build."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile


COMMIT = "d147fccf54a500fce586522f28d6044c37fd8d29"


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build", required=True, type=Path)
    parser.add_argument("--oracle", required=True, type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    build, oracle = args.build.resolve(), args.oracle.resolve()
    output = args.output.resolve() if args.output else Path(tempfile.mkdtemp(prefix="send-assets-oracle-"))
    output.mkdir(parents=True, exist_ok=True)
    revision = subprocess.check_output(["git", "-C", str(oracle), "rev-parse", "HEAD"], text=True).strip()
    status = subprocess.check_output(["git", "-C", str(oracle), "status", "--porcelain"], text=True)
    if revision != COMMIT or status:
        raise RuntimeError("oracle must be the clean pinned private rippled 3.4.1 checkout")

    commands = subprocess.check_output(
        ["ninja", "-C", str(build), "-t", "commands", "xrpld"], text=True
    ).splitlines()
    helper = "src/libxrpl/ledger/helpers/TokenHelpers.cpp"
    helper_command = shlex.split(next(line for line in commands if " -c " in line and line.endswith(helper)))
    build_source = Path(helper_command[-1]).resolve()
    if digest(build_source) != digest(oracle / helper):
        raise RuntimeError("build TokenHelpers source differs from the pinned oracle")

    harness = Path(__file__).with_name("token_helpers_harness.cpp").resolve()
    object_file = output / "token_helpers_harness.o"
    executable = output / "token_helpers_xrpld"
    evidence = output / "evidence.json"
    compile_command = shlex.split(next(
        line for line in commands if " -c " in line and line.endswith("src/test/app/MPToken_test.cpp")
    ))
    for index, token in enumerate(compile_command):
        if token.endswith("/src/test/app/MPToken_test.cpp"):
            compile_command[index] = str(harness)
        elif token in ("-o", "-MT"):
            compile_command[index + 1] = str(object_file)
        elif token == "-MF":
            compile_command[index + 1] = str(output / "token_helpers_harness.d")
    subprocess.run(compile_command, cwd=build, check=True)

    link_line = next(line for line in reversed(commands) if " -o xrpld " in f" {line} ")
    if not (link_line.startswith(": && ") and link_line.endswith(" && :")):
        raise RuntimeError("unexpected Ninja link command wrapper")
    link_command = shlex.split(link_line[5:-4])
    link_command[link_command.index("-o") + 1] = str(executable)
    link_command.insert(link_command.index("libxrpl.a"), str(object_file))
    subprocess.run(link_command, cwd=build, check=True)
    version = subprocess.check_output([str(executable), "--version"], text=True).strip()
    if version.splitlines()[0] != "xrpld version 3.4.1":
        raise RuntimeError(f"unexpected linked oracle version: {version}")

    run_command = [str(executable), "--unittest", "xrpl.ledger.TokenHelpersBoundary"]
    subprocess.run(run_command, env={**os.environ, "ISSUE_2014_EVIDENCE": str(evidence)}, check=True)
    records = {
        "harness": harness,
        "generator": Path(__file__).resolve(),
        "helper_source": build_source,
        "helper_object": build / "CMakeFiles/xrpl.libxrpl.ledger.dir" / (helper + ".o"),
        "library": build / "libxrpl.a",
        "executable": executable,
        "evidence": evidence,
    }
    provenance = {
        "commit": revision,
        "version": version,
        "build": str(build),
        "compile": compile_command,
        "link": link_command,
        "run": run_command,
        "files": {name: {"path": str(path), "sha256": digest(path)} for name, path in records.items()},
    }
    (output / "provenance.json").write_text(json.dumps(provenance, indent=2) + "\n")
    print(f"Recorded {evidence} (SHA-256 {digest(evidence)})")


if __name__ == "__main__":
    main()
