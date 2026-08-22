#!/usr/bin/env bash
set -euo pipefail

# This is an opt-in real-provider acceptance check. Keep the default matrix to
# models listed by OpenCode as free. Do not put a paid model in the override
# without explicit authorization.
if [[ "${ATENEA_OPENCODE_SMOKE:-}" != "1" ]]; then
	echo "This matrix invokes real OpenCode providers and may create local session state." >&2
	echo "Set ATENEA_OPENCODE_SMOKE=1 to confirm the opt-in run." >&2
	exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
models="${ATENEA_OPENCODE_MODELS:-opencode/hy3-free,opencode/mimo-v2.5-free,opencode-go/ox-alpha-free}"

if ! command -v opencode >/dev/null 2>&1; then
	echo "opencode is not installed" >&2
	exit 1
fi

# macOS Unix sockets have a short path limit. Keep the whole ephemeral state
# path comfortably below it even when the user's TMPDIR is deeply nested.
temp_dir="$(mktemp -d /tmp/atenea-matrix.XXXXXX)"
state_dir="$temp_dir/state"
mkdir -p "$state_dir" "$temp_dir/repository"
go build -trimpath -buildvcs=false -o "$temp_dir/atenea" ./cmd/atenea

XDG_STATE_HOME="$state_dir" "$temp_dir/atenea" run >"$temp_dir/service.log" 2>&1 &
service_pid=$!
trap 'kill "$service_pid" 2>/dev/null || true; wait "$service_pid" 2>/dev/null || true' EXIT

ready=0
for _ in $(seq 1 80); do
	if XDG_STATE_HOME="$state_dir" "$temp_dir/atenea" mcp --check >"$temp_dir/mcp-check.log" 2>&1; then
		ready=1
		break
	fi
	sleep 0.25
done
if [[ "$ready" != "1" ]]; then
	echo "Atenea service did not become ready" >&2
	sed -n '1,160p' "$temp_dir/service.log" >&2
	exit 1
fi

# First establish the provider/model baseline without MCP. The same models are
# then run again with the bridge, so a provider that only works when tools are
# present cannot hide a plain-response failure.
ATENEA_OPENCODE_SMOKE=1 \
ATENEA_OPENCODE_MODELS="$models" \
ATENEA_OPENCODE_MCP_CONFIG="" \
ATENEA_OPENCODE_SMOKE_DIR="$temp_dir/repository" \
go test ./internal/agent/opencode -run '^TestLiveOpenCodeMatrix$' -count=1 -v

# The Go smoke receives Claude's portable mcpServers shape and translates it
# to OpenCode's native config. The child command is explicit and gets only the
# isolated state directory, so the test cannot accidentally attach the user's
# persistent Atenea service.
mcp_config=$(printf '%s' "{\"mcpServers\":{\"atenea\":{\"command\":\"$temp_dir/atenea\",\"args\":[\"mcp\"],\"env\":{\"XDG_STATE_HOME\":\"$state_dir\"}}}}")

ATENEA_OPENCODE_SMOKE=1 \
ATENEA_OPENCODE_MODELS="$models" \
ATENEA_OPENCODE_MCP_CONFIG="$mcp_config" \
ATENEA_OPENCODE_SMOKE_DIR="$temp_dir/repository" \
go test ./internal/agent/opencode -run '^TestLiveOpenCodeMatrix$' -count=1 -v

echo "OpenCode provider/MCP matrix passed"
echo "models=$models"
echo "opencode=$(opencode --version 2>&1 | head -n 1)"
echo "Atenea MCP: $(sed -n '2p' "$temp_dir/mcp-check.log")"
