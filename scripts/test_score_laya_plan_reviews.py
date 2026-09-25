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
        comparison = got["by_split"]["test"]["gated vs rules"]
        self.assertEqual(comparison["gated"], 2)
        self.assertEqual(comparison["agreement"], 1)
        self.assertEqual(comparison["adjudicated"], 1)

    def test_missing_review_remains_unresolved(self):
        got = MODULE.score(self.report, {"one": "a", "two": "a"}, {"one": "a", "two": "b"})
        self.assertEqual(got["unresolved_ids"], ["two"])

    def test_context_and_no_context_comparisons_remain_separate_within_a_split(self):
        report = {
            "count": 2,
            "cases": [
                {"id": "plain", "split": "test", "blinded_a": "gated", "blinded_b": "rules"},
                {"id": "contextual", "split": "test", "blinded_a": "context_gated", "blinded_b": "gated"},
            ],
        }
        got = MODULE.score(
            report,
            {"plain": "a", "contextual": "a"},
            {"plain": "a", "contextual": "a"},
        )
        comparisons = got["by_split"]["test"]
        self.assertEqual(set(comparisons), {"gated vs rules", "context_gated vs gated"})
        self.assertEqual(comparisons["gated vs rules"]["gated"], 1)
        self.assertEqual(comparisons["context_gated vs gated"]["context_gated"], 1)

    def test_partial_or_extra_adjudication_is_rejected(self):
        with self.assertRaises(ValueError):
            MODULE.score(self.report, {"one": "a", "two": "a"}, {"one": "a", "two": "b"}, {})
        with self.assertRaises(ValueError):
            MODULE.score(self.report, {"one": "a", "two": "a"}, {"one": "a", "two": "b"}, {"one": "a", "two": "b"})


if __name__ == "__main__":
    unittest.main()
