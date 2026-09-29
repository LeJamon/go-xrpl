#!/usr/bin/env python3
"""Generate the pinned v3.4.1 amendment registry fixture.

The generated data contains feature names, SHA-512-half IDs, and the
Supported/VoteBehavior values from rippled's features.macro. It deliberately
does not copy implementation source or derive values from the Go registry.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path


ORACLE_REPOSITORY = "XRPLF/xrpld-private"
ORACLE_TAG = "3.4.1"
ORACLE_COMMIT = "d147fccf54a500fce586522f28d6044c37fd8d29"
SOURCE_RELATIVE = Path("include/xrpl/protocol/detail/features.macro")

ACTIVE = re.compile(
    r"^(XRPL_FIX|XRPL_FEATURE)\s*\(\s*([A-Za-z0-9_]+)\s*,\s*"
    r"Supported::(Yes|No)\s*,\s*VoteBehavior::(DefaultYes|DefaultNo)\s*\)$"
)
RETIRED = re.compile(r"^(XRPL_RETIRE_FIX|XRPL_RETIRE_FEATURE)\s*\(\s*([A-Za-z0-9_]+)\s*\)$")


def git(root: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(root), *args],
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if result.returncode:
        detail = result.stderr.strip() or result.stdout.strip()
        raise ValueError(f"git {' '.join(args)} failed: {detail}")
    return result.stdout.strip()


def verify_oracle(root: Path) -> bytes:
    root = root.resolve()
    if not root.is_dir():
        raise ValueError(f"oracle checkout does not exist: {root}")
    if git(root, "rev-parse", "HEAD") != ORACLE_COMMIT:
        raise ValueError(f"oracle must be {ORACLE_COMMIT}")
    if ORACLE_TAG not in git(root, "tag", "--points-at", "HEAD").splitlines():
        raise ValueError(f"oracle HEAD must be tagged {ORACLE_TAG}")
    if git(root, "status", "--porcelain=v1", "--untracked-files=all"):
        raise ValueError("oracle checkout must be clean")
    remote = git(root, "remote", "get-url", "origin")
    accepted_remotes = {
        "git@github.com:XRPLF/xrpld-private.git",
        "https://github.com/XRPLF/xrpld-private.git",
        "ssh://git@github.com/XRPLF/xrpld-private.git",
    }
    if remote not in accepted_remotes:
        raise ValueError(f"oracle origin must be {ORACLE_REPOSITORY}, got {remote!r}")
    source = root / SOURCE_RELATIVE
    if not source.is_file():
        raise ValueError(f"missing oracle source: {source}")
    return source.read_bytes()


def parse_registry(source: bytes) -> list[dict[str, object]]:
    entries: list[dict[str, object]] = []
    seen: set[str] = set()
    for line_number, raw_line in enumerate(source.decode("utf-8").splitlines(), 1):
        line = raw_line.split("//", 1)[0].strip()
        if not line or not line.startswith("XRPL_"):
            continue
        active = ACTIVE.fullmatch(line)
        if active:
            macro, source_name, supported, vote = active.groups()
            name = ("fix" if macro == "XRPL_FIX" else "") + source_name
            entry = {
                "name": name,
                "source_name": source_name,
                "id": hashlib.sha512(name.encode("utf-8")).digest()[:32].hex().upper(),
                "kind": "fix" if macro == "XRPL_FIX" else "feature",
                "supported": supported.lower(),
                "vote": {"DefaultYes": "default_yes", "DefaultNo": "default_no"}[vote],
                "retired": False,
            }
        else:
            retired = RETIRED.fullmatch(line)
            if not retired:
                raise ValueError(f"unparsed features.macro registration at line {line_number}: {raw_line}")
            macro, source_name = retired.groups()
            name = ("fix" if macro == "XRPL_RETIRE_FIX" else "") + source_name
            entry = {
                "name": name,
                "source_name": source_name,
                "id": hashlib.sha512(name.encode("utf-8")).digest()[:32].hex().upper(),
                "kind": "fix" if macro == "XRPL_RETIRE_FIX" else "feature",
                "supported": "yes",
                "vote": "obsolete",
                "retired": True,
            }
        if name in seen:
            raise ValueError(f"duplicate features.macro registration {name!r}")
        seen.add(name)
        entries.append(entry)
    if not entries:
        raise ValueError("features.macro contained no registrations")
    return entries


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "oracle_root",
        type=Path,
        help="clean pinned private rippled checkout",
    )
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    source = verify_oracle(args.oracle_root)
    entries = parse_registry(source)
    active_count = sum(not entry["retired"] for entry in entries)
    retired_count = len(entries) - active_count
    document = {
        "oracle": {
            "repository": ORACLE_REPOSITORY,
            "tag": ORACLE_TAG,
            "commit": ORACLE_COMMIT,
            "source": SOURCE_RELATIVE.as_posix(),
            "sha256": hashlib.sha256(source).hexdigest(),
            "entry_count": len(entries),
            "active_count": active_count,
            "retired_count": retired_count,
        },
        "registrations": entries,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
