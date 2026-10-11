#!/usr/bin/env python3
"""Offline diagnostic for three archived hosted attempts; never a production gate.

Usage: python3 experiments/timing-gate/analyze_hosted.py EVIDENCE/hosted-fresh
Producer API metadata and ZIP digests must be reviewed separately. A matching
fingerprint here is not authenticated provenance or evidence of main-only trust.
"""

import json
from pathlib import Path
import sys

from analyze import compare, load, summarize_group, totals, validate_samples, violations
from collect import sample_order


def fingerprint(manifest):
    required = ["revision", "collector_sources", "driver_sha256", "policy_sha256",
                "original_test_sha256", "tools", "runtime", "cpu_affinity",
                "cpu_model", "cgroup", "filesystem", "platform", "environment"]
    result = {key: manifest[key] for key in required}
    result["go_env"] = {key: manifest["go_env"][key] for key in
                        ["GOFLAGS", "GOTOOLCHAIN", "GOVERSION", "GOEXPERIMENT", "GOOS", "GOARCH", "CGO_ENABLED"]}
    result["runner_image"] = {key: manifest["hosted"][key] for key in ["ImageOS", "ImageVersion", "RUNNER_ARCH"]}
    if any(value is None or value == "" or value == {} for value in result.values()):
        raise ValueError("unevaluated: missing environment fingerprint")
    if any(not value for value in result["runner_image"].values()):
        raise ValueError("unevaluated: missing runner image")
    if set(result["cgroup"]) != {"cpu.max", "cpuset.cpus.effective", "memory.max"}:
        raise ValueError("unevaluated: missing resource limits")
    return result


def path_cost(reports, records, labels):
    return {
        **{key: sum(totals(reports[label])[key] for label in labels)
           for key in ["wall_seconds", "selected_test_seconds"]},
        "collector_command_wall_seconds": sum(records[label]["wall_seconds"] for label in labels),
    }


def read_attempt(root, attempt):
    directory = root / f"attempt-{attempt}" / "hosted-output"
    manifest = load(directory / "manifest.json")
    order = load(directory / "order.json")
    if order != [list(pair) for pair in sample_order(attempt)] or manifest["attempt"] != attempt:
        raise ValueError("unevaluated: unexpected measurement order or attempt")
    records = load(directory / "samples.json")
    if [record["label"] for record in records] != [label for label, _ in order]:
        raise ValueError("unevaluated: missing or reordered samples")
    records = {record["label"]: record for record in records}
    reports = {label: load(directory / label / "report.json") for label, _ in order}
    for report in reports.values():
        validate_samples([report])
    groups = {label: [reports[f"{index}-{label}"] for index in range(1, 6)] for label in "AB"}
    comparison = compare(groups["A"], groups["B"])
    costs = {
        "normal_including_warmups": ["warm-A", "warm-B", "1-A", "1-B"],
        "confirmation_including_warmups": list(reports),
        "publisher": ["warm-A"] + [f"{index}-A" for index in range(1, 6)],
        "published_consumer_normal": ["warm-B", "1-B"],
        "published_consumer_confirmation": ["warm-B"] + [f"{index}-B" for index in range(1, 6)],
    }
    try:
        environment = fingerprint(manifest)
        environment_error = None
    except (KeyError, ValueError) as error:
        environment = None
        environment_error = str(error)
    summary = {
        "status": "unevaluated" if environment_error else ("confirmed_regression" if violations(comparison) else "pass"),
        "environment_error": environment_error,
        "groups": {label: summarize_group(samples) for label, samples in groups.items()},
        "same_runner_comparison": comparison,
        "same_runner_median_violations": violations(comparison),
        "pair_suspicions": [violations(compare([a], [b])) for a, b in zip(groups["A"], groups["B"])],
        "path_costs": {name: path_cost(reports, records, labels) for name, labels in costs.items()},
        "original_policy_decisions": {label: report["decision"]["status"] for label, report in reports.items()},
        "fingerprint": environment,
    }
    return groups, summary


def published_comparison(base, candidate, before, after):
    differences = (["missing complete fingerprint"] if before is None or after is None else
                   sorted(key for key in before.keys() | after.keys() if before.get(key) != after.get(key)))
    numerical = compare(base, candidate)
    return {
        "status": "unevaluated" if differences else ("confirmed_regression" if violations(numerical) else "pass"),
        "incompatible_fields": differences,
        "numerical_comparison_only_if_incompatible": numerical,
        "numerical_median_violations": violations(numerical),
        "first_sample_suspicions": violations(compare(base, candidate[:1])),
    }


def main():
    root = Path(sys.argv[1])
    summary = {"diagnostic_only": True, "attempts": {}, "published_baseline": {}}
    groups = {}
    for attempt in (1, 2, 3):
        try:
            groups[attempt], summary["attempts"][str(attempt)] = read_attempt(root, attempt)
        except (OSError, KeyError, ValueError) as error:
            summary["attempts"][str(attempt)] = {"status": "unevaluated", "reason": str(error)}
    for attempt in (2, 3):
        if 1 not in groups or attempt not in groups:
            summary["published_baseline"][str(attempt)] = {"status": "unevaluated", "reason": "missing valid attempt"}
            continue
        summary["published_baseline"][str(attempt)] = published_comparison(
            groups[1]["A"], groups[attempt]["B"],
            summary["attempts"]["1"]["fingerprint"], summary["attempts"][str(attempt)]["fingerprint"])
    (root / "analysis.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n")
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
