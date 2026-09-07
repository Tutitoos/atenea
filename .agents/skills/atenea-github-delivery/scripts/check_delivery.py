#!/usr/bin/env python3
"""Validate a sanitized ATENEA issue-to-PR delivery snapshot."""
from __future__ import annotations
import argparse, json, re, sys
from pathlib import Path
BRANCH = re.compile(r"^(feat|fix|docs|refactor|chore)/(\d+)-[a-z0-9]+(?:-[a-z0-9]+)*$")
CLOSES = re.compile(r"(?im)^\s*closes\s+#(\d+)\s*$")
STAGES = {"issue", "branch", "pr", "merge"}

def integer(value: object) -> int | None:
    if isinstance(value, bool): return None
    try: number = int(value)
    except (TypeError, ValueError): return None
    return number if number > 0 else None

def validate(data: object) -> list[str]:
    if not isinstance(data, dict): return ["snapshot must be a JSON object"]
    stage = data.get("stage")
    if stage not in STAGES: return ["stage must be issue, branch, pr, or merge"]
    reasons: list[str] = []
    dependency_bot = data.get("dependency_bot", False)
    private_security_advisory = data.get("private_security_advisory", False)
    if not isinstance(dependency_bot, bool): reasons.append("dependency_bot must be boolean")
    if not isinstance(private_security_advisory, bool): reasons.append("private_security_advisory must be boolean")
    exempt = dependency_bot is True or private_security_advisory is True
    issue = data.get("issue")
    if not exempt and not isinstance(issue, dict): return ["primary issue must be an object"]
    issue = issue if isinstance(issue, dict) else {}
    issue_number = integer(issue.get("number"))
    if not exempt:
        if issue_number is None: reasons.append("primary issue number is invalid")
        if issue.get("state") != "OPEN": reasons.append("primary issue is not open")
        if integer(data.get("matching_open_issues")) != 1: reasons.append("scope must resolve to exactly one open primary issue")
    if stage == "issue": return reasons
    branch = data.get("branch")
    if not isinstance(branch, str):
        reasons.append("branch must be a string"); branch = ""
    match = BRANCH.fullmatch(branch)
    approved_exception = data.get("approved_branch_exception", False)
    if not isinstance(approved_exception, bool): reasons.append("approved_branch_exception must be boolean")
    if branch == "main":
        reasons.append("branch must not be main")
    elif not match and approved_exception is not True and not exempt:
        reasons.append("branch does not match ATENEA naming policy")
    if match and issue_number is not None and int(match.group(2)) != issue_number:
        reasons.append("branch issue number does not match primary issue")
    if integer(data.get("matching_branches")) != 1: reasons.append("scope must resolve to exactly one branch")
    if data.get("worktree_clean") is not True: reasons.append("worktree is not clean")
    if stage == "branch": return reasons
    pr = data.get("pull_request")
    if not isinstance(pr, dict):
        reasons.append("pull request must be an object"); return reasons
    if integer(data.get("matching_pull_requests")) != 1: reasons.append("scope must resolve to exactly one pull request")
    if pr.get("base") != "main": reasons.append("pull request base is not main")
    if pr.get("state") != "OPEN": reasons.append("pull request is not open")
    head = pr.get("head_sha")
    if not isinstance(head, str) or not head.strip(): reasons.append("pull request head SHA is missing")
    if not exempt:
        closes = [int(value) for value in CLOSES.findall(str(pr.get("body", "")))]
        if issue_number is None or closes != [issue_number]: reasons.append("pull request must contain exactly one matching Closes reference")
    if stage == "pr": return reasons
    if pr.get("draft") is not False: reasons.append("pull request is draft or draft state is unknown")
    if pr.get("mergeable") != "MERGEABLE": reasons.append("pull request is conflicted or mergeability is unknown")
    reviewed = data.get("reviewed_head_sha")
    if not isinstance(reviewed, str) or not reviewed or head != reviewed: reasons.append("reviewed head SHA is stale or missing")
    review_required = data.get("review_required")
    if not isinstance(review_required, bool): reasons.append("review_required must be boolean")
    review_decision = pr.get("review_decision")
    if review_decision == "CHANGES_REQUESTED": reasons.append("active review requests changes")
    elif review_required is True and review_decision != "APPROVED": reasons.append("required review is not approved")
    checks, required = pr.get("checks"), pr.get("required_checks")
    if not isinstance(checks, list) or not isinstance(required, list) or not required:
        reasons.append("required checks are missing")
    else:
        conclusions = {check.get("name"): str(check.get("conclusion", "")).upper() for check in checks if isinstance(check, dict) and isinstance(check.get("name"), str)}
        for name in required:
            if not isinstance(name, str) or conclusions.get(name) != "SUCCESS": reasons.append(f"required check is not successful: {name}")
    threads = pr.get("actionable_threads")
    if not isinstance(threads, int) or isinstance(threads, bool) or threads < 0: reasons.append("actionable review thread count is invalid")
    elif threads: reasons.append("actionable review threads remain")
    return reasons

def main() -> int:
    parser=argparse.ArgumentParser(); parser.add_argument("snapshot", type=Path); args=parser.parse_args()
    try: reasons=validate(json.loads(args.snapshot.read_text(encoding="utf-8")))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc: reasons=[f"invalid snapshot: {exc}"]
    print(json.dumps({"ready": not reasons, "reasons": reasons}, sort_keys=True))
    return 0 if not reasons else 1
if __name__ == "__main__": sys.exit(main())
