#!/usr/bin/env python3
"""Validate ATENEA's repository-owned FloowGitHub policy."""

import json
import re
import sys
from pathlib import Path

root = Path(__file__).resolve().parents[1]
policy_path = root / ".github" / "floowgithub.json"
errors: list[str] = []

try:
    policy = json.loads(policy_path.read_text(encoding="utf-8"))
except (OSError, UnicodeError, json.JSONDecodeError) as exc:
    print(f"invalid FloowGitHub policy: {exc}", file=sys.stderr)
    raise SystemExit(1)

if not isinstance(policy, dict):
    errors.append("FloowGitHub policy must be a JSON object")
else:
    if policy.get("base_branch") != "main":
        errors.append("ATENEA base_branch must be main")
    pattern = policy.get("branch_pattern")
    if not isinstance(pattern, str):
        errors.append("branch_pattern must be a string")
    else:
        try:
            compiled = re.compile(pattern)
        except re.error as exc:
            errors.append(f"branch_pattern is invalid: {exc}")
        else:
            if compiled.fullmatch("feat/159-adopt-floowgithub") is None:
                errors.append("branch_pattern must accept ATENEA issue branches")
            if compiled.fullmatch("master") is not None:
                errors.append("branch_pattern must reject master")
    if policy.get("issue_required") is not True:
        errors.append("ATENEA must require a primary issue")
    if policy.get("closing_keywords") != ["Closes"]:
        errors.append("ATENEA must use the Closes keyword")
    if policy.get("exactly_one_closing_reference") is not True:
        errors.append("ATENEA must require exactly one closing reference")

if errors:
    print("\n".join(errors), file=sys.stderr)
    raise SystemExit(1)
print("ATENEA FloowGitHub policy is valid")
