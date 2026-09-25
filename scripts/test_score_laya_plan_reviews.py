"""Focused checks for the private Laya plan-review scorer."""

import importlib.util
import unittest
from pathlib import Path


SPEC = importlib.util.spec_from_file_location(
    "score_laya_plan_reviews", Path(__file__).with_name("score-laya-plan-reviews.py")
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ScoreReviewsTests(unittest.TestCase):
    def setUp(self):
        self.report = {
            "count": 2,
            "cases": [
                {"id": "one", "split": "test", "blinded_a": "gated", "blinded_b": "rules"},
                {"id": "two", "split": "test", "blinded_a": "rules", "blinded_b": "gated"},
            ],
        }

    def test_agreement_and_adjudicated_disagreement(self):
        got = MODULE.score(
            self.report,
            {"one": "a", "two": "a"},
            {"one": "a", "two": "b"},
            {"two": "b"},
        )
        self.assertEqual(got["unresolved_count"], 0)
        self.assertEqual(got["by_split"]["test"]["gated"], 2)
        self.assertEqual(got["by_split"]["test"]["agreement"], 1)
        self.assertEqual(got["by_split"]["test"]["adjudicated"], 1)

    def test_missing_review_remains_unresolved(self):
        got = MODULE.score(self.report, {"one": "a", "two": "a"}, {"one": "a", "two": "b"})
        self.assertEqual(got["unresolved_ids"], ["two"])

    def test_partial_or_extra_adjudication_is_rejected(self):
        with self.assertRaises(ValueError):
            MODULE.score(self.report, {"one": "a", "two": "a"}, {"one": "a", "two": "b"}, {})
        with self.assertRaises(ValueError):
            MODULE.score(self.report, {"one": "a", "two": "a"}, {"one": "a", "two": "b"}, {"one": "a", "two": "b"})


if __name__ == "__main__":
    unittest.main()
