#!/usr/bin/env bash
set -euo pipefail

# Keep the user-facing v1.0 policy tied to the contracts that implement it.
# This is deliberately small and textual: it catches accidental removal or
# renaming of the policy anchors without trying to turn documentation into a
# second parser for the Go code.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

required_files=(
	"docs/content/v1-policy.md"
	"docs/content/v1-contracts.md"
	"docs/content/v1-readiness.md"
	"pkg/contract/assignment.go"
	"internal/agent/model/model.go"
	"internal/agent/reviewer/citations.go"
	"internal/adapter/serena/serena.go"
)
for file in "${required_files[@]}"; do
	test -f "$file" || { echo "missing v1 policy anchor: $file" >&2; exit 1; }
done

policy_docs=(docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md)
rg -q 'limits\.max_tokens' "${policy_docs[@]}"
rg -q 'advisory|hard cap|hard cap' "${policy_docs[@]}"
rg -q 'OpenCode.*provider|provider.*OpenCode' docs/content/v1-policy.md docs/content/v1-contracts.md
rg -q 'interactive permission|confirmación interactiva|interactivo' \
	docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md
rg -q 'citation|cita' docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md
rg -q 'symbol\.search' docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md

echo "v1 policy anchors passed"
