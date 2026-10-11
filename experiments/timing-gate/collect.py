#!/usr/bin/env python3
"""Bounded local diagnostic, not a replacement quality gate or policy updater.

Run from the repository root: python3 experiments/timing-gate/collect.py OUTPUT
OUTPUT must not exist. Keeps original collector reports, command logs, identities,
and a Go overlay adding a three-second sleep to one existing Gig adapter test.
"""

import hashlib
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]
TEST = Path("internal/tasksource/gig/gig_adapter_test.go")
ANCHOR = "func TestIntegrationAdapterContractGigFieldsRelationshipsAndLifecycle(t *testing.T) {\n"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def save(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def capture(args):
    return subprocess.check_output(args, cwd=ROOT, text=True, stderr=subprocess.STDOUT).strip()


def run_bounded(args, env, log, seconds):
    started = time.monotonic()
    with log.open("w") as output:
        process = subprocess.Popen(
            args, cwd=ROOT, env=env, stdout=output, stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        try:
            code = process.wait(timeout=seconds)
        except BaseException:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
            raise
    return {"exit_code": code, "wall_seconds": time.monotonic() - started}


def sample_order(attempt=None):
    if attempt is None:
        order = [("warm-A", "base"), ("warm-S", "slow")]
        for index, labels in enumerate(["ABS", "SBA", "BAS", "SAB", "ASB"], 1):
            order.extend((f"{index}-{label}", "slow" if label == "S" else "base") for label in labels)
        return order
    if attempt not in (1, 2, 3):
        raise ValueError("Hosted budget permits attempts 1, 2, and 3 only")
    candidate = "slow" if attempt == 3 else "base"
    labels = "BA" if attempt == 2 else "AB"
    variants = {"A": "base", "B": candidate}
    order = [(f"warm-{label}", variants[label]) for label in labels]
    for index in range(1, 6):
        order.extend((f"{index}-{label}", variants[label]) for label in labels)
        labels = labels[::-1]
    return order


def main():
    if len(sys.argv) not in (2, 3):
        raise SystemExit(__doc__)
    # Optional numeric argument is reserved for the three bounded hosted attempts.
    attempt = int(sys.argv[2]) if len(sys.argv) == 3 else None
    order = sample_order(attempt)
    if capture(["git", "diff", "HEAD", "--", "*.go", "go.mod", "go.sum", ".quality.yml"]):
        raise SystemExit("Refusing to measure modified Go sources, dependencies, or policy")
    output = Path(sys.argv[1]).resolve()
    output.relative_to(ROOT)
    output.mkdir(parents=True, exist_ok=False)
    scratch = ROOT / "experiments/timing-gate/.scratch"
    scratch.mkdir(exist_ok=True)
    original = (ROOT / TEST).read_text()
    if original.count(ANCHOR) != 1:
        raise SystemExit("Slowdown anchor does not uniquely identify the existing test")
    slow = output / "slow-source.txt"
    slow.write_text(original.replace(ANCHOR, ANCHOR + "\ttime.Sleep(3 * time.Second) // Diagnostic slowdown only.\n"))
    for variant in ["base", "slow"]:
        save(output / (variant + "-overlay.json"), {
            "Replace": {} if variant == "base" else {str(ROOT / TEST): str(slow)},
        })
    # Go's ./... traversal ignores the dot-prefixed scratch directory.
    probe = scratch / "runtime.go"
    probe.write_text('package main\nimport ("fmt"; "runtime")\nfunc main() { fmt.Printf("NumCPU=%d GOMAXPROCS=%d\\n", runtime.NumCPU(), runtime.GOMAXPROCS(0)) }\n')
    env = os.environ.copy()
    # All measurement commands are offline; installed modules/tools are prerequisites.
    env.update(GOPROXY="off", GOSUMDB="off")
    metadata = {
        "schema": "timing-investigation-v1", "revision": capture(["git", "rev-parse", "HEAD"]),
        "tree": capture(["git", "rev-parse", "HEAD^{tree}"]),
        "branch": capture(["git", "branch", "--show-current"]),
        "collector_sources": {str(p.relative_to(ROOT)): digest(p) for p in sorted((ROOT / "cmd/testcoverage").glob("*.go"))},
        "driver_sha256": digest(Path(__file__)), "policy_sha256": digest(ROOT / ".quality.yml"),
        "slow_source_sha256": digest(slow), "original_test_sha256": digest(ROOT / TEST),
        "tools": {"go": capture(["go", "version"]), "git": capture(["git", "--version"]),
                  "bd": capture(["bd", "version"]), "dolt": capture(["dolt", "version"]),
                  "python": sys.version},
        "go_env": json.loads(capture(["go", "env", "-json", "GOFLAGS", "GOTOOLCHAIN", "GOVERSION", "GOEXPERIMENT", "GOOS", "GOARCH", "CGO_ENABLED", "GOCACHE", "GOMODCACHE"])),
        "runtime": subprocess.check_output(["go", "run", str(probe)], cwd=ROOT, env=env, text=True).strip(),
        "environment": {name: env.get(name) for name in ["GOFLAGS", "GOMAXPROCS", "GODEBUG", "GOTOOLCHAIN", "GOEXPERIMENT", "CC", "CXX", "GOPROXY", "GOSUMDB"]},
        "platform": platform.platform(), "cpu_affinity": sorted(os.sched_getaffinity(0)),
        "cpu_model": next((line.strip() for line in Path("/proc/cpuinfo").read_text().splitlines() if line.startswith("model name")), "unknown"),
        "cgroup": {name: Path("/sys/fs/cgroup", name).read_text().strip() for name in ["cpu.max", "cpuset.cpus.effective", "memory.max"] if Path("/sys/fs/cgroup", name).exists()},
        "hosted": {name: env.get(name) for name in ["GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_SHA", "GITHUB_HEAD_REF", "RUNNER_NAME", "RUNNER_OS", "RUNNER_ARCH", "ImageOS", "ImageVersion"]},
        "attempt": attempt,
        "filesystem": capture(["stat", "-f", "-c", "%T", str(ROOT)]),
        "collection_note": "Unmodified Go collector; exit status is policy assessment, not sample completeness. No policy updates. Base and unchanged candidate are identical sources in one checkout; slowdown is an overlay, not a commit. Default package concurrency, -parallel=1, ORPHEUS_COVERAGE_RUN=1 retained by collector.",
        "sample_timeout_seconds": 180, "total_measurement_budget_seconds": 1200,
    }
    collector = scratch / "collector"
    build = run_bounded(["go", "build", "-o", str(collector), "./cmd/testcoverage"], env, output / "collector-build.log", 180)
    metadata["build"] = build
    metadata["collector_binary_sha256"] = digest(collector) if collector.exists() else None
    save(output / "manifest.json", metadata)
    if build["exit_code"]:
        raise SystemExit("Collector build failed")
    save(output / "order.json", order)
    records = []
    started = time.monotonic()
    for label, variant in order:
        if time.monotonic() - started + 180 > 1200:
            raise SystemExit("Local measurement budget exhausted; retain partial evidence")
        sample_dir = output / label
        sample_dir.mkdir()
        sample_env = env.copy()
        prior_flags = metadata["go_env"]["GOFLAGS"]
        if "-overlay" in prior_flags:
            raise SystemExit("An inherited overlay would make source identity ambiguous")
        sample_env["GOFLAGS"] = (prior_flags + " -overlay=" + str(output / (variant + "-overlay.json"))).strip()
        args = [str(collector), "-output", str(sample_dir / "report.json")]
        print(f"Collecting {label} ({variant})", flush=True)
        record = {"label": label, "variant": variant, "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                  "command": args, "GOFLAGS": sample_env["GOFLAGS"], "load_before": os.getloadavg()}
        record.update(run_bounded(args, sample_env, sample_dir / "command.log", 180))
        report = json.loads((sample_dir / "report.json").read_text())
        record.update(complete=report["complete"], decision=report["decision"]["status"], load_after=os.getloadavg())
        records.append(record)
        save(output / "samples.json", records)
        if not report["complete"] or not all(lane["passed"] for lane in report["lanes"].values()):
            raise SystemExit(f"Incomplete sample {label}; retain evidence without rerunning")
    if digest(ROOT / TEST) != metadata["original_test_sha256"] or digest(ROOT / ".quality.yml") != metadata["policy_sha256"]:
        raise SystemExit("Source or policy changed during collection")
    print(f"Completed {len(order)} samples (2 excluded warm-ups).", flush=True)


if __name__ == "__main__":
    main()
