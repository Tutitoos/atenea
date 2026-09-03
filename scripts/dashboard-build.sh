#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
command -v bun >/dev/null 2>&1 || { echo "Bun 1.4.0 is required to build the dashboard" >&2; exit 1; }

cd "$root"
bun ci --cwd dashboard
bun run --cwd dashboard check
bun run --cwd dashboard build

# The embedded client is generated, but it is intentionally versioned so a
# plain Go build remains possible for release and downstream packagers.
if git ls-files --error-unmatch -- internal/dashboard/web/dist/index.html >/dev/null 2>&1; then
	if git diff --quiet -- internal/dashboard/web/dist; then
		echo "dashboard dist is synchronized"
	else
		echo "dashboard dist changed; commit the generated assets" >&2
		git diff --stat -- internal/dashboard/web/dist >&2
		exit 1
	fi
else
	# A fresh checkout during development has no tracked dist yet. The build
	# above still generated the exact files Go embeds; CI will enforce the
	# tracked comparison once the initial assets are committed.
	echo "dashboard dist generated (untracked; add it to the release commit)"
fi
