#!/usr/bin/env python3
"""Validate the repository-owned ATENEA GitHub delivery skill."""

from pathlib import Path
import os
import re
import sys

root = Path(__file__).resolve().parents[1]
skill = root / ".agents" / "skills" / "atenea-github-delivery"
entry = skill / "SKILL.md"
errors: list[str] = []

try:
    text = entry.read_text(encoding="utf-8")
except OSError as exc:
    print(f"missing skill entrypoint: {exc}", file=sys.stderr)
    raise SystemExit(1)

frontmatter = re.match(r"\A---\n(.*?)\n---\n", text, re.DOTALL)
if not frontmatter:
    errors.append("SKILL.md has no YAML frontmatter")
else:
    header = frontmatter.group(1)
    if not re.search(r"(?m)^name: atenea-github-delivery$", header):
        errors.append("skill name is missing or invalid")
    if not re.search(r"(?m)^description: .{25,}$", header):
        errors.append("skill description is missing or too short")

for relative in re.findall(r"\[[^]]+\]\(([^)]+)\)", text):
    if "://" not in relative and not (skill / relative).is_file():
        errors.append(f"broken relative link: {relative}")

metadata = skill / "agents" / "openai.yaml"
metadata_text = metadata.read_text(encoding="utf-8") if metadata.is_file() else ""
for required in ('display_name: "', 'short_description: "', 'default_prompt: "', "$atenea-github-delivery"):
    if required not in metadata_text:
        errors.append(f"openai.yaml missing {required}")

checker = skill / "scripts" / "check_delivery.py"
if not checker.is_file() or not os.access(checker, os.X_OK):
    errors.append("delivery checker is missing or not executable")

for required in (skill / "references" / "github-workflow.md", skill / "references" / "templates.md", skill / "tests" / "fixtures" / "delivery-cases.json"):
    if not required.is_file():
        errors.append(f"missing required skill resource: {required.relative_to(root)}")

if errors:
    print("\n".join(errors), file=sys.stderr)
    raise SystemExit(1)
print("ATENEA GitHub delivery skill structure is valid")
