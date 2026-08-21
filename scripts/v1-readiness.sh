#!/usr/bin/env bash
set -euo pipefail

# This is the repository's reproducible pre-v1 gate. It intentionally does not
# publish a release: release publication remains owned by release.yml.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
build_dir="$(mktemp -d "${TMPDIR:-/tmp}/atenea-v1-readiness.XXXXXX")"
trap 'rm -rf "$build_dir"' EXIT

cd "$root"
export TMPDIR="${TMPDIR:-/tmp}"

echo "[1/9] repository hygiene"
test -z "$(git status --short)" || {
	echo "working tree must be clean" >&2
	git status --short >&2
	exit 1
}
git diff --check

echo "[2/9] active codebase-memory references"
if command -v rg >/dev/null 2>&1; then
	active_refs=(
		rg -n -i 'codebase.?memory|codebase_memory'
		--glob '!docs/content/measuring-the-wrong-process.md'
		--glob '!docs/content/not-built-yet.md'
		--glob '!docs/content/v1-policy.md'
		--glob '!docs/content/v1-readiness.md'
		--glob '!scripts/v1-readiness.sh'
		.
	)
else
	# Ubuntu's stock runner does not include ripgrep. Keep the gate portable
	# without weakening it when the preferred search tool is unavailable.
	active_refs=(
		grep -RIniE
		--exclude-dir=.git
		--exclude='measuring-the-wrong-process.md'
		--exclude='not-built-yet.md'
		--exclude='v1-policy.md'
		--exclude='v1-readiness.md'
		--exclude='v1-readiness.sh'
		'codebase.?memory|codebase_memory'
		.
	)
fi
if "${active_refs[@]}"; then
	echo "codebase-memory must not be active" >&2
	exit 1
fi

echo "[3/9] formatting"
unformatted="$(gofmt -l .)"
test -z "$unformatted" || {
	echo "gofmt would rewrite:" >&2
	echo "$unformatted" >&2
	exit 1
}

echo "[4/9] module graph"
go mod tidy
git diff --exit-code go.mod go.sum

echo "[5/9] static validation"
go vet ./...

echo "[6/9] build"
go build -trimpath -buildvcs=false -o "$build_dir/atenea" ./cmd/atenea

echo "[7/9] race suite"
go test -race -count=1 ./...

echo "[8/9] policy and shell entry points"
bash -n scripts/install.sh scripts/release-smoke.sh scripts/v1-readiness.sh scripts/v1-policy-check.sh
scripts/v1-policy-check.sh
"$build_dir/atenea" version

echo "v1 readiness gate passed"
