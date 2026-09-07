import importlib.util
import json
import unittest
from pathlib import Path
ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("check_delivery", ROOT / "scripts" / "check_delivery.py")
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)
class DeliveryCases(unittest.TestCase):
    def test_behavioral_fixtures(self):
        cases=json.loads((Path(__file__).parent/"fixtures"/"delivery-cases.json").read_text())
        for case in cases:
            with self.subTest(case=case["name"]):
                reasons=MODULE.validate(case["snapshot"])
                self.assertEqual(not reasons,case["ready"])
                if not case["ready"]: self.assertIn(case["reason"],reasons)
    def test_approved_legacy_branch_still_requires_issue_link(self):
        snapshot={"stage":"pr","issue":{"number":47,"state":"OPEN"},"branch":"feat/atenea-intelligent-orchestration","approved_branch_exception":True,"matching_open_issues":1,"matching_branches":1,"matching_pull_requests":1,"worktree_clean":True,"pull_request":{"body":"No closing link","base":"main","state":"OPEN","head_sha":"head"}}
        self.assertIn("pull request must contain exactly one matching Closes reference",MODULE.validate(snapshot))
    def test_invalid_types_fail_closed(self):
        self.assertEqual(MODULE.validate([]),["snapshot must be a JSON object"])
        self.assertIn("primary issue must be an object",MODULE.validate({"stage":"issue","issue":None}))
    def test_dependency_bot_exception_still_checks_delivery_identity(self):
        snapshot={"stage":"pr","dependency_bot":True,"branch":"dependabot/go/pkg","matching_branches":1,"matching_pull_requests":1,"worktree_clean":True,"pull_request":{"body":"","base":"main","state":"OPEN","head_sha":"head"}}
        self.assertEqual(MODULE.validate(snapshot),[])
if __name__ == "__main__": unittest.main()
