"""Pure order-contract checks; no measurements or subprocesses."""

import unittest

from collect import sample_order


class OrderTests(unittest.TestCase):
    def test_hosted_pairs_alternate_with_balanced_starts_across_control_jobs(self):
        for attempt, first in [(1, "AB"), (2, "BA"), (3, "AB")]:
            with self.subTest(attempt=attempt):
                order = sample_order(attempt)

                self.assertEqual(12, len(order))
                self.assertEqual([f"warm-{label}" for label in first], [label for label, _ in order[:2]])
                for index in range(1, 6):
                    labels = first if index % 2 else first[::-1]
                    self.assertEqual([f"{index}-{label}" for label in labels],
                                     [label for label, _ in order[index * 2:index * 2 + 2]])
                self.assertEqual(6 if attempt == 3 else 0,
                                 sum(variant == "slow" for _, variant in order))
                for label, variant in order:
                    self.assertEqual("slow" if attempt == 3 and label.endswith("B") else "base", variant)

    def test_out_of_budget_hosted_attempt_is_rejected(self):
        for attempt in [0, 4, -1]:
            with self.subTest(attempt=attempt), self.assertRaises(ValueError):
                sample_order(attempt)

    def test_original_local_triplet_order_is_preserved(self):
        order = sample_order()

        self.assertEqual(17, len(order))
        self.assertEqual([("warm-A", "base"), ("warm-S", "slow")], order[:2])
        self.assertEqual("ABSSBABASSABASB", "".join(label[-1] for label, _ in order[2:]))


if __name__ == "__main__":
    unittest.main()
