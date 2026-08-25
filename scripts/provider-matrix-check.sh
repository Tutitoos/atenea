#!/usr/bin/env bash
set -euo pipefail

# This validates Atenea's declared capability/provider matrix. It is deliberately
# separate from live MCP or paid-provider probes: those require external
# processes, credentials and an explicit operator opt-in.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export TMPDIR="${ATENEA_TEST_TMPDIR:-/tmp}"
build_dir="$(mktemp -d "$TMPDIR/atenea-provider-matrix.XXXXXX")"
trap 'rm -rf "$build_dir"' EXIT

cd "$root"
go build -trimpath -buildvcs=false -o "$build_dir/atenea" ./cmd/atenea
catalog="$("$build_dir/atenea" catalog)"

required=(
	"code.search|codex.search"
	"code.search|claude.search"
	"code.search|ripgrep"
	"code.context|tokensave.context"
	"code.impact|kivgraph.impact"
	"symbol.definition|kivgraph.definition"
	"symbol.definition|serena.definition"
	"repository.index|kivgraph.index"
)

# The edge, not just its right-hand side. The loop used to search the whole
# catalog for the implementation line and never asked which capability it
# appeared under -- `capability` existed only to make the error message read as
# if the arc had been checked. An implementation registered against the wrong
# capability, which is the single thing a matrix gate is for, passed unnoticed.
edges="$(awk -f "$root/scripts/provider-matrix-edges.awk" <<<"$catalog")"

missing=0
for pair in "${required[@]}"; do
	if ! grep -Fqx -- "$pair" <<<"$edges"; then
		echo "missing declared edge: ${pair%%|*} -> ${pair#*|}" >&2
		missing=1
	fi
done
# Report every broken edge before failing: a matrix that lost a provider
# usually lost more than one, and one run should name them all.
test "$missing" -eq 0 || exit 1

"$build_dir/atenea" config show >/dev/null
# Counted, not typed. The message said "8 required edges" as a literal, so a
# ninth edge added to the list above would still have been announced as eight.
echo "declared provider matrix passed (${#required[@]} required edges)"

if [[ "${ATENEA_MCP_CHECK:-0}" == "1" ]]; then
	"$build_dir/atenea" mcp --check
else
	echo "live MCP probe skipped; set ATENEA_MCP_CHECK=1 with a running service"
fi
