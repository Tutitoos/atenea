#!/usr/bin/env bash
set -euo pipefail

if [[ "$ATENEA_MCP_CHECK" != "1" ]]; then
	echo "This smoke starts a real Atenea service and MCP bridge." >&2
	echo "Set ATENEA_MCP_CHECK=1 to confirm the opt-in run." >&2
	exit 2
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
temp_dir="$(mktemp -d /tmp/atenea-mcp-live.XXXXXX)"
state_dir="$temp_dir/state"
service_pid=0
mkdir -p "$state_dir"
trap 'if [[ "$service_pid" != "0" ]]; then kill "$service_pid" 2>/dev/null || true; wait "$service_pid" 2>/dev/null || true; fi; rm -rf "$temp_dir"' EXIT

cd "$root"
go build -trimpath -buildvcs=false -o "$temp_dir/atenea" ./cmd/atenea
XDG_STATE_HOME="$state_dir" "$temp_dir/atenea" run >"$temp_dir/service.log" 2>&1 &
service_pid=$!

ready=0
for _ in $(seq 1 80); do
	if XDG_STATE_HOME="$state_dir" "$temp_dir/atenea" mcp --check >"$temp_dir/mcp.log" 2>&1; then
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

XDG_STATE_HOME="$state_dir" ATENEA_MCP_CHECK=1 scripts/provider-matrix-check.sh
echo "live Atenea MCP bridge passed"
