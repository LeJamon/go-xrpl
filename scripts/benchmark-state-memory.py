#!/usr/bin/env python3
"""Run precompiled before/after benchmarks serially and retain raw evidence."""

import argparse
import csv
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import time


def git(tree, *args):
    return subprocess.check_output(["git", "-C", str(tree), *args], text=True).strip()


def benchmark_metrics(log):
    results = []
    for line in log.splitlines():
        match = re.match(r"^(Benchmark\S+)\s+(\d+)\s+(.+)$", line)
        if not match:
            continue
        fields = match[3].split()
        metrics = {"benchmark": match[1], "iterations": int(match[2])}
        for i in range(0, len(fields) - 1, 2):
            try:
                metrics[fields[i + 1]] = float(fields[i])
            except ValueError:
                break
        results.append(metrics)
    return results


def run_process(command, tree, env, log_path):
    start = time.monotonic()
    with log_path.open("wb") as output:
        process = subprocess.Popen(command, cwd=tree, env=env, stdout=output,
                                   stderr=subprocess.STDOUT)
        try:
            _, status, usage = os.wait4(process.pid, 0)
        except BaseException:
            process.terminate()
            process.wait()
            raise
        process.returncode = os.waitstatus_to_exitcode(status)
    metrics = {
        "exit_code": process.returncode,
        "process_wall_seconds": time.monotonic() - start,
        "process_user_seconds": usage.ru_utime,
        "process_system_seconds": usage.ru_stime,
        "process_peak_rss_bytes": usage.ru_maxrss * (1 if platform.system() == "Darwin" else 1024),
        "process_major_faults": usage.ru_majflt,
        "process_minor_faults": usage.ru_minflt,
        "process_input_blocks": usage.ru_inblock,
        "process_output_blocks": usage.ru_oublock,
    }
    return metrics


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--before-tree", type=Path, required=True)
    parser.add_argument("--after-tree", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--samples", type=int, default=5)
    parser.add_argument("--cases", help="Comma-separated case names; default all")
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    if args.samples < 1:
        parser.error("--samples must be positive")
    manifest = json.loads(args.manifest.read_text())
    cases = manifest["cases"]
    if args.cases:
        names = set(args.cases.split(","))
        cases = [case for case in cases if case["name"] in names]
        missing = names - {case["name"] for case in cases}
        if missing:
            parser.error(f"unknown cases: {sorted(missing)}")
    trees = {"before": args.before_tree.resolve(), "after": args.after_tree.resolve()}
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if (output / "runs.json").exists():
        parser.error("output already contains runs.json; choose a fresh directory")
    env = os.environ.copy()
    env.update({key: str(value) for key, value in manifest["environment"].items()})
    metadata = {
        "platform": platform.platform(), "cpu_count": os.cpu_count(),
        "go": subprocess.check_output([args.go, "version"], text=True).strip(),
        "manifest": manifest, "samples": args.samples,
        "revisions": {side: {"commit": git(tree, "rev-parse", "HEAD"),
                             "status": git(tree, "status", "--porcelain"),
                             "diff": git(tree, "diff", "--stat")}
                      for side, tree in trees.items()},
        "notes": ["Process metrics include fixture setup and teardown.",
                  "RSS is the process high-water mark, not post-GC live heap.",
                  "rusage block counts are platform-dependent; zero does not prove zero I/O.",
                  "The OS page cache is not flushed; pair order alternates between samples."],
    }
    (output / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    packages = {"state": "./internal/ledger", "service": "./internal/ledger/service"}
    binaries = {}
    for side, tree in trees.items():
        for suite in sorted({case["suite"] for case in cases}):
            binary = output / f"{side}-{suite}.test"
            binaries[side, suite] = binary
            print(f"building {side} {suite}", flush=True)
            with (output / f"{side}-{suite}-build.log").open("wb") as log:
                subprocess.run([args.go, "test", "-c", "-o", str(binary), packages[suite]],
                               cwd=tree, env=env, stdout=log, stderr=subprocess.STDOUT,
                               check=True)
    metadata["binaries"] = {f"{side}-{suite}": hashlib.sha256(binary.read_bytes()).hexdigest()
                            for (side, suite), binary in binaries.items()}
    (output / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    results = []
    for sample in range(args.samples):
        order = ("before", "after") if sample % 2 == 0 else ("after", "before")
        for case in cases:
            for side in order:
                name = f"{case['name']}-{sample + 1}-{side}"
                print(f"running {name}", flush=True)
                run_env = env | {key: str(value) for key, value in case.get("environment", {}).items()}
                command = [str(binaries[side, case["suite"]]), "-test.count=1",
                           "-test.timeout=" + case.get("timeout", "20m")]
                result_path = output / f"{name}.json"
                if case["suite"] == "state":
                    run_env["STATE_BENCH_CONFIG"] = json.dumps(case["config"])
                    run_env["STATE_BENCH_OUTPUT"] = str(result_path)
                    command += ["-test.run=^TestStateMemoryBenchmark$", "-test.v"]
                else:
                    command += ["-test.run=^$", "-test.bench=" + case["benchmark"],
                                f"-test.benchtime={case['iterations']}x", "-test.benchmem"]
                log_path = output / f"{name}.log"
                result = {"case": case["name"], "sample": sample + 1, "side": side,
                          "command": command, "log": log_path.name}
                result.update(run_process(command, trees[side], run_env, log_path))
                if result["exit_code"] == 0:
                    if case["suite"] == "state":
                        result["state"] = json.loads(result_path.read_text())
                    else:
                        result["benchmarks"] = benchmark_metrics(log_path.read_text())
                        if not result["benchmarks"]:
                            raise RuntimeError(f"no benchmarks ran: {log_path}")
                results.append(result)
                (output / "runs.json").write_text(json.dumps(results, indent=2) + "\n")
                if result["exit_code"]:
                    raise RuntimeError(f"benchmark failed: {log_path}")
                if case["suite"] == "state":
                    final = result["state"]["final"]
                    if not final:
                        raise RuntimeError(f"missing correctness digest: {result_path}")
                    identity_keys = ("sequence", "state_root", "ledger_hash", "read_digest")
                    identities = [tuple(step[key] for key in identity_keys)
                                  for step in result["state"]["samples"]]
                    for previous in results[:-1]:
                        if previous["case"] != case["name"]:
                            continue
                        prior = [tuple(step[key] for key in identity_keys)
                                 for step in previous["state"]["samples"]]
                        if previous["state"]["final"] != final or prior != identities:
                            raise RuntimeError(f"correctness mismatch: {result_path}")
    rows = []
    for run in results:
        common = {key: value for key, value in run.items()
                  if key not in ("command", "state", "benchmarks")}
        for metrics in run.get("benchmarks", [{}]):
            rows.append(common | metrics)
    with (output / "process-and-service.csv").open("w", newline="") as stream:
        columns = sorted({key for row in rows for key in row})
        writer = csv.DictWriter(stream, fieldnames=columns, lineterminator="\n")
        writer.writeheader()
        writer.writerows(rows)
    print(f"results: {output / 'runs.json'}", flush=True)


if __name__ == "__main__":
    main()
