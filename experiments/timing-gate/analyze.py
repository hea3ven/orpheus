#!/usr/bin/env python3
"""Summarize retained reports; diagnostic output, never a CI pass signal.

Usage: python3 experiments/timing-gate/analyze.py EVIDENCE_DIRECTORY
Comparability here covers report structure only. Provenance and environment
compatibility require the manifest/log review described in the handoff.
"""

import json
import math
from pathlib import Path
from statistics import median
import sys

LANES = ("unit", "integration")
GIG = "github.com/hea3ven/orpheus/internal/tasksource/gig"


def load(path):
    return json.loads(path.read_text())


def stats(values):
    return {"min": min(values), "median": median(values), "max": max(values)}


def structure(lane):
    return (lane["test_count"],
            sorted((item["name"], item["statement_total"]) for item in lane["packages"]),
            sorted(item["name"] for item in lane["package_timings"]), lane["command"])


def validate_samples(samples):
    if not samples:
        raise ValueError("unevaluated: missing samples")
    for sample in samples:
        if sample["schema_version"] != 1 or not sample["complete"] or set(sample["lanes"]) != set(LANES):
            raise ValueError("unevaluated: unsupported or incomplete report")
        for name in LANES:
            lane = sample["lanes"][name]
            if not lane["passed"] or lane.get("failure"):
                raise ValueError("unevaluated: failed lane")
            values = [lane["wall_seconds"], lane["selected_test_seconds"]]
            values.extend(item["seconds"] for item in lane["package_timings"])
            if any(not math.isfinite(value) or value < 0 for value in values):
                raise ValueError("unevaluated: invalid timing")
            if not math.isclose(sum(item["seconds"] for item in lane["package_timings"]), lane["selected_test_seconds"], abs_tol=1e-8):
                raise ValueError("unevaluated: inconsistent selected work")
            if structure(lane) != structure(samples[0]["lanes"][name]):
                raise ValueError("unevaluated: inconsistent same-revision samples")


def measured_metrics(samples, name):
    return {
        "suite": median(sample["lanes"][name]["selected_test_seconds"] for sample in samples),
        **{item["name"]: median(next(t["seconds"] for t in sample["lanes"][name]["package_timings"] if t["name"] == item["name"]) for sample in samples)
           for item in samples[0]["lanes"][name]["package_timings"]},
    }


def compare(base, candidate):
    # Do not demand equal test counts or coverage/package structure across revisions.
    validate_samples(base)
    validate_samples(candidate)
    result = {}
    for name in LANES:
        if base[0]["lanes"][name]["command"] != candidate[0]["lanes"][name]["command"]:
            raise ValueError("unevaluated: incompatible lane commands")
        before = measured_metrics(base, name)
        after = measured_metrics(candidate, name)
        common = sorted(before.keys() & after.keys())
        metrics = {}
        for key in common:
            relative, absolute = (0.5, 0.5) if key == "suite" else (0.75, 0.25)
            ceiling = before[key] + max(before[key] * relative, absolute)
            metrics[key] = {"base": before[key], "candidate": after[key], "ceiling": ceiling,
                            "exceeds": after[key] > ceiling}
        result[name] = {"metrics": metrics, "added": sorted(after.keys() - before.keys()),
                        "removed": sorted(before.keys() - after.keys())}
    return result


def violations(comparison):
    return [f"{lane}:{key}" for lane, result in comparison.items()
            for key, metric in result["metrics"].items() if metric["exceeds"]]


def totals(report):
    return {metric: sum(report["lanes"][name][metric] for name in LANES)
            for metric in ["wall_seconds", "selected_test_seconds"]}


def summarize_group(samples):
    validate_samples(samples)
    return {
        "count": len(samples),
        "total": {metric: stats([totals(sample)[metric] for sample in samples])
                  for metric in ["wall_seconds", "selected_test_seconds"]},
        "lanes": {name: {
            "test_count": samples[0]["lanes"][name]["test_count"],
            "selected_package_count": len(samples[0]["lanes"][name]["package_timings"]),
            "wall_seconds": stats([sample["lanes"][name]["wall_seconds"] for sample in samples]),
            "selected_test_seconds": stats([sample["lanes"][name]["selected_test_seconds"] for sample in samples]),
            "gig_seconds": stats([next(item["seconds"] for item in sample["lanes"][name]["package_timings"] if item["name"] == GIG) for sample in samples]),
        } for name in LANES},
    }


def main():
    root = Path(sys.argv[1])
    local = root / "local"
    groups = {label: [load(local / f"{index}-{label}" / "report.json") for index in range(1, 6)] for label in "ABS"}
    summary = {
        "diagnostic_only": True,
        "groups": {label: summarize_group(samples) for label, samples in groups.items()},
        "comparisons": {"unchanged": compare(groups["A"], groups["B"]), "slowdown": compare(groups["A"], groups["S"])},
        "first_pair_suspicions": {}, "observed_collection_cost": {},
    }
    for label in "BS":
        summary["first_pair_suspicions"][label] = [violations(compare([base], [candidate])) for base, candidate in zip(groups["A"], groups[label])]
    records = load(local / "samples.json")
    for scope, selected in [("all_including_warmups", records), ("measured", [r for r in records if not r["label"].startswith("warm")])]:
        reports = [load(local / r["label"] / "report.json") for r in selected]
        summary["observed_collection_cost"][scope] = {
            "collector_command_wall_seconds": sum(r["wall_seconds"] for r in selected),
            **{metric: sum(totals(report)[metric] for report in reports) for metric in ["wall_seconds", "selected_test_seconds"]},
        }
    # Observed counterfactual path costs: shared A evidence is reused only for analysis.
    summary["path_costs"] = {}
    for label in "BS":
        warm_base = load(local / "warm-A/report.json")
        warm_candidate = load(local / ("warm-S" if label == "S" else "warm-A") / "report.json")
        paths = {"normal_measured_pair": [groups["A"][0], groups[label][0]],
                 "confirmation_measured_ten": groups["A"] + groups[label],
                 "warmup_pair": [warm_base, warm_candidate]}
        summary["path_costs"][label] = {name: {metric: sum(totals(report)[metric] for report in reports)
                                                       for metric in ["wall_seconds", "selected_test_seconds"]}
                                           for name, reports in paths.items()}
    historical = root / "hosted-historical"
    samples = [load(historical / f"pull-request-quality-37363908772-{index}" / "report.json") for index in range(3, 8)]
    summary["historical_hosted"] = {
        "group": summarize_group(samples),
        "fixed_first_artifact_vs_later": [violations(compare([samples[0]], [sample])) for sample in samples[1:]],
        "warning": "Historical reruns, not fresh experiment jobs. No same-runner pairing, slowdown, or full scheduling fingerprint. No causal host-isolation claim.",
    }
    (root / "analysis.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n")
    print(json.dumps({"groups": summary["groups"], "path_costs": summary["path_costs"],
                      "suspicions": summary["first_pair_suspicions"], "historical": summary["historical_hosted"]}, indent=2))


if __name__ == "__main__":
    main()
