#!/usr/bin/env python3
"""Inventory freshly recorded v4 fixtures without rewriting their inputs or outputs."""

import argparse
import hashlib
import itertools
import json
import subprocess
from pathlib import Path

ORACLE_REPOSITORY = "XRPLF/xrpld-private"
ORACLE_TAG = "3.4.1"
ORACLE_COMMIT = "d147fccf54a500fce586522f28d6044c37fd8d29"
RECORDER_SOURCES = {
    "scripts/oracle/record-v4.py",
    "scripts/oracle/record-v4.sh",
    "scripts/oracle/strict-corpus-config.json",
    "scripts/oracle/strict_recorder.cpp",
}


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args]).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--oracle", required=True, type=Path)
    parser.add_argument("--corpus", required=True, type=Path)
    parser.add_argument("--recorder-commit", required=True)
    parser.add_argument("--recorder-source", action="append", required=True)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--config", required=True, type=Path)
    parser.add_argument("--build-identity", required=True)
    parser.add_argument("--coverage-limit", action="append", required=True)
    parser.add_argument("--unsupported-profile", action="append", default=[], metavar="ID=REASON")
    args = parser.parse_args()
    repo = Path(__file__).resolve().parent.parent
    oracle = args.oracle.resolve()
    if Path(git(oracle, "rev-parse", "--show-toplevel").decode()).resolve() != oracle:
        parser.error("--oracle must name the checkout root")
    if git(oracle, "rev-parse", "HEAD").decode() != ORACLE_COMMIT:
        parser.error("oracle HEAD does not match the pinned private 3.4.1 commit")
    if git(oracle, "rev-parse", f"refs/tags/{ORACLE_TAG}^{{commit}}").decode() != ORACLE_COMMIT:
        parser.error("oracle tag does not resolve to the pinned commit")
    remote = git(oracle, "remote", "get-url", "origin").decode()
    if remote not in (f"git@github.com:{ORACLE_REPOSITORY}.git", f"https://github.com/{ORACLE_REPOSITORY}.git", f"https://github.com/{ORACLE_REPOSITORY}"):
        parser.error("oracle origin must identify XRPLF/xrpld-private")
    if git(oracle, "status", "--porcelain", "--untracked-files=normal"):
        parser.error("oracle checkout must be clean")
    recorder_commit = git(repo, "rev-parse", f"{args.recorder_commit}^{{commit}}").decode()
    sources = {}
    for name in args.recorder_source:
        path = (repo / name).resolve()
        if not path.is_relative_to(repo) or not path.is_file():
            parser.error(f"recorder source must be a regular repository file: {name}")
        relative = path.relative_to(repo).as_posix()
        data = path.read_bytes()
        if data != git_blob(repo, recorder_commit, relative):
            parser.error(f"recorder source differs from {recorder_commit}: {relative}")
        sources[relative] = sha256(data)
    if set(sources) != RECORDER_SOURCES:
        parser.error(f"recorder source inventory must be exactly {sorted(RECORDER_SOURCES)}")
    archive = Path("scripts/oracle/recorded") / recorder_commit
    for name, checksum in sources.items():
        snapshot = repo / archive / name
        data = git_blob(repo, recorder_commit, name)
        if sha256(data) != checksum:
            parser.error(f"recorded source checksum changed: {name}")
        if snapshot.exists() and (snapshot.is_symlink() or snapshot.read_bytes() != data):
            parser.error(f"recorded source archive differs: {snapshot}")
        snapshot.parent.mkdir(parents=True, exist_ok=True)
        snapshot.write_bytes(data)
    config = args.config.resolve()
    if config != repo / "scripts/oracle/strict-corpus-config.json":
        parser.error("--config must select scripts/oracle/strict-corpus-config.json")
    version = subprocess.check_output([str(args.binary.resolve()), "--version"], text=True)
    if version.splitlines()[0] != "xrpld version 3.4.1":
        parser.error("binary does not report pinned version 3.4.1")
    fixtures = {}
    profiles_seen = set()
    for path in sorted(args.corpus.rglob("*.json")):
        if path.name == "manifest.json" and path.parent == args.corpus:
            continue
        if path.is_symlink() or not path.is_file():
            parser.error(f"fixture must be a regular file: {path}")
        data = path.read_bytes()
        fixture = json.loads(data)
        identity = (fixture.get("fixture_version"), fixture.get("oracle_repository"), fixture.get("oracle_tag"), fixture.get("oracle_commit"))
        if identity != ("v4", ORACLE_REPOSITORY, ORACLE_TAG, ORACLE_COMMIT):
            parser.error(f"fixture has stale/mixed identity: {path}")
        pin = {field: fixture[field] for field in ("suite", "family", "profile")}
        if any(not isinstance(value, str) or not value for value in pin.values()):
            parser.error(f"fixture metadata missing: {path}")
        profiles_seen.add(pin["profile"])
        fixtures[path.relative_to(args.corpus).as_posix()] = {"sha256": sha256(data), **pin}
    if not fixtures:
        parser.error("corpus is empty")
    matrix = []
    profile_ids = set()
    unsupported = {}
    for declaration in args.unsupported_profile:
        name, separator, reason = declaration.partition("=")
        if not separator or not reason.strip() or name in unsupported:
            parser.error("unsupported profiles require unique ID=REASON declarations")
        unsupported[name] = reason
    for cleanup, lending, batch, fix in itertools.product((False, True), repeat=4):
        name = f"c{int(cleanup)}-l{int(lending)}-b{int(batch)}-f{int(fix)}"
        profile_ids.add(name)
        if name in unsupported and (batch or not fix):
            parser.error(f"profile must execute: {name}")
        if name in unsupported and name in profiles_seen:
            parser.error(f"unsupported profile must have no fixture files: {name}")
        if name not in unsupported and name not in profiles_seen:
            parser.error(f"required amendment profile was not recorded: {name}")
        profile = {"id": name, "fixCleanup3_4_0": cleanup, "LendingProtocolV1_1": lending,
                   "BatchV1_1": batch, "fixBatchV1_2": fix, "supported": name not in unsupported}
        if name in unsupported:
            profile["unsupported_reason"] = unsupported[name]
        matrix.append(profile)
    unknown = (profiles_seen | set(unsupported)) - profile_ids
    if unknown:
        parser.error(f"unknown profile IDs: {unknown}")
    manifest = {
        "schema": 4,
        "fixture_version": "v4",
        "oracle_repository": ORACLE_REPOSITORY,
        "rippled_tag": ORACLE_TAG,
        "rippled_commit": ORACLE_COMMIT,
        "recorder_commit": recorder_commit,
        "recorder_source_archive": archive.as_posix(),
        "recorder_sources": sources,
        "binary_sha256": sha256(args.binary.read_bytes()),
        "build_identity": args.build_identity,
        "config_identity": sha256(config.read_bytes()),
        "config_source": config.relative_to(repo).as_posix(),
        "amendment_matrix": matrix,
        "fixture_count": len(fixtures),
        "fixtures": fixtures,
        "coverage_limits": args.coverage_limit,
    }
    output = args.corpus / "manifest.json"
    output.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    print(f"Wrote {output}: {len(fixtures)} cases, {len(matrix)} profiles; no fixtures modified")


def git_blob(root, commit, path):
    return subprocess.check_output(["git", "-C", str(root), "show", f"{commit}:{path}"])


if __name__ == "__main__":
    main()
