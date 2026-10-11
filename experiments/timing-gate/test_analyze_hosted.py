"""Offline fixtures for environment rejection and separate path-cost metrics."""

import unittest

from analyze_hosted import fingerprint, path_cost, published_comparison
from test_analyze import report


class HostedAnalysisTests(unittest.TestCase):
    def test_incompatible_baseline_is_unevaluated_even_when_numbers_pass(self):
        result = published_comparison([report()], [report()], {"image": "a"}, {"image": "b"})

        self.assertEqual("unevaluated", result["status"])
        self.assertEqual(["image"], result["incompatible_fields"])
        self.assertEqual([], result["numerical_median_violations"])

    def test_missing_fingerprint_keeps_arithmetic_but_cannot_pass(self):
        result = published_comparison([report()], [report()], None, None)

        self.assertEqual("unevaluated", result["status"])
        self.assertEqual(["missing complete fingerprint"], result["incompatible_fields"])
        self.assertEqual([], result["numerical_median_violations"])

    def test_compatible_slowdown_is_detected_without_using_wall_time(self):
        candidate = report(3)
        for lane in candidate["lanes"].values():
            lane["wall_seconds"] = 0.01

        result = published_comparison([report()], [candidate], {"image": "a"}, {"image": "a"})

        self.assertEqual("confirmed_regression", result["status"])
        self.assertEqual(4, len(result["numerical_median_violations"]))

    def test_missing_fingerprint_is_not_compatible(self):
        with self.assertRaises(KeyError):
            fingerprint({})

    def test_missing_image_is_rejected(self):
        manifest = dict.fromkeys(["revision", "collector_sources", "driver_sha256", "policy_sha256",
                                 "original_test_sha256", "tools", "runtime", "cpu_affinity",
                                 "cpu_model", "cgroup", "filesystem", "platform", "environment"], "recorded")
        manifest["go_env"] = dict.fromkeys(["GOFLAGS", "GOTOOLCHAIN", "GOVERSION", "GOEXPERIMENT",
                                          "GOOS", "GOARCH", "CGO_ENABLED"], "recorded")
        manifest["hosted"] = {"ImageOS": "ubuntu24", "ImageVersion": None, "RUNNER_ARCH": "X64"}

        with self.assertRaisesRegex(ValueError, "missing runner image"):
            fingerprint(manifest)

    def test_path_cost_separates_command_wall_from_package_work(self):
        reports = {"warm": report(1), "measured": report(2)}
        records = {"warm": {"wall_seconds": 21}, "measured": {"wall_seconds": 22}}

        result = path_cost(reports, records, ["warm", "measured"])

        self.assertEqual({"wall_seconds": 40, "selected_test_seconds": 6,
                          "collector_command_wall_seconds": 43}, result)


if __name__ == "__main__":
    unittest.main()
