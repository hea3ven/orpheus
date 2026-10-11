"""Pure fixtures for the diagnostic arithmetic; no lanes or child processes."""

from copy import deepcopy
import unittest

from analyze import compare, validate_samples, violations


def report(seconds=1):
    lane = {"passed": True, "test_count": 1, "command": ["go", "test"],
            "wall_seconds": 10, "selected_test_seconds": seconds,
            "packages": [{"name": "example/pkg", "statement_total": 10}],
            "package_timings": [{"name": "example/pkg", "seconds": seconds}]}
    return {"schema_version": 1, "complete": True,
            "lanes": {"unit": deepcopy(lane), "integration": deepcopy(lane)}}


class ComparisonTests(unittest.TestCase):
    def test_unchanged_samples_do_not_exceed_and_wall_is_not_a_gate(self):
        candidate = report()
        candidate["lanes"]["unit"]["wall_seconds"] = 1000

        result = compare([report()], [candidate])

        self.assertEqual([], violations(result))

    def test_slowdown_exceeds_suite_and_package_thresholds(self):
        result = compare([report()], [report(3)])

        self.assertEqual(4, len(violations(result)))
        self.assertEqual(1.5, result["unit"]["metrics"]["suite"]["ceiling"])
        self.assertEqual(1.75, result["unit"]["metrics"]["example/pkg"]["ceiling"])

    def test_absolute_headroom_and_exact_boundary_do_not_exceed(self):
        result = compare([report(0.1)], [report(0.35)])

        self.assertEqual([], violations(result))
        self.assertEqual(0.6, result["unit"]["metrics"]["suite"]["ceiling"])

    def test_cross_revision_package_and_test_changes_are_reported_not_rejected(self):
        candidate = report(3)
        for lane in candidate["lanes"].values():
            lane["test_count"] = 7
            lane["packages"][0]["name"] = "example/new"
            lane["packages"][0]["statement_total"] = 20
            lane["package_timings"][0]["name"] = "example/new"

        result = compare([report()], [candidate])

        self.assertEqual(["example/new"], result["unit"]["added"])
        self.assertEqual(["example/pkg"], result["unit"]["removed"])
        self.assertEqual(["integration:suite", "unit:suite"], sorted(violations(result)))

    def test_inconsistent_counts_within_one_revision_are_unevaluated(self):
        candidate = report()
        candidate["lanes"]["unit"]["test_count"] = 2

        with self.assertRaisesRegex(ValueError, "unevaluated"):
            validate_samples([report(), candidate])

    def test_missing_failed_incomplete_nonfinite_and_incompatible_are_unevaluated(self):
        incomplete = report()
        incomplete["complete"] = False
        failed = report()
        failed["lanes"]["unit"]["passed"] = False
        nonfinite = report(float("nan"))
        incompatible = report()
        incompatible["lanes"]["unit"]["command"] += ["-p=1"]
        inconsistent = report()
        inconsistent["lanes"]["unit"]["selected_test_seconds"] = 9

        for samples in [[], [incomplete], [failed], [nonfinite], [incompatible], [inconsistent]]:
            with self.subTest(samples=samples), self.assertRaisesRegex(ValueError, "unevaluated"):
                compare([report()], samples)


if __name__ == "__main__":
    unittest.main()
