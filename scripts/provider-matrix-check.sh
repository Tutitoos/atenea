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

for pair in "${required[@]}"; do
	capability="${pair%%|*}"
	implementation="${pair#*|}"
	if ! grep -Fq "    $implementation (provider " <<<"$catalog"; then
		echo "missing declared implementation: $capability -> $implementation" >&2
		exit 1
	fi
done

"$build_dir/atenea" config show >/dev/null
echo "declared provider matrix passed (8 required edges)"

if [[ "${ATENEA_MCP_CHECK:-0}" == "1" ]]; then
	"$build_dir/atenea" mcp --check
else
	echo "live MCP probe skipped; set ATENEA_MCP_CHECK=1 with a running service"
fi
