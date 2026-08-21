#!/usr/bin/env bash
set -euo pipefail

if [[ "${ATENEA_OPENCODE_SMOKE:-}" != "1" ]]; then
	echo "This smoke test can invoke a real provider and may incur cost." >&2
	echo "Set ATENEA_OPENCODE_SMOKE=1 to confirm the opt-in run." >&2
	exit 2
fi
if [[ -z "${ATENEA_OPENCODE_MODEL:-}" ]]; then
	echo "ATENEA_OPENCODE_MODEL must be provider/model, for example provider/model-id." >&2
	exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
export ATENEA_OPENCODE_SMOKE=1
export ATENEA_OPENCODE_SMOKE_DIR="${ATENEA_OPENCODE_SMOKE_DIR:-$root}"

go test ./internal/agent/opencode -run '^TestLiveOpenCodeSmoke$' -count=1 -v
