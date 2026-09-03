#!/usr/bin/env bash
set -euo pipefail

# This is the repository's reproducible pre-v1 gate. It intentionally does not
# publish a release: release publication remains owned by release.yml.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# The suite pins its own temporary root now (internal/testroot), so this no
# longer decides whether the tests can bind a socket -- it only gives this
# script's own scratch directory a short home. It honours the same override the
# suite does, so naming one place still names it everywhere.
scratch_root="${ATENEA_TEST_TMPDIR:-${TMPDIR:-/tmp}}"
build_dir="$(mktemp -d "$scratch_root/atenea-v1-readiness.XXXXXX")"
trap 'rm -rf "$build_dir"' EXIT

cd "$root"
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
# The separator is optional. The retired adapter's directory spelled the two
# words with nothing between them, and a pattern that required a separator
# could not see the one place the name was most likely to survive.
legacy_pattern="${legacy_prefix}[-_. ]?${legacy_suffix}"

# One tool, and it is neither of the two this used to choose between. ripgrep
# honours .gitignore and skips hidden files; grep -R honours neither and walks
# dist/, coverage profiles and whatever local provider state the checkout has
# collected. The pair therefore disagreed about what "the repository contains"
# means, and the gate's verdict depended on which of them the machine had.
#
# git grep asks the only question this gate is actually about -- what is
# committed -- gives the same answer everywhere, and is present by definition in
# a git checkout. Step [1/9] has already refused a dirty tree, so tracked and
# present are the same set by the time this runs.
set +e
git grep -Ini -E -e "$legacy_pattern"
legacy_status=$?
set -e
case "$legacy_status" in
0)
	echo "retired backend references must not be present" >&2
	exit 1
	;;
1) ;;
*)
	# A search that could not run is not a search that found nothing. The old
	# `if grep ...; then` form read grep's error status as a clean result.
	echo "retired backend scan failed with status $legacy_status" >&2
	exit 1
	;;
esac

bash scripts/serena-retirement-check.sh

echo "[3/9] formatting"
unformatted="$(gofmt -l .)"
test -z "$unformatted" || {
	echo "gofmt would rewrite:" >&2
	echo "$unformatted" >&2
	exit 1
}

echo "[4/9] module graph"
go mod tidy -diff

echo "[5/9] static validation"
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

echo "[6/9] build"
bash scripts/dashboard-build.sh
go build -trimpath -buildvcs=false -o "$build_dir/atenea" ./cmd/atenea

echo "[7/9] race suite"
go test -race -count=1 ./...

echo "[8/9] policy, load and provider entry points"
# The glob, not a list. The list was maintained by hand, so every script added
# after it was written started life outside the only gate that parses it:
# build-claude-mcpb.sh shipped a release before anything ran `bash -n` over it.
# A syntax error that reaches nobody until an operator runs the script in anger
# is exactly what this step exists to prevent.
bash -n scripts/*.sh
scripts/v1-policy-check.sh
scripts/benchmark-check.sh
scripts/load-check.sh
scripts/provider-matrix-check.sh
scripts/scrapling-spider-check.sh
"$build_dir/atenea" version

echo "v1 readiness gate passed"
