#!/usr/bin/env bash
set -euo pipefail

# This is the repository's reproducible pre-v1 gate. It intentionally does not
# publish a release: release publication remains owned by release.yml.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
build_dir="$(mktemp -d "${TMPDIR:-/tmp}/atenea-v1-readiness.XXXXXX")"
trap 'rm -rf "$build_dir"' EXIT

cd "$root"
export TMPDIR="${TMPDIR:-/tmp}"

echo "[1/8] repository hygiene"
test -z "$(git status --short)" || {
	echo "working tree must be clean" >&2
	git status --short >&2
	exit 1
}
git diff --check

echo "[2/8] active codebase-memory references"
if rg -n -i 'codebase.?memory|codebase_memory' \
	--glob '!docs/content/measuring-the-wrong-process.md' \
	--glob '!docs/content/not-built-yet.md' .; then
	echo "codebase-memory must not be active" >&2
	exit 1
fi

echo "[3/8] formatting"
unformatted="$(gofmt -l .)"
test -z "$unformatted" || {
	echo "gofmt would rewrite:" >&2
	echo "$unformatted" >&2
	exit 1
}

echo "[4/8] module graph"
go mod tidy
git diff --exit-code go.mod go.sum

echo "[5/8] static validation"
go vet ./...

echo "[6/8] build"
go build -trimpath -buildvcs=false -o "$build_dir/atenea" ./cmd/atenea

echo "[7/8] race suite"
go test -race -count=1 ./...

echo "[8/8] shell entry points"
bash -n scripts/install.sh scripts/release-smoke.sh scripts/v1-readiness.sh
"$build_dir/atenea" version

echo "v1 readiness gate passed"
