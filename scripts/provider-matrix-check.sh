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
catalog="$("$build_dir/atenea" --config "$root/internal/config/default.toml" catalog)"

required=(
	"code.search|ripgrep"
	"code.search|claude.search"
	"code.search|codex.search"
	"code.context|tokensave.context"
	"symbol.overview|tokensave.overview"
	"symbol.calls|tokensave.calls"
	# The desktop edges. Checked here for the reason the gate exists at all: an
	# implementation registered against the wrong capability is the one mistake
	# a catalog cannot notice on its own, and on this provider it would mean a
	# capability that reads answering with one that types.
	"desktop.apps|macos.apps"
	"desktop.open|macos.open"
	"desktop.inspect|macos.inspect"
	"desktop.screenshot|macos.screenshot"
	"desktop.click|macos.click"
	"desktop.move|macos.move"
	"desktop.drag|macos.drag"
	"desktop.scroll|macos.scroll"
	"desktop.type|macos.type"
	"desktop.key|macos.key"
	"android.devices|adb.devices"
	"android.diagnose|adb.helper-diagnose"
	"android.screenshot|adb.screenshot"
	"android.inspect|uiautomator.inspect"
	"android.tap|adb.tap"
	"android.swipe|adb.swipe"
	"android.type|adb.type"
	"android.key|adb.key"
	"android.mirror|scrcpy.mirror"
	"android.unmirror|scrcpy.unmirror"
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

if [[ "$(wc -l <<<"$edges" | tr -d ' ')" -ne "${#required[@]}" ]]; then
	echo "provider matrix must cover every declared implementation (listed ${#required[@]})" >&2
	exit 1
fi

# Removed-provider guard: Kivgraph and the capabilities only it served left the
# shipped catalog together (#208), and Serena before it.
for capability in symbol.search symbol.intent_search symbol.definition symbol.references \
	symbol.implementations symbol.dependencies symbol.consumers symbol.get symbol.unresolved \
	symbol.source symbol.impact graph.status graph.repositories graph.ensure_fresh \
	repository.index code.impact; do
	if grep -q "^capability $capability " <<<"$catalog"; then
		echo "$capability reappeared in the catalog although no provider serves it" >&2
		exit 1
	fi
done
if grep -qi kivgraph <<<"$catalog"; then
	echo "removed Kivgraph provider reappeared in the catalog" >&2
	exit 1
fi
if grep -qi serena <<<"$catalog"; then
	echo "retired Serena provider reappeared in the catalog" >&2
	exit 1
fi

"$build_dir/atenea" --config "$root/internal/config/default.toml" config show >/dev/null
# Counted, not typed. The message said "8 required edges" as a literal, so a
# ninth edge added to the list above would still have been announced as eight.
echo "declared provider matrix passed (${#required[@]} required edges)"

if [[ "${ATENEA_MCP_CHECK:-0}" == "1" ]]; then
	"$build_dir/atenea" mcp --check
else
	echo "live MCP probe skipped; set ATENEA_MCP_CHECK=1 with a running service"
fi
