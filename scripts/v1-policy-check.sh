#!/usr/bin/env bash
set -euo pipefail

# Keep the user-facing v1.0 policy tied to the contracts that implement it.
# This is deliberately small and textual: it catches accidental removal or
# renaming of the policy anchors without trying to turn documentation into a
# second parser for the Go code.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

contains() {
	local pattern="$1"
	shift
	if command -v rg >/dev/null 2>&1; then
		rg -q "$pattern" "$@"
	else
		grep -RIniEq --exclude-dir=.git "$pattern" "$@"
	fi
}

required_files=(
	"docs/content/v1-policy.md"
	"docs/content/v1-contracts.md"
	"docs/content/v1-readiness.md"
	"docs/content/v1-final-audit.md"
	"pkg/contract/assignment.go"
	"internal/agent/model/model.go"
	"scripts/opencode-smoke.sh"
	"scripts/opencode-matrix.sh"
	"internal/agent/reviewer/citations.go"
	"internal/agent/review_integration_test.go"
	"internal/adapter/serena/serena.go"
)
for file in "${required_files[@]}"; do
	test -f "$file" || { echo "missing v1 policy anchor: $file" >&2; exit 1; }
done

policy_docs=(docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md)
contains 'limits\.max_tokens' "${policy_docs[@]}"
contains 'advisory|hard cap' "${policy_docs[@]}"
contains 'OpenCode.*provider|provider.*OpenCode' docs/content/v1-policy.md docs/content/v1-contracts.md
contains 'interactive permission|confirmación interactiva|interactivo' \
	docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md
contains 'citation|cita' docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md
contains 'citation_count|uncited_fields|resolved_path' docs/content/v1-contracts.md docs/content/v1-readiness.md
contains 'Tokensave|Semgrep|Context7|claude-mem|Headroom' docs/content/v1-final-audit.md
contains 'symbol\.search' docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md
contains '75\.0%' .github/workflows/ci.yml docs/content/v1-policy.md docs/content/v1-readiness.md
contains 'code\.impact.*repository\.index|repository\.index.*code\.impact' docs/content/v1-policy.md docs/content/v1-readiness.md docs/content/v1-final-audit.md

echo "v1 policy anchors passed"
