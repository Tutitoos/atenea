#!/usr/bin/env bash
set -euo pipefail

# Keep the user-facing v1.0 policy tied to the contracts that implement it.
# This is deliberately small and textual: it catches accidental removal or
# renaming of the policy anchors without trying to turn documentation into a
# second parser for the Go code.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

# contains asserts that EVERY file named still carries the anchor.
#
# It used to be an OR: one match anywhere in the list passed, which made a
# policy anchor easy to satisfy accidentally. Where an OR is really what is
# wanted -- one page among several must say a thing -- `contains_any` says so
# out loud.
#
# One tool, not two: `rg -q` is case-sensitive and `grep -q -i` is not, so the
# pair could disagree about the same anchor. grep is on every runner.
contains() {
	local pattern="$1"
	shift
	local file missing=0
	for file in "$@"; do
		grep -IniEq -- "$pattern" "$file" || {
			echo "missing anchor /$pattern/ in $file" >&2
			missing=1
		}
	done
	test "$missing" -eq 0
}

contains_any() {
	local pattern="$1"
	shift
	local file
	for file in "$@"; do
		if grep -IniEq -- "$pattern" "$file"; then
			return 0
		fi
	done
	echo "missing anchor /$pattern/ in all of: $*" >&2
	return 1
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
contains_any 'advisory|hard cap' "${policy_docs[@]}"
contains_any 'OpenCode.*provider|provider.*OpenCode' docs/content/v1-policy.md docs/content/v1-contracts.md
contains 'interactive permission|confirmación interactiva|interactivo' \
	docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md
contains 'citation|cita' docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md
contains_any 'citation_count|uncited_fields|resolved_path' docs/content/v1-contracts.md docs/content/v1-readiness.md
contains_any 'Tokensave|Semgrep|Context7|claude-mem|Headroom' docs/content/v1-final-audit.md
contains 'symbol\.search' docs/content/v1-policy.md docs/content/v1-contracts.md docs/content/v1-readiness.md
contains_any 'code\.impact.*repository\.index|repository\.index.*code\.impact' docs/content/v1-policy.md docs/content/v1-readiness.md docs/content/v1-final-audit.md

echo "v1 policy anchors passed"
