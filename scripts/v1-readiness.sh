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

echo "[2/9] retired backend references"
# Keep the retired-backend check without retaining its historical product name
# in repository text. The two fragments are joined only while the gate runs.
legacy_prefix="codebase"
legacy_suffix="memory"
legacy_pattern="${legacy_prefix}.${legacy_suffix}|${legacy_prefix}_${legacy_suffix}"
if command -v rg >/dev/null 2>&1; then
	active_refs=(
		rg -n -i "$legacy_pattern"
		.
	)
else
	# Ubuntu's stock runner does not include ripgrep. Keep the gate portable
	# without weakening it when the preferred search tool is unavailable.
	active_refs=(
		grep -RIniE
		--exclude-dir=.git
		"$legacy_pattern"
		.
	)
fi
if "${active_refs[@]}"; then
	echo "retired backend references must not be present" >&2
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
bash -n scripts/install.sh scripts/release-smoke.sh scripts/opencode-smoke.sh scripts/opencode-matrix.sh scripts/v1-readiness.sh scripts/v1-policy-check.sh
scripts/v1-policy-check.sh
"$build_dir/atenea" version

echo "v1 readiness gate passed"
